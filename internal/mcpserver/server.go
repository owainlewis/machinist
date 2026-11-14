// Package mcpserver lets a coding session assign and follow tickets over MCP.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/owainlewis/machinist/internal/sources"
	"github.com/owainlewis/machinist/internal/tickets"
)

type callerKey struct{}

// WithCaller records the name of the token making this request.
func WithCaller(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, callerKey{}, name)
}

// Ticket is what every tool returns.
type Ticket struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Repo     string `json:"repo"`
	Title    string `json:"title"`
	Link     string `json:"link"`
	PR       int    `json:"pr,omitempty"`
	PRLink   string `json:"pr_link,omitempty"`
	Question string `json:"question,omitempty"`
}

// TicketList is what list_tickets returns.
type TicketList struct {
	Tickets []Ticket `json:"tickets"`
}

type assignInput struct {
	RequestID   string `json:"request_id" jsonschema:"A unique ID you choose. Retrying with the same ID returns the same ticket."`
	Repo        string `json:"repo" jsonschema:"The repository, as owner/name."`
	IssueNumber int    `json:"issue_number,omitempty" jsonschema:"The issue to work on. Send this, or both title and body."`
	Title       string `json:"title,omitempty" jsonschema:"A short title, when there is no issue."`
	Body        string `json:"body,omitempty" jsonschema:"The full description of the work, when there is no issue."`
}

type idInput struct {
	ID string `json:"id" jsonschema:"The ticket ID."`
}

type listInput struct {
	State string `json:"state,omitempty" jsonschema:"Only tickets in this state: working, needs_you, pr_ready, done, failed or cancelled."`
	Repo  string `json:"repo,omitempty" jsonschema:"Only tickets in this repository, as owner/name."`
}

type answerInput struct {
	ID   string `json:"id" jsonschema:"The ticket ID."`
	Text string `json:"text" jsonschema:"The answer to the ticket's question."`
}

type server struct {
	store   *tickets.Store
	intake  *sources.Intake
	version string
	schemas *mcp.SchemaCache
}

// New serves the five tools. It holds no session, so a control plane restart
// does not break a connected coding session.
func New(store *tickets.Store, intake *sources.Intake, version string) http.Handler {
	s := &server{store: store, intake: intake, version: version, schemas: mcp.NewSchemaCache()}
	return mcp.NewStreamableHTTPHandler(s.forRequest, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

func baseURL(r *http.Request) string {
	if r.TLS != nil {
		return "https://" + r.Host
	}
	return "http://" + r.Host
}

// forRequest builds a server bound to this caller and address.
func (s *server) forRequest(r *http.Request) *mcp.Server {
	caller, _ := r.Context().Value(callerKey{}).(string)
	base := baseURL(r)
	result := mcp.NewServer(&mcp.Implementation{Name: "machinist", Version: s.version}, &mcp.ServerOptions{SchemaCache: s.schemas})

	mcp.AddTool(result, &mcp.Tool{Name: "assign_ticket", Description: "Give a piece of work to Machinist. Returns at once with a ticket. An agent then does the work and opens a pull request."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input assignInput) (*mcp.CallToolResult, Ticket, error) {
			ticket, _, err := s.intake.Assign(ctx, sources.AssignRequest(input), caller)
			if err != nil {
				return nil, Ticket{}, publicError(err)
			}
			return s.result(ctx, ticket, base)
		})

	mcp.AddTool(result, &mcp.Tool{Name: "get_ticket", Description: "Read a ticket's state, pull request and any question waiting for an answer."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input idInput) (*mcp.CallToolResult, Ticket, error) {
			ticket, err := s.store.Ticket(ctx, input.ID)
			if err != nil {
				return nil, Ticket{}, publicError(err)
			}
			return s.result(ctx, ticket, base)
		})

	mcp.AddTool(result, &mcp.Tool{Name: "list_tickets", Description: "List the newest tickets, optionally by state or repository."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input listInput) (*mcp.CallToolResult, TicketList, error) {
			found, err := s.store.ListTickets(ctx, input.State, input.Repo)
			if err != nil {
				return nil, TicketList{}, publicError(err)
			}
			list := TicketList{Tickets: make([]Ticket, 0, len(found))}
			for _, ticket := range found {
				view, err := s.view(ctx, ticket, base)
				if err != nil {
					return nil, TicketList{}, publicError(err)
				}
				list.Tickets = append(list.Tickets, view)
			}
			return nil, list, nil
		})

	mcp.AddTool(result, &mcp.Tool{Name: "answer_ticket", Description: "Answer the question a ticket is waiting on. The ticket then continues."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input answerInput) (*mcp.CallToolResult, Ticket, error) {
			if strings.TrimSpace(input.Text) == "" {
				return nil, Ticket{}, errors.New("text is required")
			}
			ticket, err := s.store.Answer(ctx, input.ID, caller, input.Text)
			if err != nil {
				return nil, Ticket{}, publicError(err)
			}
			return s.result(ctx, ticket, base)
		})

	mcp.AddTool(result, &mcp.Tool{Name: "cancel_ticket", Description: "Stop a ticket and any run in progress. Its pull request stays open."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input idInput) (*mcp.CallToolResult, Ticket, error) {
			ticket, err := s.store.Cancel(ctx, input.ID, caller)
			if err != nil {
				return nil, Ticket{}, publicError(err)
			}
			return s.result(ctx, ticket, base)
		})
	return result
}

func publicError(err error) error {
	if errors.Is(err, tickets.ErrNotFound) {
		return errors.New("ticket not found")
	}
	return err
}

func (s *server) result(ctx context.Context, ticket tickets.Ticket, base string) (*mcp.CallToolResult, Ticket, error) {
	view, err := s.view(ctx, ticket, base)
	return nil, view, err
}

func (s *server) view(ctx context.Context, ticket tickets.Ticket, base string) (Ticket, error) {
	question, err := s.store.WaitingQuestion(ctx, ticket)
	if err != nil {
		return Ticket{}, err
	}
	view := Ticket{ID: ticket.ID, State: ticket.State, Repo: ticket.Repo, Title: ticket.Title, Link: base + "/#/work/" + ticket.ID, PR: ticket.PRNumber, Question: question}
	if ticket.PRNumber != 0 {
		view.PRLink = fmt.Sprintf("https://github.com/%s/pull/%d", ticket.Repo, ticket.PRNumber)
	}
	return view, nil
}
