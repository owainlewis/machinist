package controlplane

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

func createAutomation(t *testing.T, s *GitHubFactoryServer, cron string) factoryAutomation {
	t.Helper()
	var a factoryAutomation
	raw := factoryHTTP(t, s, "POST", "/api/v1/automations", map[string]any{"name": "Find bugs", "repository": "repo", "prompt": "Review from the repository root. Report findings privately; do not change files.", "cron": cron, "timezone": "Europe/London"}, 200)
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	return a
}
func automationRun(t *testing.T, s *GitHubFactoryServer, a factoryAutomation, key string, want int) string {
	t.Helper()
	raw := factoryHTTP(t, s, "POST", "/api/v1/automations/"+a.ID+"/run", map[string]string{"client_request_id": key}, want)
	var result map[string]string
	json.Unmarshal(raw, &result)
	return result["id"]
}
func TestAutomationManualAdmissionAndRecovery(t *testing.T) {
	client := newMemoryFactoryGitHub()
	s := githubFactoryTestServer(t, client)
	a := createAutomation(t, s, "")
	id := automationRun(t, s, a, "click", 201)
	job := factoryJob(t, s, id)
	if job.Recipe != "single" || len(job.Runs) != 1 || job.Prompt != a.Prompt || job.Repository != "repo" {
		t.Fatalf("not a normal single-agent job: %+v", job)
	}
	automationRun(t, s, a, "other", 409)
	s = restartFactoryTestServer(t, s, client)
	if got := automationRun(t, s, a, "click", 201); got != id {
		t.Fatal("manual retry duplicated work")
	}
	run := factoryPoll(t, s)
	if run == nil || run.Factory.Role != "implement" || run.Factory.Recipe != "single" {
		t.Fatal("automation cannot be claimed by normal worker")
	}
	factoryFinish(t, s, run, protocol.FactoryResult{Report: "Private findings"})
	if factoryJob(t, s, id).State != "succeeded" {
		t.Fatal("single job did not finish")
	}
	if next := automationRun(t, s, a, "next", 201); next == id {
		t.Fatal("new manual run reused prior result")
	}
	if client.creates != 0 || len(client.comments) != 0 {
		t.Fatal("internal automation state published to GitHub")
	}
}
func TestAutomationScheduleCoalescesAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	client := newMemoryFactoryGitHub()
	s := githubFactoryTestServer(t, client)
	a := createAutomation(t, s, "* * * * *")
	due := *a.NextDue
	// A sleeping server admits only one run, not every missed occurrence.
	now := due.Add(3 * time.Hour)
	if err := s.tickAutomations(ctx, now); err != nil {
		t.Fatal(err)
	}
	a, err := s.local.automation(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.LastJob == "" || !a.NextDue.After(now) {
		t.Fatal("cursor not advanced atomically with admission")
	}
	id := a.LastJob
	s = restartFactoryTestServer(t, s, client)
	if err = s.tickAutomations(ctx, now); err != nil {
		t.Fatal(err)
	}
	factoryJob(t, s, id)
	if len(s.tasks) != 1 {
		t.Fatal("restart duplicated scheduled job")
	}
	// An active or unresolved job skips the next occurrence without accumulating.
	now = a.NextDue.Add(time.Second)
	if err = s.tickAutomations(ctx, now); err != nil {
		t.Fatal(err)
	}
	a, _ = s.local.automation(ctx, a.ID)
	if a.LastJob != id || !a.NextDue.After(now) || !strings.Contains(a.LastError, "skipped") {
		t.Fatal("overlap was not coalesced")
	}
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/cancel", map[string]any{"run_id": factoryJob(t, s, id).Runs[0].ID}, 200)
	now = *a.NextDue
	if err = s.tickAutomations(ctx, now); err != nil {
		t.Fatal(err)
	}
	a, _ = s.local.automation(ctx, a.ID)
	if a.LastJob == id || a.LastError != "" {
		t.Fatal("schedule did not recover after terminal job")
	}
}
func TestAutomationValidationPauseAndVersioning(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	a := createAutomation(t, s, "0 9 * * 1-5")
	if a.NextDue == nil || a.Timezone != "Europe/London" {
		t.Fatal("timezone schedule missing")
	}
	originalDue := *a.NextDue
	a.Prompt = "Updated Markdown prompt"
	raw := factoryHTTP(t, s, "PUT", "/api/v1/automations/"+a.ID, a, 200)
	var updated factoryAutomation
	json.Unmarshal(raw, &updated)
	if updated.Version != a.Version+1 || !updated.NextDue.Equal(originalDue) {
		t.Fatal("prompt edit reset schedule")
	}
	factoryHTTP(t, s, "PUT", "/api/v1/automations/"+a.ID, a, 409)
	updated.Paused = true
	raw = factoryHTTP(t, s, "PUT", "/api/v1/automations/"+a.ID, updated, 200)
	updated = factoryAutomation{}
	json.Unmarshal(raw, &updated)
	if updated.NextDue != nil {
		t.Fatal("paused schedule remains due")
	}
	automationRun(t, s, updated, "paused-manual", 201)
	// Pausing affects schedules, not manual execution or running jobs.
	factoryHTTP(t, s, "DELETE", "/api/v1/automations/"+a.ID, map[string]int{"version": 1}, 409)
	factoryHTTP(t, s, "DELETE", "/api/v1/automations/"+a.ID, map[string]int{"version": updated.Version}, 200)
	var list []factoryAutomation
	json.Unmarshal(factoryHTTP(t, s, "GET", "/api/v1/automations", nil, 200), &list)
	if len(list) != 0 {
		t.Fatal("delete retained definition")
	}
	factoryHTTP(t, s, "GET", "/api/v1/status", nil, 200)
	if len(s.tasks) != 1 {
		t.Fatal("delete erased job history")
	}
	for _, patch := range []map[string]string{
		{"name": "x", "repository": "repo", "prompt": "x", "cron": "invalid", "timezone": "UTC"},
		{"name": "x", "repository": "repo", "prompt": "x", "cron": "* * * * *", "timezone": "Missing/Zone"},
		{"name": "x", "repository": "unknown", "prompt": "x"},
		{"name": "x", "repository": "repo", "prompt": ""},
	} {
		factoryHTTP(t, s, "POST", "/api/v1/automations", patch, 400)
	}
}
func TestAutomationMutationRequiresAuthentication(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		path := "/api/v1/automations"
		if method != "POST" {
			path += "/missing"
		}
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		out := httptest.NewRecorder()
		s.Handler().ServeHTTP(out, req)
		if out.Code < 400 {
			t.Fatalf("unauthenticated %s allowed", method)
		}
	}
}

func TestAutomationCancelledProcessNeedsExplicitStopConfirmation(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	a := createAutomation(t, s, "* * * * *")
	id := automationRun(t, s, a, "first", 201)
	run := factoryPoll(t, s)
	if run == nil {
		t.Fatal("no run")
	}
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/cancel", map[string]any{"run_id": run.ID}, 200)
	automationRun(t, s, a, "second", 409)
	due := a.NextDue.Add(time.Second)
	if err := s.tickAutomations(context.Background(), due); err != nil {
		t.Fatal(err)
	}
	current, _ := s.local.automation(context.Background(), a.ID)
	if current.LastJob != id || !strings.Contains(current.LastError, "stopped") {
		t.Fatal("cancelled process allowed recurrence")
	}
	raw := factoryHTTP(t, s, "POST", "/api/v1/automations/"+a.ID+"/run", map[string]any{"client_request_id": "second", "previous_process_stopped": true}, 201)
	var result map[string]string
	json.Unmarshal(raw, &result)
	if result["id"] == id {
		t.Fatal("replacement not admitted")
	}
	current, _ = s.local.automation(context.Background(), a.ID)
	if current.StopConfirmedAt == nil {
		t.Fatal("stop confirmation not durable")
	}
}

func TestAutomationHistoricalRetriesCannotOverlapNewerRuns(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	a := createAutomation(t, s, "")
	id := automationRun(t, s, a, "first", 201)
	run := factoryPoll(t, s)
	factoryHTTP(t, s, "POST", "/api/v1/runs/"+run.ID+"/complete", protocol.Completion{InstanceID: "worker-instance", LeaseToken: run.LeaseToken, State: "failed", Error: "failed"}, 204)
	next := automationRun(t, s, a, "second", 201)
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/retry", map[string]any{"run_id": run.ID}, 409)
	currentRun := factoryPoll(t, s)
	factoryFinish(t, s, currentRun, protocol.FactoryResult{Report: "No findings"})
	if factoryJob(t, s, next).State != "succeeded" {
		t.Fatal("newer job not done")
	}
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/retry", map[string]any{"run_id": run.ID}, 200)
	retried := factoryPoll(t, s)
	if retried == nil || retried.JobID != id {
		t.Fatal("successful retry was not immediately claimable")
	}
	automationRun(t, s, a, "third", 409)
	var list []factoryAutomation
	json.Unmarshal(factoryHTTP(t, s, "GET", "/api/v1/automations", nil, 200), &list)
	if len(list) != 1 || !list[0].Busy {
		t.Fatal("historical active retry missing from automation status")
	}
}

func TestAutomationAdmissionAndCursorRollbackTogether(t *testing.T) {
	ctx := context.Background()
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	a := createAutomation(t, s, "* * * * *")
	// Force the final cursor write to fail after the transaction inserts the job.
	_, err := s.local.db.Exec(`CREATE TRIGGER reject_cursor BEFORE UPDATE ON factory_automations BEGIN SELECT RAISE(ABORT,'disk write failed'); END`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.enqueueAutomation(ctx, &a, "scheduled:test", *a.NextDue, true, false)
	if err == nil {
		t.Fatal("expected transaction failure")
	}
	var count int
	s.local.db.QueryRow(`SELECT count(*) FROM job_requests`).Scan(&count)
	if count != 0 {
		t.Fatal("failed cursor transaction retained a job receipt")
	}
	tasks, _ := s.local.tasks(ctx)
	if len(tasks) != 0 {
		t.Fatal("failed cursor transaction retained a job")
	}
	stored, _ := s.local.automation(ctx, a.ID)
	if stored.LastJob != "" || !stored.NextDue.Equal(*a.NextDue) {
		t.Fatal("cursor advanced on failed admission")
	}
	if _, err = s.local.db.Exec(`DROP TRIGGER reject_cursor`); err != nil {
		t.Fatal(err)
	}
	if err = s.tickAutomations(ctx, *a.NextDue); err != nil {
		t.Fatal(err)
	}
	stored, _ = s.local.automation(ctx, a.ID)
	if stored.LastJob == "" {
		t.Fatal("failed occurrence did not recover")
	}
}

func TestAutomationLargeCronStep(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	a := createAutomation(t, s, "1/"+strconv.Itoa(int(^uint(0)>>1))+" * * * *")
	if a.NextDue == nil || a.NextDue.Minute() != 1 {
		t.Fatal("large cron step lost its first valid occurrence")
	}
}

func TestAutomationRejectsMultilineCron(t *testing.T) {
	for _, newline := range []string{"\n", "\r", "\r\n"} {
		t.Run(strconv.Quote(newline), func(t *testing.T) {
			s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
			a := createAutomation(t, s, "0 9 * * *")
			a.Cron = "0 9" + newline + "* * *"
			for _, method := range []string{"POST", "PUT"} {
				path := "/api/v1/automations"
				if method == "PUT" {
					path += "/" + a.ID
				}
				raw := factoryHTTP(t, s, method, path, a, 400)
				if !strings.Contains(string(raw), "one line") {
					t.Fatalf("unexpected validation error: %s", raw)
				}
			}
			list, err := s.local.automations(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != 1 || list[0].Cron != "0 9 * * *" || list[0].Version != a.Version || !list[0].NextDue.Equal(*a.NextDue) {
				t.Fatalf("rejected schedule changed saved automations: %+v", list)
			}
		})
	}
}
