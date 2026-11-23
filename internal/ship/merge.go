package ship

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/owainlewis/machinist/internal/protocol"
)

// GH runs gh with these arguments and returns what it printed on stdout and
// stderr.
type GH func(ctx context.Context, args ...string) (stdout []byte, stderr string, err error)

// MergeOptions configures the merge step.
type MergeOptions struct {
	// Control is the control directory, where review.json, result.md and
	// ship.json live.
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
}

// Merge hands the job's pull request to the control plane, which waits for its
// checks and merges it when auto-merge is on, the review accepted the change
// and every check passed. The step writes what it found to ship.json and ends
// at once, so the runner's slot is free while CI runs. result.md says whether
// the pull request will merge or stays open, and why.
//
// A pull request that a person already merged or closed is recorded and not
// handed over. With no pull request, the step does nothing.
func Merge(ctx context.Context, options MergeOptions) error {
	if options.Log == nil {
		options.Log = io.Discard
	}
	pr, found, err := findPR(ctx, options)
	if err != nil {
		return err
	}
	if !found {
		// The publish step opened nothing, because the work needed no change.
		fmt.Fprintln(options.Log, "No pull request to merge")
		return nil
	}
	switch pr.State {
	case "MERGED":
		fmt.Fprintf(options.Log, "Pull request #%d is already merged\n", pr.Number)
		return writeResult(options.Control, "Merged: "+pr.URL)
	case "CLOSED":
		fmt.Fprintf(options.Log, "Pull request #%d was closed without merging\n", pr.Number)
		return writeResult(options.Control, "Closed without merging: "+pr.URL)
	}
	request := shipRequest(options)
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(options.Control, protocol.ShipFile), body, 0o600); err != nil {
		return err
	}
	if reason := Blocker(options.AutoMerge, request); reason != "" {
		fmt.Fprintf(options.Log, "Pull request #%d stays open: %s. Machinist still watches its checks.\n", pr.Number, reason)
		return writeResult(options.Control, fmt.Sprintf("Left open: %s. %s", strings.TrimRight(reason, ". "), pr.URL))
	}
	fmt.Fprintf(options.Log, "Pull request #%d merges once its checks pass. Machinist waits for them, not this runner.\n", pr.Number)
	return writeResult(options.Control, "Merges once its checks pass: "+pr.URL)
}

// shipRequest is what the step found: the review's verdict, or why the pull
// request must stay open.
func shipRequest(options MergeOptions) protocol.Ship {
	request := protocol.Ship{WithoutReview: options.WithoutReview}
	if !options.AutoMerge {
		request.Hold = "auto-merge is off"
	}
	review, err := ReadReview(options.Control)
	switch {
	case errors.Is(err, ErrNoReview) && options.WithoutReview:
	case err != nil:
		if request.Hold == "" {
			request.Hold = err.Error()
		}
	default:
		request.Verdict = review.Verdict
		if request.Hold == "" {
			request.Hold = mergeBlocker(review)
		}
	}
	return request
}

// Blocker says why a pull request must stay open, or "" when it may merge
// once its checks pass. It needs auto-merge on and an accepted review, or no
// review at all in a preset without one. The control plane asks it again with
// its own github.auto_merge, so no step can merge what the settings forbid.
func Blocker(autoMerge bool, request protocol.Ship) string {
	switch {
	case !autoMerge:
		return "auto-merge is off"
	case request.Hold != "":
		return request.Hold
	case request.Verdict == Accepted:
		return ""
	case request.Verdict == "" && request.WithoutReview:
		return ""
	case request.Verdict == "":
		return ErrNoReview.Error()
	default:
		return mergeBlocker(Review{Verdict: request.Verdict})
	}
}

type pullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"`
}

// findPR finds the pull request on the job's branch, in whatever state.
func findPR(ctx context.Context, options MergeOptions) (pullRequest, bool, error) {
	var pr pullRequest
	stdout, stderr, err := options.GH(ctx, "pr", "view", options.Branch, "--json", "number,url,state")
	if err != nil {
		if strings.Contains(stderr, "no pull requests found") {
			return pr, false, nil
		}
		return pr, false, fmt.Errorf("find the pull request: %s", firstLine(stderr, err))
	}
	if err := json.Unmarshal(stdout, &pr); err != nil {
		return pr, false, fmt.Errorf("read the pull request: %w", err)
	}
	return pr, true, nil
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
