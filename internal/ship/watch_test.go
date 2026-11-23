package ship

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeGitHub is one pull request. Each look reads views and checks from the
// front of their lists, and the last entry repeats.
type fakeGitHub struct {
	views  []PullRequest
	checks [][]Check
	// refuse is what GitHub answers to each merge; "" merges.
	refuse     []string
	updateErr  error
	readErr    error
	merged     []string // the head commits merges named
	updates    int
	viewCount  int
	checkCount int
}

func (f *fakeGitHub) PullRequest(context.Context) (PullRequest, error) {
	if f.readErr != nil {
		return PullRequest{}, f.readErr
	}
	pr := f.views[min(f.viewCount, len(f.views)-1)]
	f.viewCount++
	return pr, nil
}

func (f *fakeGitHub) Checks(context.Context) ([]Check, error) {
	checks := f.checks[min(f.checkCount, len(f.checks)-1)]
	f.checkCount++
	return checks, nil
}

func (f *fakeGitHub) Merge(_ context.Context, head string) error {
	attempt := len(f.merged)
	f.merged = append(f.merged, head)
	if len(f.refuse) > 0 {
		if answer := f.refuse[min(attempt, len(f.refuse)-1)]; answer != "" {
			return errors.New(answer)
		}
	}
	return nil
}

func (f *fakeGitHub) UpdateBranch(context.Context) error {
	f.updates++
	return f.updateErr
}

func open(head, mergeState string) PullRequest {
	return PullRequest{Number: 7, URL: "https://github.com/o/r/pull/7", State: "OPEN", MergeStateStatus: mergeState, HeadRefOid: head}
}

var (
	passed  = []Check{{Name: "test", Bucket: "pass"}, {Name: "lint", Bucket: "skipping"}}
	running = []Check{{Name: "test", Bucket: "pending"}, {Name: "lint", Bucket: "pass"}}
	start   = time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
)

func look(t *testing.T, github *fakeGitHub, watch Watch, now time.Time) Look {
	t.Helper()
	got, err := LookOnce(t.Context(), github, watch, now)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestLookMergesTheCheckedHeadOnceEveryCheckPassed(t *testing.T) {
	github := &fakeGitHub{views: []PullRequest{open("sha1", "CLEAN")}, checks: [][]Check{running, passed}}
	watch := Watch{Since: start}
	first := look(t, github, watch, start.Add(time.Minute))
	if first.Outcome != Waiting || len(github.merged) != 0 {
		t.Fatalf("first look = %+v, merged %v", first, github.merged)
	}
	second := look(t, github, first.Watch, start.Add(2*time.Minute))
	if second.Outcome != Merged || second.Message != "Merged: https://github.com/o/r/pull/7" || !slices.Equal(github.merged, []string{"sha1"}) {
		t.Fatalf("second look = %+v, merged %v", second, github.merged)
	}
}

func TestLookFailsOnAFailedCheckEvenWhenTheChangeStaysOpen(t *testing.T) {
	for _, hold := range []string{"", "auto-merge is off"} {
		github := &fakeGitHub{views: []PullRequest{open("sha1", "CLEAN")}, checks: [][]Check{{{Name: "lint", Bucket: "pass"}, {Name: "test", Bucket: "fail", Link: "https://ci/2"}}}}
		got := look(t, github, Watch{Hold: hold, Since: start}, start)
		if got.Outcome != CheckFailed || got.Message != `Left open: check "test" failed: https://ci/2. https://github.com/o/r/pull/7` || len(github.merged) != 0 {
			t.Fatalf("hold %q: look = %+v, merged %v", hold, got, github.merged)
		}
	}
	cancelled := &fakeGitHub{views: []PullRequest{open("sha1", "CLEAN")}, checks: [][]Check{{{Name: "test", Bucket: "cancel"}}}}
	if got := look(t, cancelled, Watch{Since: start}, start); got.Outcome != CheckFailed {
		t.Fatalf("a cancelled check: %+v", got)
	}
}

func TestLookLeavesAHeldPullRequestOpenOnceChecksPass(t *testing.T) {
	github := &fakeGitHub{views: []PullRequest{open("sha1", "CLEAN")}, checks: [][]Check{passed}}
	got := look(t, github, Watch{Hold: "auto-merge is off", Since: start}, start)
	// The run's result already said why, so nothing more is said.
	if got.Outcome != LeftOpen || got.Message != "" || len(github.merged) != 0 {
		t.Fatalf("look = %+v, merged %v", got, github.merged)
	}
}

func TestLookWaitsForChecksToStartThenGoesOnWithoutAny(t *testing.T) {
	github := &fakeGitHub{views: []PullRequest{open("sha1", "CLEAN")}, checks: [][]Check{nil}}
	if got := look(t, github, Watch{Since: start}, start.Add(StartGrace-time.Second)); got.Outcome != Waiting || len(github.merged) != 0 {
		t.Fatalf("early look = %+v", got)
	}
	if got := look(t, github, Watch{Since: start}, start.Add(StartGrace)); got.Outcome != Merged {
		t.Fatalf("late look = %+v", got)
	}
}

func TestLookUpdatesABranchThatFellBehindAndMergesItsNewHead(t *testing.T) {
	github := &fakeGitHub{
		views:  []PullRequest{open("sha1", "BEHIND"), open("sha1", "CLEAN"), open("sha2", "CLEAN")},
		checks: [][]Check{passed, {{Name: "lint", Bucket: "pass"}}, running, passed},
	}
	now := start
	got := look(t, github, Watch{Since: start}, now)
	if got.Outcome != Waiting || github.updates != 1 || got.Watch.OldHead != "sha1" || got.Watch.Updates != 1 || !slices.Equal(got.Watch.Seen, []string{"test", "lint"}) {
		t.Fatalf("first look = %+v, updates %d", got, github.updates)
	}
	// GitHub still reports the old head, whose checks passed: no merge.
	now = now.Add(time.Minute)
	if got = look(t, github, got.Watch, now); got.Outcome != Waiting || github.checkCount != 1 {
		t.Fatalf("look at the old head = %+v", got)
	}
	// The new head has only one of the checks so far, then one is running.
	for range 2 {
		now = now.Add(time.Minute)
		if got = look(t, github, got.Watch, now); got.Outcome != Waiting || len(github.merged) != 0 {
			t.Fatalf("look at the new head = %+v", got)
		}
	}
	now = now.Add(time.Minute)
	if got = look(t, github, got.Watch, now); got.Outcome != Merged || !slices.Equal(github.merged, []string{"sha2"}) {
		t.Fatalf("last look = %+v, merged %v", got, github.merged)
	}
}

func TestLookLeavesItOpenWhenTheHeadDoesNotMoveAfterAnUpdate(t *testing.T) {
	github := &fakeGitHub{views: []PullRequest{open("sha1", "CLEAN")}, checks: [][]Check{passed}}
	watch := Watch{Since: start, OldHead: "sha1", Seen: []string{"test"}, Updates: 1}
	if got := look(t, github, watch, start.Add(time.Minute)); got.Outcome != Waiting {
		t.Fatalf("early look = %+v", got)
	}
	got := look(t, github, watch, start.Add(StartGrace))
	if got.Outcome != LeftOpen || got.Message != "Left open: the pull request did not move to the updated branch. https://github.com/o/r/pull/7" || len(github.merged) != 0 {
		t.Fatalf("look = %+v", got)
	}
}

func TestLookLeavesItOpenWhenACheckDoesNotComeBackAfterAnUpdate(t *testing.T) {
	github := &fakeGitHub{views: []PullRequest{open("sha2", "CLEAN")}, checks: [][]Check{{{Name: "lint", Bucket: "pass"}}}}
	watch := Watch{Since: start, Seen: []string{"test", "lint"}, Updates: 1}
	if got := look(t, github, watch, start.Add(time.Minute)); got.Outcome != Waiting {
		t.Fatalf("early look = %+v", got)
	}
	got := look(t, github, watch, start.Add(StartGrace))
	if got.Outcome != LeftOpen || !strings.Contains(got.Message, `the checks "test" did not start on the updated branch`) || len(github.merged) != 0 {
		t.Fatalf("look = %+v", got)
	}
}

func TestLookLeavesItOpenWhenGitHubRefusesTheMerge(t *testing.T) {
	github := &fakeGitHub{views: []PullRequest{open("sha1", "CLEAN")}, checks: [][]Check{passed}, refuse: []string{"Required review is missing"}}
	got := look(t, github, Watch{Since: start}, start)
	if got.Outcome != LeftOpen || got.Message != "Left open: GitHub refused the merge: Required review is missing. https://github.com/o/r/pull/7" {
		t.Fatalf("look = %+v", got)
	}
}

func TestLookUpdatesTheBranchWhenAnotherMergeLandsFirst(t *testing.T) {
	github := &fakeGitHub{views: []PullRequest{open("sha1", "CLEAN"), open("sha1", "BEHIND")}, checks: [][]Check{passed}, refuse: []string{"Base branch was modified"}}
	got := look(t, github, Watch{Since: start}, start)
	if got.Outcome != Waiting || github.updates != 1 || got.Watch.OldHead != "sha1" {
		t.Fatalf("look = %+v, updates %d", got, github.updates)
	}
}

func TestLookGivesUpWhenTheBranchKeepsFallingBehind(t *testing.T) {
	github := &fakeGitHub{views: []PullRequest{open("sha4", "BEHIND")}, checks: [][]Check{passed}}
	got := look(t, github, Watch{Since: start, Seen: []string{"test", "lint"}, Updates: 3}, start)
	if got.Outcome != LeftOpen || !strings.Contains(got.Message, "fell behind the default branch three times") || github.updates != 0 {
		t.Fatalf("look = %+v, updates %d", got, github.updates)
	}
}

func TestLookLeavesItOpenWhenTheBranchCannotBeUpdated(t *testing.T) {
	github := &fakeGitHub{views: []PullRequest{open("sha1", "BEHIND")}, checks: [][]Check{passed}, updateErr: errors.New("merge conflict between base and head")}
	got := look(t, github, Watch{Since: start}, start)
	if got.Outcome != LeftOpen || !strings.Contains(got.Message, "GitHub could not update the branch: merge conflict") {
		t.Fatalf("look = %+v", got)
	}
}

func TestLookLeavesAPullRequestAPersonSettledToTheWatcher(t *testing.T) {
	merged := open("sha1", "CLEAN")
	merged.State = "MERGED"
	github := &fakeGitHub{views: []PullRequest{merged}, checks: [][]Check{passed}}
	if got := look(t, github, Watch{Since: start}, start); got.Outcome != Settled || github.checkCount != 0 {
		t.Fatalf("look = %+v", got)
	}
}

func TestLookReturnsAReadErrorSoTheWatcherBacksOff(t *testing.T) {
	github := &fakeGitHub{readErr: errors.New("rate limit")}
	if _, err := LookOnce(t.Context(), github, Watch{Since: start}, start); err == nil {
		t.Fatal("no error")
	}
}
