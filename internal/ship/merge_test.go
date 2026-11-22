package ship

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
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
	repo       string   // stdout of gh api repos/{owner}/{repo}; "" allows merge commits
	repoErr    string   // gh api fails with this text
	// headLag is how many pr views after an update still report the old head
	// commit; -1 means the head never moves.
	headLag int
	updates int // head commit n is "sha<n>"
	moveAt  int // the pr view from which the head is the updated one
	calls   []string
	views   int
	looks   int
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
		return []byte(`{"number":7,"url":"https://github.com/o/r/pull/7","state":"` + state + `","mergeStateStatus":"` + merge + `","headRefOid":"` + f.head() + `"}`), "", nil
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
		if want := args[len(args)-1]; want != f.head() {
			return nil, "Head branch was modified. Review and try the merge again.", errors.New("exit status 1")
		}
		return nil, "", nil
	case strings.HasPrefix(call, "pr update-branch"):
		if f.headLag >= 0 {
			f.updates++
			f.moveAt = f.views + f.headLag
		}
		return nil, "", nil
	case call == "api repos/{owner}/{repo}":
		if f.repoErr != "" {
			return nil, f.repoErr, errors.New("exit status 1")
		}
		if f.repo == "" {
			return []byte(`{"allow_merge_commit":true,"allow_squash_merge":true,"allow_rebase_merge":true}`), "", nil
		}
		return []byte(f.repo), "", nil
	}
	f.t.Fatalf("unexpected gh call: %s", call)
	return nil, "", nil
}

// head is the head commit GitHub reports to the next pr view.
func (f *fakeGH) head() string {
	n := f.updates
	if n > 0 && f.views < f.moveAt {
		n--
	}
	return "sha" + strconv.Itoa(n)
}

func (f *fakeGH) merged() bool { return f.mergedWith("merge") }

func (f *fakeGH) mergedWith(method string) bool {
	for _, call := range f.calls {
		if strings.HasPrefix(call, "pr merge 7 --"+method+" --delete-branch ") {
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

// The merge uses the first of merge, squash and rebase that the repository
// allows, so squash-only and rebase-only repositories merge too.
func TestMergeUsesAMethodTheRepositoryAllows(t *testing.T) {
	for _, test := range []struct {
		name, repo, repoErr, want string
	}{
		{"all allowed", `{"allow_merge_commit":true,"allow_squash_merge":true,"allow_rebase_merge":true}`, "", "merge"},
		{"squash and rebase", `{"allow_merge_commit":false,"allow_squash_merge":true,"allow_rebase_merge":true}`, "", "squash"},
		{"squash only", `{"allow_merge_commit":false,"allow_squash_merge":true,"allow_rebase_merge":false}`, "", "squash"},
		{"rebase only", `{"allow_merge_commit":false,"allow_squash_merge":false,"allow_rebase_merge":true}`, "", "rebase"},
		// Without the settings, merge as before and let GitHub's answer say why.
		{"settings left out", `{"name":"r"}`, "", "merge"},
		{"settings unreadable", "", "HTTP 404: Not Found", "merge"},
		{"not json", `{"allow_`, "", "merge"},
	} {
		t.Run(test.name, func(t *testing.T) {
			control, options := setup(t, `{"verdict":"accepted"}`)
			gh := &fakeGH{t: t, checks: []string{passed}, repo: test.repo, repoErr: test.repoErr}
			options.GH = gh.run
			if err := Merge(t.Context(), options); err != nil {
				t.Fatal(err)
			}
			if !gh.mergedWith(test.want) {
				t.Fatalf("did not merge with --%s; calls %v", test.want, gh.calls)
			}
			if got := result(t, control); got != "Merged: https://github.com/o/r/pull/7" {
				t.Fatalf("result = %q", got)
			}
		})
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

// A long wait prints each time the pending count changes, so the step's idle
// timeout does not stop it, and stays quiet while nothing changes.
func TestMergeSaysWhenThePendingChecksChange(t *testing.T) {
	_, options := setup(t, `{"verdict":"accepted","summary":"Good.","findings":[]}`)
	two := `[{"name":"a","bucket":"pending"},{"name":"b","bucket":"pending"}]`
	one := `[{"name":"a","bucket":"pass"},{"name":"b","bucket":"pending"}]`
	gh := &fakeGH{t: t, checks: []string{two, two, two, one, one, `[{"name":"a","bucket":"pass"},{"name":"b","bucket":"pass"}]`}}
	options.GH = gh.run
	var log strings.Builder
	options.Log = &log
	if err := Merge(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(log.String(), "▸ Wait for"); got != 2 || !strings.Contains(log.String(), "Wait for 1 of 2") {
		t.Fatalf("log:\n%s", log.String())
	}
}

// After an update the new head commit's checks take a while to register. A
// pull request that had checks never merges on a round that sees none.
func TestMergeWaitsForChecksOnTheUpdatedBranch(t *testing.T) {
	control, options := setup(t, `{"verdict":"accepted"}`)
	options.StartTries = 3
	gh := &fakeGH{t: t, checks: []string{passed, "none"}, mergeState: []string{"CLEAN", "BEHIND", "CLEAN"}}
	options.GH = gh.run
	if err := Merge(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if gh.merged() {
		t.Fatalf("merged with no checks on the updated branch; calls %v", gh.calls)
	}
	if got := result(t, control); got != `Left open: the checks "test" did not start on the updated branch. https://github.com/o/r/pull/7` {
		t.Fatalf("result = %q", got)
	}
}

// Each check from the first round must come back, so one fast check does not
// stand in for a slow one that has not registered yet.
func TestMergeWaitsForEveryCheckToComeBackAfterAnUpdate(t *testing.T) {
	_, options := setup(t, `{"verdict":"accepted"}`)
	both := `[{"name":"lint","bucket":"pass"},{"name":"test","bucket":"pass"}]`
	gh := &fakeGH{t: t, checks: []string{both, "none", `[{"name":"lint","bucket":"pass"}]`, both}, mergeState: []string{"CLEAN", "BEHIND", "CLEAN"}}
	options.GH = gh.run
	if err := Merge(t.Context(), options); err != nil || !gh.merged() {
		t.Fatalf("err = %v, merged = %v", err, gh.merged())
	}
	if gh.looks != 4 {
		t.Fatalf("looks = %d, want 4", gh.looks)
	}
}

func TestMergeLeavesItOpenWhenACheckDoesNotComeBackAfterAnUpdate(t *testing.T) {
	control, options := setup(t, `{"verdict":"accepted"}`)
	options.StartTries = 2
	both := `[{"name":"lint","bucket":"pass"},{"name":"test","bucket":"pass"}]`
	gh := &fakeGH{t: t, checks: []string{both, `[{"name":"lint","bucket":"pass"}]`}, mergeState: []string{"CLEAN", "BEHIND", "CLEAN"}}
	options.GH = gh.run
	if err := Merge(t.Context(), options); err != nil || gh.merged() {
		t.Fatalf("err = %v, merged = %v", err, gh.merged())
	}
	if got := result(t, control); got != `Left open: the checks "test" did not start on the updated branch. https://github.com/o/r/pull/7` {
		t.Fatalf("result = %q", got)
	}
}

// Right after an update gh can still report the old head commit and its passed
// checks. The pull request waits for the new head, and never merges on the old
// head's checks.
func TestMergeLeavesItOpenWhenTheHeadDoesNotMoveAfterAnUpdate(t *testing.T) {
	control, options := setup(t, `{"verdict":"accepted"}`)
	options.StartTries = 2
	gh := &fakeGH{t: t, checks: []string{passed}, mergeState: []string{"CLEAN", "BEHIND", "CLEAN"}, headLag: -1}
	options.GH = gh.run
	if err := Merge(t.Context(), options); err != nil || gh.merged() {
		t.Fatalf("err = %v, merged = %v; calls %v", err, gh.merged(), gh.calls)
	}
	if gh.looks != 1 {
		t.Fatalf("looks = %d, want 1: the old head's checks were read again", gh.looks)
	}
	if got := result(t, control); got != "Left open: the pull request did not move to the updated branch. https://github.com/o/r/pull/7" {
		t.Fatalf("result = %q", got)
	}
}

func TestMergeWaitsForTheNewHeadAndMergesThatCommit(t *testing.T) {
	_, options := setup(t, `{"verdict":"accepted"}`)
	gh := &fakeGH{t: t, checks: []string{passed}, mergeState: []string{"CLEAN", "BEHIND", "CLEAN"}, headLag: 2}
	options.GH = gh.run
	if err := Merge(t.Context(), options); err != nil || !gh.merged() {
		t.Fatalf("err = %v, merged = %v; calls %v", err, gh.merged(), gh.calls)
	}
	if last := gh.calls[len(gh.calls)-1]; last != "pr merge 7 --merge --delete-branch --match-head-commit sha1" {
		t.Fatalf("merge call = %q", last)
	}
}

// A push during the wait moves the head after its checks were read, so the
// merge names the checked commit and GitHub refuses it.
func TestMergeNamesTheCheckedHeadCommit(t *testing.T) {
	control, options := setup(t, `{"verdict":"accepted"}`)
	gh := &fakeGH{t: t, checks: []string{passed}}
	options.GH = func(ctx context.Context, args ...string) ([]byte, string, error) {
		if args[1] == "checks" {
			gh.updates, gh.moveAt = 1, 0
		}
		return gh.run(ctx, args...)
	}
	if err := Merge(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(gh.calls, "\n"), "--match-head-commit sha0") {
		t.Fatalf("calls = %v", gh.calls)
	}
	if got := result(t, control); !strings.HasPrefix(got, "Left open: GitHub refused the merge: Head branch was modified") {
		t.Fatalf("result = %q", got)
	}
}
