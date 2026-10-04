package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTaskDetailPreservesWhitespaceInGitFilenames(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Filenames", "Brief", "")
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	names := []string{"with spaces.txt", "with\ttab.txt", "with\nnewline.txt", " leading.txt", "trailing.txt ", "é unicode.txt"}
	for _, name := range names {
		if e = os.WriteFile(filepath.Join(task.Directory, name), []byte("changed\n"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	args := append([]string{"add", "--"}, names...)
	if _, e = s.git("project", task.Directory, args...); e != nil {
		t.Fatal(e)
	}
	w := call(s, "GET", "tasks/"+task.ID, "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var detail struct {
		Files          []string
		FilesTruncated bool `json:"files_truncated"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &detail); e != nil {
		t.Fatal(e)
	}
	found := map[string]bool{}
	for _, name := range detail.Files {
		found[name] = true
	}
	if detail.FilesTruncated || len(detail.Files) != len(names) {
		t.Fatal("incorrect filename count", detail.Files)
	}
	for _, name := range names {
		if !found[name] {
			t.Fatalf("filename changed: %q in %#v", name, detail.Files)
		}
	}
}
