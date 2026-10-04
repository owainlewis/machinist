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

func TestScriptCompletionCommitsWithNotification(t *testing.T) {
	for _, reject := range []string{"none", "task", "worker", "foreman"} {
		t.Run(reject, func(t *testing.T) {
			s, db := fixture(t)
			s.mu.Lock()
			task, e := s.create("project", "Checks", "Brief", "")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			task.Step = 3
			task.Stage = "Build"
			task.Revision = task.BaseRevision
			_ = s.saveTask(task)
			worker := &Session{ID: "checks", TaskID: task.ID, ProjectID: "project", Step: 3, Directory: task.Directory, Status: "running"}
			s.sessions[worker.ID] = worker
			s.active = worker.ID
			_ = s.saveSession(worker)
			foreman, e := s.foreman("project")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			s.mu.Unlock()
			calls := make(chan string, 2)
			s.SetRunner(func(ctx context.Context, r RunRequest, emit func(Event), permission func(context.Context, string) (bool, error)) (string, error) {
				calls <- r.Prompt
				return "foreman", nil
			})
			if reject != "none" {
				condition := "NEW.kind='task'"
				if reject == "worker" {
					condition = "NEW.kind='session' AND NEW.id='checks'"
				}
				if reject == "foreman" {
					condition = "NEW.kind='session' AND NEW.id='" + foreman.ID + "'"
				}
				_, e = db.Exec(`CREATE TRIGGER reject_completion BEFORE INSERT ON factory_records WHEN ` + condition + ` BEGIN SELECT RAISE(ABORT,'completion failed'); END`)
				if e != nil {
					t.Fatal(e)
				}
			}
			s.execute(context.Background(), *worker, "token", s.runner, "")
			idle(t, s)
			s.mu.Lock()
			step, status, workerStatus := task.Step, task.Status, worker.Status
			s.mu.Unlock()
			if reject == "none" {
				if step != 4 || workerStatus != "completed" || len(calls) != 1 {
					t.Fatal("successful check result did not dispatch next coordinator turn", step, workerStatus, len(calls))
				}
				if !strings.Contains(<-calls, "Script result") {
					t.Fatal("script notification missing")
				}
			} else {
				if step != 3 || status != "interrupted" || workerStatus != "interrupted" || len(calls) != 0 {
					t.Fatal("failed completion claimed progress", step, status, workerStatus, len(calls))
				}
				_, e = db.Exec(`DROP TRIGGER reject_completion`)
				if e != nil {
					t.Fatal(e)
				}
			}
			var saved string
			if e = db.QueryRow(`SELECT data FROM factory_records WHERE kind='task' AND id=?`, task.ID).Scan(&saved); e != nil {
				t.Fatal(e)
			}
			var record taskRecord
			if e = json.Unmarshal([]byte(saved), &record); e != nil {
				t.Fatal(e)
			}
			if record.Step != step {
				t.Fatal("task advanced without result notification")
			}
			s.Close()
			restored, e := New(db, s.cfg, "machinist")
			if e != nil {
				t.Fatal(e)
			}
			defer restored.Close()
			restored.mu.Lock()
			same := restored.tasks[task.ID].Step == step && len(restored.queue) == 0
			if reject != "none" {
				same = same && restored.sessions[worker.ID].Status == "interrupted"
			}
			restored.mu.Unlock()
			if !same {
				t.Fatal("restart lost completion or retried uncertain script")
			}
		})
	}
}

func TestUnreportedExitNotificationFailureLeavesRecovery(t *testing.T) {
	s, db := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Worker", "Brief", "")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	worker := &Session{ID: "worker", TaskID: task.ID, ProjectID: "project", Directory: task.Directory, Step: 0, Status: "running"}
	s.sessions[worker.ID] = worker
	s.active = worker.ID
	_ = s.saveTask(task)
	_ = s.saveSession(worker)
	foreman, e := s.foreman("project")
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`CREATE TRIGGER reject_attention BEFORE INSERT ON factory_records WHEN NEW.kind='session' AND NEW.id='` + foreman.ID + `' BEGIN SELECT RAISE(ABORT,'attention failed'); END`)
	if e != nil {
		t.Fatal(e)
	}
	s.execute(context.Background(), *worker, "token", func(context.Context, RunRequest, func(Event), func(context.Context, string) (bool, error)) (string, error) {
		return "", nil
	}, "")
	s.mu.Lock()
	same := task.Step == 0 && task.Status == "interrupted" && worker.Status == "interrupted" && strings.Contains(worker.Error, "persist turn result")
	s.mu.Unlock()
	if !same {
		t.Fatal("unpersisted attention claimed completion")
	}
}

func TestCancellationDuringScriptVerificationIsPreserved(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Cancel checks", "Brief", "")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	task.Step = 3
	task.Revision = task.BaseRevision
	_ = s.saveTask(task)
	worker := &Session{ID: "checks", TaskID: task.ID, ProjectID: "project", Directory: task.Directory, Step: 3, Status: "running"}
	s.sessions[worker.ID] = worker
	s.active = worker.ID
	_ = s.saveSession(worker)
	copyWorker := *worker
	s.mu.Unlock()
	git, e := exec.LookPath("git")
	if e != nil {
		t.Fatal(e)
	}
	bin := t.TempDir()
	ready := filepath.Join(bin, "ready")
	release := filepath.Join(bin, "release")
	script := "#!/bin/sh\nif [ \"$1\" = 'rev-parse' ] && [ \"$2\" = 'HEAD' ]; then touch " + shellQuote(ready) + "; while [ ! -e " + shellQuote(release) + " ]; do sleep 0.01; done; fi\nexec " + shellQuote(git) + " \"$@\"\n"
	if e = os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer os.WriteFile(release, nil, 0600)
	done := make(chan struct{})
	go func() { s.execute(context.Background(), copyWorker, "token", s.runner, ""); close(done) }()
	deadline := time.Now().Add(time.Second)
	for {
		if _, e = os.Stat(ready); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Git verification did not block")
		}
		time.Sleep(time.Millisecond)
	}
	w := call(s, "POST", "tasks/"+task.ID+"/cancel", `{}`, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if e = os.WriteFile(release, nil, 0600); e != nil {
		t.Fatal(e)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("script verification did not finish")
	}
	s.mu.Lock()
	same := task.Status == "cancelled" && task.Step == 3 && len(task.Checks) == 0 && worker.Status == "cancelled"
	s.mu.Unlock()
	if !same {
		t.Fatal("stale script completion revived a cancelled task")
	}
}
