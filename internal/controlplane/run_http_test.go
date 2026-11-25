package controlplane

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/sources"
	"github.com/owainlewis/machinist/internal/tickets"
)

func decodeJob(t *testing.T, body []byte) job {
	t.Helper()
	var view job
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return view
}

func runRequest(key string) sources.RunRequest {
	return sources.RunRequest{Key: key, Preset: "default", Vars: map[string]string{"repo": "owner/repo"}, Prompt: "Add a retry to the upload client\n\nSee ENG-142."}
}

func (f *fixture) run(t *testing.T, key string) job {
	t.Helper()
	f.connect(t)
	response, body := f.call(t, http.MethodPost, "/machinist/run", f.token, runRequest(key))
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	return decodeJob(t, body)
}

func TestRunCreatesAQueuedJobAndAKeyRepeatsIt(t *testing.T) {
	f := newFixture(t)
	first := f.run(t, "linear:ENG-142")
	if first.State != JobQueued || first.Preset != "default" || first.Vars["repo"] != "owner/repo" || first.CreatedBy != "runner-1" {
		t.Fatalf("job = %+v", first)
	}
	if first.Title != "Add a retry to the upload client" {
		t.Fatalf("title = %q", first.Title)
	}
	entries, err := f.store.Entries(t.Context(), first.ID)
	if err != nil || len(entries) == 0 || !strings.Contains(entries[0].Body, "See ENG-142.") {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
	response, body := f.call(t, http.MethodPost, "/machinist/run", f.token, runRequest("linear:ENG-142"))
	if response.StatusCode != http.StatusOK || decodeJob(t, body).ID != first.ID {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
}

func TestRunWithoutAPresetUsesTheDefault(t *testing.T) {
	f := newFixture(t)
	f.connect(t)
	request := runRequest("one-off")
	request.Preset = ""
	response, body := f.call(t, http.MethodPost, "/machinist/run", f.token, request)
	if response.StatusCode != http.StatusCreated || decodeJob(t, body).Preset != "default" {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
}

func TestRunRejectsBadRequests(t *testing.T) {
	f := newFixture(t)
	f.connect(t)
	for name, change := range map[string]func(*sources.RunRequest){
		"no key":         func(r *sources.RunRequest) { r.Key = "" },
		"long key":       func(r *sources.RunRequest) { r.Key = strings.Repeat("k", 201) },
		"unknown preset": func(r *sources.RunRequest) { r.Preset = "missing" },
		"no repo":        func(r *sources.RunRequest) { r.Vars = nil },
		"other repo":     func(r *sources.RunRequest) { r.Vars["repo"] = "owner/other" },
		"unknown var":    func(r *sources.RunRequest) { r.Vars["branch"] = "main" },
		"no prompt":      func(r *sources.RunRequest) { r.Prompt = "  \n" },
	} {
		request := runRequest("k")
		change(&request)
		if response, body := f.call(t, http.MethodPost, "/machinist/run", f.token, request); response.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: status = %d: %s", name, response.StatusCode, body)
		}
	}
}

func TestJobStateFollowsTheTicket(t *testing.T) {
	running := []tickets.Run{{Status: tickets.RunRunning}}
	ended := []tickets.Run{{Status: tickets.RunSucceeded}}
	for _, test := range []struct {
		state string
		runs  []tickets.Run
		want  string
	}{
		{tickets.StateWorking, nil, JobQueued},
		{tickets.StateWorking, ended, JobQueued},
		{tickets.StateWorking, running, JobRunning},
		{tickets.StatePRReady, ended, JobDone},
		{tickets.StateDone, ended, JobDone},
		{tickets.StateFailed, ended, JobFailed},
		{tickets.StateCancelled, nil, JobCancelled},
	} {
		if got := jobState(tickets.Ticket{State: test.state}, test.runs); got != test.want {
			t.Fatalf("%s with %v = %s, want %s", test.state, test.runs, got, test.want)
		}
	}
}

func TestJobRoutesGetListAndCancel(t *testing.T) {
	f := newFixture(t)
	created := f.run(t, "k1")

	response, body := f.call(t, http.MethodGet, "/machinist/jobs/"+created.ID, f.token, nil)
	if response.StatusCode != http.StatusOK || decodeJob(t, body).ID != created.ID {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	if response, _ := f.call(t, http.MethodGet, "/machinist/jobs/t_missing", f.token, nil); response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", response.StatusCode)
	}

	response, body = f.call(t, http.MethodGet, "/machinist/jobs", f.token, nil)
	var list struct {
		Jobs []job `json:"jobs"`
	}
	if err := json.Unmarshal(body, &list); err != nil || response.StatusCode != http.StatusOK || len(list.Jobs) != 1 {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}

	// Jobs never wait for a person, so there is nothing to answer.
	if response, _ := f.call(t, http.MethodPost, "/machinist/jobs/"+created.ID+"/input", f.token, map[string]string{"text": "Postgres"}); response.StatusCode == http.StatusOK {
		t.Fatalf("input: status = %d", response.StatusCode)
	}

	response, body = f.call(t, http.MethodPost, "/machinist/jobs/"+created.ID+"/cancel", f.token, nil)
	if cancelled := decodeJob(t, body); response.StatusCode != http.StatusOK || cancelled.State != JobCancelled {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
}

// A job shows the caller's key, the step it is on, and what its runs used.
// Starting to run counts as an update.
func TestJobShowsKeyProgressTokensAndCost(t *testing.T) {
	f := newFixture(t)
	start := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	f.store.SetClock(func() time.Time { return start })
	created := f.run(t, "linear:ENG-148")
	if created.Key != "linear:ENG-148" || created.CurrentStep != "" || created.Tokens != 0 || created.CostUSD != nil {
		t.Fatalf("created = %+v", created)
	}
	if err := f.server.engine.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	claimed := start.Add(time.Minute)
	f.store.SetClock(func() time.Time { return claimed })
	response, body := f.call(t, http.MethodPost, "/api/v1/runners/poll", f.token, protocol.PollRequest{Harnesses: []string{protocol.StepsHarness}})
	var run runner.RunRequest
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &run) != nil {
		t.Fatalf("poll = %d: %s", response.StatusCode, body)
	}
	event := json.RawMessage(`{"sequence":1,"time":"2026-01-02T03:01:00Z","type":"step.started","message":"name=work"}`)
	if response, body := f.call(t, http.MethodPost, "/api/v1/runs/"+run.RunID+"/events", f.token, protocol.RunEventBatch{Events: []json.RawMessage{event}}); response.StatusCode != http.StatusOK {
		t.Fatalf("events = %d: %s", response.StatusCode, body)
	}

	running := f.getJob(t, created.ID)
	if running.State != JobRunning || running.CurrentStep != "work" || !running.UpdatedAt.Equal(claimed) {
		t.Fatalf("running = %+v", running)
	}

	tokens, cost := int64(1200), 0.42
	completion := protocol.Completion{State: tickets.RunSucceeded, Tokens: &tokens, Cost: &cost, Report: &protocol.Report{Outcome: "no_change", Summary: "Done"}}
	if response, body := f.call(t, http.MethodPost, "/api/v1/runs/"+run.RunID+"/complete", f.token, completion); response.StatusCode != http.StatusNoContent {
		t.Fatalf("complete = %d: %s", response.StatusCode, body)
	}
	done := f.getJob(t, created.ID)
	if done.State != JobDone || done.CurrentStep != "" || done.Tokens != 1200 || done.CostUSD == nil || *done.CostUSD != 0.42 {
		t.Fatalf("done = %+v", done)
	}
	response, body = f.call(t, http.MethodGet, "/machinist/jobs", f.token, nil)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"key":"linear:ENG-148"`) || !strings.Contains(string(body), `"tokens":1200`) || !strings.Contains(string(body), `"cost_usd":0.42`) {
		t.Fatalf("list = %d: %s", response.StatusCode, body)
	}
}

// A job's cost_usd is the cost in its ticket's figures, across runs that did
// and did not report one. The self-test saw it null while the figures had one.
func TestJobCostMatchesTicketFigures(t *testing.T) {
	f := newFixture(t)
	created := f.run(t, "linear:ENG-149")
	finish := func(cost *float64) {
		t.Helper()
		if err := f.server.engine.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		response, body := f.call(t, http.MethodPost, "/api/v1/runners/poll", f.token, protocol.PollRequest{Harnesses: []string{protocol.StepsHarness}})
		var run runner.RunRequest
		if response.StatusCode != http.StatusOK || json.Unmarshal(body, &run) != nil {
			t.Fatalf("poll = %d: %s", response.StatusCode, body)
		}
		tokens := int64(100)
		completion := protocol.Completion{State: tickets.RunSucceeded, Tokens: &tokens, Cost: cost, Report: &protocol.Report{Outcome: "no_change", Summary: "Done"}}
		if response, body := f.call(t, http.MethodPost, "/api/v1/runs/"+run.RunID+"/complete", f.token, completion); response.StatusCode != http.StatusNoContent {
			t.Fatalf("complete = %d: %s", response.StatusCode, body)
		}
	}
	rerun := func() {
		t.Helper()
		if response, body := f.call(t, http.MethodPost, "/machinist/jobs/"+created.ID+"/rerun", f.token, nil); response.StatusCode != http.StatusOK {
			t.Fatalf("rerun = %d: %s", response.StatusCode, body)
		}
	}
	figures := func() *float64 {
		t.Helper()
		response, body := f.call(t, http.MethodGet, "/api/v1/tickets/"+created.ID, f.token, nil)
		var detail struct {
			Figures tickets.Figures `json:"figures"`
		}
		if response.StatusCode != http.StatusOK || json.Unmarshal(body, &detail) != nil {
			t.Fatalf("ticket = %d: %s", response.StatusCode, body)
		}
		return detail.Figures.Cost
	}

	finish(nil)
	if job, cost := f.getJob(t, created.ID), figures(); job.CostUSD != nil || cost != nil {
		t.Fatalf("no cost reported: cost_usd = %v, figures = %v", job.CostUSD, cost)
	}
	first, second := 0.42, 0.1
	rerun()
	finish(&first)
	rerun()
	finish(nil)
	rerun()
	finish(&second)
	job, cost := f.getJob(t, created.ID), figures()
	if job.CostUSD == nil || cost == nil || *job.CostUSD != *cost || *cost != first+second || job.Tokens != 400 {
		t.Fatalf("cost_usd = %v, figures = %v, job = %+v", job.CostUSD, cost, job)
	}
}

func (f *fixture) getJob(t *testing.T, id string) job {
	t.Helper()
	response, body := f.call(t, http.MethodGet, "/machinist/jobs/"+id, f.token, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	return decodeJob(t, body)
}

func TestJobAPIMergeGateJSONSerialization(t *testing.T) {
	f := newFixture(t)
	created := f.run(t, "linear:ENG-192")

	// Before any merge gate runs, merge_gate_* fields are omitted from JSON.
	for _, path := range []string{"/machinist/jobs/" + created.ID, "/machinist/jobs"} {
		response, body := f.call(t, http.MethodGet, path, f.token, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status = %d: %s", path, response.StatusCode, body)
		}
		raw := string(body)
		for _, field := range []string{"merge_gate_decision", "merge_gate_risk", "merge_gate_reason"} {
			if strings.Contains(raw, field) {
				t.Fatalf("GET %s should omit %s when no merge gate ran: %s", path, field, raw)
			}
		}
	}

	// After persisting a merge-gate verdict, both single and list job endpoints include it.
	if err := f.store.Apply(t.Context(), created.ID, tickets.StateWorking, tickets.Transition{
		State:    tickets.StatePRReady,
		PRNumber: 91,
		MergeGate: &tickets.MergeGate{
			Decision: "human_review",
			Risk:     "medium",
			Reason:   "Modifies control plane HTTP response fields.",
		},
	}); err != nil {
		t.Fatal(err)
	}

	got := f.getJob(t, created.ID)
	if got.MergeGateDecision != "human_review" || got.MergeGateRisk != "medium" || got.MergeGateReason != "Modifies control plane HTTP response fields." {
		t.Fatalf("job = %+v", got)
	}

	response, body := f.call(t, http.MethodGet, "/machinist/jobs", f.token, nil)
	var list struct {
		Jobs []job `json:"jobs"`
	}
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &list) != nil || len(list.Jobs) != 1 {
		t.Fatalf("list = %d: %s", response.StatusCode, body)
	}
	listed := list.Jobs[0]
	if listed.MergeGateDecision != "human_review" || listed.MergeGateRisk != "medium" || listed.MergeGateReason != "Modifies control plane HTTP response fields." {
		t.Fatalf("listed job = %+v", listed)
	}
}
