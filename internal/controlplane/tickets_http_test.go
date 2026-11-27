package controlplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/harness"
	"github.com/owainlewis/machinist/internal/protocol"
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
	if _, _, err := f.store.Apply(t.Context(), second.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateFailed}); err != nil {
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

func TestListTicketsPaginationAndBoardView(t *testing.T) {
	f := newFixture(t)
	f.connect(t)

	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	tick := 0
	f.store.SetClock(func() time.Time {
		tick++
		return start.Add(time.Duration(tick) * time.Second)
	})

	// Create 3 active tickets first, then 207 finished tickets (210 total, past the old 200 cliff).
	activeIDs := map[string]bool{}
	for i := range 3 {
		created, _, err := f.store.CreateTicket(t.Context(), tickets.NewTicket{
			Source: "api", Origin: "api", Repo: "owner/repo", Title: fmt.Sprintf("Active %d", i), Body: "Do it", Agent: "default", CreatedBy: "alice", SourceKey: fmt.Sprintf("api:a:%d", i),
		})
		if err != nil {
			t.Fatal(err)
		}
		activeIDs[created.ID] = true
	}
	for i := range 207 {
		created, _, err := f.store.CreateTicket(t.Context(), tickets.NewTicket{
			Source: "api", Origin: "api", Repo: "owner/repo", Title: fmt.Sprintf("Done %d", i), Body: "Do it", Agent: "default", CreatedBy: "alice", SourceKey: fmt.Sprintf("api:d:%d", i),
		})
		if err != nil {
			t.Fatal(err)
		}
		state := tickets.StateDone
		if i%2 == 1 {
			state = tickets.StateFailed
		}
		if _, _, err := f.store.Apply(t.Context(), created.ID, tickets.StateWorking, tickets.Transition{State: state}); err != nil {
			t.Fatal(err)
		}
	}

	getPage := func(query string) tickets.TicketPage {
		t.Helper()
		response, body := f.call(t, http.MethodGet, "/api/v1/tickets"+query, f.token, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d: %s", query, response.StatusCode, body)
		}
		var page tickets.TicketPage
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		return page
	}

	// Default call preserves the 200-ticket default while reporting total and has_more.
	def := getPage("")
	if len(def.Tickets) != 200 || def.Total != 210 || def.Limit != 200 || def.Offset != 0 || !def.HasMore {
		t.Fatalf("default = len=%d total=%d limit=%d offset=%d has_more=%v", len(def.Tickets), def.Total, def.Limit, def.Offset, def.HasMore)
	}

	// Paginating with limit=50 reaches items 201..210 cleanly.
	page5 := getPage("?limit=50&offset=200")
	if len(page5.Tickets) != 10 || page5.Total != 210 || page5.Limit != 50 || page5.Offset != 200 || page5.HasMore {
		t.Fatalf("page5 = len=%d total=%d limit=%d offset=%d has_more=%v", len(page5.Tickets), page5.Total, page5.Limit, page5.Offset, page5.HasMore)
	}
	// Multi-state filter works via comma-separated states.
	doneOnly := getPage("?state=done,pr_ready&limit=50&offset=0")
	if doneOnly.Total != 104 || len(doneOnly.Tickets) != 50 || !doneOnly.HasMore {
		t.Fatalf("doneOnly = len=%d total=%d has_more=%v", len(doneOnly.Tickets), doneOnly.Total, doneOnly.HasMore)
	}

	// Board view returns all 3 active tickets (even though older than 200 finished jobs)
	// plus exactly 20 finished tickets, with done_total=207 and has_more=true.
	board := getPage("?view=board")
	if board.DoneTotal == nil || *board.DoneTotal != 207 || board.DoneLimit == nil || *board.DoneLimit != 20 || !board.HasMore {
		t.Fatalf("board = done_total=%v done_limit=%v has_more=%v", board.DoneTotal, board.DoneLimit, board.HasMore)
	}
	if len(board.Tickets) != 23 || board.Total != 210 {
		t.Fatalf("board len=%d total=%d, want 23 and 210", len(board.Tickets), board.Total)
	}
	seenActive := 0
	for _, ticket := range board.Tickets {
		if activeIDs[ticket.ID] {
			seenActive++
		}
	}
	if seenActive != 3 {
		t.Fatalf("board active = %d, want 3", seenActive)
	}

	// Invalid pagination or view parameters return 400 Bad Request.
	for _, bad := range []string{"?limit=0", "?limit=-5", "?limit=201", "?limit=abc", "?offset=-1", "?offset=abc", "?view=board&done_limit=0", "?view=board&done_limit=201", "?view=board&done_limit=x", "?sort=random", "?view=grid"} {
		if response, body := f.call(t, http.MethodGet, "/api/v1/tickets"+bad, f.token, nil); response.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: status = %d: %s", bad, response.StatusCode, body)
		}
	}
}

func TestGetTicketReturnsEntriesRunsAndFigures(t *testing.T) {
	f := newFixture(t)
	f.queueRun(t)
	listed, err := f.store.ListTickets(t.Context(), tickets.TicketFilter{})
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

// A run that could not push its change sends it as a patch, which the job page
// downloads. A patch is larger than other requests, so its completion is too.
func TestARunsSavedPatchDownloads(t *testing.T) {
	f := newFixture(t)
	run := f.queueRun(t)
	path := "/api/v1/runs/" + run.RunID + "/patch"
	if response, _ := f.call(t, http.MethodGet, path, f.token, nil); response.StatusCode != http.StatusNotFound {
		t.Fatalf("status before completion = %d", response.StatusCode)
	}
	patch := []byte("Subject: [PATCH] Use Postgres\n" + strings.Repeat("+x\n", 1<<20))
	if response, body := f.call(t, http.MethodPost, "/api/v1/runs/"+run.RunID+"/complete", f.token, protocol.Completion{State: tickets.RunFailed, Error: "step publish: GitHub rejected the push", Patch: patch}); response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	response, body := f.call(t, http.MethodGet, path, f.token, nil)
	if response.StatusCode != http.StatusOK || !bytes.Equal(body, patch) {
		t.Fatalf("status = %d, %d bytes", response.StatusCode, len(body))
	}
	if got := response.Header.Get("Content-Disposition"); got != `attachment; filename="`+run.TicketID+`.patch"` {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if response, _ := f.call(t, http.MethodGet, "/api/v1/runs/run_missing/patch", f.token, nil); response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestPatchNameKeepsOnlySafeCharacters(t *testing.T) {
	if got := patchName(`t_1"; x=y`); got != "t_1xy.patch" {
		t.Fatalf("name = %q", got)
	}
	if got := patchName(`"`); got != "change.patch" {
		t.Fatalf("name = %q", got)
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
	var listed struct {
		Automations []sources.AutomationStatus `json:"automations"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Automations) != 1 || listed.Automations[0].Name != "nightly" || listed.Automations[0].Repo != "owner/repo" || listed.Automations[0].Agent != "default" || listed.Automations[0].Schedule != "0 9 * * *" || listed.Automations[0].Timezone != "UTC" || listed.Automations[0].Prompt != "Do the nightly work" {
		t.Fatalf("automations = %+v", listed.Automations)
	}

	response, body = f.call(t, http.MethodPost, "/api/v1/automations/nightly/run", f.token, nil)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	first := decodeTicket(t, body)
	if first.Source != "schedule:nightly" || first.CreatedBy != "runner-1" {
		t.Fatalf("ticket = %+v", first)
	}
	entries, err := f.store.Entries(t.Context(), first.ID)
	if err != nil || len(entries) != 1 || entries[0].Body != "Do the nightly work" {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
	response, body = f.call(t, http.MethodPost, "/api/v1/automations/nightly/run", f.token, nil)
	if response.StatusCode != http.StatusOK || decodeTicket(t, body).ID != first.ID {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	if response, _ := f.call(t, http.MethodPost, "/api/v1/automations/missing/run", f.token, nil); response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if _, _, err := f.store.Apply(t.Context(), first.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateDone, Message: "Checked 3 PRs"}); err != nil {
		t.Fatal(err)
	}
	response, body = f.call(t, http.MethodGet, "/api/v1/automations", f.token, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Automations) != 1 || listed.Automations[0].LastTicket == nil || listed.Automations[0].LastTicket.ID != first.ID || listed.Automations[0].LastTicket.State != tickets.StateDone || listed.Automations[0].LastTicket.Outcome != "Checked 3 PRs" {
		t.Fatalf("automations = %+v", listed.Automations)
	}

	// An edited prompt overrides this run only without changing the saved prompt.
	response, body = f.call(t, http.MethodPost, "/api/v1/automations/nightly/run", f.token, map[string]string{"prompt": "  Focus only on flaky tests  "})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("edited run status = %d: %s", response.StatusCode, body)
	}
	edited := decodeTicket(t, body)
	if edited.ID == first.ID {
		t.Fatalf("expected a new ticket, got %s", edited.ID)
	}
	entries, err = f.store.Entries(t.Context(), edited.ID)
	if err != nil || len(entries) != 1 || entries[0].Body != "Focus only on flaky tests" {
		t.Fatalf("edited entries = %+v, err = %v", entries, err)
	}
	response, body = f.call(t, http.MethodGet, "/api/v1/automations", f.token, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Automations[0].Prompt != "Do the nightly work" {
		t.Fatalf("saved prompt changed to %q", listed.Automations[0].Prompt)
	}

	// End the edited run, then confirm an empty JSON object falls back to the saved prompt.
	if _, err := f.store.Cancel(t.Context(), edited.ID, "test"); err != nil {
		t.Fatal(err)
	}
	response, body = f.call(t, http.MethodPost, "/api/v1/automations/nightly/run", f.token, map[string]any{})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("empty object run status = %d: %s", response.StatusCode, body)
	}
	fallback := decodeTicket(t, body)
	entries, err = f.store.Entries(t.Context(), fallback.ID)
	if err != nil || len(entries) != 1 || entries[0].Body != "Do the nightly work" {
		t.Fatalf("fallback entries = %+v, err = %v", entries, err)
	}

	// Reject a blank prompt override or malformed JSON body.
	if response, body := f.call(t, http.MethodPost, "/api/v1/automations/nightly/run", f.token, map[string]string{"prompt": "   "}); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("blank prompt status = %d: %s", response.StatusCode, body)
	}
	badReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.http.URL+"/api/v1/automations/nightly/run", strings.NewReader("{bad"))
	if err != nil {
		t.Fatal(err)
	}
	badReq.Header.Set("Authorization", "Bearer "+f.token)
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatal(err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed json status = %d", badResp.StatusCode)
	}
}

func TestAutomationCRUDAndRunOverrides(t *testing.T) {
	f := newFixture(t)
	if err := f.store.AddProject(t.Context(), "owner/repo", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddProject(t.Context(), "owner/other", "alice"); err != nil {
		t.Fatal(err)
	}
	cron, err := triggers.ParseCron("0 9 * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	f.server.schedule.Automations = []factory.Automation{{Name: "from-file", Repo: "owner/repo", Preset: "default", Prompt: "File prompt", Cron: cron}}

	// Reject colliding with a file automation or invalid inputs.
	if resp, body := f.call(t, http.MethodPost, "/api/v1/automations", f.token, map[string]string{
		"name": "from-file", "repo": "owner/repo", "preset": "default", "prompt": "Clash",
	}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("file clash = %d %s", resp.StatusCode, body)
	}
	if resp, body := f.call(t, http.MethodPost, "/api/v1/automations", f.token, map[string]string{
		"name": "bad-tz", "repo": "owner/repo", "preset": "default", "prompt": "Go", "timezone": "UTC",
	}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("timezone without schedule = %d %s", resp.StatusCode, body)
	}
	if resp, body := f.call(t, http.MethodPost, "/api/v1/automations", f.token, map[string]string{
		"name": "bad-harness", "repo": "owner/repo", "preset": "default", "prompt": "Go", "harness": "unknown",
	}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad harness = %d %s", resp.StatusCode, body)
	}

	// Create a manual-only automation with harness and model.
	resp, body := f.call(t, http.MethodPost, "/api/v1/automations", f.token, map[string]string{
		"name":    "codex-audit",
		"repo":    "owner/repo",
		"preset":  "default",
		"harness": "codex",
		"model":   "gpt-5-codex",
		"prompt":  "Audit the repo.",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %s", resp.StatusCode, body)
	}

	// List returns both file (editable=false) and UI (editable=true, no next_due).
	resp, body = f.call(t, http.MethodGet, "/api/v1/automations", f.token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list = %d %s", resp.StatusCode, body)
	}
	var listed struct {
		Automations []sources.AutomationStatus `json:"automations"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Automations) != 2 {
		t.Fatalf("automations = %+v", listed.Automations)
	}
	uiItem, fileItem := listed.Automations[0], listed.Automations[1]
	if uiItem.Name != "codex-audit" || !uiItem.Editable || uiItem.NextDue != nil || uiItem.Harness != "codex" || uiItem.Model != "gpt-5-codex" {
		t.Fatalf("uiItem = %+v", uiItem)
	}
	if fileItem.Name != "from-file" || fileItem.Editable || fileItem.NextDue == nil {
		t.Fatalf("fileItem = %+v", fileItem)
	}

	// Update UI automation; reject editing or deleting file automation.
	resp, body = f.call(t, http.MethodPut, "/api/v1/automations/codex-audit", f.token, map[string]string{
		"repo":     "owner/other",
		"preset":   "default",
		"harness":  "claude",
		"model":    "opus",
		"schedule": "0 8 * * 1-5",
		"timezone": "UTC",
		"prompt":   "Updated audit prompt.",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update = %d %s", resp.StatusCode, body)
	}
	if resp, body := f.call(t, http.MethodPut, "/api/v1/automations/from-file", f.token, map[string]string{
		"repo": "owner/repo", "preset": "default", "prompt": "No",
	}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("edit file = %d %s", resp.StatusCode, body)
	}
	if resp, body := f.call(t, http.MethodDelete, "/api/v1/automations/from-file", f.token, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete file = %d %s", resp.StatusCode, body)
	}

	// Run with per-run overrides and verify ticket & job detail reflect them.
	resp, body = f.call(t, http.MethodPost, "/api/v1/automations/codex-audit/run", f.token, map[string]string{
		"repo":    "owner/repo",
		"preset":  "default",
		"harness": "codex",
		"model":   "gpt-5.3-codex",
		"prompt":  "One-off Codex run.",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("run override = %d %s", resp.StatusCode, body)
	}
	started := decodeTicket(t, body)
	if started.Repo != "owner/repo" || started.Harness != "codex" || started.Model != "gpt-5.3-codex" {
		t.Fatalf("started = %+v", started)
	}

	// Delete UI automation.
	if resp, body := f.call(t, http.MethodDelete, "/api/v1/automations/codex-audit", f.token, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d %s", resp.StatusCode, body)
	}
	if resp, body := f.call(t, http.MethodDelete, "/api/v1/automations/codex-audit", f.token, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete = %d %s", resp.StatusCode, body)
	}
}

func TestRerunRouteAndPresetSteps(t *testing.T) {
	f := newFixture(t)
	ticket := f.assign(t, "r1")
	response, body := f.call(t, http.MethodGet, "/api/v1/tickets/"+ticket.ID, f.token, nil)
	var detail struct {
		Steps []stepView `json:"steps"`
	}
	if err := json.Unmarshal(body, &detail); err != nil || response.StatusCode != http.StatusOK || len(detail.Steps) != 1 || detail.Steps[0].Name != "work" {
		t.Fatalf("status = %d, detail = %+v, err = %v", response.StatusCode, detail, err)
	}
	path := "/api/v1/tickets/" + ticket.ID + "/rerun"
	if response, _ := f.call(t, http.MethodPost, path, f.token, nil); response.StatusCode != http.StatusConflict {
		t.Fatalf("working: status = %d", response.StatusCode)
	}
	if _, _, err := f.store.Apply(t.Context(), ticket.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateFailed}); err != nil {
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
	// The dashboard reads these even when nothing has merged yet.
	for _, field := range []string{`"cycle":{"merged":0,`, `"touches":{"replies":0,`, `"prs":{"opened":0,`, `"steps":[]`, `"steps":{}`, `"models":[]`, `"model_steps":[]`, `"month_spend":{`, `"repositories":["owner/repo"]`} {
		if !strings.Contains(string(body), field) {
			t.Fatalf("stats lack %s: %s", field, body)
		}
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

func TestStatsRouteFiltersByRepoAndReportsModelsAndSpend(t *testing.T) {
	f := newFixture(t)
	f.server.intake.Repositories = []string{"owner/app", "owner/api"}
	ctx := t.Context()

	assignRepo := func(id, repo string) tickets.Ticket {
		t.Helper()
		response, body := f.call(t, http.MethodPost, "/api/v1/tickets", f.token, sources.AssignRequest{RequestID: id, Repo: repo, Title: "T " + id, Body: "Do it"})
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d: %s", response.StatusCode, body)
		}
		return decodeTicket(t, body)
	}
	complete := func(ticket tickets.Ticket, merged bool, stages ...protocol.StageResult) {
		t.Helper()
		if _, err := f.store.QueueRun(ctx, ticket.ID, protocol.StepsHarness); err != nil {
			t.Fatal(err)
		}
		claim, err := f.store.ClaimRun(ctx, "runner-1", []string{protocol.StepsHarness}, time.Now().Add(time.Hour), func(cand tickets.Ticket) bool { return cand.ID == ticket.ID })
		if err != nil || claim == nil {
			t.Fatalf("claim = %v, err = %v", claim, err)
		}
		tokens, cost := protocol.Totals(stages)
		now := time.Now().UTC()
		transition := tickets.Transition{State: tickets.StatePRReady, PRNumber: 1, PROpenedAt: &now}
		if merged {
			transition.State = tickets.StateDone
			transition.MergedAt = &now
		}
		if _, _, err := f.store.CompleteRun(ctx, tickets.RunCompletion{RunID: claim.Run.ID, Status: tickets.RunSucceeded, Tokens: tokens, Cost: cost, Stages: stages}, func(tickets.Ticket, string) tickets.Transition {
			return transition
		}); err != nil {
			t.Fatal(err)
		}
	}

	appTicket := assignRepo("r-app", "owner/app")
	apiTicket := assignRepo("r-api", "owner/api")
	tok1, cost1 := int64(10_000), 1.25
	tok2 := int64(25_000)
	complete(appTicket, true,
		protocol.StageResult{Name: "implement", State: tickets.RunSucceeded, DurationMillis: 40_000, Tokens: &tok1, Cost: &cost1, Agents: []protocol.Agent{{Name: "coder", Harness: "claude", Model: "claude-opus-5-5"}}},
		protocol.StageResult{Name: "review", State: tickets.RunSucceeded, DurationMillis: 20_000, Tokens: &tok2, CostUnknown: true, Agents: []protocol.Agent{{Name: "review", Harness: "agy", Model: "gemini-4-neon-high"}}})
	tok3, cost3 := int64(5_000), 0.50
	complete(apiTicket, false,
		protocol.StageResult{Name: "implement", State: tickets.RunSucceeded, DurationMillis: 15_000, Tokens: &tok3, Cost: &cost3, Agents: []protocol.Agent{{Name: "coder", Harness: "claude", Model: "claude-opus-5-5"}}})

	getStats := func(query string) tickets.Stats {
		t.Helper()
		response, body := f.call(t, http.MethodGet, "/api/v1/stats"+query, f.token, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d: %s", query, response.StatusCode, body)
		}
		var stats tickets.Stats
		if err := json.Unmarshal(body, &stats); err != nil {
			t.Fatal(err)
		}
		return stats
	}

	all := getStats("?days=7")
	if all.Jobs.Total != 2 || len(all.Repositories) != 2 || len(all.Models) != 2 || len(all.ModelSteps) != 2 || all.Cost != 1.75 || all.CostUnknown != 1 || all.MonthSpend.Cost != 1.75 || all.CostPerMergedPR == nil || *all.CostPerMergedPR != 1.75 {
		t.Fatalf("all = %+v", all)
	}

	app := getStats("?days=7&repo=owner/app")
	if app.Repo != "owner/app" || len(app.Repositories) != 2 || app.Jobs.Total != 1 || app.Cost != 1.25 || app.CostUnknown != 1 || app.MonthSpend.Cost != 1.25 || app.CostPerMergedPR == nil || *app.CostPerMergedPR != 1.25 || len(app.Models) != 2 {
		t.Fatalf("app = %+v", app)
	}

	api := getStats("?days=7&repo=owner/api")
	if api.Repo != "owner/api" || api.Jobs.Total != 1 || api.Cost != 0.50 || api.CostUnknown != 0 || api.CostPerMergedPR != nil || len(api.Models) != 1 || api.Models[0].Harness != "claude" {
		t.Fatalf("api = %+v", api)
	}
}

func TestPresetsRouteListsEachPresetsSteps(t *testing.T) {
	f := newFixture(t)
	f.server.presets = presetViews(&factory.Factory{
		Presets: map[string]factory.Preset{
			"spec":    {Name: "spec", Description: "Write a spec.", Steps: []runner.Step{{Name: "checkout", Run: "checkout.sh"}, {Name: "write", Run: "machinist agent writer"}}},
			"default": {Name: "default", Description: "Do the work.", Steps: []runner.Step{{Name: "work", Run: "review.sh"}}},
		},
		Agents: map[string]harness.Agent{
			"writer": {Name: "writer", Harness: "claude"},
			"review": {Name: "review", Harness: "agy", Model: "gemini-4-neon-high"},
			"revise": {Name: "revise", Harness: "codex"},
		},
		Files: map[string]string{"scripts/review.sh": "#!/bin/sh\nmachinist agent review\nmachinist   agent revise\nmachinist agent ghost\n"},
	})
	response, body := f.call(t, http.MethodGet, "/api/v1/presets", f.token, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	// Each step names its command, referenced scripts, and the agents it runs.
	want := `{"default":"default","presets":[` +
		`{"name":"default","description":"Do the work.","steps":[{"name":"work","run":"review.sh","scripts":[{"path":"scripts/review.sh","content":"#!/bin/sh\nmachinist agent review\nmachinist   agent revise\nmachinist agent ghost\n"}],"agents":[{"name":"review","harness":"agy","model":"gemini-4-neon-high"},{"name":"revise","harness":"codex"}]}]},` +
		`{"name":"spec","description":"Write a spec.","steps":[{"name":"checkout","run":"checkout.sh","agents":[]},{"name":"write","run":"machinist agent writer","agents":[{"name":"writer","harness":"claude"}]}]}]}`
	if strings.TrimSpace(string(body)) != want {
		t.Fatalf("body = %s\nwant = %s", body, want)
	}
	if response, _ := f.call(t, http.MethodGet, "/api/v1/presets", "", nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed out status = %d", response.StatusCode)
	}
}

func TestPresetsRouteResolvesExtendsAndHarnessModelOverrides(t *testing.T) {
	f := newFixture(t)
	loaded := &factory.Factory{
		Presets: map[string]factory.Preset{
			"default": {
				Name:        "default",
				Description: "Default pipeline.",
				Timeout:     60 * time.Minute,
				Steps: []runner.Step{
					{Name: "checkout", Run: "checkout.sh", TimeoutMillis: (5 * time.Minute).Milliseconds()},
					{Name: "implement", Run: "machinist agent implement"},
					{Name: "review", Run: "review.sh"},
				},
			},
			"neon": {
				Name:        "neon",
				Description: "Default pipeline on Antigravity (Gemini 4 Neon).",
				Extends:     "default",
				Timeout:     60 * time.Minute,
				Harness:     "agy",
				Model:       "gemini-4-neon-high",
				Steps: []runner.Step{
					{Name: "checkout", Run: "checkout.sh", TimeoutMillis: (5 * time.Minute).Milliseconds(), Harness: "agy", Model: "gemini-4-neon-high"},
					{Name: "implement", Run: "machinist agent implement", Harness: "agy", Model: "gemini-4-neon-high"},
					{Name: "review", Run: "review.sh", Harness: "claude", Model: "opus"},
				},
			},
		},
		Agents: map[string]harness.Agent{
			"implement": {Name: "implement", Harness: "claude"},
			"review":    {Name: "review", Harness: "claude", Model: "sonnet"},
		},
		Files: map[string]string{
			"scripts/review.sh": "#!/bin/sh\nmachinist agent review\n",
		},
	}
	f.server.factory = loaded
	f.server.presets = presetViews(loaded)

	response, body := f.call(t, http.MethodGet, "/api/v1/presets", f.token, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	want := `{"default":"default","presets":[` +
		`{"name":"default","description":"Default pipeline.","timeout_millis":3600000,"steps":[` +
		`{"name":"checkout","run":"checkout.sh","timeout_millis":300000,"agents":[]},` +
		`{"name":"implement","run":"machinist agent implement","agents":[{"name":"implement","harness":"claude"}]},` +
		`{"name":"review","run":"review.sh","scripts":[{"path":"scripts/review.sh","content":"#!/bin/sh\nmachinist agent review\n"}],"agents":[{"name":"review","harness":"claude","model":"sonnet"}]}]},` +
		`{"name":"neon","description":"Default pipeline on Antigravity (Gemini 4 Neon).","extends":"default","harness":"agy","model":"gemini-4-neon-high","timeout_millis":3600000,"steps":[` +
		`{"name":"checkout","run":"checkout.sh","timeout_millis":300000,"agents":[]},` +
		`{"name":"implement","run":"machinist agent implement","agents":[{"name":"implement","harness":"agy","model":"gemini-4-neon-high"}]},` +
		`{"name":"review","run":"review.sh","scripts":[{"path":"scripts/review.sh","content":"#!/bin/sh\nmachinist agent review\n"}],"agents":[{"name":"review","harness":"claude","model":"opus"}]}]}]}`
	if strings.TrimSpace(string(body)) != want {
		t.Fatalf("body = %s\nwant = %s", body, want)
	}
}

// ENG-132: Run again straight after Cancel must not revive the cancelled run.
func TestRerunConflictsUntilTheCancelledRunStops(t *testing.T) {
	f := newFixture(t)
	run := f.queueRun(t)
	ticketPath := "/api/v1/tickets/" + run.TicketID
	runPath := "/api/v1/runs/" + run.RunID
	if response, body := f.call(t, http.MethodPost, ticketPath+"/cancel", f.token, nil); response.StatusCode != http.StatusOK {
		t.Fatalf("cancel = %d: %s", response.StatusCode, body)
	}
	if response, body := f.call(t, http.MethodPost, ticketPath+"/rerun", f.token, nil); response.StatusCode != http.StatusConflict {
		t.Fatalf("rerun while the run is still going = %d: %s", response.StatusCode, body)
	}
	response, body := f.call(t, http.MethodPost, runPath+"/heartbeat", f.token, struct{}{})
	var beat protocol.HeartbeatResponse
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &beat) != nil || !beat.Cancel {
		t.Fatalf("heartbeat = %d: %s", response.StatusCode, body)
	}
	completion := protocol.Completion{State: tickets.RunSucceeded, Report: &protocol.Report{Outcome: "no_change", Summary: "Old result"}}
	if response, body := f.call(t, http.MethodPost, runPath+"/complete", f.token, completion); response.StatusCode != http.StatusNoContent {
		t.Fatalf("complete = %d: %s", response.StatusCode, body)
	}
	response, body = f.call(t, http.MethodPost, ticketPath+"/rerun", f.token, nil)
	if response.StatusCode != http.StatusOK || decodeTicket(t, body).State != tickets.StateWorking {
		t.Fatalf("rerun = %d: %s", response.StatusCode, body)
	}
	if report, err := f.store.LastReport(t.Context(), run.TicketID); err != nil || report != "" {
		t.Fatalf("the cancelled run's report landed: %q, %v", report, err)
	}
}

func TestTicketAPIMergeGateJSONSerialization(t *testing.T) {
	f := newFixture(t)
	ticket := f.assign(t, "gate-json-1")

	// Before any merge gate runs, merge_gate_* keys are omitted for backwards compatibility.
	for _, path := range []string{"/api/v1/tickets/" + ticket.ID, "/api/v1/tickets", "/api/v1/tickets?view=board"} {
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

	// Persist a merge-gate verdict and verify /api/v1/tickets/{id}, list, and board responses expose it.
	if _, _, err := f.store.Apply(t.Context(), ticket.ID, tickets.StateWorking, tickets.Transition{
		State:    tickets.StatePRReady,
		PRNumber: 42,
		MergeGate: &tickets.MergeGate{
			Decision: "human_review",
			Risk:     "high",
			Reason:   "Alters authentication token handling.",
		},
	}); err != nil {
		t.Fatal(err)
	}

	response, body := f.call(t, http.MethodGet, "/api/v1/tickets/"+ticket.ID, f.token, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET ticket: status = %d: %s", response.StatusCode, body)
	}
	var detail struct {
		Ticket tickets.Ticket `json:"ticket"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Ticket.MergeGateDecision != "human_review" || detail.Ticket.MergeGateRisk != "high" || detail.Ticket.MergeGateReason != "Alters authentication token handling." {
		t.Fatalf("detail ticket = %+v", detail.Ticket)
	}

	for _, path := range []string{"/api/v1/tickets", "/api/v1/tickets?view=board"} {
		response, body := f.call(t, http.MethodGet, path, f.token, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status = %d: %s", path, response.StatusCode, body)
		}
		var page tickets.TicketPage
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Tickets) != 1 || page.Tickets[0].MergeGateDecision != "human_review" || page.Tickets[0].MergeGateRisk != "high" || page.Tickets[0].MergeGateReason != "Alters authentication token handling." {
			t.Fatalf("GET %s tickets = %+v", path, page.Tickets)
		}
	}
}
