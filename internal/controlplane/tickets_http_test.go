package controlplane

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/sources"
	"github.com/owainlewis/machinist/internal/tickets"
	"github.com/owainlewis/machinist/internal/triggers"
)

func (f *fixture) connect(t *testing.T) {
	t.Helper()
	f.server.intake.Repositories = []string{"owner/repo"}
}

func decodeTicket(t *testing.T, body []byte) ticketView {
	t.Helper()
	var view ticketView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return view
}

func (f *fixture) assign(t *testing.T, id string) ticketView {
	t.Helper()
	f.connect(t)
	response, body := f.call(t, http.MethodPost, "/api/v1/tickets", f.token, sources.AssignRequest{RequestID: id, Repo: "owner/repo", Title: "T", Body: "Do it"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	return decodeTicket(t, body)
}

func (f *fixture) ask(t *testing.T, id string) {
	t.Helper()
	if err := f.store.Apply(t.Context(), id, tickets.StateWorking, tickets.Transition{State: tickets.StateNeedsYou, Message: "Which database?"}); err != nil {
		t.Fatal(err)
	}
}

func TestCreateTicketReturns200WhenItExisted(t *testing.T) {
	f := newFixture(t)
	first := f.assign(t, "r1")
	if first.CreatedBy != "runner-1" || first.State != tickets.StateWorking {
		t.Fatalf("ticket = %+v", first)
	}
	response, body := f.call(t, http.MethodPost, "/api/v1/tickets", f.token, sources.AssignRequest{RequestID: "r1", Repo: "owner/repo", Title: "T", Body: "Do it"})
	if response.StatusCode != http.StatusOK || decodeTicket(t, body).ID != first.ID {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
}

func TestCreateTicketBadRequest(t *testing.T) {
	f := newFixture(t)
	f.connect(t)
	response, body := f.call(t, http.MethodPost, "/api/v1/tickets", f.token, sources.AssignRequest{RequestID: "r1", Repo: "elsewhere/repo", Title: "T", Body: "B"})
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "not connected") {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
}

func TestListTicketsFiltersNewestFirst(t *testing.T) {
	f := newFixture(t)
	first := f.assign(t, "r1")
	time.Sleep(2 * time.Millisecond)
	second := f.assign(t, "r2")
	f.ask(t, second.ID)
	list := func(query string) []ticketView {
		t.Helper()
		response, body := f.call(t, http.MethodGet, "/api/v1/tickets"+query, f.token, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", response.StatusCode, body)
		}
		var page struct {
			Tickets []ticketView `json:"tickets"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		return page.Tickets
	}
	if all := list(""); len(all) != 2 || all[0].ID != second.ID || all[1].ID != first.ID || all[0].Question != "Which database?" {
		t.Fatalf("all = %+v", all)
	}
	if waiting := list("?state=needs_you&repo=owner/repo"); len(waiting) != 1 || waiting[0].ID != second.ID {
		t.Fatalf("waiting = %+v", waiting)
	}
	if none := list("?repo=other/repo"); len(none) != 0 {
		t.Fatalf("none = %+v", none)
	}
}

func TestGetTicketReturnsEntriesRunsAndFigures(t *testing.T) {
	f := newFixture(t)
	f.queueRun(t)
	listed, err := f.store.ListTickets(t.Context(), "", "")
	if err != nil || len(listed) != 1 {
		t.Fatalf("listed = %+v, err = %v", listed, err)
	}
	response, body := f.call(t, http.MethodGet, "/api/v1/tickets/"+listed[0].ID, f.token, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	var detail struct {
		Ticket  ticketView      `json:"ticket"`
		Entries []tickets.Entry `json:"entries"`
		Runs    []tickets.Run   `json:"runs"`
		Figures tickets.Figures `json:"figures"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Ticket.ID != listed[0].ID || len(detail.Entries) != 1 || len(detail.Runs) != 1 || detail.Runs[0].Runner != "runner-1" || detail.Figures.TicketID != listed[0].ID {
		t.Fatalf("detail = %+v", detail)
	}
	if response, _ := f.call(t, http.MethodGet, "/api/v1/tickets/t_missing", f.token, nil); response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestAnswerConflictsUnlessWaiting(t *testing.T) {
	f := newFixture(t)
	ticket := f.assign(t, "r1")
	path := "/api/v1/tickets/" + ticket.ID + "/answer"
	if response, _ := f.call(t, http.MethodPost, path, f.token, map[string]string{"text": "Too soon"}); response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d", response.StatusCode)
	}
	f.ask(t, ticket.ID)
	if response, _ := f.call(t, http.MethodPost, path, f.token, map[string]string{"text": " "}); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", response.StatusCode)
	}
	response, body := f.call(t, http.MethodPost, path, f.token, map[string]string{"text": "Postgres"})
	if response.StatusCode != http.StatusOK || decodeTicket(t, body).State != tickets.StateWorking {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	if response, _ := f.call(t, http.MethodPost, "/api/v1/tickets/t_missing/answer", f.token, map[string]string{"text": "x"}); response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestCancelConflictsOnceEnded(t *testing.T) {
	f := newFixture(t)
	ticket := f.assign(t, "r1")
	path := "/api/v1/tickets/" + ticket.ID + "/cancel"
	response, body := f.call(t, http.MethodPost, path, f.token, nil)
	if response.StatusCode != http.StatusOK || decodeTicket(t, body).State != tickets.StateCancelled {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	if response, _ := f.call(t, http.MethodPost, path, f.token, nil); response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestRunEventsPage(t *testing.T) {
	f := newFixture(t)
	run := f.queueRun(t)
	if response, body := f.call(t, http.MethodPost, "/api/v1/runs/"+run.RunID+"/events", f.token, map[string]any{"events": []map[string]any{{"sequence": 1, "type": "assistant"}, {"sequence": 2, "type": "assistant"}}}); response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	response, body := f.call(t, http.MethodGet, "/api/v1/runs/"+run.RunID+"/events?after=1", f.token, nil)
	var page struct {
		Cursor int64             `json:"cursor"`
		Events []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(body, &page); err != nil || response.StatusCode != http.StatusOK || page.Cursor != 2 || len(page.Events) != 1 {
		t.Fatalf("status = %d, page = %+v, err = %v", response.StatusCode, page, err)
	}
	if response, _ := f.call(t, http.MethodGet, "/api/v1/runs/"+run.RunID+"/events?after=x", f.token, nil); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if response, _ := f.call(t, http.MethodGet, "/api/v1/runs/run_missing/events", f.token, nil); response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestLedgerRoute(t *testing.T) {
	f := newFixture(t)
	ticket := f.assign(t, "r1")
	merged := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if err := f.store.PRMerged(t.Context(), ticket.ID, 2, merged); err != nil {
		t.Fatal(err)
	}
	read := func(query string) tickets.Ledger {
		t.Helper()
		response, body := f.call(t, http.MethodGet, "/api/v1/ledger"+query, f.token, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", response.StatusCode, body)
		}
		var ledger tickets.Ledger
		if err := json.Unmarshal(body, &ledger); err != nil {
			t.Fatal(err)
		}
		return ledger
	}
	if ledger := read("?repo=owner/repo"); ledger.MergedPRs != 1 || ledger.HumanCommits != 2 {
		t.Fatalf("ledger = %+v", ledger)
	}
	if ledger := read("?since=2026-06-01T00:00:00Z"); ledger.MergedPRs != 0 {
		t.Fatalf("ledger = %+v", ledger)
	}
	if response, _ := f.call(t, http.MethodGet, "/api/v1/ledger?since=yesterday", f.token, nil); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestUsageRoute(t *testing.T) {
	f := newFixture(t)
	response, body := f.call(t, http.MethodGet, "/api/v1/usage", f.token, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	var usage tickets.Usage
	if err := json.Unmarshal(body, &usage); err != nil {
		t.Fatal(err)
	}
	if len(usage.Days) != 14 || usage.Days[13].Day != time.Now().UTC().Format(time.DateOnly) {
		t.Fatalf("usage = %+v", usage)
	}
	if response, _ := f.call(t, http.MethodGet, "/api/v1/usage", "", nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestAutomationRoutes(t *testing.T) {
	f := newFixture(t)
	cron, err := triggers.ParseCron("0 9 * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	f.server.schedule.Automations = []factory.Automation{{Name: "nightly", Repo: "owner/repo", Agent: "default", Prompt: "Do the nightly work", Cron: cron}}
	response, body := f.call(t, http.MethodGet, "/api/v1/automations", f.token, nil)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"next_due"`) || strings.Contains(string(body), "last_ticket") {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	response, body = f.call(t, http.MethodPost, "/api/v1/automations/nightly/run", f.token, nil)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	first := decodeTicket(t, body)
	if first.Source != "schedule:nightly" || first.CreatedBy != "runner-1" {
		t.Fatalf("ticket = %+v", first)
	}
	response, body = f.call(t, http.MethodPost, "/api/v1/automations/nightly/run", f.token, nil)
	if response.StatusCode != http.StatusOK || decodeTicket(t, body).ID != first.ID {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	if response, _ := f.call(t, http.MethodPost, "/api/v1/automations/missing/run", f.token, nil); response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", response.StatusCode)
	}
	response, body = f.call(t, http.MethodGet, "/api/v1/automations", f.token, nil)
	if !strings.Contains(string(body), first.ID) {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
}
