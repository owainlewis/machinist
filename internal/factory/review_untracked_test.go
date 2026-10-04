package factory

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func readRecoveryDetail(t *testing.T, s *Service, task *Task) (string, bool, []string, bool) {
	t.Helper()
	w := call(s, "GET", "tasks/"+task.ID, "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var detail struct {
		Diff           string
		DiffTruncated  bool `json:"diff_truncated"`
		Files          []string
		FilesTruncated bool `json:"files_truncated"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &detail); e != nil {
		t.Fatal(e)
	}
	return detail.Diff, detail.DiffTruncated, detail.Files, detail.FilesTruncated
}

func TestInterruptedTaskDetailIncludesUntrackedFiles(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Recover new files", "Brief", "")
	if e == nil {
		task.Status = "interrupted"
		task.Activity = "Agent stopped"
	}
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	names := []string{"new file.txt", "with\ttab.txt", "with\nnewline.txt"}
	for _, name := range names {
		if e = os.WriteFile(filepath.Join(task.Directory, name), []byte("unfinished implementation\n"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e = os.WriteFile(filepath.Join(task.Directory, "file.txt"), []byte("tracked change\n"), 0600); e != nil {
		t.Fatal(e)
	}
	diff, truncated, files, filesTruncated := readRecoveryDetail(t, s, task)
	if truncated || filesTruncated || len(files) != 4 || !strings.Contains(diff, "+unfinished implementation") || !strings.Contains(diff, "+tracked change") {
		t.Fatal("recovery evidence incomplete", diff, files)
	}
	found := map[string]bool{}
	for _, name := range files {
		found[name] = true
	}
	for _, name := range names {
		if !found[name] {
			t.Fatalf("untracked filename altered: %q %#v", name, files)
		}
	}
}

func TestTaskDetailSharesDiffBudgetWithLargeUntrackedFile(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Large new file", "Brief", "")
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(task.Directory, "file.txt"), []byte(strings.Repeat("tracked\n", 1000)), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(task.Directory, "large-new.txt"), []byte(strings.Repeat("untracked\n", 200000)), 0600); e != nil {
		t.Fatal(e)
	}
	diff, truncated, files, filesTruncated := readRecoveryDetail(t, s, task)
	if !truncated || filesTruncated || len(diff) > (1<<20)+200 || len(files) != 2 || !strings.Contains(diff, "Output truncated:") || !strings.Contains(diff, "+tracked") {
		t.Fatal("aggregate diff budget not enforced", len(diff), truncated, files)
	}
}

func TestUntrackedSymlinkReviewDoesNotReadOutsideContents(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Symlink recovery", "Brief", "")
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	outside := filepath.Join(t.TempDir(), "private.txt")
	if e = os.WriteFile(outside, []byte("PRIVATE_CONTENT_MUST_NOT_APPEAR"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(outside, filepath.Join(task.Directory, "outside-link")); e != nil {
		t.Skip("symlink unavailable", e)
	}
	diff, truncated, files, filesTruncated := readRecoveryDetail(t, s, task)
	if truncated || filesTruncated || len(files) != 1 || files[0] != "outside-link" || strings.Contains(diff, "PRIVATE_CONTENT_MUST_NOT_APPEAR") || !strings.Contains(diff, "new file mode 120000") {
		t.Fatal("symlink target was read or link evidence missing", diff, files)
	}
}

func TestUntrackedNameBudgetReportsIncompleteReview(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Many new files", "Brief", "")
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(task.Directory, "untracked")
	if e = os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	// Long names reach the filename budget with few files and little disk data.
	for i := 0; i < 350; i++ {
		name := strings.Repeat("name", 50) + id("_")
		if e = os.WriteFile(filepath.Join(dir, name), nil, 0600); e != nil {
			t.Fatal(e)
		}
	}
	diff, truncated, files, filesTruncated := readRecoveryDetail(t, s, task)
	if !filesTruncated || !truncated || len(strings.Join(files, "\x00")) > (64<<10) || len(files) >= 350 || !strings.Contains(diff, "Output truncated:") {
		t.Fatal("untracked list truncation is not explicit", truncated, filesTruncated, len(files))
	}
}

func TestNestedUntrackedRepositoryDoesNotHideRecoveryEvidence(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Nested repository", "Brief", "")
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	nested := filepath.Join(task.Directory, "nested repository")
	if e = os.Mkdir(nested, 0700); e != nil {
		t.Fatal(e)
	}
	if output, e := exec.Command("git", "init", nested).CombinedOutput(); e != nil {
		t.Fatal(string(output), e)
	}
	if e = os.WriteFile(filepath.Join(nested, "nested-file.txt"), []byte("NESTED_CONTENT_NOT_RENDERED"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(task.Directory, "new-file.txt"), []byte("regular untracked recovery\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(task.Directory, "file.txt"), []byte("regular tracked recovery\n"), 0600); e != nil {
		t.Fatal(e)
	}
	diff, truncated, files, filesTruncated := readRecoveryDetail(t, s, task)
	found := map[string]bool{}
	for _, name := range files {
		found[name] = true
	}
	if truncated || filesTruncated || len(files) != 3 || !found["nested repository/"] || !found["new-file.txt"] || !found["file.txt"] {
		t.Fatal("nested repository hid filenames", files)
	}
	if !strings.Contains(diff, "+regular untracked recovery") || !strings.Contains(diff, "+regular tracked recovery") || !strings.Contains(diff, `Untracked directory or nested repository: "nested repository/"`) || !strings.Contains(diff, "Contents are not rendered") || strings.Contains(diff, "NESTED_CONTENT_NOT_RENDERED") {
		t.Fatal("nested repository hid or exposed unintended content", diff)
	}
}
