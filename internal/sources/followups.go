package sources

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/owainlewis/machinist/internal/engine"
	"github.com/owainlewis/machinist/internal/ghclient"
	"github.com/owainlewis/machinist/internal/tickets"
)

const githubAuthor = "github"

func (p *poll) followUps(ctx context.Context) error {
	active, err := p.Store.ActiveWithPR(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, ticket := range active {
		if err := p.followUp(ctx, ticket); err != nil {
			failures = append(failures, fmt.Errorf("ticket %s: %w", ticket.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (p *poll) followUp(ctx context.Context, ticket tickets.Ticket) error {
	pr, err := p.Client.PullRequest(ctx, ticket.Repo, ticket.PRNumber)
	if err != nil {
		return err
	}
	switch pr.State {
	case ghclient.StateMerged:
		return p.merged(ctx, ticket, pr)
	case ghclient.StateClosed:
		return p.Engine.PRClosed(ctx, ticket, fmt.Sprintf("github:closed:%d:%s", ticket.PRNumber, pr.ClosedAt))
	case ghclient.StateOpen:
		if err := p.reviews(ctx, ticket, pr); err != nil {
			return err
		}
		return p.checks(ctx, ticket, pr)
	default:
		return fmt.Errorf("unknown PR state %q", pr.State)
	}
}

func (p *poll) merged(ctx context.Context, ticket tickets.Ticket, pr ghclient.PullRequest) error {
	emails, err := p.Client.CommitEmails(ctx, ticket.Repo, ticket.PRNumber)
	if err != nil {
		return err
	}
	human := 0
	for _, email := range emails {
		if !strings.EqualFold(email, engine.BotEmail(p.Handle)) {
			human++
		}
	}
	mergedAt := p.now()
	if pr.MergedAt != nil {
		mergedAt = *pr.MergedAt
	}
	return p.Engine.PRMerged(ctx, ticket, human, mergedAt)
}

// reviews records each person's latest request for changes.
func (p *poll) reviews(ctx context.Context, ticket tickets.Ticket, pr ghclient.PullRequest) error {
	latest := map[string]string{}
	for _, review := range pr.Reviews {
		// A plain comment leaves that person's earlier verdict standing.
		if review.State == ghclient.ReviewChangesRequested || review.State == "APPROVED" || review.State == "DISMISSED" {
			latest[review.Author] = review.ID
		}
	}
	for _, review := range pr.Reviews {
		if review.State != ghclient.ReviewChangesRequested || latest[review.Author] != review.ID || p.isBot(review.Author) {
			continue
		}
		key := "github:review:" + review.ID
		if seen, err := p.Store.HasSourceKey(ctx, key); err != nil || seen {
			if err != nil {
				return err
			}
			continue
		}
		allowed, err := p.canTrigger(ctx, ticket.Repo, review.Author)
		if err != nil {
			return err
		}
		if !allowed {
			fmt.Fprintf(p.Log, "machinist: ignored review by %s on %s#%d: no write access\n", review.Author, ticket.Repo, ticket.PRNumber)
			continue
		}
		body := fmt.Sprintf("%s requested changes on the PR: %s", review.Author, review.URL)
		if _, _, err := p.Store.AddInput(ctx, ticket.ID, tickets.Input{Kind: tickets.EntryReview, Author: review.Author, Body: body, SourceKey: key}); err != nil {
			return err
		}
	}
	return nil
}

// checks records one failure per commit. While the ticket is working, the
// commit is probably about to be replaced, and a run spent on it would be
// wasted. If it is still the head when the run ends, a later poll records it.
func (p *poll) checks(ctx context.Context, ticket tickets.Ticket, pr ghclient.PullRequest) error {
	if ticket.State == tickets.StateWorking || len(pr.FailedChecks) == 0 {
		return nil
	}
	body := fmt.Sprintf("CI failed on %s: %s", shortSHA(pr.HeadSHA), strings.Join(pr.FailedChecks, ", "))
	_, _, err := p.Store.AddInput(ctx, ticket.ID, tickets.Input{Kind: tickets.EntryCI, Author: githubAuthor, Body: body, SourceKey: "github:ci:" + pr.HeadSHA})
	return err
}

func shortSHA(sha string) string { return sha[:min(len(sha), 7)] }
