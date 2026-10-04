package factory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInspectionBoundsLargeArtifactsAndSupportsTarget(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Inspect", "Brief", "")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	task.Design = strings.Repeat("<", 2000000)
	task.Checks = []Check{{Name: "failed test", Output: strings.Repeat("<", 2000000)}}
	foreman, e := s.foreman("project")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	foreman.Status = "running"
	s.active = foreman.ID
	s.tokens["inspect"] = foreman.ID
	for i := 0; i < 200; i++ {
		copyTask := *task
		copyTask.ID = id("large_")
		s.tasks[copyTask.ID] = &copyTask
	}
	s.mu.Unlock()
	w := call(s, "POST", "tools/inspect_tasks", `{}`, "inspect")
	if w.Code != 200 || w.Body.Len() >= 1<<20 {
		t.Fatalf("inspection oversized or failed: %d %d", w.Code, w.Body.Len())
	}
	var result struct {
		Tasks []struct {
			ID               string
			Design           string
			Checks           []Check
			DetailsTruncated bool `json:"details_truncated"`
		}
		TasksTruncated bool `json:"tasks_truncated"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if !result.TasksTruncated {
		t.Fatal("missing aggregate truncation flag")
	}
	w = call(s, "POST", "tools/inspect_tasks", `{"task_id":"`+task.ID+`"}`, "inspect")
	if e = json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if len(result.Tasks) != 1 || result.Tasks[0].ID != task.ID || !result.Tasks[0].DetailsTruncated || !strings.Contains(result.Tasks[0].Checks[0].Output, "truncated") {
		t.Fatal("targeted inspection missing useful bounded details")
	}
}

func TestReportFailureDoesNotPublishTaskOrForeman(t *testing.T) {
	for _, outcome := range []string{"complete", "blocked", "changes"} {
		t.Run(outcome, func(t *testing.T) {
			s, db := fixture(t)
			s.mu.Lock()
			task, e := s.create("project", "Report", "Brief", "")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			if outcome == "changes" {
				task.Step = 2
				task.Stage = "Build"
			}
			foreman, e := s.foreman("project")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			foreman.Status = "interrupted"
			foreman.ReportQueue = []string{"previous"}
			_ = s.saveSession(foreman)
			worker := &Session{ID: "report-worker", TaskID: task.ID, ProjectID: "project", Step: task.Step, Status: "running", Pending: "original"}
			s.sessions[worker.ID] = worker
			s.active = worker.ID
			s.tokens["report-token"] = worker.ID
			_ = s.saveTask(task)
			_ = s.saveSession(worker)
			before, _ := json.Marshal(diskTask(task))
			s.mu.Unlock()
			_, e = db.Exec(`CREATE TRIGGER reject_report_marker BEFORE INSERT ON factory_records WHEN NEW.kind='request' AND NEW.id GLOB 'report:*' BEGIN SELECT RAISE(ABORT,'report persistence failed'); END`)
			if e != nil {
				t.Fatal(e)
			}
			body := `{"task_id":"` + task.ID + `","report_id":"once","summary":"feedback","outcome":"` + outcome + `","design":"Approved plan"}`
			w := call(s, "POST", "tools/report", body, "report-token")
			if w.Code != 409 {
				t.Fatal(w.Body.String())
			}
			s.mu.Lock()
			after, _ := json.Marshal(diskTask(task))
			unchanged := string(before) == string(after) && len(foreman.ReportQueue) == 1 && worker.Pending == "original" && !worker.Reported
			s.mu.Unlock()
			if !unchanged {
				t.Fatal("failed report mutated live state")
			}
			_, e = db.Exec(`DROP TRIGGER reject_report_marker`)
			if e != nil {
				t.Fatal(e)
			}
			w = call(s, "POST", "tools/report", body, "report-token")
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			retry := call(s, "POST", "tools/report", body, "report-token")
			if retry.Code != 200 {
				t.Fatal(retry.Body.String())
			}
			s.mu.Lock()
			count := len(foreman.ReportQueue)
			reported := worker.Reported
			s.mu.Unlock()
			var saved string
			if e = db.QueryRow(`SELECT data FROM factory_records WHERE kind='task' AND id=?`, task.ID).Scan(&saved); e != nil {
				t.Fatal(e)
			}
			after, _ = json.Marshal(diskTask(task))
			if saved != string(after) || count != 2 || !reported {
				t.Fatal("retry did not persist exactly one report")
			}
			var events int
			if e = db.QueryRow(`SELECT COUNT(*) FROM factory_events WHERE session_id=?`, worker.ID).Scan(&events); e != nil || events != 1 {
				t.Fatalf("report events: %d %v", events, e)
			}
			s.mu.Lock()
			s.active = ""
			s.mu.Unlock()
			s.Close()
			restored, err := New(db, s.cfg, "machinist")
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			restored.mu.Lock()
			restoredWorker := restored.sessions[worker.ID]
			restoredWorker.Status = "running"
			restored.active = worker.ID
			restored.tokens["report-token"] = worker.ID
			restored.mu.Unlock()
			w = call(restored, "POST", "tools/report", body, "report-token")
			if w.Code != 200 {
				t.Fatal("restart lost report retry marker", w.Body.String())
			}
			restored.mu.Lock()
			same := restored.tasks[task.ID].Step == task.Step && restored.tasks[task.ID].Version == task.Version && len(restored.sessions[foreman.ID].ReportQueue) == 2
			restored.active = ""
			restored.mu.Unlock()
			if !same {
				t.Fatal("restart replay changed accepted report")
			}
			if e = db.QueryRow(`SELECT COUNT(*) FROM factory_events WHERE session_id=?`, worker.ID).Scan(&events); e != nil || events != 1 {
				t.Fatal("restart duplicated report event")
			}
		})
	}
}
