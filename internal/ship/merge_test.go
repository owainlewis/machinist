package ship

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
)

// stepGH answers the merge step's one gh call.
type stepGH struct {
	state string // "" means there is no pull request
	calls []string
}

func (f *stepGH) run(_ context.Context, args ...string) ([]byte, string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if f.state == "" {
		return nil, "no pull requests found for branch \"machinist/t_1\"", errors.New("exit status 1")
	}
	return []byte(`{"number":7,"url":"https://github.com/o/r/pull/7","state":"` + f.state + `"}`), "", nil
}

func control(t *testing.T, review string) string {
	t.Helper()
	directory := t.TempDir()
	if review != "" {
		if err := os.WriteFile(filepath.Join(directory, ReviewFile), []byte(review), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}

func readFile(t *testing.T, directory, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(body))
}

func handedOver(t *testing.T, directory string) *protocol.Ship {
	t.Helper()
	body := readFile(t, directory, protocol.ShipFile)
	if body == "" {
		return nil
	}
	var request protocol.Ship
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatal(err)
	}
	return &request
}

const accepted = `{"verdict":"accepted","summary":"Fine.","findings":[]}`

func TestMergeHandsAnAcceptedChangeToTheControlPlaneAndEnds(t *testing.T) {
	directory := control(t, accepted)
	gh := &stepGH{state: "OPEN"}
	if err := Merge(t.Context(), MergeOptions{Control: directory, Branch: "machinist/t_1", AutoMerge: true, GH: gh.run}); err != nil {
		t.Fatal(err)
	}
	if request := handedOver(t, directory); request == nil || *request != (protocol.Ship{Verdict: Accepted}) {
		t.Fatalf("ship.json = %+v", request)
	}
	if result := readFile(t, directory, "result.md"); result != "Merges once its checks pass: https://github.com/o/r/pull/7" {
		t.Fatalf("result.md = %q", result)
	}
	// The step looks at the pull request once: it never waits on checks.
	if len(gh.calls) != 1 || !strings.HasPrefix(gh.calls[0], "pr view machinist/t_1") {
		t.Fatalf("calls = %q", gh.calls)
	}
}

func TestMergeSaysWhyAPullRequestStaysOpenAndStillHandsItOver(t *testing.T) {
	for _, test := range []struct {
		name, review string
		autoMerge    bool
		want         protocol.Ship
		result       string
	}{
		{"auto-merge off", accepted, false, protocol.Ship{Verdict: Accepted, Hold: "auto-merge is off"}, "Left open: auto-merge is off."},
		{"revision needed", `{"verdict":"revision_needed","summary":"x"}`, true, protocol.Ship{Verdict: RevisionNeeded, Hold: "the review still asks for changes"}, "Left open: the review still asks for changes."},
		{"needs a person", `{"verdict":"human_decision_needed","summary":"Pick a database"}`, true, protocol.Ship{Verdict: HumanDecisionNeeded, Hold: "the review needs a person's decision: Pick a database"}, "Left open: the review needs a person's decision: Pick a database."},
		{"no review", "", true, protocol.Ship{Hold: ErrNoReview.Error()}, "Left open: the review step wrote no review.json."},
		{"invalid review", `{"verdict":"maybe"}`, true, protocol.Ship{Hold: `review.json has an unknown verdict "maybe"`}, `Left open: review.json has an unknown verdict "maybe".`},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := control(t, test.review)
			gh := &stepGH{state: "OPEN"}
			if err := Merge(t.Context(), MergeOptions{Control: directory, Branch: "machinist/t_1", AutoMerge: test.autoMerge, GH: gh.run}); err != nil {
				t.Fatal(err)
			}
			// The control plane still watches its checks, so a failed one fails the job.
			if request := handedOver(t, directory); request == nil || *request != test.want {
				t.Fatalf("ship.json = %+v, want %+v", request, test.want)
			}
			if result := readFile(t, directory, "result.md"); result != test.result+" https://github.com/o/r/pull/7" {
				t.Fatalf("result.md = %q", result)
			}
		})
	}
}

func TestMergeWithoutReviewNeedsTheFlag(t *testing.T) {
	directory := control(t, "")
	gh := &stepGH{state: "OPEN"}
	if err := Merge(t.Context(), MergeOptions{Control: directory, Branch: "machinist/t_1", AutoMerge: true, WithoutReview: true, GH: gh.run}); err != nil {
		t.Fatal(err)
	}
	request := handedOver(t, directory)
	if request == nil || *request != (protocol.Ship{WithoutReview: true}) || Blocker(true, *request) != "" {
		t.Fatalf("ship.json = %+v", request)
	}
}

func TestMergeDoesNothingWithoutAPullRequest(t *testing.T) {
	directory := control(t, accepted)
	gh := &stepGH{}
	if err := Merge(t.Context(), MergeOptions{Control: directory, Branch: "machinist/t_1", AutoMerge: true, GH: gh.run}); err != nil {
		t.Fatal(err)
	}
	if request := handedOver(t, directory); request != nil {
		t.Fatalf("ship.json = %+v", request)
	}
	if result := readFile(t, directory, "result.md"); result != "" {
		t.Fatalf("result.md = %q", result)
	}
}

func TestMergeRecordsAPullRequestAPersonAlreadySettled(t *testing.T) {
	for state, want := range map[string]string{"MERGED": "Merged: ", "CLOSED": "Closed without merging: "} {
		t.Run(state, func(t *testing.T) {
			directory := control(t, accepted)
			gh := &stepGH{state: state}
			if err := Merge(t.Context(), MergeOptions{Control: directory, Branch: "machinist/t_1", AutoMerge: true, GH: gh.run}); err != nil {
				t.Fatal(err)
			}
			if request := handedOver(t, directory); request != nil {
				t.Fatalf("ship.json = %+v", request)
			}
			if result := readFile(t, directory, "result.md"); result != want+"https://github.com/o/r/pull/7" {
				t.Fatalf("result.md = %q", result)
			}
		})
	}
}

func TestMergeFailsWhenGitHubCannotBeRead(t *testing.T) {
	directory := control(t, accepted)
	gh := func(context.Context, ...string) ([]byte, string, error) {
		return nil, "HTTP 502: Bad Gateway", errors.New("exit status 1")
	}
	err := Merge(t.Context(), MergeOptions{Control: directory, Branch: "machinist/t_1", AutoMerge: true, GH: gh})
	if err == nil || !strings.Contains(err.Error(), "Bad Gateway") {
		t.Fatalf("err = %v", err)
	}
}

// The control plane applies its own github.auto_merge, so a step cannot merge
// what the settings forbid, and only an accepted review may merge.
func TestBlockerNeedsAutoMergeAndAnAcceptedReview(t *testing.T) {
	for _, test := range []struct {
		autoMerge bool
		request   protocol.Ship
		want      string
	}{
		{true, protocol.Ship{Verdict: Accepted}, ""},
		{false, protocol.Ship{Verdict: Accepted}, "auto-merge is off"},
		{true, protocol.Ship{Verdict: Accepted, Hold: "held"}, "held"},
		{true, protocol.Ship{WithoutReview: true}, ""},
		{true, protocol.Ship{}, ErrNoReview.Error()},
		{true, protocol.Ship{Verdict: RevisionNeeded}, "the review still asks for changes"},
		{true, protocol.Ship{Verdict: HumanDecisionNeeded}, "the review needs a person's decision"},
		{true, protocol.Ship{Verdict: "maybe"}, "the review needs a person's decision"},
	} {
		if got := Blocker(test.autoMerge, test.request); got != test.want {
			t.Errorf("Blocker(%v, %+v) = %q, want %q", test.autoMerge, test.request, got, test.want)
		}
	}
}
