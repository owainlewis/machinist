package controlplane

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/tickets"
)

func TestPollReturnsNoContentWithoutWork(t *testing.T) {
	f := newFixture(t)
	response, body := f.call(t, http.MethodPost, "/api/v1/runners/poll", f.token, protocol.PollRequest{Harnesses: []string{"claude"}})
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
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"name":"runner-1"`) || !strings.Contains(string(body), `"harnesses":["claude"]`) {
		t.Fatalf("%d: %s", response.StatusCode, body)
	}
}

func TestRunnersListShowsSlotsJobsAndQueue(t *testing.T) {
	f := newFixture(t)
	run := f.queueRun(t)
	request := protocol.PollRequest{Harnesses: []string{"claude"}, Kind: "kubernetes", Slots: 4}
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
