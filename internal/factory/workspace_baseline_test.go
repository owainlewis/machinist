package factory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceBaselineFailureCleansPinnedAttempt(t *testing.T) {
	s, _ := fixture(t)
	git, e := exec.LookPath("git")
	if e != nil {
		t.Fatal(e)
	}
	bin := t.TempDir()
	marker := filepath.Join(bin, "failed-once")
	script := "#!/bin/sh\nif [ \"$1\" = 'rev-parse' ] && [ \"$2\" = 'HEAD' ]; then\n case \"$PWD\" in */factory/t_*) if [ ! -e " + shellQuote(marker) + " ]; then touch " + shellQuote(marker) + "; exit 1; fi;; esac\nfi\nexec " + shellQuote(git) + " \"$@\"\n"
	if e = os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	s.mu.Lock()
	_, e = s.create("project", "Fail baseline", "Brief", "")
	count := len(s.tasks)
	s.mu.Unlock()
	if e == nil || !strings.Contains(e.Error(), "base revision") || count != 0 {
		t.Fatal("baseline failure did not abort task", e)
	}
	root := s.cfg.Projects["project"].Path
	if len(fixtureWorktrees(t, root)) != 1 || fixtureFactoryBranches(t, root) != "" {
		t.Fatal("baseline failure left orphan worktree or branch")
	}
	s.mu.Lock()
	_, e = s.create("project", "Retry", "Brief", "")
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
}

func TestWorkspacePinsSourceBeforeMovingHEAD(t *testing.T) {
	s, _ := fixture(t)
	root := s.cfg.Projects["project"].Path
	expected, e := s.git("project", root, "rev-parse", "HEAD")
	if e != nil {
		t.Fatal(e)
	}
	git, e := exec.LookPath("git")
	if e != nil {
		t.Fatal(e)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = 'worktree' ] && [ \"$2\" = 'add' ]; then " + shellQuote(git) + " -C " + shellQuote(root) + " commit --allow-empty -m 'Concurrent source commit' >/dev/null || exit 1; fi\nexec " + shellQuote(git) + " \"$@\"\n"
	if e = os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	s.mu.Lock()
	task, e := s.create("project", "Pin baseline", "Brief", "")
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	actual, e := s.git("project", task.Directory, "rev-parse", "HEAD")
	if e != nil {
		t.Fatal(e)
	}
	moved, e := s.git("project", root, "rev-parse", "HEAD")
	if e != nil {
		t.Fatal(e)
	}
	if task.BaseRevision != expected || actual != expected || moved == expected {
		t.Fatal("worktree followed moving HEAD instead of captured baseline")
	}
}
