package factory

import (
	"encoding/json"
	"testing"
)

func TestChangesAndNotificationCommitTogether(t *testing.T) {
	for _, stage := range []string{"Design", "Review"} {
		t.Run(stage, func(t *testing.T) {
			s, db := fixture(t)
			s.mu.Lock()
			task, e := s.create("project", "Changes", "Brief", "")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			task.Design = "Plan"
			s.advance(task)
			if stage == "Review" {
				task.Step = len(task.Steps) - 1
				task.Stage = stage
				task.ApprovalSubject = "code"
			}
			worker := &Session{ID: "builder", TaskID: task.ID, ProjectID: "project", Step: 2, Status: "completed", Pending: "old", Delivery: true}
			s.sessions[worker.ID] = worker
			_ = s.saveSession(worker)
			_ = s.saveTask(task)
			foreman, e := s.foreman("project")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			foreman.Status = "interrupted"
			foreman.ReportQueue = []string{"old"}
			_ = s.saveSession(foreman)
			before, _ := json.Marshal(diskTask(task))
			version := task.Version
			s.mu.Unlock()
			_, e = db.Exec(`CREATE TRIGGER reject_changes_notification BEFORE INSERT ON factory_records WHEN NEW.kind='session' BEGIN SELECT RAISE(ABORT,'notification failed'); END`)
			if e != nil {
				t.Fatal(e)
			}
			s.mu.Lock()
			e = s.changes(task, version, "Please correct this")
			after, _ := json.Marshal(diskTask(task))
			same := worker.Pending == "old" && worker.Delivery && len(foreman.ReportQueue) == 1
			s.mu.Unlock()
			if e == nil || !same || string(before) != string(after) {
				t.Fatal("failed changes transaction changed live state", e)
			}
			var persisted string
			if e = db.QueryRow(`SELECT data FROM factory_records WHERE kind='task' AND id=?`, task.ID).Scan(&persisted); e != nil || persisted != string(before) {
				t.Fatal("task persisted without feedback", e)
			}
			_, e = db.Exec(`DROP TRIGGER reject_changes_notification`)
			if e != nil {
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
			e = restored.changes(rt, version, "Please correct this")
			count := len(restored.sessions[foreman.ID].ReportQueue)
			newVersion := rt.Version
			rw := restored.sessions[worker.ID]
			updated := stage == "Design" || (rw.Pending == "Please correct this" && !rw.Delivery)
			restored.mu.Unlock()
			if e != nil || count != 2 || newVersion != version+1 || !updated {
				t.Fatal("restart retry lost transition or worker feedback", e)
			}
			var saved sessionRecord
			if e = db.QueryRow(`SELECT data FROM factory_records WHERE kind='session' AND id=?`, foreman.ID).Scan(&persisted); e != nil {
				t.Fatal(e)
			}
			if e = json.Unmarshal([]byte(persisted), &saved); e != nil || len(saved.ReportQueue) != 2 {
				t.Fatal("feedback not durable", e)
			}
		})
	}
}
