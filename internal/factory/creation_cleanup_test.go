package factory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureWorktrees(t *testing.T, root string) []string {
	t.Helper()
	b, e := exec.Command("git", "-C", root, "worktree", "list", "--porcelain").CombinedOutput()
	if e != nil {
		t.Fatalf("%s %v", b, e)
	}
	dirs := []string{}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			dirs = append(dirs, strings.TrimPrefix(line, "worktree "))
		}
	}
	return dirs
}
func fixtureFactoryBranches(t *testing.T, root string) string {
	t.Helper()
	b, e := exec.Command("git", "-C", root, "for-each-ref", "--format=%(refname)", "refs/heads/codex/factory-*").CombinedOutput()
	if e != nil {
		t.Fatalf("%s %v", b, e)
	}
	return strings.TrimSpace(string(b))
}
func TestAbortedCreationRemovesOnlyUnchangedAttemptWorkspace(t *testing.T) {
	for _, change := range []string{"none", "untracked", "ignored", "new-commit"} {
		t.Run(change, func(t *testing.T) {
			s, _ := fixture(t)
			root := s.cfg.Projects["project"].Path
			if change == "ignored" {
				if e := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("notes.secret\n"), 0600); e != nil {
					t.Fatal(e)
				}
				for _, args := range [][]string{{"add", ".gitignore"}, {"commit", "-m", "Ignore private notes"}} {
					if b, e := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); e != nil {
						t.Fatalf("%s %v", b, e)
					}
				}
			}
			var dir string
			s.mu.Lock()
			task, e := s.create("project", "Abort", "Do not publish this attempt", "", func() bool {
				dirs := fixtureWorktrees(t, root)
				if len(dirs) != 2 {
					t.Error("attempt workspace missing")
					return false
				}
				dir = dirs[1]
				switch change {
				case "untracked":
					if e := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0600); e != nil {
						t.Error(e)
					}
				case "ignored":
					if e := os.WriteFile(filepath.Join(dir, "notes.secret"), []byte("keep"), 0600); e != nil {
						t.Error(e)
					}
				case "new-commit":
					if e := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("new committed work"), 0600); e != nil {
						t.Error(e)
					}
					for _, args := range [][]string{{"add", "file.txt"}, {"commit", "-m", "Preserve unexpected work"}} {
						if b, e := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); e != nil {
							t.Errorf("%s %v", b, e)
						}
					}
				}
				return false
			})
			count := len(s.tasks)
			s.mu.Unlock()
			if e == nil || task != nil || count != 0 {
				t.Fatal("aborted attempt published")
			}
			if change == "none" {
				if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
					t.Fatal("unused attempt directory retained")
				}
				if len(fixtureWorktrees(t, root)) != 1 || fixtureFactoryBranches(t, root) != "" {
					t.Fatal("aborted attempt left a worktree or branch")
				}
			} else {
				if !strings.Contains(e.Error(), "workspace retained for inspection") {
					t.Fatal("changed workspace retention not explained")
				}
				if _, statErr := os.Stat(dir); statErr != nil {
					t.Fatal("changed attempt directory deleted")
				}
				if len(fixtureWorktrees(t, root)) != 2 || fixtureFactoryBranches(t, root) == "" {
					t.Fatal("changed attempt branch/worktree deleted")
				}
				name := "notes.txt"
				if change == "ignored" {
					name = "notes.secret"
				}
				if change == "new-commit" {
					name = "file.txt"
				}
				data, readErr := os.ReadFile(filepath.Join(dir, name))
				if readErr != nil || len(data) == 0 {
					t.Fatal("saved work removed")
				}
			}
		})
	}
}
