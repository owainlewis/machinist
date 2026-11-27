package runner

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
)

func initBareRemote(t *testing.T) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "remote.git")
	seed := t.TempDir()
	runGitCmd := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + t.TempDir(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=tester",
			"GIT_AUTHOR_EMAIL=tester@example.test",
			"GIT_COMMITTER_NAME=tester",
			"GIT_COMMITTER_EMAIL=tester@example.test",
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGitCmd(filepath.Dir(bare), "init", "--bare", "--initial-branch=main", bare)
	runGitCmd(seed, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(seed, "add", "README.md")
	runGitCmd(seed, "commit", "-m", "Initial commit")
	runGitCmd(seed, "remote", "add", "origin", bare)
	runGitCmd(seed, "push", "-u", "origin", "main")
	return bare
}

func checkoutScript(bare string) string {
	return `git clone --quiet '` + bare + `' . && (git switch "$MACHINIST_BRANCH" 2>/dev/null || git switch -c "$MACHINIST_BRANCH")`
}

func TestCheckpointCapturesCommitsUncommittedAndControlFilesAndResumes(t *testing.T) {
	bare := initBareRemote(t)
	req1 := stepsRequest(
		Step{Name: "checkout", Run: checkoutScript(bare)},
		Step{Name: "implement", Run: `
echo "feature code" > feature.txt
git add feature.txt
git commit -m "Add feature"
echo "updated readme" >> README.md
echo "untracked note" > notes.txt
printf 'Chose compact patch.\n' > "$MACHINIST_DIR/assumptions.md"
`},
		Step{Name: "review", Run: `
printf '{"verdict":"revision_needed"}\n' > "$MACHINIST_DIR/review.json"
echo "review failed" >&2
exit 1
`},
		Step{Name: "publish", Run: `echo "published" > "$MACHINIST_DIR/result.md"`},
	)

	res1, _ := run(t, req1)
	if res1.State != StateFailed || res1.FailedStep != "review" {
		t.Fatalf("res1 = %+v", res1)
	}
	if res1.Checkpoint == nil {
		t.Fatalf("expected checkpoint on review failure, got nil")
	}
	if res1.Checkpoint.FailedStep != "review" || res1.Checkpoint.HeadSHA == "" {
		t.Fatalf("checkpoint metadata = %+v", res1.Checkpoint)
	}
	if len(res1.Checkpoint.Patch) == 0 || !bytes.Contains(res1.Checkpoint.Patch, []byte("Add feature")) {
		t.Fatalf("expected commit patch containing Add feature, got %q", string(res1.Checkpoint.Patch))
	}
	if len(res1.Checkpoint.Worktree) == 0 || !bytes.Contains(res1.Checkpoint.Worktree, []byte("updated readme")) || !bytes.Contains(res1.Checkpoint.Worktree, []byte("untracked note")) {
		t.Fatalf("expected worktree patch with modified and untracked files, got %q", string(res1.Checkpoint.Worktree))
	}
	if res1.Checkpoint.ControlFiles["assumptions.md"] != "Chose compact patch.\n" {
		t.Fatalf("assumptions.md = %q", res1.Checkpoint.ControlFiles["assumptions.md"])
	}
	if !strings.Contains(res1.Checkpoint.ControlFiles["review.json"], "revision_needed") {
		t.Fatalf("review.json = %q", res1.Checkpoint.ControlFiles["review.json"])
	}

	// Resume from review in a brand new workspace; implement must be skipped.
	req2 := stepsRequest(
		Step{Name: "checkout", Run: checkoutScript(bare)},
		Step{Name: "implement", Run: `echo "should not run" >&2; exit 99`},
		Step{Name: "review", Run: `
test "$(git rev-list --count origin/HEAD..HEAD)" = "1"
grep -q "feature code" feature.txt
grep -q "updated readme" README.md
grep -q "untracked note" notes.txt
grep -q "Chose compact patch." "$MACHINIST_DIR/assumptions.md"
printf '{"verdict":"accepted"}\n' > "$MACHINIST_DIR/review.json"
`},
		Step{Name: "publish", Run: `
grep -q '"accepted"' "$MACHINIST_DIR/review.json"
printf 'Published after resume.\n' > "$MACHINIST_DIR/result.md"
`},
	)
	req2.ResumeFromStep = "review"
	req2.Checkpoint = res1.Checkpoint

	res2, _ := run(t, req2)
	if res2.State != StateSucceeded {
		t.Fatalf("res2 = %+v", res2)
	}
	if res2.Report == nil || res2.Report.Summary != "Published after resume." {
		t.Fatalf("report = %+v", res2.Report)
	}
	if len(res2.Stages) != 4 {
		t.Fatalf("stages = %+v", res2.Stages)
	}
	if res2.Stages[0].State != string(StateSucceeded) ||
		res2.Stages[1].State != protocol.StageSkipped || res2.Stages[1].Outcome != protocol.StageOutcomeResumed ||
		res2.Stages[2].State != string(StateSucceeded) ||
		res2.Stages[3].State != string(StateSucceeded) {
		t.Fatalf("unexpected stages = %+v", res2.Stages)
	}
}

func TestCheckpointNotCapturedWhenCheckoutFails(t *testing.T) {
	req := stepsRequest(
		Step{Name: "checkout", Run: `echo "clone failed" >&2; exit 1`},
		Step{Name: "implement", Run: `true`},
	)
	res, _ := run(t, req)
	if res.State != StateFailed || res.FailedStep != "checkout" {
		t.Fatalf("res = %+v", res)
	}
	if res.Checkpoint != nil {
		t.Fatalf("expected no checkpoint when checkout fails, got %+v", res.Checkpoint)
	}
}

func TestCheckpointWhenBranchAlreadyPushedPreservesHeadAndControlFiles(t *testing.T) {
	bare := initBareRemote(t)
	req1 := stepsRequest(
		Step{Name: "checkout", Run: checkoutScript(bare)},
		Step{Name: "implement", Run: `
echo "pushed change" > pushed.txt
git add pushed.txt
git commit -m "Pushed change"
`},
		Step{Name: "publish", Run: `
git push -u origin HEAD
printf '{"verdict":"accepted"}\n' > "$MACHINIST_DIR/review.json"
printf '{"decision":"approve","risk":"low","reason":"ok"}\n' > "$MACHINIST_DIR/merge-gate.json"
`},
		Step{Name: "merge", Run: `echo "transient github error" >&2; exit 1`},
	)
	res1, _ := run(t, req1)
	if res1.State != StateFailed || res1.FailedStep != "merge" || res1.Checkpoint == nil {
		t.Fatalf("res1 = %+v", res1)
	}
	if len(res1.Checkpoint.Patch) != 0 {
		t.Fatalf("expected empty patch when commits are already on origin/$MACHINIST_BRANCH, got %d bytes", len(res1.Checkpoint.Patch))
	}
	if !strings.Contains(res1.Checkpoint.ControlFiles["merge-gate.json"], "approve") {
		t.Fatalf("merge-gate.json not saved: %+v", res1.Checkpoint.ControlFiles)
	}

	// Resume from merge: checkout fetches origin/$MACHINIST_BRANCH with the exact SHA.
	req2 := stepsRequest(
		Step{Name: "checkout", Run: checkoutScript(bare)},
		Step{Name: "implement", Run: `exit 99`},
		Step{Name: "publish", Run: `exit 99`},
		Step{Name: "merge", Run: `
test "$(git rev-parse HEAD)" = "` + res1.Checkpoint.HeadSHA + `"
grep -q "pushed change" pushed.txt
grep -q '"approve"' "$MACHINIST_DIR/merge-gate.json"
grep -q '"accepted"' "$MACHINIST_DIR/review.json"
printf 'Merged cleanly.\n' > "$MACHINIST_DIR/result.md"
`},
	)
	req2.ResumeFromStep = "merge"
	req2.Checkpoint = res1.Checkpoint

	res2, _ := run(t, req2)
	if res2.State != StateSucceeded || res2.Report == nil || res2.Report.Summary != "Merged cleanly." {
		t.Fatalf("res2 = %+v", res2)
	}
	if res2.Stages[1].State != protocol.StageSkipped || res2.Stages[2].State != protocol.StageSkipped {
		t.Fatalf("stages = %+v", res2.Stages)
	}
}

func TestCheckpointRespectsMaxPatchBytesCap(t *testing.T) {
	bare := initBareRemote(t)
	req := stepsRequest(
		Step{Name: "checkout", Run: checkoutScript(bare)},
		Step{Name: "implement", Run: `head -c 4194400 /dev/zero | tr '\0' a > huge.txt`},
		Step{Name: "review", Run: `printf 'Failed with huge diff.\n' > "$MACHINIST_DIR/result.md"; exit 1`},
	)
	res, _ := run(t, req)
	if res.State != StateFailed || res.FailedStep != "review" {
		t.Fatalf("res = %+v", res)
	}
	if res.Report == nil || res.Report.FailedStep != "review" {
		t.Fatalf("report = %+v", res.Report)
	}
	if res.Checkpoint != nil {
		t.Fatalf("expected oversized checkpoint to be dropped, got %d bytes", res.Checkpoint.SizeBytes())
	}
}

func TestCheckpointHandlesMergeCommitsAndReusesChangePatch(t *testing.T) {
	bare := initBareRemote(t)
	req1 := stepsRequest(
		Step{Name: "checkout", Run: checkoutScript(bare)},
		Step{Name: "implement", Run: `
echo "branch work" > work.txt
git add work.txt
git commit -m "Branch work"
git checkout -b side origin/main
echo "side work" > side.txt
git add side.txt
git commit -m "Side work"
git checkout "$MACHINIST_BRANCH"
git merge --no-ff side -m "Merge side"
`},
		Step{Name: "publish", Run: `echo "push rejected" >&2; exit 1`},
	)
	res1, _ := run(t, req1)
	if res1.State != StateFailed || res1.FailedStep != "publish" || res1.Checkpoint == nil || len(res1.Checkpoint.Patch) == 0 {
		t.Fatalf("res1 = %+v", res1)
	}

	req2 := stepsRequest(
		Step{Name: "checkout", Run: checkoutScript(bare)},
		Step{Name: "implement", Run: `exit 99`},
		Step{Name: "publish", Run: `
grep -q "branch work" work.txt
grep -q "side work" side.txt
printf 'Merged commits restored.\n' > "$MACHINIST_DIR/result.md"
`},
	)
	req2.ResumeFromStep = "publish"
	req2.Checkpoint = res1.Checkpoint

	res2, _ := run(t, req2)
	if res2.State != StateSucceeded || res2.Report == nil || res2.Report.Summary != "Merged commits restored." {
		t.Fatalf("res2 = %+v", res2)
	}
}
