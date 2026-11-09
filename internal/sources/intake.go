package sources

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/owainlewis/machinist/internal/ghclient"
	"github.com/owainlewis/machinist/internal/tickets"
)

const maxRequestIDBytes = 200

// ErrInvalidRequest means the caller must change the request to succeed.
var ErrInvalidRequest = errors.New("invalid request")

// AssignRequest names an issue, or carries the ticket's text.
type AssignRequest struct {
	RequestID   string `json:"request_id"`
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number,omitempty"`
	Title       string `json:"title,omitempty"`
	Body        string `json:"body,omitempty"`
}

// Intake is the one way the Assign box and MCP create tickets.
type Intake struct {
	Client       *ghclient.Client
	Store        *tickets.Store
	DefaultAgent string
	Repositories []string
}

func invalid(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, arguments...))
}

func issueText(issue ghclient.Issue, author, request string) string {
	return fmt.Sprintf("%s\n\n%s\n\nRequest from %s:\n%s", issue.Title, issue.Body, author, request)
}

func (i *Intake) repository(name string) (string, bool) {
	for _, repository := range i.Repositories {
		if strings.EqualFold(repository, name) {
			return repository, true
		}
	}
	return "", false
}

// Assign returns the ticket for a request. A repeated request ID, or an issue
// that already has an active ticket, returns the existing one. The bool reports
// whether this call created it.
func (i *Intake) Assign(ctx context.Context, request AssignRequest, by string) (tickets.Ticket, bool, error) {
	if request.RequestID == "" || len(request.RequestID) > maxRequestIDBytes {
		return tickets.Ticket{}, false, invalid("request_id is required and at most %d bytes", maxRequestIDBytes)
	}
	repository, connected := i.repository(request.Repo)
	if !connected {
		return tickets.Ticket{}, false, invalid("repository %q is not connected", request.Repo)
	}
	hasText := strings.TrimSpace(request.Title) != "" && strings.TrimSpace(request.Body) != ""
	if request.IssueNumber < 0 || (request.IssueNumber > 0) == hasText {
		return tickets.Ticket{}, false, invalid("send either issue_number, or both title and body")
	}
	key := "api:" + request.RequestID
	ticket := tickets.NewTicket{Source: "api", Repo: repository, Title: request.Title, Body: request.Body, Agent: i.DefaultAgent, CreatedBy: by, SourceKey: key}
	if request.IssueNumber > 0 {
		if existing, found, err := i.existing(ctx, key, repository, request.IssueNumber); err != nil || found {
			return existing, false, err
		}
		issue, err := i.Client.Issue(ctx, repository, request.IssueNumber)
		if ghclient.IsNotFound(err) {
			return tickets.Ticket{}, false, invalid("%s has no issue %d", repository, request.IssueNumber)
		}
		if err != nil {
			return tickets.Ticket{}, false, err
		}
		if issue.IsPR() {
			return tickets.Ticket{}, false, invalid("%d is a pull request, not an issue", request.IssueNumber)
		}
		ticket.IssueNumber, ticket.Title = request.IssueNumber, issue.Title
		ticket.Body = issueText(issue, by, "Work on this issue.")
	}
	return i.Store.CreateTicket(ctx, ticket)
}

// existing finds a ticket before reading GitHub, so a retry costs no GitHub call.
func (i *Intake) existing(ctx context.Context, key, repository string, issue int) (tickets.Ticket, bool, error) {
	ticket, err := i.Store.TicketBySourceKey(ctx, key)
	if err == nil {
		return ticket, true, nil
	}
	if !errors.Is(err, tickets.ErrNotFound) {
		return tickets.Ticket{}, false, err
	}
	ticket, err = i.Store.ActiveTicketFor(ctx, repository, issue)
	if errors.Is(err, tickets.ErrNotFound) {
		return tickets.Ticket{}, false, nil
	}
	return ticket, err == nil, err
}
