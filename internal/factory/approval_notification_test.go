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

func TestDesignGatesRejectIgnoredImplementation(t *testing.T) {
	for _, gate := range []string{"report", "approval"} {
		t.Run(gate, func(t *testing.T) {
			s, _ := fixture(t)
			root := s.cfg.Projects["project"].Path
			if e := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("generated.txt\n"), 0600); e != nil {
				t.Fatal(e)
			}
			for _, args := range [][]string{{"add", ".gitignore"}, {"commit", "-m", "Ignore generated output"}} {
				if _, e := s.git("project", root, args...); e != nil {
					t.Fatal(e)
				}
			}
			s.mu.Lock()
			task, e := s.create("project", "Ignored code", "Brief", "")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			if gate == "report" {
				worker := &Session{ID: "planner", TaskID: task.ID, ProjectID: "project", Status: "running"}
				s.sessions[worker.ID] = worker
				s.active = worker.ID
				s.tokens["planner"] = worker.ID
			} else {
				task.Design = "Plan"
				s.advance(task)
			}
			s.mu.Unlock()
			if e = os.WriteFile(filepath.Join(task.Directory, "generated.txt"), []byte("implementation"), 0600); e != nil {
				t.Fatal(e)
			}
			s.mu.Lock()
			_, normalDirty, e := s.workspaceState(task)
			s.mu.Unlock()
			if e != nil || normalDirty != "" {
				t.Fatal("fixture is not ignored", e, normalDirty)
			}
			if gate == "report" {
				w := call(s, "POST", "tools/report", `{"task_id":"`+task.ID+`","report_id":"once","summary":"Plan","outcome":"complete","design":"Design"}`, "planner")
				if w.Code != 409 || !strings.Contains(w.Body.String(), "planning only") {
					t.Fatal(w.Code, w.Body.String())
				}
			} else {
				s.mu.Lock()
				e = s.approve(task, task.Version, "design")
				s.mu.Unlock()
				if e == nil {
					t.Fatal("ignored implementation passed approval")
				}
			}
			s.mu.Lock()
			same := task.DesignApprovalVersion == 0 && task.Step <= 1
			s.mu.Unlock()
			if !same {
				t.Fatal("ignored implementation advanced design gate")
			}
		})
	}
}

func TestApprovalAndForemanNotificationCommitTogether(t *testing.T) {
	s, db := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Approve once", "Brief", "")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	task.Design = "Plan"
	s.advance(task)
	_ = s.saveTask(task)
	foreman, e := s.foreman("project")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	foreman.Status = "interrupted"
	foreman.ReportQueue = []string{"previous"}
	_ = s.saveSession(foreman)
	before, _ := json.Marshal(diskTask(task))
	s.mu.Unlock()
	if _, e = db.Exec(`CREATE TRIGGER reject_approval_notification BEFORE INSERT ON factory_records WHEN NEW.kind='session' BEGIN SELECT RAISE(ABORT,'notification failed'); END`); e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	e = s.approve(task, task.Version, "design")
	after, _ := json.Marshal(diskTask(task))
	same := len(foreman.ReportQueue) == 1 && len(s.queue) == 0
	s.mu.Unlock()
	if e == nil || string(before) != string(after) || !same {
		t.Fatal("failed notification published approval", e)
	}
	var persisted string
	if e = db.QueryRow(`SELECT data FROM factory_records WHERE kind='task' AND id=?`, task.ID).Scan(&persisted); e != nil || persisted != string(before) {
		t.Fatal("task committed without foreman notification", e)
	}
	if _, e = db.Exec(`DROP TRIGGER reject_approval_notification`); e != nil {
		t.Fatal(e)
	}
	s.Close()
	restored, e := New(db, s.cfg, "machinist")
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	restored.mu.Lock()
	rt := restored.tasks[task.ID]
	e = restored.approve(rt, rt.Version, "design")
	again := restored.approve(rt, rt.Version, "design")
	count := len(restored.sessions[foreman.ID].ReportQueue)
	step := rt.Step
	restored.mu.Unlock()
	if e != nil || again != nil || count != 2 || step != 2 {
		t.Fatal("retry lost or duplicated notification", e, again, count, step)
	}
	var saved sessionRecord
	if e = db.QueryRow(`SELECT data FROM factory_records WHERE kind='session' AND id=?`, foreman.ID).Scan(&persisted); e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal([]byte(persisted), &saved); e != nil || len(saved.ReportQueue) != 2 {
		t.Fatal("notification not durable", e)
	}
}

func TestCommittedApprovalDispatchesIdleForeman(t *testing.T) {
	s, _ := fixture(t)
	dispatched := make(chan RunRequest, 1)
	s.SetRunner(func(ctx context.Context, r RunRequest, emit func(Event), permission func(context.Context, string) (bool, error)) (string, error) {
		dispatched <- r
		return "foreman", nil
	})
	s.mu.Lock()
	task, e := s.create("project", "Dispatch", "Brief", "")
	if e == nil {
		task.Design = "Plan"
		s.advance(task)
		e = s.approve(task, task.Version, "design")
	}
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	select {
	case r := <-dispatched:
		if !strings.Contains(r.Prompt, "Human approved design") {
			t.Fatal("approval notification missing", r.Prompt)
		}
	case <-time.After(time.Second):
		t.Fatal("committed approval did not dispatch idle foreman")
	}
}
