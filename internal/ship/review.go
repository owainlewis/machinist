// Package ship holds the deterministic end of a job: reading the review's
// verdict and merging the pull request when the rules allow it. No agent
// decides whether a change merges.
package ship

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ReviewFile is the file the review step writes in the control directory.
const ReviewFile = "review.json"

// The verdicts a review can give.
const (
	Accepted            = "accepted"
	RevisionNeeded      = "revision_needed"
	HumanDecisionNeeded = "human_decision_needed"
)

// Finding is one thing the review found.
type Finding struct {
	File        string `json:"file"`
	Line        int    `json:"line"`
	Unambiguous bool   `json:"unambiguous"`
	Text        string `json:"text"`
	Addressed   bool   `json:"addressed"`
}

// Review is what the review step wrote.
type Review struct {
	Verdict  string    `json:"verdict"`
	Summary  string    `json:"summary"`
	Findings []Finding `json:"findings"`
}

// ErrNoReview means the control directory has no review.json.
var ErrNoReview = errors.New("the review step wrote no review.json")

// ReadReview reads and checks review.json in the control directory.
func ReadReview(control string) (Review, error) {
	var review Review
	body, err := os.ReadFile(filepath.Join(control, ReviewFile))
	if errors.Is(err, os.ErrNotExist) {
		return review, ErrNoReview
	}
	if err != nil {
		return review, err
	}
	if err := json.Unmarshal(body, &review); err != nil {
		return review, fmt.Errorf("review.json is not valid JSON: %w", err)
	}
	switch review.Verdict {
	case Accepted, RevisionNeeded, HumanDecisionNeeded:
		return review, nil
	case "":
		return review, errors.New("review.json has no verdict")
	default:
		return review, fmt.Errorf("review.json has an unknown verdict %q", review.Verdict)
	}
}

// mergeBlocker says why a review stops a merge, or "" when it allows one.
// Only an accepted review allows a merge. A revision is re-reviewed before
// publish, so a verdict other than accepted means the change still needs work
// or a person.
func mergeBlocker(review Review) string {
	switch review.Verdict {
	case Accepted:
		return ""
	case RevisionNeeded:
		return "the review still asks for changes"
	default:
		if review.Summary != "" {
			return "the review needs a person's decision: " + review.Summary
		}
		return "the review needs a person's decision"
	}
}
