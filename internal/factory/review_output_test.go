package factory

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoppedForemanRetainsReportsUntilExplicitMessage(t *testing.T) {
	s, db := fixture(t)
	first := make(chan RunRequest, 1)
	s.SetRunner(func(ctx context.Context, r RunRequest, emit func(Event), permission func(context.Context, string) (bool, error)) (string, error) {
		first <- r
		<-ctx.Done()
		return "foreman-provider", ctx.Err()
	})
	w := call(s, "POST", "projects/project/messages", `{"request_id":"start","message":"Coordinate work"}`, "")
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("foreman did not start")
	}
	s.mu.Lock()
	foreman, e := s.foreman("project")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	s.notify("project", "Worker report: saved-result")
	s.mu.Unlock()
	w = call(s, "POST", "sessions/"+foreman.ID+"/cancel", `{}`, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	idle(t, s)
	s.mu.Lock()
	status := foreman.Status
	reports := append([]string(nil), foreman.ReportQueue...)
	s.mu.Unlock()
	if status != "cancelled" || len(reports) != 1 {
		t.Fatalf("stopped foreman restarted or lost reports: status=%s reports=%v", status, reports)
	}
	select {
	case <-first:
		t.Fatal("Stop restarted foreman to process queued reports")
	default:
	}
	s.Close()
	restored, e := New(db, s.cfg, "machinist")
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	if len(restored.sessions[foreman.ID].ReportQueue) != 1 {
		t.Fatal("queued reports were not durable")
	}
	next := make(chan RunRequest, 1)
	restored.SetRunner(func(ctx context.Context, r RunRequest, emit func(Event), permission func(context.Context, string) (bool, error)) (string, error) {
		next <- r
		return "foreman-provider", nil
	})
	w = call(restored, "POST", "projects/project/messages", `{"request_id":"continue","message":"Continue reviewing work"}`, "")
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	var request RunRequest
	select {
	case request = <-next:
	case <-time.After(time.Second):
		t.Fatal("explicit message did not start foreman")
	}
	if strings.Count(request.Prompt, "saved-result") != 1 {
		t.Fatal("saved report was omitted or duplicated")
	}
	idle(t, restored)
	restored.mu.Lock()
	remaining := len(restored.sessions[foreman.ID].ReportQueue)
	restored.mu.Unlock()
	if remaining != 0 {
		t.Fatal("delivered reports were not consumed")
	}
}

func TestTaskDetailBoundsLargeDiffAndReportsTruncation(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Large change", "Inspect generated changes", "")
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(task.Directory, "file.txt"), []byte(strings.Repeat("generated content\n", 150000)), 0600); e != nil {
		t.Fatal(e)
	}
	w := call(s, "GET", "tasks/"+task.ID, "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var detail struct {
		Diff           string   `json:"diff"`
		DiffTruncated  bool     `json:"diff_truncated"`
		Files          []string `json:"files"`
		FilesTruncated bool     `json:"files_truncated"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &detail); e != nil {
		t.Fatal(e)
	}
	if !detail.DiffTruncated || len(detail.Diff) > 1<<20+200 || !strings.Contains(detail.Diff, "[Output truncated:") {
		t.Fatalf("large diff was not visibly bounded: bytes=%d truncated=%v", len(detail.Diff), detail.DiffTruncated)
	}
	if detail.FilesTruncated || len(detail.Files) != 1 || detail.Files[0] != "file.txt" {
		t.Fatalf("normal filename output changed: %+v", detail.Files)
	}
}

func TestTaskDetailBoundsFilenameOutput(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Many files", "Inspect generated filenames", "")
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	git, e := exec.LookPath("git")
	if e != nil {
		t.Fatal(e)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = diff ] && [ \"$2\" = --name-only ]; then awk 'BEGIN { for (i=0;i<10000;i++) printf \"generated_filename_%05d.txt\\0\", i }'; exit; fi\nexec " + shellQuote(git) + " \"$@\"\n"
	if e = os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	w := call(s, "GET", "tasks/"+task.ID, "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var detail struct {
		Files          []string `json:"files"`
		FilesTruncated bool     `json:"files_truncated"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &detail); e != nil {
		t.Fatal(e)
	}
	if !detail.FilesTruncated || len(strings.Join(detail.Files, "\n")) > 64<<10 || len(detail.Files) >= 10000 {
		t.Fatal("filename output was not bounded")
	}
	for _, name := range detail.Files {
		if !strings.HasSuffix(name, ".txt") {
			t.Fatal("partial filename included in truncated result")
		}
	}
}
