package sources

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

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

// Intake is the one way the Assign box, the run API and schedules create tickets.
type Intake struct {
	Client        *ghclient.Client
	Store         *tickets.Store
	DefaultPreset string
	Repositories  []string
	// Presets reports whether a preset exists.
	Presets func(name string) bool
	mu      sync.RWMutex
}

// Update replaces the reloadable intake settings atomically.
func (i *Intake) Update(defaultPreset string, repositories []string, presets func(name string) bool) {
	i.mu.Lock()
	i.DefaultPreset = defaultPreset
	i.Repositories = slices.Clone(repositories)
	i.Presets = presets
	i.mu.Unlock()
}

// ConfigRepositories returns a copy of the repositories listed in config.yaml.
func (i *Intake) ConfigRepositories() []string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return slices.Clone(i.Repositories)
}

func (i *Intake) presetConfig() (string, func(name string) bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.DefaultPreset, i.Presets
}

func invalid(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, arguments...))
}

func issueText(issue ghclient.Issue, author, request string) string {
	return fmt.Sprintf("%s\n\n%s\n\nRequest from %s:\n%s", issue.Title, issue.Body, author, request)
}

// repository finds a connected repository by name, in any case: one listed in
// config.yaml, or one added in the web app.
func (i *Intake) repository(ctx context.Context, name string) (string, bool, error) {
	repositories, err := i.Connected(ctx)
	if err != nil {
		return "", false, err
	}
	for _, repository := range repositories {
		if strings.EqualFold(repository, name) {
			return repository, true, nil
		}
	}
	return "", false, nil
}

// Connected lists the repositories Machinist works on: the ones in
// config.yaml, then the ones added in the web app.
func (i *Intake) Connected(ctx context.Context) ([]string, error) {
	repositories := i.ConfigRepositories()
	projects, err := i.Store.Projects(ctx)
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		if !slices.ContainsFunc(repositories, func(listed string) bool { return strings.EqualFold(listed, project.Repo) }) {
			repositories = append(repositories, project.Repo)
		}
	}
	return repositories, nil
}

// Assign returns the ticket for a request. A repeated request ID, or an issue
// that already has an active ticket, returns the existing one. The bool reports
// whether this call created it.
func (i *Intake) Assign(ctx context.Context, request AssignRequest, by string) (tickets.Ticket, bool, error) {
	if request.RequestID == "" || len(request.RequestID) > maxRequestIDBytes {
		return tickets.Ticket{}, false, invalid("request_id is required and at most %d bytes", maxRequestIDBytes)
	}
	repository, connected, err := i.repository(ctx, request.Repo)
	if err != nil {
		return tickets.Ticket{}, false, err
	}
	if !connected {
		return tickets.Ticket{}, false, invalid("repository %q is not connected", request.Repo)
	}
	hasText := strings.TrimSpace(request.Title) != "" && strings.TrimSpace(request.Body) != ""
	if request.IssueNumber < 0 || (request.IssueNumber > 0) == hasText {
		return tickets.Ticket{}, false, invalid("send either issue_number, or both title and body")
	}
	key := "api:" + request.RequestID
	defaultPreset, _ := i.presetConfig()
	ticket := tickets.NewTicket{Source: "api", Origin: tickets.OriginAPI, Repo: repository, Title: request.Title, Body: request.Body, Agent: defaultPreset, CreatedBy: by, SourceKey: key}
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
		ticket.SourceURL = tickets.GitHubIssueURL(repository, request.IssueNumber)
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
