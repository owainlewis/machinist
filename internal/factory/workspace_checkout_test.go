package factory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRequiredSmudgeFailureLeavesNoAttemptBranch(t *testing.T) {
	s, _ := fixture(t)
	root := s.cfg.Projects["project"].Path
	if e := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("file.txt filter=broken\n"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{{"add", ".gitattributes"}, {"commit", "-m", "Required checkout filter"}, {"config", "filter.broken.smudge", "false"}, {"config", "filter.broken.required", "true"}} {
		if _, e := s.git("project", root, args...); e != nil {
			t.Fatal(e)
		}
	}
	s.mu.Lock()
	_, e := s.create("project", "Checkout fails", "Brief", "")
	count := len(s.tasks)
	s.mu.Unlock()
	if e == nil || count != 0 {
		t.Fatal("required smudge filter did not fail checkout", e)
	}
	if len(fixtureWorktrees(t, root)) != 1 || fixtureFactoryBranches(t, root) != "" {
		t.Fatal("failed checkout left an orphan worktree or attempt branch")
	}
	if _, e = s.git("project", root, "config", "filter.broken.smudge", "cat"); e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	_, e = s.create("project", "Checkout retry", "Brief", "")
	s.mu.Unlock()
	if e != nil {
		t.Fatal("checkout retry failed", e)
	}
}
