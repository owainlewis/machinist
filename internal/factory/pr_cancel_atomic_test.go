package factory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPRHeadRepairPersistenceFailureCanRetryAfterRestart(t *testing.T) {
	for _, reject := range []string{"task", "foreman", "worker"} {
		t.Run(reject, func(t *testing.T) {
			s, db := fixture(t)
			s.mu.Lock()
			task, e := s.create("project", "PR repair", "Brief", "")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			task.Step = len(task.Steps)
			task.Stage = "Review"
			task.Revision = task.BaseRevision
			task.CodeApproved = task.Revision
			task.GitHubHead = task.Revision
			_ = s.saveTask(task)
			worker := &Session{ID: "builder", TaskID: task.ID, ProjectID: "project", Step: 2, Status: "completed", Pending: "old", Delivery: true}
			s.sessions[worker.ID] = worker
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
			condition := "NEW.kind='task'"
			if reject == "foreman" {
				condition = "NEW.kind='session' AND NEW.id='" + foreman.ID + "'"
			}
			if reject == "worker" {
				condition = "NEW.kind='session' AND NEW.id='builder'"
			}
			_, e = db.Exec(`CREATE TRIGGER reject_pr_repair BEFORE INSERT ON factory_records WHEN ` + condition + ` BEGIN SELECT RAISE(ABORT,'PR repair persistence failed'); END`)
			if e != nil {
				t.Fatal(e)
			}
			pr := githubPR{State: "OPEN", HeadRefOID: "new-head"}
			s.mu.Lock()
			e = s.applyPR(task, pr)
			after, _ := json.Marshal(diskTask(task))
			same := worker.Pending == "old" && worker.Delivery && len(foreman.ReportQueue) == 0
			s.mu.Unlock()
			if e == nil || string(before) != string(after) || !same {
				t.Fatal("failed PR repair changed live state", e)
			}
			var data string
			if e = db.QueryRow(`SELECT data FROM factory_records WHERE kind='task' AND id=?`, task.ID).Scan(&data); e != nil || data != string(before) {
				t.Fatal("partial PR repair persisted", e)
			}
			_, e = db.Exec(`DROP TRIGGER reject_pr_repair`)
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
			e = restored.applyPR(rt, pr)
			again := restored.applyPR(rt, pr)
			rw := restored.sessions[worker.ID]
			same = rt.Repairs == 1 && rt.Version == 2 && rt.CodeApproved == "" && strings.Contains(rw.Pending, "new-head") && !rw.Delivery && len(restored.sessions[foreman.ID].ReportQueue) == 1
			restored.mu.Unlock()
			if e != nil || again != nil || !same {
				t.Fatal("PR retry missing or duplicated repair feedback", e, again)
			}
		})
	}
}

func TestTaskCancellationPersistenceFailureDoesNotSignalOrPublish(t *testing.T) {
	for _, endpoint := range []string{"http-local", "http-remote", "mcp"} {
		for _, reject := range []string{"task", "session"} {
			t.Run(endpoint+"-"+reject, func(t *testing.T) {
				s, db := fixture(t)
				s.mu.Lock()
				task, e := s.create("project", "Cancel", "Brief", "")
				if e != nil {
					s.mu.Unlock()
					t.Fatal(e)
				}
				worker := &Session{ID: "worker", TaskID: task.ID, ProjectID: "project", Status: "running"}
				s.sessions[worker.ID] = worker
				s.active = worker.ID
				if endpoint == "http-remote" {
					p := s.cfg.Projects["project"]
					p.Host = "vm"
					s.cfg.Projects["project"] = p
				}
				token := ""
				path := "tasks/" + task.ID + "/cancel"
				body := `{}`
				if endpoint == "mcp" {
					foreman, err := s.foreman("project")
					if err != nil {
						s.mu.Unlock()
						t.Fatal(err)
					}
					foreman.Status = "running"
					s.active = foreman.ID
					s.tokens["cancel-token"] = foreman.ID
					token = "cancel-token"
					path = "tools/cancel_task"
					body = `{"task_id":"` + task.ID + `"}`
					worker.Status = "queued"
				}
				s.queue = []string{worker.ID}
				_ = s.saveSession(worker)
				_ = s.saveTask(task)
				signals := 0
				s.cancel = func() { signals++ }
				s.mu.Unlock()
				_, e = db.Exec(`CREATE TRIGGER reject_cancellation BEFORE INSERT ON factory_records WHEN NEW.kind='` + reject + `' BEGIN SELECT RAISE(ABORT,'cancellation persistence failed'); END`)
				if e != nil {
					t.Fatal(e)
				}
				w := call(s, "POST", path, body, token)
				if w.Code != 409 {
					t.Fatal(w.Code, w.Body.String())
				}
				s.mu.Lock()
				same := task.Status == "active" && worker.Status != "cancelled" && worker.Status != "interrupted" && len(s.queue) == 1 && signals == 0
				s.mu.Unlock()
				if !same {
					t.Fatal("failed cancellation published state or signalled process")
				}
				_, e = db.Exec(`DROP TRIGGER reject_cancellation`)
				if e != nil {
					t.Fatal(e)
				}
				w = call(s, "POST", path, body, token)
				if w.Code != 200 {
					t.Fatal(w.Body.String())
				}
				s.mu.Lock()
				expected := "cancelled"
				if endpoint == "http-remote" {
					expected = "interrupted"
				}
				same = task.Status == "cancelled" && worker.Status == expected && len(s.queue) == 0
				if endpoint != "mcp" {
					same = same && signals == 1
				}
				s.active = ""
				s.cancel = nil
				s.mu.Unlock()
				if !same {
					t.Fatal("durable cancellation not applied")
				}
				s.Close()
				restored, e := New(db, s.cfg, "machinist")
				if e != nil {
					t.Fatal(e)
				}
				defer restored.Close()
				restored.mu.Lock()
				same = restored.tasks[task.ID].Status == "cancelled" && len(restored.queue) == 0 && restored.sessions[worker.ID].Status == expected
				restored.mu.Unlock()
				if !same {
					t.Fatal("restart revived cancelled task")
				}
			})
		}
	}
}
