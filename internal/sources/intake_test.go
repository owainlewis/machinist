package sources

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/fakegh"
	"github.com/owainlewis/machinist/internal/ghclient"
	"github.com/owainlewis/machinist/internal/tickets"
)

func newIntake(t *testing.T) (*Intake, *fakegh.GitHub) {
	t.Helper()
	github := fakegh.Install(t, bot)
	store, err := tickets.Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &Intake{Client: ghclient.New(), Store: store, DefaultAgent: "default", Repositories: []string{"owner/repo"}}, github
}

func TestAssignFromTextHasNoIssue(t *testing.T) {
	intake, github := newIntake(t)
	ticket, created, err := intake.Assign(t.Context(), AssignRequest{RequestID: "r1", Repo: "owner/repo", Title: "Add a thing", Body: "All the detail"}, "alice")
	if err != nil || !created {
		t.Fatalf("created = %v, err = %v", created, err)
	}
	if ticket.Source != "api" || ticket.IssueNumber != 0 || ticket.Title != "Add a thing" || ticket.CreatedBy != "alice" || ticket.Agent != "default" {
		t.Fatalf("ticket = %+v", ticket)
	}
	if len(github.Calls()) != 0 {
		t.Fatalf("calls = %v", github.Calls())
	}
}

func TestAssignFromIssueReadsIt(t *testing.T) {
	intake, github := newIntake(t)
	github.SetIssue(5, "Broken login", "Login fails on Safari")
	ticket, created, err := intake.Assign(t.Context(), AssignRequest{RequestID: "r1", Repo: "OWNER/repo", IssueNumber: 5}, "alice")
	if err != nil || !created || ticket.IssueNumber != 5 || ticket.Title != "Broken login" || ticket.Repo != "owner/repo" {
		t.Fatalf("ticket = %+v, created = %v, err = %v", ticket, created, err)
	}
	entries, _ := intake.Store.Entries(t.Context(), ticket.ID)
	if !strings.Contains(entries[0].Body, "Login fails on Safari") || entries[1].Kind != tickets.EntryMessage {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestRetriedAssignReturnsTheSameTicketWithoutGitHub(t *testing.T) {
	intake, github := newIntake(t)
	first, _, err := intake.Assign(t.Context(), AssignRequest{RequestID: "r1", Repo: "owner/repo", IssueNumber: 5}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	calls := len(github.Calls())
	again, created, err := intake.Assign(t.Context(), AssignRequest{RequestID: "r1", Repo: "owner/repo", IssueNumber: 5}, "alice")
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("again = %+v, created = %v, err = %v", again, created, err)
	}
	other, created, err := intake.Assign(t.Context(), AssignRequest{RequestID: "r2", Repo: "owner/repo", IssueNumber: 5}, "bob")
	if err != nil || created || other.ID != first.ID {
		t.Fatalf("other = %+v, created = %v, err = %v", other, created, err)
	}
	if len(github.Calls()) != calls {
		t.Fatalf("GitHub was called again: %v", github.Calls()[calls:])
	}
}

func TestRetriedTextAssignAfterTheTicketEndedReturnsIt(t *testing.T) {
	intake, _ := newIntake(t)
	first, _, err := intake.Assign(t.Context(), AssignRequest{RequestID: "r1", Repo: "owner/repo", Title: "T", Body: "B"}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := intake.Store.Cancel(t.Context(), first.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	again, created, err := intake.Assign(t.Context(), AssignRequest{RequestID: "r1", Repo: "owner/repo", Title: "T", Body: "B"}, "alice")
	if err != nil || created || again.ID != first.ID || again.State != tickets.StateCancelled {
		t.Fatalf("again = %+v, created = %v, err = %v", again, created, err)
	}
}

func TestAssignRejectsBadRequests(t *testing.T) {
	intake, github := newIntake(t)
	github.SetIssuePR(9)
	github.RemoveIssue(404)
	for name, request := range map[string]AssignRequest{
		"no request id":    {Repo: "owner/repo", Title: "T", Body: "B"},
		"long request id":  {RequestID: strings.Repeat("x", maxRequestIDBytes+1), Repo: "owner/repo", Title: "T", Body: "B"},
		"not connected":    {RequestID: "r", Repo: "other/repo", Title: "T", Body: "B"},
		"nothing":          {RequestID: "r", Repo: "owner/repo"},
		"title only":       {RequestID: "r", Repo: "owner/repo", Title: "T"},
		"body only":        {RequestID: "r", Repo: "owner/repo", Body: "B"},
		"blank text":       {RequestID: "r", Repo: "owner/repo", Title: " ", Body: " "},
		"issue and text":   {RequestID: "r", Repo: "owner/repo", IssueNumber: 5, Title: "T", Body: "B"},
		"negative issue":   {RequestID: "r", Repo: "owner/repo", IssueNumber: -1},
		"pull request":     {RequestID: "r", Repo: "owner/repo", IssueNumber: 9},
		"no such an issue": {RequestID: "r", Repo: "owner/repo", IssueNumber: 404},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := intake.Assign(t.Context(), request, "alice"); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestAssignReportsGitHubFailureAsNotTheCallersFault(t *testing.T) {
	intake, github := newIntake(t)
	github.Fail("gh: Bad credentials (HTTP 401)")
	_, _, err := intake.Assign(t.Context(), AssignRequest{RequestID: "r", Repo: "owner/repo", IssueNumber: 5}, "alice")
	if err == nil || errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v", err)
	}
}
