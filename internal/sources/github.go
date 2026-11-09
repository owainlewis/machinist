// Package sources turns outside events into tickets and ticket input.
package sources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/engine"
	"github.com/owainlewis/machinist/internal/ghclient"
	"github.com/owainlewis/machinist/internal/tickets"
)

const (
	pollInterval   = time.Minute
	commentsCursor = "github:comments"
	// commentReadBack covers clock differences. Source keys remove the repeats.
	commentReadBack = 5 * time.Minute
)

type Options struct {
	Client       *ghclient.Client
	Store        *tickets.Store
	Engine       *engine.Engine
	Handle       string
	Repositories []string
	DefaultAgent string
	Log          io.Writer
}

// GitHub polls connected repositories, because the control plane has a private
// address that webhooks cannot reach.
type GitHub struct {
	Options
	// Interval is the time between polls.
	Interval time.Duration
	mention  *regexp.Regexp
	now      func() time.Time
}

func NewGitHub(options Options) *GitHub {
	if options.Log == nil {
		options.Log = io.Discard
	}
	return &GitHub{
		Options:  options,
		Interval: pollInterval,
		mention:  regexp.MustCompile(`(?i)(?:^|[^\w/.-])@` + regexp.QuoteMeta(options.Handle) + `(?:$|[^\w-])`),
		now:      time.Now,
	}
}

// Run polls at once, then every interval until ctx ends.
func (g *GitHub) Run(ctx context.Context) {
	ticker := time.NewTicker(g.Interval)
	defer ticker.Stop()
	for {
		if err := g.Poll(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintf(g.Log, "machinist: GitHub poll: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// poll is one pass over GitHub. It remembers permissions so that a login is
// checked once per repository per pass.
type poll struct {
	*GitHub
	permissions map[string]bool
}

// Poll reads comments on every repository, then every open ticket's PR. One
// failure does not hold up the rest.
func (g *GitHub) Poll(ctx context.Context) error {
	pass := &poll{GitHub: g, permissions: map[string]bool{}}
	var failures []error
	for _, repository := range g.Repositories {
		if err := pass.comments(ctx, repository); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", repository, err))
		}
	}
	if err := pass.followUps(ctx); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (p *poll) canTrigger(ctx context.Context, repository, login string) (bool, error) {
	key := repository + "\x00" + login
	if allowed, known := p.permissions[key]; known {
		return allowed, nil
	}
	permission, err := p.Client.Permission(ctx, repository, login)
	if err != nil {
		return false, err
	}
	allowed := ghclient.CanWrite(permission)
	p.permissions[key] = allowed
	return allowed, nil
}

func (g *GitHub) isBot(login string) bool { return strings.EqualFold(login, g.Handle) }

// comments handles new comments. The cursor moves only when every comment was
// handled, so a failed pass is retried.
func (p *poll) comments(ctx context.Context, repository string) error {
	cursor, found, err := p.Store.Cursor(ctx, commentsCursor, repository)
	if err != nil {
		return err
	}
	started := p.now()
	if !found {
		return p.Store.SetCursor(ctx, commentsCursor, repository, started)
	}
	comments, err := p.Client.Comments(ctx, repository, cursor.Add(-commentReadBack))
	if err != nil {
		return err
	}
	var failures []error
	for _, comment := range comments {
		if err := p.route(ctx, repository, comment); err != nil {
			failures = append(failures, fmt.Errorf("comment %d: %w", comment.ID, err))
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	return p.Store.SetCursor(ctx, commentsCursor, repository, started)
}

// route applies the routing rules. Permission is checked last, so a comment
// that changes nothing costs no GitHub call.
func (p *poll) route(ctx context.Context, repository string, comment ghclient.Comment) error {
	if comment.Author == "" || p.isBot(comment.Author) {
		return nil
	}
	key := fmt.Sprintf("github:comment:%d", comment.ID)
	if seen, err := p.Store.HasSourceKey(ctx, key); err != nil || seen {
		return err
	}
	mentioned := p.mention.MatchString(comment.Body)
	ticket, err := p.Store.ActiveTicketFor(ctx, repository, comment.Number)
	active := err == nil
	if err != nil && !errors.Is(err, tickets.ErrNotFound) {
		return err
	}
	if active && !mentioned && ticket.State != tickets.StateNeedsYou {
		return nil
	}
	if !active && !mentioned {
		return nil
	}
	if !active && comment.OnPR {
		fmt.Fprintf(p.Log, "machinist: ignored mention by %s on %s#%d: the PR has no ticket\n", comment.Author, repository, comment.Number)
		return nil
	}
	allowed, err := p.canTrigger(ctx, repository, comment.Author)
	if err != nil {
		return err
	}
	if !allowed {
		fmt.Fprintf(p.Log, "machinist: ignored comment by %s on %s#%d: no write access\n", comment.Author, repository, comment.Number)
		return nil
	}
	if active {
		_, _, err = p.Store.AddInput(ctx, ticket.ID, tickets.Input{Kind: tickets.EntryComment, Author: comment.Author, Body: comment.Body, SourceKey: key})
		return err
	}
	issue, err := p.Client.Issue(ctx, repository, comment.Number)
	if err != nil {
		return err
	}
	_, _, err = p.Store.CreateTicket(ctx, tickets.NewTicket{
		Source:      "github",
		Repo:        repository,
		IssueNumber: comment.Number,
		Title:       issue.Title,
		Body:        issueText(issue, comment.Author, comment.Body),
		Agent:       p.DefaultAgent,
		CreatedBy:   comment.Author,
		SourceKey:   key,
	})
	return err
}
