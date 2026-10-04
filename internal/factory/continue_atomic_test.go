package factory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestContinueFailureCanRetryAfterRestartAndWakeForeman(t *testing.T) {
	for _, kind := range []string{"task", "session"} {
		t.Run(kind, func(t *testing.T) {
			s, db := fixture(t)
			s.mu.Lock()
			task, e := s.create("project", "Continue", "Brief", "")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			task.Repairs = 3
			task.Status = "failed"
			task.Stage = "Build"
			task.Step = 2
			_ = s.saveTask(task)
			foreman, e := s.foreman("project")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			before, _ := json.Marshal(diskTask(task))
			s.mu.Unlock()
			_, e = db.Exec(`CREATE TRIGGER reject_continue BEFORE INSERT ON factory_records WHEN NEW.kind='` + kind + `' BEGIN SELECT RAISE(ABORT,'continuation persistence failed'); END`)
			if e != nil {
				t.Fatal(e)
			}
			w := call(s, "POST", "tasks/"+task.ID+"/continue", `{"version":1}`, "")
			if w.Code != 409 {
				t.Fatal(w.Code, w.Body.String())
			}
			s.mu.Lock()
			after, _ := json.Marshal(diskTask(task))
			same := foreman.Status == "idle" && foreman.Pending == "" && len(s.queue) == 0
			s.mu.Unlock()
			if string(before) != string(after) || !same {
				t.Fatal("failed continuation published state")
			}
			var data string
			if e = db.QueryRow(`SELECT data FROM factory_records WHERE kind='task' AND id=?`, task.ID).Scan(&data); e != nil || data != string(before) {
				t.Fatal("partial task transition persisted", e)
			}
			_, e = db.Exec(`DROP TRIGGER reject_continue`)
			if e != nil {
				t.Fatal(e)
			}
			s.Close()
			restored, e := New(db, s.cfg, "machinist")
			if e != nil {
				t.Fatal(e)
			}
			defer restored.Close()
			dispatched := make(chan string, 1)
			restored.SetRunner(func(ctx context.Context, r RunRequest, emit func(Event), permission func(context.Context, string) (bool, error)) (string, error) {
				dispatched <- r.Prompt
				return "foreman", nil
			})
			w = call(restored, "POST", "tasks/"+task.ID+"/continue", `{"version":1}`, "")
			if w.Code != 200 {
				t.Fatal("same authorization could not retry", w.Body.String())
			}
			select {
			case prompt := <-dispatched:
				if !strings.Contains(prompt, "authorized another repair pass") {
					t.Fatal("missing continuation prompt", prompt)
				}
			case <-time.After(time.Second):
				t.Fatal("committed continuation did not wake foreman")
			}
			restored.mu.Lock()
			rt := restored.tasks[task.ID]
			accepted := rt.Version == 2 && rt.Repairs == 0 && rt.Status == "active"
			restored.mu.Unlock()
			if !accepted {
				t.Fatal("continuation not accepted exactly once")
			}
			w = call(restored, "POST", "tasks/"+task.ID+"/continue", `{"version":1}`, "")
			if w.Code != 409 {
				t.Fatal("stale continuation authorized again")
			}
		})
	}
}

func TestRecoveryFailurePreservesInterruptedOwnership(t *testing.T) {
	for _, recovery := range []string{"reported", "repair-limit", "same-step"} {
		t.Run(recovery, func(t *testing.T) {
			s, db := fixture(t)
			s.mu.Lock()
			task, e := s.create("project", "Recover", "Brief", "")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			task.Design = "Plan"
			s.advance(task)
			task.Status = "interrupted"
			if recovery == "same-step" {
				task.Step = 0
			}
			if recovery == "repair-limit" {
				task.Repairs = 3
			}
			worker := &Session{ID: "worker", TaskID: task.ID, ProjectID: "project", Step: 0, Status: "interrupted", Error: "disconnect"}
			s.sessions[worker.ID] = worker
			_ = s.saveTask(task)
			_ = s.saveSession(worker)
			_, e = s.foreman("project")
			s.mu.Unlock()
			if e != nil {
				t.Fatal(e)
			}
			_, e = db.Exec(`CREATE TRIGGER reject_recovery BEFORE INSERT ON factory_records WHEN NEW.kind='task' BEGIN SELECT RAISE(ABORT,'recovery persistence failed'); END`)
			if e != nil {
				t.Fatal(e)
			}
			w := call(s, "POST", "sessions/worker/resume", `{"confirmed_stopped":true}`, "")
			if w.Code != 409 {
				t.Fatal(w.Code, w.Body.String())
			}
			s.mu.Lock()
			same := worker.Status == "interrupted" && worker.Error == "disconnect" && task.Status == "interrupted"
			s.mu.Unlock()
			if !same {
				t.Fatal("failed recovery released uncertain ownership")
			}
			_, e = db.Exec(`DROP TRIGGER reject_recovery`)
			if e != nil {
				t.Fatal(e)
			}
			w = call(s, "POST", "sessions/worker/resume", `{"confirmed_stopped":true}`, "")
			if w.Code != 200 && !(recovery == "same-step" && w.Code == 202) {
				t.Fatal("confirmed recovery could not retry", w.Body.String())
			}
		})
	}
}
