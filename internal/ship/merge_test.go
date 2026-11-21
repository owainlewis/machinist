package ship

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGH answers gh calls from a script of check results, one per look.
type fakeGH struct {
	t          *testing.T
	noPR       bool
	state      string
	mergeState []string // mergeStateStatus on each pr view, the last repeats
	checks     []string // stdout of each pr checks call, the last repeats
	pending    bool     // pr checks exits non-zero, as gh does while pending
	refuse     string   // pr merge fails with this text
	calls      []string
	views      int
	looks      int
}

func (f *fakeGH) run(_ context.Context, args ...string) ([]byte, string, error) {
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	switch {
	case strings.HasPrefix(call, "pr view"):
		if f.noPR {
			return nil, "no pull requests found for branch \"machinist/t_1\"", errors.New("exit status 1")
		}
		state := f.state
		if state == "" {
			state = "OPEN"
		}
		merge := "CLEAN"
		if len(f.mergeState) > 0 {
			merge = f.mergeState[min(f.views, len(f.mergeState)-1)]
		}
		f.views++
		return []byte(`{"number":7,"url":"https://github.com/o/r/pull/7","state":"` + state + `","mergeStateStatus":"` + merge + `"}`), "", nil
	case strings.HasPrefix(call, "pr checks"):
		out := f.checks[min(f.looks, len(f.checks)-1)]
		f.looks++
		if out == "none" {
			return nil, "no checks reported on the 'machinist/t_1' branch", errors.New("exit status 1")
		}
		if strings.Contains(out, "pending") {
			return []byte(out), "", errors.New("exit status 8")
		}
		return []byte(out), "", nil
	case strings.HasPrefix(call, "pr merge"):
		if f.refuse != "" {
			return nil, f.refuse, errors.New("exit status 1")
		}
		return nil, "", nil
	case strings.HasPrefix(call, "pr update-branch"):
		return nil, "", nil
	}
	f.t.Fatalf("unexpected gh call: %s", call)
	return nil, "", nil
}

func (f *fakeGH) merged() bool {
	for _, call := range f.calls {
		if strings.HasPrefix(call, "pr merge 7 --merge --delete-branch") {
			return true
		}
	}
	return false
}

const passed = `[{"name":"test","bucket":"pass","link":"https://ci/1"}]`

func setup(t *testing.T, review string) (string, MergeOptions) {
	t.Helper()
	control := t.TempDir()
	if review != "" {
		if err := os.WriteFile(filepath.Join(control, ReviewFile), []byte(review), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return control, MergeOptions{
		Control:   control,
		Branch:    "machinist/t_1",
		AutoMerge: true,
		Sleep:     func(context.Context, time.Duration) bool { return true },
	}
}

func result(t *testing.T, control string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(control, "result.md"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(body))
}

func TestMergeMergesAnAcceptedChangeOnceChecksPass(t *testing.T) {
	control, options := setup(t, `{"verdict":"accepted","summary":"Good.","findings":[]}`)
	gh := &fakeGH{t: t, checks: []string{`[{"name":"test","bucket":"pending"}]`, passed}}
	options.GH = gh.run
	if err := Merge(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if !gh.merged() || gh.looks != 2 {
		t.Fatalf("merged = %v after %d looks; calls %v", gh.merged(), gh.looks, gh.calls)
	}
	if got := result(t, control); got != "Merged: https://github.com/o/r/pull/7" {
		t.Fatalf("result = %q", got)
	}
}

// Every way the review can fall short leaves the pull request open, without
// failing the job, and says why.
func TestMergeLeavesThePullRequestOpenUnlessTheReviewAccepted(t *testing.T) {
	for _, test := range []struct {
		name, review, want string
		autoMerge          bool
	}{
		{"auto-merge off", `{"verdict":"accepted"}`, "Left open: auto-merge is off.", false},
		{"no review", "", "Left open: the review step wrote no review.json.", true},
		{"not json", `{"verdict":`, "Left open: review.json is not valid JSON", true},
		{"no verdict", `{"summary":"x"}`, "Left open: review.json has no verdict.", true},
		{"unknown verdict", `{"verdict":"lgtm"}`, `Left open: review.json has an unknown verdict "lgtm".`, true},
		// revise marks its own fixes addressed, which is not a review of them.
		{"revised but not re-reviewed", `{"verdict":"revision_needed","findings":[{"unambiguous":true,"addressed":true,"text":"x"}]}`, "Left open: the review still asks for changes.", true},
		{"needs a person", `{"verdict":"human_decision_needed","summary":"Pick a retry limit."}`, "Left open: the review needs a person's decision: Pick a retry limit. ", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			control, options := setup(t, test.review)
			options.AutoMerge = test.autoMerge
			gh := &fakeGH{t: t, checks: []string{passed}}
			options.GH = gh.run
			if err := Merge(t.Context(), options); err != nil {
				t.Fatal(err)
			}
			if gh.merged() || gh.looks != 1 {
				t.Fatalf("merged = %v, looks = %d", gh.merged(), gh.looks)
			}
			if got := result(t, control); !strings.HasPrefix(got, test.want) || !strings.HasSuffix(got, "https://github.com/o/r/pull/7") {
				t.Fatalf("result = %q, want %q", got, test.want)
			}
		})
	}
}

func TestMergeWithoutReviewNeedsTheFlag(t *testing.T) {
	_, options := setup(t, "")
	options.WithoutReview = true
	gh := &fakeGH{t: t, checks: []string{passed}}
	options.GH = gh.run
	if err := Merge(t.Context(), options); err != nil || !gh.merged() {
		t.Fatalf("err = %v, merged = %v", err, gh.merged())
	}
}

func TestMergeFailsTheStepWhenACheckFails(t *testing.T) {
	control, options := setup(t, `{"verdict":"accepted"}`)
	gh := &fakeGH{t: t, checks: []string{`[{"name":"lint","bucket":"pass"},{"name":"test","bucket":"fail","link":"https://ci/2"}]`}}
	options.GH = gh.run
	err := Merge(t.Context(), options)
	var failed *CheckFailedError
	if !errors.As(err, &failed) || failed.Check != "test" || gh.merged() {
		t.Fatalf("err = %v, merged = %v", err, gh.merged())
	}
	if got := result(t, control); !strings.Contains(got, `check "test" failed: https://ci/2`) {
		t.Fatalf("result = %q", got)
	}
}

func TestMergeWaitsForChecksToStartThenGoesOnWithoutAny(t *testing.T) {
	_, options := setup(t, `{"verdict":"accepted"}`)
	options.StartTries = 3
	gh := &fakeGH{t: t, checks: []string{"none", "[]"}}
	options.GH = gh.run
	if err := Merge(t.Context(), options); err != nil || !gh.merged() {
		t.Fatalf("err = %v, merged = %v", err, gh.merged())
	}
	if gh.looks != 4 {
		t.Fatalf("looks = %d, want 4", gh.looks)
	}
}

func TestMergeUpdatesABranchThatFellBehind(t *testing.T) {
	_, options := setup(t, `{"verdict":"accepted"}`)
	gh := &fakeGH{t: t, checks: []string{passed}, mergeState: []string{"CLEAN", "BEHIND", "CLEAN"}}
	options.GH = gh.run
	if err := Merge(t.Context(), options); err != nil || !gh.merged() {
		t.Fatalf("err = %v, merged = %v", err, gh.merged())
	}
	if !strings.Contains(strings.Join(gh.calls, "\n"), "pr update-branch 7") || gh.looks != 2 {
		t.Fatalf("calls = %v", gh.calls)
	}
}

func TestMergeLeavesItOpenWhenGitHubRefuses(t *testing.T) {
	control, options := setup(t, `{"verdict":"accepted"}`)
	gh := &fakeGH{t: t, checks: []string{passed}, refuse: "Pull request is not mergeable: approval required\nmore"}
	options.GH = gh.run
	if err := Merge(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if got := result(t, control); got != "Left open: GitHub refused the merge: Pull request is not mergeable: approval required. https://github.com/o/r/pull/7" {
		t.Fatalf("result = %q", got)
	}
}

func TestMergeDoesNothingWithoutAPullRequest(t *testing.T) {
	control, options := setup(t, `{"verdict":"accepted"}`)
	if err := os.WriteFile(filepath.Join(control, "result.md"), []byte("The work needed no change.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gh := &fakeGH{t: t, noPR: true}
	options.GH = gh.run
	if err := Merge(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if got := result(t, control); got != "The work needed no change." {
		t.Fatalf("result = %q", got)
	}
}

func TestMergeStopsWhenCancelled(t *testing.T) {
	_, options := setup(t, `{"verdict":"accepted"}`)
	ctx, cancel := context.WithCancel(t.Context())
	options.Sleep = func(context.Context, time.Duration) bool { cancel(); return false }
	gh := &fakeGH{t: t, checks: []string{`[{"name":"test","bucket":"pending"}]`}}
	options.GH = gh.run
	if err := Merge(ctx, options); !errors.Is(err, context.Canceled) || gh.merged() {
		t.Fatalf("err = %v, merged = %v", err, gh.merged())
	}
}

// A pull request left for a person still fails the job when a check fails.
func TestMergeFailsOnAFailedCheckEvenWhenLeavingItOpen(t *testing.T) {
	control, options := setup(t, `{"verdict":"human_decision_needed"}`)
	options.AutoMerge = false
	gh := &fakeGH{t: t, checks: []string{`[{"name":"test","bucket":"fail","link":"https://ci/3"}]`}}
	options.GH = gh.run
	var failed *CheckFailedError
	if err := Merge(t.Context(), options); !errors.As(err, &failed) || gh.merged() {
		t.Fatalf("err = %v, merged = %v", err, gh.merged())
	}
	if got := result(t, control); !strings.Contains(got, `check "test" failed`) {
		t.Fatalf("result = %q", got)
	}
}
