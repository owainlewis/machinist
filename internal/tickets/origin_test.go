package tickets

import (
	"path/filepath"
	"testing"
)

func TestCreateTicketRecordsItsSourceAndLink(t *testing.T) {
	store := openStore(t)
	linear := request("linear:abc")
	linear.Source, linear.Origin, linear.SourceURL = "linear", "linear", "https://linear.app/team/issue/ENG-1/fix"
	created, _, err := store.CreateTicket(t.Context(), linear)
	if err != nil {
		t.Fatal(err)
	}
	if got := reload(t, store, created.ID); got.Origin != "linear" || got.SourceURL != "https://linear.app/team/issue/ENG-1/fix" {
		t.Fatalf("source = %q, link = %q", got.Origin, got.SourceURL)
	}

	// A link that is not http or https is never stored, so the UI cannot be
	// made to run script.
	bad := request("linear:bad")
	bad.Source, bad.Origin, bad.SourceURL = "linear", "linear", "javascript:alert(1)"
	created, _, err = store.CreateTicket(t.Context(), bad)
	if err != nil {
		t.Fatal(err)
	}
	if got := reload(t, store, created.ID); got.SourceURL != "" {
		t.Fatalf("link = %q", got.SourceURL)
	}

	// With no origin given, it is worked out from the intake and the key.
	if got := createTicket(t, store, "api:plain"); got.Origin != OriginAPI {
		t.Fatalf("source = %q", got.Origin)
	}
}

func TestListTicketsFiltersBySource(t *testing.T) {
	store := openStore(t)
	createTicket(t, store, "api:1")
	linear := request("linear:1")
	linear.Source, linear.Origin = "linear", "linear"
	want, _, err := store.CreateTicket(t.Context(), linear)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := store.ListTickets(t.Context(), TicketFilter{Source: "linear"})
	if err != nil || len(listed) != 1 || listed[0].ID != want.ID {
		t.Fatalf("listed = %+v, err = %v", listed, err)
	}
	if listed, _ := store.ListTickets(t.Context(), TicketFilter{Source: "jira"}); len(listed) != 0 {
		t.Fatalf("listed = %+v", listed)
	}
	if listed, _ := store.ListTickets(t.Context(), TicketFilter{}); len(listed) != 2 {
		t.Fatalf("listed = %+v", listed)
	}
}

// A database from before Machinist recorded sources gets one for every job,
// worked out from what it kept.
func TestOpenGivesExistingJobsASource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tickets.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	make := func(ticket NewTicket) string {
		t.Helper()
		created, _, err := store.CreateTicket(t.Context(), ticket)
		if err != nil {
			t.Fatal(err)
		}
		return created.ID
	}
	web := request("run:ui-0123abcd")
	web.Source = "run"
	curl := request("run:deploy-42")
	curl.Source = "run"
	issue := request("api:issue")
	issue.IssueNumber = 7
	box := request("api:box")
	nightly := request("schedule:nightly:2026-11-20T00:00:00Z")
	nightly.Source = "schedule:nightly"
	linear := request("linear:abc")
	linear.Source = "linear"
	linear.Body = "Fix the thing\n\nIt is broken.\n\nIssue ENG-9: https://linear.app/team/issue/ENG-9/fix"
	jira := request("jira:abc")
	jira.Source = "jira"
	jira.Body = "Fix the thing\n\nIssue OPS-1: javascript:alert(1)"
	ids := map[string]string{
		"web": make(web), "curl": make(curl), "issue": make(issue), "box": make(box),
		"nightly": make(nightly), "linear": make(linear), "jira": make(jira),
	}
	for _, statement := range []string{`ALTER TABLE tickets DROP COLUMN origin`, `ALTER TABLE tickets DROP COLUMN source_url`} {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for name, want := range map[string][2]string{
		"web":     {OriginWeb, ""},
		"curl":    {OriginAPI, ""},
		"issue":   {OriginAPI, "https://github.com/owner/repo/issues/7"},
		"box":     {OriginAPI, ""},
		"nightly": {OriginSchedule, ""},
		"linear":  {"linear", "https://linear.app/team/issue/ENG-9/fix"},
		"jira":    {"jira", ""},
	} {
		got := reload(t, reopened, ids[name])
		if got.Origin != want[0] || got.SourceURL != want[1] {
			t.Errorf("%s: source = %q, link = %q, want %q, %q", name, got.Origin, got.SourceURL, want[0], want[1])
		}
	}
}
