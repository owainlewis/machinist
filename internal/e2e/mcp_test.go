package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/owainlewis/machinist/internal/mcpserver"
	"github.com/owainlewis/machinist/internal/tickets"
)

type bearer struct{ token string }

func (b bearer) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(request)
}

func (s *system) connect(t *testing.T, token string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "coding-session", Version: "1"}, nil)
	return client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   "http://" + s.address + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{token}},
		MaxRetries: -1,
	}, nil)
}

func assignIssue(t *testing.T, session *mcp.ClientSession, requestID string) mcpserver.Ticket {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "assign_ticket", Arguments: map[string]any{"request_id": requestID, "repo": "owner/repo", "issue_number": 5}})
	if err != nil || result.IsError {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var ticket mcpserver.Ticket
	if err := json.Unmarshal(encoded, &ticket); err != nil {
		t.Fatal(err)
	}
	return ticket
}

// CUJ 9.
func TestAssignFromACodingSession(t *testing.T) {
	s := startBuilder(t, 5, true)
	token, err := s.store.CreateToken(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.connect(t, token)
	if err != nil {
		t.Fatal(err)
	}
	assigned := assignIssue(t, session, "session-request-1")
	if assigned.State != tickets.StateWorking || !strings.HasPrefix(assigned.ID, "t_") || !strings.Contains(assigned.Link, s.address+"/#/work/"+assigned.ID) {
		t.Fatalf("ticket = %+v", assigned)
	}
	if again := assignIssue(t, session, "session-request-1"); again.ID != assigned.ID {
		t.Fatalf("a retry made another ticket: %+v", again)
	}

	// Closing the session must not affect the ticket.
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	ready := s.waitForState(t, assigned.ID, tickets.StatePRReady)
	if ready.CreatedBy != "alice" || ready.IssueNumber != 5 || ready.PRNumber != 7 {
		t.Fatalf("ticket = %+v", ready)
	}
	s.waitForPost(t, "Working on this as ticket "+assigned.ID)
	s.waitForPost(t, "https://github.com/owner/repo/pull/7")
	eventually(t, "the reviewer's comment", func() bool { return len(s.github.PRComments()) == 1 })
	if s.posted("Working on this") != 1 {
		t.Fatalf("posted = %+v", s.github.Posted())
	}

	reconnected, err := s.connect(t, token)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close()
	result, err := reconnected.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_ticket", Arguments: map[string]any{"id": assigned.ID}})
	if err != nil || result.IsError {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, `"pr":7`) || !strings.Contains(text, "pull/7") {
		t.Fatalf("text = %s", text)
	}
}

func TestMCPNeedsAValidToken(t *testing.T) {
	s := startBuilder(t, 5, false)
	if session, err := s.connect(t, "wrong"); err == nil {
		session.Close()
		t.Fatal("connected with a bad token")
	}
}
