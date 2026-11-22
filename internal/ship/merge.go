package ship

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// GH runs gh with these arguments and returns what it printed on stdout and
// stderr. gh exits non-zero while checks are pending, so callers read stdout
// even when err is set.
type GH func(ctx context.Context, args ...string) (stdout []byte, stderr string, err error)

// MergeOptions configures one merge.
type MergeOptions struct {
	// Control is the control directory, where review.json and result.md live.
	Control string
	// Branch is the job's branch, whose pull request is merged.
	Branch string
	// AutoMerge is MACHINIST_AUTO_MERGE: false leaves every pull request open.
	AutoMerge bool
	// WithoutReview merges with no review.json, for presets with no review
	// step. Otherwise a missing review leaves the pull request open.
	WithoutReview bool
	GH            GH
	Log           io.Writer
	// Poll is how long to wait between looks at the checks.
	Poll time.Duration
	// StartWait is how long to wait for checks to appear, StartTries times,
	// before treating the repository as having none.
	StartWait  time.Duration
	StartTries int
	// Sleep waits, and reports false when ctx ended first. Tests replace it.
	Sleep func(ctx context.Context, d time.Duration) bool
}

const (
	defaultPoll       = 15 * time.Second
	waitReminder      = 5 * time.Minute
	defaultStartWait  = 30 * time.Second
	defaultStartTries = 5
	maxBranchUpdates  = 3
)

// CheckFailedError means a check on the pull request failed, so the job fails.
type CheckFailedError struct{ Check, Link string }

func (err *CheckFailedError) Error() string {
	if err.Link != "" {
		return fmt.Sprintf("check %q failed: %s", err.Check, err.Link)
	}
	return fmt.Sprintf("check %q failed", err.Check)
}

type pullRequest struct {
	Number           int    `json:"number"`
	URL              string `json:"url"`
	State            string `json:"state"`
	MergeStateStatus string `json:"mergeStateStatus"`
	HeadRefOid       string `json:"headRefOid"`
}

type check struct {
	Name   string `json:"name"`
	Bucket string `json:"bucket"`
	Link   string `json:"link"`
}

// Merge waits for the checks on the job's pull request, and merges it when
// auto-merge is on, the review accepted the change and every check passed.
// Otherwise it leaves the pull request open and says why in result.md, which is
// not an error. A failed check is an error, and result.md names the check.
func Merge(ctx context.Context, options MergeOptions) error {
	options = withDefaults(options)
	pr, found, err := findPR(ctx, options)
	if err != nil {
		return err
	}
	if !found {
		// The publish step opened nothing, because the work needed no change.
		fmt.Fprintln(options.Log, "No pull request to merge")
		return nil
	}
	// A failed check fails the job whether or not the change may merge, so a
	// pull request left for a person never hides one.
	reason := blocker(options)
	// The checks seen on the last round. After an update the new head commit's
	// checks take a while to register, so each one must come back before the
	// pull request may merge, or a round that sees none would merge without CI.
	var seen []string
	// The head commit whose checks the last round saw. The merge names it, so
	// GitHub refuses to merge any other commit.
	var head string
	for updates := 0; ; updates++ {
		head = pr.HeadRefOid
		names, err := awaitChecks(ctx, options, pr, seen)
		if err != nil {
			var failed *CheckFailedError
			if errors.As(err, &failed) {
				return errors.Join(writeResult(options.Control, fmt.Sprintf("Left open: %s. %s", failed.Error(), pr.URL)), err)
			}
			var missing *missingChecksError
			if errors.As(err, &missing) {
				return leaveOpen(options, pr, missing.Error())
			}
			return err
		}
		seen = names
		if reason != "" {
			return leaveOpen(options, pr, reason)
		}
		pr, _, err = findPR(ctx, options)
		if err != nil {
			return err
		}
		if pr.MergeStateStatus != "BEHIND" {
			break
		}
		if updates == maxBranchUpdates {
			return leaveOpen(options, pr, "the branch fell behind the default branch three times")
		}
		fmt.Fprintf(options.Log, "▸ Update the branch of pull request #%d\n", pr.Number)
		if _, stderr, err := options.GH(ctx, "pr", "update-branch", strconv.Itoa(pr.Number)); err != nil {
			return leaveOpen(options, pr, "GitHub could not update the branch: "+firstLine(stderr, err))
		}
		// Until the pull request reports the new head, gh still shows the old
		// head's passed checks, which would merge the update without CI.
		moved, ok, err := awaitNewHead(ctx, options, pr)
		if err != nil {
			return err
		}
		if !ok {
			return leaveOpen(options, pr, "the pull request did not move to the updated branch")
		}
		pr = moved
	}
	method := mergeMethod(ctx, options)
	fmt.Fprintf(options.Log, "▸ Merge pull request #%d with %s\n", pr.Number, method)
	if _, stderr, err := options.GH(ctx, "pr", "merge", strconv.Itoa(pr.Number), "--"+method, "--delete-branch", "--match-head-commit", head); err != nil {
		return leaveOpen(options, pr, "GitHub refused the merge: "+firstLine(stderr, err))
	}
	return writeResult(options.Control, "Merged: "+pr.URL)
}

// repository holds the merge methods a repository allows. A field is nil when
// GitHub leaves it out of the response.
type repository struct {
	AllowMergeCommit *bool `json:"allow_merge_commit"`
	AllowSquashMerge *bool `json:"allow_squash_merge"`
	AllowRebaseMerge *bool `json:"allow_rebase_merge"`
}

// mergeMethod picks the first of merge, squash and rebase that the repository
// allows, as the gh pr merge flag without its dashes. When the settings cannot
// be read it picks merge, and GitHub's answer to the merge says what went wrong.
func mergeMethod(ctx context.Context, options MergeOptions) string {
	const fallback = "merge"
	stdout, stderr, err := options.GH(ctx, "api", "repos/{owner}/{repo}")
	if err != nil {
		fmt.Fprintf(options.Log, "Could not read the repository's merge settings, so merge with a merge commit: %s\n", firstLine(stderr, err))
		return fallback
	}
	var repo repository
	if err := json.Unmarshal(stdout, &repo); err != nil {
		fmt.Fprintf(options.Log, "Could not read the repository's merge settings, so merge with a merge commit: %v\n", err)
		return fallback
	}
	for _, method := range []struct {
		name    string
		allowed *bool
	}{
		{"merge", repo.AllowMergeCommit},
		{"squash", repo.AllowSquashMerge},
		{"rebase", repo.AllowRebaseMerge},
	} {
		if method.allowed != nil && *method.allowed {
			return method.name
		}
	}
	return fallback
}

func withDefaults(options MergeOptions) MergeOptions {
	if options.Log == nil {
		options.Log = io.Discard
	}
	if options.Poll <= 0 {
		options.Poll = defaultPoll
	}
	if options.StartWait <= 0 {
		options.StartWait = defaultStartWait
	}
	if options.StartTries <= 0 {
		options.StartTries = defaultStartTries
	}
	if options.Sleep == nil {
		options.Sleep = sleep
	}
	return options
}

// blocker says why the pull request must stay open, or "" when it may merge.
func blocker(options MergeOptions) string {
	if !options.AutoMerge {
		return "auto-merge is off"
	}
	review, err := ReadReview(options.Control)
	switch {
	case errors.Is(err, ErrNoReview) && options.WithoutReview:
		return ""
	case err != nil:
		return err.Error()
	}
	return mergeBlocker(review)
}

func leaveOpen(options MergeOptions, pr pullRequest, reason string) error {
	fmt.Fprintf(options.Log, "Left pull request #%d open: %s\n", pr.Number, reason)
	return writeResult(options.Control, fmt.Sprintf("Left open: %s. %s", strings.TrimRight(reason, ". "), pr.URL))
}

// findPR finds the open pull request on the job's branch.
func findPR(ctx context.Context, options MergeOptions) (pullRequest, bool, error) {
	var pr pullRequest
	stdout, stderr, err := options.GH(ctx, "pr", "view", options.Branch, "--json", "number,url,state,mergeStateStatus,headRefOid")
	if err != nil {
		if strings.Contains(stderr, "no pull requests found") {
			return pr, false, nil
		}
		return pr, false, fmt.Errorf("find the pull request: %s", firstLine(stderr, err))
	}
	if err := json.Unmarshal(stdout, &pr); err != nil {
		return pr, false, fmt.Errorf("read the pull request: %w", err)
	}
	if pr.State != "OPEN" {
		return pr, false, fmt.Errorf("pull request #%d is %s, not open", pr.Number, strings.ToLower(pr.State))
	}
	return pr, true, nil
}

// awaitNewHead waits until the pull request's head commit is no longer the one
// in pr, and returns the pull request as it now is. It reports false when the
// head has not moved after StartTries waits.
func awaitNewHead(ctx context.Context, options MergeOptions, pr pullRequest) (pullRequest, bool, error) {
	for tries := 0; ; tries++ {
		now, _, err := findPR(ctx, options)
		if err != nil {
			return pr, false, err
		}
		if now.HeadRefOid != "" && now.HeadRefOid != pr.HeadRefOid {
			return now, true, nil
		}
		if tries == options.StartTries {
			return pr, false, nil
		}
		if !options.Sleep(ctx, options.StartWait) {
			return pr, false, ctx.Err()
		}
	}
}

// missingChecksError means checks seen on an earlier round never started on
// the updated branch, so the pull request stays open.
type missingChecksError struct{ Checks []string }

func (err *missingChecksError) Error() string {
	quoted := make([]string, len(err.Checks))
	for i, name := range err.Checks {
		quoted[i] = strconv.Quote(name)
	}
	return fmt.Sprintf("the checks %s did not start on the updated branch", strings.Join(quoted, ", "))
}

// awaitChecks waits until every check on the pull request has finished, and
// fails on the first that did not pass. It returns the names of the checks.
// Each check named in want must appear, and a *missingChecksError says which
// did not after StartTries waits. With want empty, a repository with no checks
// passes once none have appeared after StartTries waits.
func awaitChecks(ctx context.Context, options MergeOptions, pr pullRequest, want []string) ([]string, error) {
	empty := 0
	// The step prints whenever the count of pending checks changes, and at
	// least every few minutes, so its idle timeout never stops a long wait.
	lastPending, lastSaid := -1, time.Time{}
	for {
		checks, err := readChecks(ctx, options, pr.Number)
		if err != nil {
			return nil, err
		}
		names := make([]string, len(checks))
		pending := 0
		for i, check := range checks {
			names[i] = check.Name
			switch check.Bucket {
			case "fail", "cancel":
				return nil, &CheckFailedError{Check: check.Name, Link: check.Link}
			case "pending":
				pending++
			}
		}
		missing := absent(want, names)
		if len(checks) == 0 || len(missing) > 0 {
			if empty == options.StartTries {
				if len(missing) > 0 {
					return nil, &missingChecksError{Checks: missing}
				}
				fmt.Fprintln(options.Log, "No checks started, so there are none to wait for")
				return nil, nil
			}
			empty++
			if !options.Sleep(ctx, options.StartWait) {
				return nil, ctx.Err()
			}
			continue
		}
		if pending == 0 {
			fmt.Fprintf(options.Log, "All %d checks passed\n", len(checks))
			return names, nil
		}
		if pending != lastPending || time.Since(lastSaid) >= waitReminder {
			fmt.Fprintf(options.Log, "▸ Wait for %d of %d checks on pull request #%d\n", pending, len(checks), pr.Number)
			lastPending, lastSaid = pending, time.Now()
		}
		if !options.Sleep(ctx, options.Poll) {
			return nil, ctx.Err()
		}
	}
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

func readChecks(ctx context.Context, options MergeOptions, number int) ([]check, error) {
	stdout, stderr, err := options.GH(ctx, "pr", "checks", strconv.Itoa(number), "--json", "name,bucket,link")
	var checks []check
	if jsonErr := json.Unmarshal(stdout, &checks); jsonErr == nil {
		// gh exits 8 while checks are pending, with the list on stdout.
		return checks, nil
	}
	if strings.Contains(stderr, "no checks reported") {
		return nil, nil
	}
	if err == nil {
		err = errors.New("gh printed no checks")
	}
	return nil, fmt.Errorf("read the checks: %s", firstLine(stderr, err))
}

func writeResult(control, text string) error {
	return os.WriteFile(filepath.Join(control, "result.md"), []byte(text+"\n"), 0o600)
}

func firstLine(stderr string, err error) string {
	if line, _, _ := strings.Cut(strings.TrimSpace(stderr), "\n"); line != "" {
		return line
	}
	return err.Error()
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
