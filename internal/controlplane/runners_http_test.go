package controlplane

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/harness"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/tickets"
)

func TestPollReturnsNoContentWithoutWork(t *testing.T) {
	f := newFixture(t)
	response, body := f.call(t, http.MethodPost, "/api/v1/runners/poll", f.token, protocol.PollRequest{Harnesses: []string{protocol.StepsHarness}})
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("%d: %s", response.StatusCode, body)
	}
}

func TestPollRecordsRunnerAndNamesRunAfterToken(t *testing.T) {
	f := newFixture(t)
	run := f.queueRun(t)
	stored, err := f.store.Run(t.Context(), run.RunID)
	if err != nil || stored.Runner != "runner-1" {
		t.Fatalf("run = %+v, err = %v", stored, err)
	}
	response, body := f.call(t, http.MethodGet, "/api/v1/runners", f.token, nil)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"name":"runner-1"`) || !strings.Contains(string(body), `"harnesses":["steps"]`) {
		t.Fatalf("%d: %s", response.StatusCode, body)
	}
}

func TestRunnersListShowsSlotsJobsAndQueue(t *testing.T) {
	f := newFixture(t)
	run := f.queueRun(t)
	request := protocol.PollRequest{Harnesses: []string{protocol.StepsHarness}, Kind: "kubernetes", Slots: 4}
	if response, body := f.call(t, http.MethodPost, "/api/v1/runners/poll", f.token, request); response.StatusCode != http.StatusNoContent {
		t.Fatalf("%d: %s", response.StatusCode, body)
	}
	waiting, _, err := f.store.CreateTicket(t.Context(), tickets.NewTicket{Source: "api", Repo: "owner/repo", Title: "Waiting", Body: "Wait", Agent: "default", CreatedBy: "alice", SourceKey: "api:waiting"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.QueueRun(t.Context(), waiting.ID, "codex"); err != nil {
		t.Fatal(err)
	}
	response, body := f.call(t, http.MethodGet, "/api/v1/runners", f.token, nil)
	var fleet struct {
		Runners []tickets.Runner `json:"runners"`
		Queue   tickets.Queue    `json:"queue"`
	}
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &fleet) != nil || len(fleet.Runners) != 1 {
		t.Fatalf("%d: %s", response.StatusCode, body)
	}
	got := fleet.Runners[0]
	if got.Kind != "kubernetes" || got.Slots != 4 {
		t.Fatalf("runner = %+v", got)
	}
	if len(got.Jobs) != 1 || got.Jobs[0].RunID != run.RunID || got.Jobs[0].TicketID != run.TicketID || got.Jobs[0].StartedAt.IsZero() {
		t.Fatalf("jobs = %+v", got.Jobs)
	}
	if fleet.Queue.Waiting != 1 || fleet.Queue.Oldest == nil {
		t.Fatalf("queue = %+v", fleet.Queue)
	}
}

func TestPollCarriesNoSecrets(t *testing.T) {
	f := newFixture(t)
	run := f.queueRun(t)
	body, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), f.token) || strings.Contains(strings.ToLower(string(body)), "gh_token") {
		t.Fatalf("run carries a secret: %s", body)
	}
}

func TestHeartbeatEventsCompleteLifecycle(t *testing.T) {
	f := newFixture(t)
	run := f.queueRun(t)
	path := "/api/v1/runs/" + run.RunID

	response, body := f.call(t, http.MethodPost, path+"/heartbeat", f.token, struct{}{})
	var beat protocol.HeartbeatResponse
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &beat) != nil || beat.Cancel {
		t.Fatalf("heartbeat = %d: %s", response.StatusCode, body)
	}

	event, _ := json.Marshal(protocol.RunEvent{Sequence: 1, Type: "agent.message"})
	response, body = f.call(t, http.MethodPost, path+"/events", f.token, protocol.RunEventBatch{Events: []json.RawMessage{event}})
	var page protocol.RunEventPage
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &page) != nil || page.Cursor != 1 {
		t.Fatalf("events = %d: %s", response.StatusCode, body)
	}

	completion := protocol.Completion{State: tickets.RunSucceeded, Report: &protocol.Report{Outcome: "no_change", Summary: "Done"}}
	response, body = f.call(t, http.MethodPost, path+"/complete", f.token, completion)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("complete = %d: %s", response.StatusCode, body)
	}
	assertTicketState(t, f, run, tickets.StateDone)

	for _, suffix := range []string{"/heartbeat", "/complete"} {
		response, body = f.call(t, http.MethodPost, path+suffix, f.token, completion)
		if response.StatusCode != http.StatusConflict {
			t.Fatalf("%s after end = %d: %s", suffix, response.StatusCode, body)
		}
	}
	response, body = f.call(t, http.MethodPost, path+"/events", f.token, protocol.RunEventBatch{Events: []json.RawMessage{event}})
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("events after end = %d: %s", response.StatusCode, body)
	}
}

func assertTicketState(t *testing.T, f *fixture, run runner.RunRequest, want string) {
	t.Helper()
	ticket, err := f.store.Ticket(t.Context(), run.TicketID)
	if err != nil || ticket.State != want {
		t.Fatalf("ticket = %+v, err = %v", ticket, err)
	}
}

func TestRunRouteErrors(t *testing.T) {
	f := newFixture(t)
	if response, body := f.call(t, http.MethodPost, "/api/v1/runs/run_missing/heartbeat", f.token, struct{}{}); response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown run = %d: %s", response.StatusCode, body)
	}
	run := f.queueRun(t)
	path := "/api/v1/runs/" + run.RunID
	if response, body := f.call(t, http.MethodPost, path+"/complete", f.token, protocol.Completion{State: "exploded"}); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad state = %d: %s", response.StatusCode, body)
	}
	bad, _ := json.Marshal(protocol.RunEvent{Sequence: 9, Type: "x"})
	if response, body := f.call(t, http.MethodPost, path+"/events", f.token, protocol.RunEventBatch{Events: []json.RawMessage{bad}}); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("gap = %d: %s", response.StatusCode, body)
	}
	if response, body := f.call(t, http.MethodPost, path+"/complete", f.token, "not an object"); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad body = %d: %s", response.StatusCode, body)
	}
}

func TestHeartbeatTellsRunnerToCancel(t *testing.T) {
	f := newFixture(t)
	run := f.queueRun(t)
	if _, err := f.store.Cancel(t.Context(), run.TicketID, "alice"); err != nil {
		t.Fatal(err)
	}
	response, body := f.call(t, http.MethodPost, "/api/v1/runs/"+run.RunID+"/heartbeat", f.token, struct{}{})
	var beat protocol.HeartbeatResponse
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &beat) != nil || !beat.Cancel {
		t.Fatalf("heartbeat = %d: %s", response.StatusCode, body)
	}
}

// An operator can remove a runner that is offline and idle, such as an old
// laptop, but not one that is online or still holds a running job.
func TestRemoveRunnerForgetsOnlyAnIdleOfflineRunner(t *testing.T) {
	f := newFixture(t)
	run := f.queueRun(t)
	operator := f.newToken(t, "alice", tickets.RoleOperator)
	remove := func(name string) (*http.Response, []byte) {
		return f.call(t, http.MethodDelete, "/api/v1/runners/"+name, operator, nil)
	}
	if response, body := remove("runner-1"); response.StatusCode != http.StatusConflict || !strings.Contains(string(body), "online") {
		t.Fatalf("removing an online runner = %d: %s", response.StatusCode, body)
	}
	lastWeek := time.Now().Add(-7 * 24 * time.Hour)
	f.store.SetClock(func() time.Time { return lastWeek })
	for _, name := range []string{"runner-1", "old-laptop"} {
		if err := f.store.RecordRunner(t.Context(), name, "local", []string{protocol.StepsHarness}, 1, true); err != nil {
			t.Fatal(err)
		}
	}
	f.store.SetClock(time.Now)
	if response, body := remove("runner-1"); response.StatusCode != http.StatusConflict || !strings.Contains(string(body), "running jobs") {
		t.Fatalf("removing a runner with run %s = %d: %s", run.RunID, response.StatusCode, body)
	}
	if response, body := remove("nobody"); response.StatusCode != http.StatusNotFound {
		t.Fatalf("removing an unknown runner = %d: %s", response.StatusCode, body)
	}
	if response, body := f.call(t, http.MethodDelete, "/api/v1/runners/old-laptop", "", nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("removing without a token = %d: %s", response.StatusCode, body)
	}
	response, body := remove("old-laptop")
	var fleet struct {
		Runners []tickets.Runner `json:"runners"`
	}
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &fleet) != nil || len(fleet.Runners) != 1 || fleet.Runners[0].Name != "runner-1" {
		t.Fatalf("removing an idle offline runner = %d: %s", response.StatusCode, body)
	}
}

// A queued job whose agents need a harness no online runner has says which
// one it waits for, on the Runners page and on the job.
func TestQueuedRunsSayWhichHarnessTheyWaitFor(t *testing.T) {
	f := newFixture(t)
	f.server.factory = &factory.Factory{
		Presets: map[string]factory.Preset{
			"default": {Name: "default", Steps: []runner.Step{{Name: "work", Run: "machinist agent implement"}}},
			"review":  {Name: "review", Steps: []runner.Step{{Name: "review", Run: "machinist agent review\n# machinist agent agy"}}},
		},
		Agents: map[string]harness.Agent{
			"implement": {Name: "implement", Harness: "claude"},
			"review":    {Name: "review", Harness: "codex"},
			"agy":       {Name: "agy", Harness: "agy"},
		},
	}
	f.server.engine.Factory = f.server.factory
	queue := func(key, preset string) tickets.Ticket {
		t.Helper()
		ticket, _, err := f.store.CreateTicket(t.Context(), tickets.NewTicket{Source: "api", Repo: "owner/repo", Title: key, Body: "Do it", Agent: preset, CreatedBy: "alice", SourceKey: key})
		if err != nil {
			t.Fatal(err)
		}
		return ticket
	}
	queue("first", "review")
	review := queue("second", "review")
	implement := queue("third", "default")
	if err := f.server.engine.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	type fleetView struct {
		Queue tickets.Queue `json:"queue"`
	}
	blocked := func() []tickets.HarnessWait {
		t.Helper()
		response, body := f.call(t, http.MethodGet, "/api/v1/runners", f.token, nil)
		var fleet fleetView
		if response.StatusCode != http.StatusOK || json.Unmarshal(body, &fleet) != nil {
			t.Fatalf("%d: %s", response.StatusCode, body)
		}
		return fleet.Queue.Blocked
	}
	waitingFor := func(id string) []string {
		t.Helper()
		response, body := f.call(t, http.MethodGet, "/api/v1/tickets/"+id, f.token, nil)
		var detail struct {
			WaitingFor []string `json:"waiting_for"`
		}
		if response.StatusCode != http.StatusOK || json.Unmarshal(body, &detail) != nil {
			t.Fatalf("%d: %s", response.StatusCode, body)
		}
		return detail.WaitingFor
	}
	if got := blocked(); len(got) != 0 {
		t.Fatalf("with no runner online the queue waits for any runner, not a harness: %+v", got)
	}
	if err := f.store.RecordRunner(t.Context(), "laptop", "local", []string{protocol.StepsHarness, "claude"}, 1, true); err != nil {
		t.Fatal(err)
	}
	if got := blocked(); len(got) != 1 || !slices.Equal(got[0].Harnesses, []string{"codex"}) || got[0].Waiting != 2 {
		t.Fatalf("blocked = %+v", got)
	}
	if got := waitingFor(review.ID); !slices.Equal(got, []string{"codex"}) {
		t.Fatalf("waiting_for = %v", got)
	}
	if got := waitingFor(implement.ID); got != nil {
		t.Fatalf("a job an online runner can take waits for no harness: %v", got)
	}
	if err := f.store.RecordRunner(t.Context(), "laptop", "local", []string{protocol.StepsHarness, "claude", "codex"}, 1, true); err != nil {
		t.Fatal(err)
	}
	if got := blocked(); len(got) != 0 {
		t.Fatalf("blocked = %+v", got)
	}
}

func TestMissingHarnessesNamesWhatTheClosestRunnerLacks(t *testing.T) {
	probed := func(harnesses ...string) tickets.Runner { return tickets.Runner{Harnesses: harnesses, Probed: true} }
	needs := []string{"agy", "claude", "codex"}
	for _, test := range []struct {
		name   string
		online []tickets.Runner
		want   []string
	}{
		{"no runner online", nil, nil},
		{"a runner has them all", []tickets.Runner{probed("claude"), probed("agy", "claude", "codex")}, nil},
		{"a runner that did not look may have them", []tickets.Runner{probed(), {}}, nil},
		{"the closest runner", []tickets.Runner{probed("claude"), probed("agy", "claude"), probed("codex")}, []string{"codex"}},
		{"the first of equals", []tickets.Runner{probed("claude"), probed("codex")}, []string{"agy", "codex"}},
	} {
		if got := missingHarnesses(test.online, needs); !slices.Equal(got, test.want) {
			t.Errorf("%s: missing = %v, want %v", test.name, got, test.want)
		}
	}
	if got := missingHarnesses([]tickets.Runner{probed()}, nil); got != nil {
		t.Errorf("a job that needs nothing: missing = %v", got)
	}
}
