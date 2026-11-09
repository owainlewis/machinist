package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/owainlewis/machinist/internal/fakegh"
	"github.com/owainlewis/machinist/internal/ghclient"
	"github.com/owainlewis/machinist/internal/sources"
	"github.com/owainlewis/machinist/internal/tickets"
)

type fixture struct {
	store   *tickets.Store
	session *mcp.ClientSession
	address string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	fakegh.Install(t, "machinist-bot")
	store, err := tickets.Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	intake := &sources.Intake{Client: ghclient.New(), Store: store, DefaultAgent: "default", Repositories: []string{"owner/repo"}}
	handler := New(store, intake, "test")
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(WithCaller(r.Context(), "alice")))
	}))
	t.Cleanup(web.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: web.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &fixture{store: store, session: session, address: web.URL}
}

func (f *fixture) call(t *testing.T, tool string, arguments map[string]any, target any) *mcp.CallToolResult {
	t.Helper()
	result, err := f.session.CallTool(context.WithoutCancel(t.Context()), &mcp.CallToolParams{Name: tool, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if target != nil {
		if result.IsError {
			t.Fatalf("%s failed: %+v", tool, result.Content[0])
		}
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, target); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func errorText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if !result.IsError {
		t.Fatalf("result = %+v", result)
	}
	return result.Content[0].(*mcp.TextContent).Text
}

func (f *fixture) assign(t *testing.T, id string) Ticket {
	t.Helper()
	var ticket Ticket
	f.call(t, "assign_ticket", map[string]any{"request_id": id, "repo": "owner/repo", "title": "Add a thing", "body": "Details"}, &ticket)
	return ticket
}

func TestServerListsTheFiveTools(t *testing.T) {
	f := newFixture(t)
	listed, err := f.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "answer_ticket,assign_ticket,cancel_ticket,get_ticket,list_tickets" {
		t.Fatalf("tools = %s", got)
	}
}

func TestAssignReturnsAtOnceAndIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ticket := f.assign(t, "r1")
	if !strings.HasPrefix(ticket.ID, "t_") || ticket.State != tickets.StateWorking || ticket.Link != f.address+"/#/work/"+ticket.ID {
		t.Fatalf("ticket = %+v", ticket)
	}
	if again := f.assign(t, "r1"); again.ID != ticket.ID {
		t.Fatalf("again = %+v", again)
	}
	stored, err := f.store.Ticket(t.Context(), ticket.ID)
	if err != nil || stored.CreatedBy != "alice" {
		t.Fatalf("stored = %+v, err = %v", stored, err)
	}
}

func TestAssignFromAnIssue(t *testing.T) {
	f := newFixture(t)
	var ticket Ticket
	f.call(t, "assign_ticket", map[string]any{"request_id": "r1", "repo": "owner/repo", "issue_number": 123}, &ticket)
	stored, _ := f.store.Ticket(t.Context(), ticket.ID)
	if stored.IssueNumber != 123 {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestAssignErrorsAreToolErrors(t *testing.T) {
	f := newFixture(t)
	result := f.call(t, "assign_ticket", map[string]any{"request_id": "r1", "repo": "other/repo", "title": "T", "body": "B"}, nil)
	if text := errorText(t, result); !strings.Contains(text, "not connected") {
		t.Fatalf("text = %q", text)
	}
}

func TestGetShowsTheQuestionAndPR(t *testing.T) {
	f := newFixture(t)
	ticket := f.assign(t, "r1")
	if err := f.store.Apply(t.Context(), ticket.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateNeedsYou, Message: "Which database?", PRNumber: 42}); err != nil {
		t.Fatal(err)
	}
	var got Ticket
	f.call(t, "get_ticket", map[string]any{"id": ticket.ID}, &got)
	if got.State != tickets.StateNeedsYou || got.Question != "Which database?" || got.PR != 42 || got.PRLink != "https://github.com/owner/repo/pull/42" {
		t.Fatalf("ticket = %+v", got)
	}
}

func TestGetUnknownTicket(t *testing.T) {
	f := newFixture(t)
	if text := errorText(t, f.call(t, "get_ticket", map[string]any{"id": "t_missing"}, nil)); text != "ticket not found" {
		t.Fatalf("text = %q", text)
	}
}

func TestAnswerContinuesAndNeedsAWaitingTicket(t *testing.T) {
	f := newFixture(t)
	ticket := f.assign(t, "r1")
	if text := errorText(t, f.call(t, "answer_ticket", map[string]any{"id": ticket.ID, "text": "Too soon"}, nil)); !strings.Contains(text, "not waiting") {
		t.Fatalf("text = %q", text)
	}
	if err := f.store.Apply(t.Context(), ticket.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateNeedsYou, Message: "Which?"}); err != nil {
		t.Fatal(err)
	}
	var got Ticket
	f.call(t, "answer_ticket", map[string]any{"id": ticket.ID, "text": "Postgres"}, &got)
	if got.State != tickets.StateWorking || got.Question != "" {
		t.Fatalf("ticket = %+v", got)
	}
	entries, _ := f.store.Entries(t.Context(), ticket.ID)
	if last := entries[len(entries)-1]; last.Author != "alice" || last.Body != "Postgres" {
		t.Fatalf("entry = %+v", last)
	}
}

func TestCancelNamesTheCaller(t *testing.T) {
	f := newFixture(t)
	ticket := f.assign(t, "r1")
	var got Ticket
	f.call(t, "cancel_ticket", map[string]any{"id": ticket.ID}, &got)
	if got.State != tickets.StateCancelled {
		t.Fatalf("ticket = %+v", got)
	}
	entries, _ := f.store.Entries(t.Context(), ticket.ID)
	if last := entries[len(entries)-1]; last.Body != "Cancelled by alice" {
		t.Fatalf("entry = %+v", last)
	}
	if text := errorText(t, f.call(t, "cancel_ticket", map[string]any{"id": ticket.ID}, nil)); !strings.Contains(text, "ended") {
		t.Fatalf("text = %q", text)
	}
}

func TestListFilters(t *testing.T) {
	f := newFixture(t)
	for index := range 3 {
		f.assign(t, fmt.Sprintf("r%d", index))
	}
	first := f.assign(t, "cancel-me")
	if _, err := f.store.Cancel(t.Context(), first.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	var list TicketList
	f.call(t, "list_tickets", map[string]any{}, &list)
	if len(list.Tickets) != 4 {
		t.Fatalf("list = %+v", list)
	}
	f.call(t, "list_tickets", map[string]any{"state": tickets.StateCancelled, "repo": "owner/repo"}, &list)
	if len(list.Tickets) != 1 || list.Tickets[0].ID != first.ID {
		t.Fatalf("list = %+v", list)
	}
	var empty TicketList
	f.call(t, "list_tickets", map[string]any{"repo": "other/repo"}, &empty)
	if len(empty.Tickets) != 0 {
		t.Fatalf("list = %+v", empty)
	}
}
