package factory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDesignReportRejectsWorkspaceChanges(t *testing.T) {
	for _, change := range []string{"tracked", "untracked", "commit"} {
		t.Run(change, func(t *testing.T) {
			s, _ := fixture(t)
			s.mu.Lock()
			task, e := s.create("project", "Plan", "Brief", "")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			worker := &Session{ID: "planner", TaskID: task.ID, ProjectID: "project", Status: "running"}
			s.sessions[worker.ID] = worker
			s.active = worker.ID
			s.tokens["planner"] = worker.ID
			s.mu.Unlock()
			name := "file.txt"
			if change == "untracked" {
				name = "new-file"
			}
			if e = os.WriteFile(filepath.Join(task.Directory, name), []byte("implementation"), 0600); e != nil {
				t.Fatal(e)
			}
			if change == "commit" {
				for _, args := range [][]string{{"add", "."}, {"commit", "-m", "Premature implementation"}} {
					if _, e = s.git("project", task.Directory, args...); e != nil {
						t.Fatal(e)
					}
				}
			}
			w := call(s, "POST", "tools/report", `{"task_id":"`+task.ID+`","report_id":"plan","summary":"Plan","outcome":"complete","design":"Design"}`, "planner")
			if w.Code != 409 || !strings.Contains(w.Body.String(), "planning only") {
				t.Fatal(w.Code, w.Body.String())
			}
			s.mu.Lock()
			unchanged := task.Step == 0 && task.Design == "" && task.DesignApprovalVersion == 0 && !worker.Reported
			s.mu.Unlock()
			if !unchanged {
				t.Fatal("unapproved implementation advanced design gate")
			}
		})
	}
}

func TestApprovalPersistenceFailureRetriesAfterRestart(t *testing.T) {
	for _, subject := range []string{"design", "code"} {
		t.Run(subject, func(t *testing.T) {
			s, db := fixture(t)
			s.mu.Lock()
			task, e := s.create("project", "Approve", "Brief", "")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			task.Design = "Design"
			s.advance(task)
			if subject == "code" {
				task.Step = len(task.Steps) - 1
				task.Stage = "Review"
				task.ApprovalSubject = "code"
				task.Revision = task.BaseRevision
				task.Checks = []Check{{Passed: true, Revision: task.Revision}}
			}
			_ = s.saveTask(task)
			before, _ := json.Marshal(diskTask(task))
			s.mu.Unlock()
			_, e = db.Exec(`CREATE TRIGGER reject_approval BEFORE INSERT ON factory_records WHEN NEW.kind='task' BEGIN SELECT RAISE(ABORT,'approval persistence failed'); END`)
			if e != nil {
				t.Fatal(e)
			}
			s.mu.Lock()
			e = s.approve(task, task.Version, subject)
			after, _ := json.Marshal(diskTask(task))
			s.mu.Unlock()
			if e == nil || string(before) != string(after) {
				t.Fatal("failed approval mutated task", e)
			}
			s.Close()
			restored, e := New(db, s.cfg, "machinist")
			if e != nil {
				t.Fatal(e)
			}
			defer restored.Close()
			_, e = db.Exec(`DROP TRIGGER reject_approval`)
			if e != nil {
				t.Fatal(e)
			}
			restored.mu.Lock()
			rt := restored.tasks[task.ID]
			e = restored.approve(rt, rt.Version, subject)
			step := rt.Step
			again := restored.approve(rt, rt.Version, subject)
			restored.mu.Unlock()
			if e != nil || again != nil || step != task.Step+1 {
				t.Fatal("approval retry failed", e, again, step)
			}
			var data string
			if e = db.QueryRow(`SELECT data FROM factory_records WHERE kind='task' AND id=?`, task.ID).Scan(&data); e != nil {
				t.Fatal(e)
			}
			var record taskRecord
			if e = json.Unmarshal([]byte(data), &record); e != nil || record.Step != step {
				t.Fatal("approval missing from storage", e)
			}
		})
	}
}

func TestDesignApprovalRejectsLateWorkspaceChangesAndBusyOwner(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Late change", "Brief", "")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	task.Design = "Design"
	s.advance(task)
	s.sessions["owner"] = &Session{ID: "owner", TaskID: task.ID, Status: "interrupted"}
	if e = s.approve(task, task.Version, "design"); e == nil {
		s.mu.Unlock()
		t.Fatal("uncertain worker ownership approved")
	}
	delete(s.sessions, "owner")
	s.mu.Unlock()
	if e = os.WriteFile(filepath.Join(task.Directory, "late-file"), []byte("implementation"), 0600); e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	e = s.approve(task, task.Version, "design")
	same := task.Step == 1 && task.DesignApprovalVersion == 0
	s.mu.Unlock()
	if e == nil || !same {
		t.Fatal("late implementation passed design approval")
	}
}

func TestDesignRunRequestsReadOnly(t *testing.T) {
	s, _ := fixture(t)
	seen := make(chan bool, 1)
	s.SetRunner(func(ctx context.Context, r RunRequest, emit func(Event), permission func(context.Context, string) (bool, error)) (string, error) {
		if !strings.HasPrefix(r.SystemPrompt, "Foreman") {
			seen <- r.ReadOnly
		}
		return "", nil
	})
	s.mu.Lock()
	task, e := s.create("project", "Read only", "Brief", "")
	if e == nil {
		_, e = s.start(task)
	}
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	select {
	case readOnly := <-seen:
		if !readOnly {
			t.Fatal("Design run did not request read-only runtime")
		}
	case <-time.After(time.Second):
		t.Fatal("design run not dispatched")
	}
}
