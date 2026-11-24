package ship

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

// StartGrace is how long checks have to appear, on a new pull request or on
// an updated branch, before a repository counts as having none. It is also
// how long an updated branch has to show its new head commit.
const StartGrace = 150 * time.Second

// maxBranchUpdates is how many times the branch is brought up to date before
// the pull request is left for a person.
const maxBranchUpdates = 3

// PullRequest is a pull request as GitHub reports it.
type PullRequest struct {
	Number           int    `json:"number"`
	URL              string `json:"url"`
	State            string `json:"state"`
	MergeStateStatus string `json:"mergeStateStatus"`
	HeadRefOid       string `json:"headRefOid"`
}

// Check is one check on a pull request's head commit. Bucket is gh's: pass,
// fail, pending, skipping or cancel.
type Check struct {
	Name   string `json:"name"`
	Bucket string `json:"bucket"`
	Link   string `json:"link"`
}

// GitHub is what watching one pull request needs. A non-transient error from
// Merge or UpdateBranch is GitHub's refusal, which the person is told; a
// TransientError (or context deadline/cancellation) is returned so the watcher
// keeps waiting and retries on a later look.
type GitHub interface {
	PullRequest(ctx context.Context) (PullRequest, error)
	Checks(ctx context.Context) ([]Check, error)
	// Merge merges the pull request only if its head is still head.
	Merge(ctx context.Context, head string) error
	UpdateBranch(ctx context.Context) error
}

// TransientError marks a temporary GitHub failure from Merge or UpdateBranch
// so LookOnce returns it instead of permanently leaving the pull request open.
type TransientError struct{ Err error }

func (e *TransientError) Error() string { return e.Err.Error() }
func (e *TransientError) Unwrap() error { return e.Err }

func isTransient(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var transient *TransientError
	return errors.As(err, &transient)
}

// Watch is what the control plane keeps about a pull request between looks.
type Watch struct {
	// Hold says why the pull request must stay open once its checks pass, or
	// is empty when it may merge.
	Hold string `json:"hold,omitempty"`
	// Since is when the wait began for checks to appear, or for the branch
	// update in OldHead to show.
	Since time.Time `json:"since"`
	// Seen are the checks that passed before the last branch update. Each
	// must come back on the new head before the pull request may merge, or a
	// look that sees none would merge without CI.
	Seen []string `json:"seen,omitempty"`
	// OldHead is the head commit before the last branch update. Until GitHub
	// reports a new head, gh still shows the old head's passed checks.
	OldHead string `json:"old_head,omitempty"`
	// Updates counts the branch updates so far.
	Updates int `json:"updates,omitempty"`
	// Head is the only head commit that may merge: the one the review saw,
	// or the one the last branch update made. A commit pushed after that
	// leaves the pull request for a person. It is empty for a watch an older
	// Machinist started.
	Head string `json:"head,omitempty"`
}

// NewWatch starts watching a pull request a run handed over. autoMerge is
// the control plane's github.auto_merge.
func NewWatch(autoMerge bool, request protocol.Ship, now time.Time) Watch {
	return Watch{Hold: Blocker(autoMerge, request), Since: now, Head: request.Head}
}

// The outcomes of one look.
const (
	// Waiting means look again later, with Look.Watch.
	Waiting = "waiting"
	// Merged means this look merged the pull request.
	Merged = "merged"
	// LeftOpen means the pull request stays open for a person.
	LeftOpen = "left_open"
	// CheckFailed means a check failed, which fails the job.
	CheckFailed = "check_failed"
	// Settled means a person merged or closed the pull request, which the
	// pull request watcher records.
	Settled = "settled"
)

// Look is what one look at a pull request found.
type Look struct {
	Outcome string
	// Watch is what to keep for the next look, when Outcome is Waiting.
	Watch Watch
	// Message tells the person what happened, or is empty when the run's own
	// result already said it.
	Message string
	// Log says what the look did, for the control plane's log.
	Log string
}

// LookOnce looks at the pull request once and does what its checks call for.
// It never waits: a check still running returns Waiting, and the control plane
// looks again later. A failed check is CheckFailed whether or not the pull
// request may merge, so a pull request left for a person never hides one. A
// passing pull request merges when the watch has no hold, naming the head
// commit whose checks passed, so GitHub refuses any other. Only the head the
// review saw, or the one Machinist's own branch update made from it, may
// merge: a commit pushed after that leaves the pull request for a person. A
// branch behind its base is updated, and its new head's checks must pass in
// turn.
func LookOnce(ctx context.Context, github GitHub, watch Watch, now time.Time) (Look, error) {
	pr, err := github.PullRequest(ctx)
	if err != nil {
		return Look{}, err
	}
	if pr.State != "OPEN" {
		return Look{Outcome: Settled}, nil
	}
	if watch.OldHead != "" {
		if pr.HeadRefOid == "" || pr.HeadRefOid == watch.OldHead {
			if now.Sub(watch.Since) >= StartGrace {
				return leaveOpen(pr, "the pull request did not move to the updated branch"), nil
			}
			return waiting(watch), nil
		}
		watch.OldHead, watch.Since, watch.Head = "", now, pr.HeadRefOid
	}
	if watch.Head != "" && pr.HeadRefOid != watch.Head {
		return leaveOpen(pr, "the branch has commits pushed after the review"), nil
	}
	checks, err := github.Checks(ctx)
	if err != nil {
		return Look{}, err
	}
	names := make([]string, len(checks))
	pending := 0
	for i, check := range checks {
		names[i] = check.Name
		switch check.Bucket {
		case "fail", "cancel":
			failed := &CheckFailedError{Check: check.Name, Link: check.Link}
			return Look{Outcome: CheckFailed, Message: fmt.Sprintf("Left open: %s. %s", failed.Error(), pr.URL), Log: failed.Error()}, nil
		case "pending":
			pending++
		}
	}
	missing := absent(watch.Seen, names)
	switch {
	case len(checks) == 0 || len(missing) > 0:
		if now.Sub(watch.Since) < StartGrace {
			return waiting(watch), nil
		}
		if len(missing) > 0 {
			return leaveOpen(pr, (&missingChecksError{Checks: missing}).Error()), nil
		}
		// No checks started in all that time, so there are none to wait for.
	case pending > 0:
		return waiting(watch), nil
	}
	if watch.Hold != "" {
		// The run's result already said why it stays open.
		return Look{Outcome: LeftOpen, Log: "checks passed; left open: " + watch.Hold}, nil
	}
	if watch.Head == "" {
		watch.Head = pr.HeadRefOid
	}
	if pr.MergeStateStatus != "BEHIND" {
		err := github.Merge(ctx, watch.Head)
		if err == nil {
			return Look{Outcome: Merged, Message: "Merged: " + pr.URL, Log: "merged"}, nil
		}
		if isTransient(ctx, err) {
			return Look{}, err
		}
		refused := "GitHub refused the merge: " + err.Error()
		// Another pull request can land between the look and the merge,
		// which leaves this branch behind. Look again, and update the branch
		// if that is why GitHub refused.
		if pr, err = github.PullRequest(ctx); err != nil {
			return Look{}, err
		}
		if pr.State != "OPEN" {
			return Look{Outcome: Settled}, nil
		}
		if pr.HeadRefOid != watch.Head {
			return leaveOpen(pr, "the branch has commits pushed after the review"), nil
		}
		if pr.MergeStateStatus != "BEHIND" {
			return leaveOpen(pr, refused), nil
		}
	}
	if watch.Updates == maxBranchUpdates {
		return leaveOpen(pr, "the branch fell behind the default branch three times"), nil
	}
	if err := github.UpdateBranch(ctx); err != nil {
		if isTransient(ctx, err) {
			return Look{}, err
		}
		return leaveOpen(pr, "GitHub could not update the branch: "+err.Error()), nil
	}
	next := Watch{Hold: watch.Hold, Since: now, Seen: names, OldHead: pr.HeadRefOid, Updates: watch.Updates + 1, Head: watch.Head}
	return Look{Outcome: Waiting, Watch: next, Log: "updated the branch"}, nil
}

func waiting(watch Watch) Look { return Look{Outcome: Waiting, Watch: watch} }

func leaveOpen(pr PullRequest, reason string) Look {
	reason = strings.TrimRight(reason, ". ")
	return Look{Outcome: LeftOpen, Message: fmt.Sprintf("Left open: %s. %s", reason, pr.URL), Log: "left open: " + reason}
}

// CheckFailedError is a check on the pull request that failed.
type CheckFailedError struct{ Check, Link string }

func (err *CheckFailedError) Error() string {
	if err.Link != "" {
		return fmt.Sprintf("check %q failed: %s", err.Check, err.Link)
	}
	return fmt.Sprintf("check %q failed", err.Check)
}

// missingChecksError names checks seen before a branch update that never
// started on the updated branch.
type missingChecksError struct{ Checks []string }

func (err *missingChecksError) Error() string {
	quoted := make([]string, len(err.Checks))
	for i, name := range err.Checks {
		quoted[i] = strconv.Quote(name)
	}
	return fmt.Sprintf("the checks %s did not start on the updated branch", strings.Join(quoted, ", "))
}

// absent returns the names in want that are not in have.
func absent(want, have []string) []string {
	var missing []string
	for _, name := range want {
		if !slices.Contains(have, name) {
			missing = append(missing, name)
		}
	}
	return missing
}
