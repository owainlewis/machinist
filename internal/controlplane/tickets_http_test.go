package controlplane

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/sources"
	"github.com/owainlewis/machinist/internal/tickets"
	"github.com/owainlewis/machinist/internal/triggers"
)

func (f *fixture) connect(t *testing.T) {
	t.Helper()
	f.server.intake.Repositories = []string{"owner/repo"}
}

func decodeTicket(t *testing.T, body []byte) tickets.Ticket {
	t.Helper()
	var view tickets.Ticket
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return view
}

func (f *fixture) assign(t *testing.T, id string) tickets.Ticket {
	t.Helper()
	f.connect(t)
	response, body := f.call(t, http.MethodPost, "/api/v1/tickets", f.token, sources.AssignRequest{RequestID: id, Repo: "owner/repo", Title: "T", Body: "Do it"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	return decodeTicket(t, body)
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
	if err := f.store.Apply(t.Context(), second.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateFailed}); err != nil {
		t.Fatal(err)
	}
	list := func(query string) []tickets.Ticket {
		t.Helper()
		response, body := f.call(t, http.MethodGet, "/api/v1/tickets"+query, f.token, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", response.StatusCode, body)
		}
		var page struct {
			Tickets []tickets.Ticket `json:"tickets"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		return page.Tickets
	}
	if all := list(""); len(all) != 2 || all[0].ID != second.ID || all[1].ID != first.ID {
		t.Fatalf("all = %+v", all)
	}
	if failed := list("?state=failed&repo=owner/repo"); len(failed) != 1 || failed[0].ID != second.ID {
		t.Fatalf("failed = %+v", failed)
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
		Ticket  tickets.Ticket  `json:"ticket"`
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
	f.assign(t, "r1")
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
	if ledger := read("?repo=owner/repo&since=2026-06-01T00:00:00Z"); ledger.MergedPRs != 0 {
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
	f.server.schedule.Automations = []factory.Automation{{Name: "nightly", Repo: "owner/repo", Preset: "default", Prompt: "Do the nightly work", Cron: cron}}
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

func TestRerunRouteAndPresetSteps(t *testing.T) {
	f := newFixture(t)
	ticket := f.assign(t, "r1")
	response, body := f.call(t, http.MethodGet, "/api/v1/tickets/"+ticket.ID, f.token, nil)
	var detail struct {
		Steps []string `json:"steps"`
	}
	if err := json.Unmarshal(body, &detail); err != nil || response.StatusCode != http.StatusOK || strings.Join(detail.Steps, ",") != "work" {
		t.Fatalf("status = %d, detail = %+v, err = %v", response.StatusCode, detail, err)
	}
	path := "/api/v1/tickets/" + ticket.ID + "/rerun"
	if response, _ := f.call(t, http.MethodPost, path, f.token, nil); response.StatusCode != http.StatusConflict {
		t.Fatalf("working: status = %d", response.StatusCode)
	}
	if err := f.store.Apply(t.Context(), ticket.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateFailed}); err != nil {
		t.Fatal(err)
	}
	response, body = f.call(t, http.MethodPost, path, f.token, nil)
	if response.StatusCode != http.StatusOK || decodeTicket(t, body).State != tickets.StateWorking {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	if response, _ := f.call(t, http.MethodPost, path, "", nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestStatsRoute(t *testing.T) {
	f := newFixture(t)
	f.assign(t, "r1")
	response, body := f.call(t, http.MethodGet, "/api/v1/stats?days=7", f.token, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	var stats tickets.Stats
	if err := json.Unmarshal(body, &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Days != 7 || len(stats.Daily) != 7 || stats.Jobs.Total != 1 || stats.Jobs.Active != 1 || len(stats.Repos) != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	for _, query := range []string{"?days=0", "?days=91", "?days=week"} {
		if response, _ := f.call(t, http.MethodGet, "/api/v1/stats"+query, f.token, nil); response.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: status = %d", query, response.StatusCode)
		}
	}
	if response, _ := f.call(t, http.MethodGet, "/api/v1/stats", "", nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestPresetsRouteListsEachPresetsSteps(t *testing.T) {
	f := newFixture(t)
	f.server.presets = presetViews(&factory.Factory{Presets: map[string]factory.Preset{
		"spec":    {Name: "spec", Description: "Write a spec.", Steps: []runner.Step{{Name: "checkout"}, {Name: "write"}}},
		"default": {Name: "default", Description: "Do the work.", Steps: []runner.Step{{Name: "work"}}},
	}})
	response, body := f.call(t, http.MethodGet, "/api/v1/presets", f.token, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	want := `{"default":"default","presets":[{"name":"default","description":"Do the work.","steps":["work"]},{"name":"spec","description":"Write a spec.","steps":["checkout","write"]}]}`
	if strings.TrimSpace(string(body)) != want {
		t.Fatalf("body = %s", body)
	}
	if response, _ := f.call(t, http.MethodGet, "/api/v1/presets", "", nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed out status = %d", response.StatusCode)
	}
}
