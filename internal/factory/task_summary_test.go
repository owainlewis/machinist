package factory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskCollectionsOmitLargeDetailArtifacts(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Task title", "Task brief", "")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	task.Activity = "Permission needed"
	task.Checks = []Check{{Name: "Large check", Output: strings.Repeat("CHECK-PAYLOAD", 100000)}}
	task.Design = strings.Repeat("DESIGN-PAYLOAD", 100000)
	task.Review = strings.Repeat("REVIEW-PAYLOAD", 100000)
	s.sessions["worker"] = &Session{ID: "worker", TaskID: task.ID, ProjectID: "project", Status: "completed"}
	s.permissions["permission"] = &Permission{ID: "permission", SessionID: "worker", Status: "pending"}
	s.mu.Unlock()
	for _, endpoint := range []string{"status", "projects/project"} {
		w := call(s, "GET", endpoint, "", "")
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		if w.Body.Len() > 10000 {
			t.Fatal("collection includes large task artifacts")
		}
		var collection struct{ Tasks []map[string]any }
		if e = json.Unmarshal(w.Body.Bytes(), &collection); e != nil {
			t.Fatal(e)
		}
		if len(collection.Tasks) != 1 {
			t.Fatal("task summary missing")
		}
		summary := collection.Tasks[0]
		for _, key := range []string{"checks", "design", "review", "steps", "agents"} {
			if _, present := summary[key]; present {
				t.Fatalf("collection exposes task detail %s", key)
			}
		}
		if summary["id"] != task.ID || summary["title"] != "Task title" || summary["brief"] != "Task brief" || summary["stage"] != "Design" || summary["activity"] != "Permission needed" || summary["pending_permissions"] != float64(1) {
			t.Fatal("board or attention fields missing")
		}
	}
	detail := call(s, "GET", "tasks/"+task.ID, "", "")
	if detail.Code != 200 {
		t.Fatal(detail.Body.String())
	}
	var full struct {
		Task   Task
		Checks []Check
	}
	if e = json.Unmarshal(detail.Body.Bytes(), &full); e != nil {
		t.Fatal(e)
	}
	if full.Task.Design != task.Design || full.Task.Review != task.Review || len(full.Checks) != 1 || full.Checks[0].Output != task.Checks[0].Output {
		t.Fatal("detail endpoint lost full artifacts")
	}
}

func TestTaskSummaryBoundsDisplayText(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", strings.Repeat("title", 500), strings.Repeat("brief", 500), "")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	task.Activity = strings.Repeat("activity", 500)
	s.mu.Unlock()
	w := call(s, "GET", "projects/project", "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var collection struct{ Tasks []taskSummary }
	if e = json.Unmarshal(w.Body.Bytes(), &collection); e != nil {
		t.Fatal(e)
	}
	for _, value := range []string{collection.Tasks[0].Title, collection.Tasks[0].Brief, collection.Tasks[0].Activity} {
		if len(value) > 515 || !strings.HasSuffix(value, "...") {
			t.Fatal("collection display text is unbounded")
		}
	}
}

func TestSummaryTracksDetailMetadataWithoutCheckOutput(t *testing.T) {
	s, _ := fixture(t)
	task := &Task{ID: "task", ProjectID: "project", Revision: "revision", PRURL: "https://github.com/owner/repo/pull/1", Step: 3, Checks: []Check{{StepID: "checks", Passed: false, Revision: "revision", Output: strings.Repeat("output", 200000)}}}
	s.mu.Lock()
	initial := s.summarize(task)
	task.Checks[0].Passed = true
	passed := s.summarize(task)
	task.GitHubChecksPass = true
	observed := s.summarize(task)
	task.GitHubError = strings.Repeat("error", 1000)
	failed := s.summarize(task)
	s.mu.Unlock()
	if initial.Step != 3 || initial.Revision != task.Revision || initial.PRURL != task.PRURL || initial.CheckState == passed.CheckState || passed.CheckState == observed.CheckState {
		t.Fatal("detail metadata changes not visible")
	}
	if len(failed.GitHubError) > 520 {
		t.Fatal("GitHub error is unbounded")
	}
	data, e := json.Marshal(failed)
	if e != nil || len(data) > 2000 || strings.Contains(string(data), "outputoutput") {
		t.Fatal("summary exposed check logs", e)
	}
}
