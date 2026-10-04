package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerMessageRepairCommitsWithTurnAndRetryMarker(t *testing.T) {
	for _, kind := range []string{"task", "session", "request"} {
		t.Run(kind, func(t *testing.T) {
			s, db := fixture(t)
			s.mu.Lock()
			task, e := s.create("project", "Repair", "Brief", "")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			task.Status = "failed"
			task.Stage = "Build"
			task.Step = 3
			_ = s.saveTask(task)
			worker := &Session{ID: "builder", TaskID: task.ID, ProjectID: "project", Step: 2, Status: "completed", Pending: "old", Delivery: true}
			s.sessions[worker.ID] = worker
			_ = s.saveSession(worker)
			foreman, e := s.foreman("project")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			foreman.Status = "running"
			s.active = foreman.ID
			s.tokens["foreman"] = foreman.ID
			_ = s.saveSession(foreman)
			before, _ := json.Marshal(diskTask(task))
			s.mu.Unlock()
			_, e = db.Exec(`CREATE TRIGGER reject_worker_message BEFORE INSERT ON factory_records WHEN NEW.kind='` + kind + `' BEGIN SELECT RAISE(ABORT,'message failed'); END`)
			if e != nil {
				t.Fatal(e)
			}
			body := `{"task_id":"` + task.ID + `","request_id":"same","message":"Fix tests"}`
			w := call(s, "POST", "tools/send_message", body, "foreman")
			if w.Code != 409 {
				t.Fatal(w.Code, w.Body.String())
			}
			s.mu.Lock()
			after, _ := json.Marshal(diskTask(task))
			same := worker.Pending == "old" && worker.Delivery && worker.Status == "completed" && len(s.queue) == 0
			s.active = ""
			s.mu.Unlock()
			if string(before) != string(after) || !same {
				t.Fatal("failed repair turn published partial state")
			}
			_, e = db.Exec(`DROP TRIGGER reject_worker_message`)
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
			rf := restored.sessions[foreman.ID]
			rf.Status = "running"
			restored.active = rf.ID
			restored.tokens["foreman"] = rf.ID
			restored.mu.Unlock()
			w = call(restored, "POST", "tools/send_message", body, "foreman")
			retry := call(restored, "POST", "tools/send_message", body, "foreman")
			if w.Code != 200 || retry.Code != 200 {
				t.Fatal(w.Body.String(), retry.Body.String())
			}
			restored.mu.Lock()
			rt := restored.tasks[task.ID]
			rw := restored.sessions[worker.ID]
			same = rt.Repairs == 1 && rt.Version == 2 && rt.Step == 2 && rw.Pending == "Fix tests" && !rw.Delivery && len(restored.queue) == 1
			restored.active = ""
			restored.mu.Unlock()
			if !same {
				t.Fatal("retry lost or duplicated repair turn")
			}
		})
	}
}

func TestRepairThresholdMessagePausesDurably(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Threshold", "Brief", "")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	task.Status = "failed"
	task.Stage = "Build"
	task.Step = 3
	task.Repairs = 2
	foreman, e := s.foreman("project")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	foreman.Status = "running"
	s.active = foreman.ID
	s.tokens["foreman"] = foreman.ID
	s.mu.Unlock()
	body := `{"task_id":"` + task.ID + `","request_id":"threshold","message":"Repair"}`
	w := call(s, "POST", "tools/send_message", body, "foreman")
	retry := call(s, "POST", "tools/send_message", body, "foreman")
	if w.Code != 409 || retry.Code != 409 {
		t.Fatal(w.Code, retry.Code)
	}
	s.mu.Lock()
	same := task.Repairs == 3 && task.Status == "failed" && len(s.queue) == 0 && len(foreman.ReportQueue) == 1
	s.mu.Unlock()
	if !same {
		t.Fatal("repair threshold not paused exactly once")
	}
}

func TestStartPersistenceFailurePreservesTaskAndCanRetry(t *testing.T) {
	for _, kind := range []string{"task", "session", "request"} {
		for _, stage := range []string{"agent", "review"} {
			if stage == "review" && kind != "task" {
				continue
			}
			t.Run(kind+stage, func(t *testing.T) {
				s, db := fixture(t)
				s.mu.Lock()
				task, e := s.create("project", "Start", "Brief", "")
				if e != nil {
					s.mu.Unlock()
					t.Fatal(e)
				}
				if stage == "review" {
					task.Step = 4
					task.Stage = "Review"
				}
				_ = s.saveTask(task)
				before, _ := json.Marshal(diskTask(task))
				s.mu.Unlock()
				_, e = db.Exec(`CREATE TRIGGER reject_start BEFORE INSERT ON factory_records WHEN NEW.kind='` + kind + `' BEGIN SELECT RAISE(ABORT,'start failed'); END`)
				if e != nil {
					t.Fatal(e)
				}
				s.mu.Lock()
				_, e = s.start(task)
				after, _ := json.Marshal(diskTask(task))
				same := len(s.sessions) == 0 && len(s.queue) == 0
				s.mu.Unlock()
				if e == nil || string(before) != string(after) || !same {
					t.Fatal("failed start changed task or worker", e)
				}
				_, e = db.Exec(`DROP TRIGGER reject_start`)
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
				_, e = restored.start(restored.tasks[task.ID])
				restored.mu.Unlock()
				if e != nil {
					t.Fatal("start retry failed", e)
				}
			})
		}
	}
}

func TestLinkPRPersistenceFailureCanRetryAfterRestart(t *testing.T) {
	for _, kind := range []string{"task", "session"} {
		t.Run(kind, func(t *testing.T) {
			s, db := fixture(t)
			s.mu.Lock()
			task, e := s.create("project", "Publish", "Brief", "")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			task.Step = len(task.Steps)
			task.Revision = task.BaseRevision
			task.CodeApproved = task.Revision
			task.Stage = "Review"
			_ = s.saveTask(task)
			worker := &Session{ID: "delivery", TaskID: task.ID, ProjectID: "project", Step: 2, Status: "running", Delivery: true}
			s.sessions[worker.ID] = worker
			s.active = worker.ID
			s.tokens["delivery"] = worker.ID
			_ = s.saveSession(worker)
			foreman, e := s.foreman("project")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			foreman.Status = "interrupted"
			_ = s.saveSession(foreman)
			before, _ := json.Marshal(diskTask(task))
			s.mu.Unlock()
			bin := t.TempDir()
			script := "#!/bin/sh\nprintf '%s' '{\"state\":\"OPEN\",\"headRefOid\":\"" + task.Revision + "\",\"statusCheckRollup\":[]}'\n"
			if e = os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); e != nil {
				t.Fatal(e)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			_, e = db.Exec(`CREATE TRIGGER reject_link BEFORE INSERT ON factory_records WHEN NEW.kind='` + kind + `' BEGIN SELECT RAISE(ABORT,'link failed'); END`)
			if e != nil {
				t.Fatal(e)
			}
			body := `{"task_id":"` + task.ID + `","pr_url":"https://github.com/example/project/pull/7"}`
			w := call(s, "POST", "tools/link_pr", body, "delivery")
			if w.Code != 409 {
				t.Fatal(w.Code, w.Body.String())
			}
			s.mu.Lock()
			after, _ := json.Marshal(diskTask(task))
			same := !worker.Reported && len(foreman.ReportQueue) == 0
			s.active = ""
			s.mu.Unlock()
			if string(before) != string(after) || !same {
				t.Fatal("failed link published state")
			}
			_, e = db.Exec(`DROP TRIGGER reject_link`)
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
			rw := restored.sessions[worker.ID]
			rw.Status = "running"
			restored.active = rw.ID
			restored.tokens["delivery"] = rw.ID
			restored.mu.Unlock()
			w = call(restored, "POST", "tools/link_pr", body, "delivery")
			retry := call(restored, "POST", "tools/link_pr", body, "delivery")
			if w.Code != 200 || retry.Code != 200 {
				t.Fatal(w.Body.String(), retry.Body.String())
			}
			restored.mu.Lock()
			same = strings.HasSuffix(restored.tasks[task.ID].PRURL, "/7") && rw.Reported && len(restored.sessions[foreman.ID].ReportQueue) == 1
			restored.active = ""
			restored.mu.Unlock()
			if !same {
				t.Fatal("retry missing link or duplicated notification")
			}
		})
	}
}
