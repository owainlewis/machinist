package ship

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoGit runs real git in dir, like machinist push does in the workspace,
// and rejects every push, so Push saves the patch.
func repoGit(dir string) Git {
	return func(ctx context.Context, stdout io.Writer, args ...string) (string, error) {
		if args[0] == "push" {
			return " ! [remote rejected] HEAD -> machinist/t_1 (no workflows)\n", errors.New("exit status 1")
		}
		var stderr bytes.Buffer
		command := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		command.Stdout, command.Stderr = stdout, &stderr
		err := command.Run()
		return stderr.String(), err
	}
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Job", "-c", "user.email=job@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// savedPatch makes a repository whose main branch has notes.txt, lets branch
// shape the job branch, rejects the push and returns the saved patch.
func savedPatch(t *testing.T, branch func(repo string)) (repo, patch, log string) {
	t.Helper()
	repo = t.TempDir()
	run(t, repo, "init", "--quiet")
	write(t, filepath.Join(repo, "notes.txt"), "one\n")
	run(t, repo, "add", ".")
	run(t, repo, "commit", "--quiet", "-m", "Start")
	run(t, repo, "checkout", "--quiet", "-b", "machinist/t_1")
	branch(repo)
	path := filepath.Join(t.TempDir(), "change.patch")
	var output bytes.Buffer
	err := Push(t.Context(), PushOptions{Git: repoGit(repo), Log: &output, Patch: path, Base: "main"})
	var rejected *PushRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("err = %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no patch: %v\n%s", err, output.String())
	}
	return repo, string(body), output.String()
}

// applyToMain applies the patch to main with git am, as the job page advises,
// and returns notes.txt after it.
func applyToMain(t *testing.T, repo, patch string) string {
	t.Helper()
	clone := t.TempDir()
	run(t, clone, "clone", "--quiet", "--branch", "main", repo, ".")
	write(t, filepath.Join(clone, "change.patch"), patch)
	run(t, clone, "am", "--quiet", "change.patch")
	body, err := os.ReadFile(filepath.Join(clone, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// A job that merged the base branch in to resolve a conflict keeps the
// resolution: format-patch alone skips merge commits, so its patch left the
// resolution out and did not apply to the base branch.
func TestThePatchKeepsAMergeCommitsResolution(t *testing.T) {
	repo, patch, log := savedPatch(t, func(repo string) {
		write(t, filepath.Join(repo, "notes.txt"), "job\n")
		run(t, repo, "commit", "--quiet", "-am", "Use the job's notes")
		run(t, repo, "checkout", "--quiet", "main")
		write(t, filepath.Join(repo, "notes.txt"), "main\n")
		run(t, repo, "commit", "--quiet", "-am", "Change the notes on main")
		run(t, repo, "checkout", "--quiet", "machinist/t_1")
		command := exec.Command("git", "-C", repo, "-c", "user.name=Job", "-c", "user.email=job@example.com", "merge", "--quiet", "main")
		if output, err := command.CombinedOutput(); err == nil {
			t.Fatalf("the merge did not conflict:\n%s", output)
		}
		write(t, filepath.Join(repo, "notes.txt"), "job and main\n")
		run(t, repo, "commit", "--quiet", "-am", "Merge main")
	})
	if !strings.Contains(patch, "+job and main") || !strings.Contains(patch, "Subject: [PATCH] Use the job's notes") {
		t.Fatalf("patch:\n%s", patch)
	}
	if !strings.Contains(log, "merge commits") {
		t.Fatalf("log = %s", log)
	}
	if got := applyToMain(t, repo, patch); got != "job and main\n" {
		t.Fatalf("notes.txt = %q", got)
	}
}

// A branch with no merge commits keeps one patch per commit.
func TestThePatchKeepsEachCommitOfALinearBranch(t *testing.T) {
	repo, patch, _ := savedPatch(t, func(repo string) {
		write(t, filepath.Join(repo, "notes.txt"), "two\n")
		run(t, repo, "commit", "--quiet", "-am", "Write two")
		write(t, filepath.Join(repo, "notes.txt"), "three\n")
		run(t, repo, "commit", "--quiet", "-am", "Write three")
	})
	if !strings.Contains(patch, "Subject: [PATCH 1/2] Write two") || !strings.Contains(patch, "Subject: [PATCH 2/2] Write three") {
		t.Fatalf("patch:\n%s", patch)
	}
	if got := applyToMain(t, repo, patch); got != "three\n" {
		t.Fatalf("notes.txt = %q", got)
	}
}
