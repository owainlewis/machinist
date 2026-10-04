package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMutationResponsesOmitLargeSavedArtifacts(t *testing.T) {
	for _, operation := range []string{"create_retry", "start_step", "send_message", "cancel_task", "link_pr", "report"} {
		t.Run(operation, func(t *testing.T) {
			s, _ := fixture(t)
			s.mu.Lock()
			foreman, e := s.foreman("project")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			foreman.Status = "running"
			foreman.RequestID = "mutation-turn"
			s.active = foreman.ID
			s.tokens["foreman"] = foreman.ID
			s.mu.Unlock()
			var task *Task
			createBody := `{"title":"Bounded reply","brief":"Useful task brief"}`
			if operation == "create_retry" {
				w := call(s, "POST", "tools/create_task", createBody, "foreman")
				if w.Code != 200 {
					t.Fatal(w.Body.String())
				}
				var created struct{ Task Task }
				if e = json.Unmarshal(w.Body.Bytes(), &created); e != nil {
					t.Fatal(e)
				}
				s.mu.Lock()
				task = s.tasks[created.Task.ID]
				s.mu.Unlock()
			} else {
				s.mu.Lock()
				task, e = s.create("project", "Bounded reply", "Useful task brief", "")
				s.mu.Unlock()
				if e != nil {
					t.Fatal(e)
				}
			}
			s.mu.Lock()
			payload := strings.Repeat("<", (1<<20)-32)
			task.Checks = []Check{{StepID: "checks", Name: "Large check", Passed: true, Output: payload, Revision: task.BaseRevision}}
			task.Design = strings.Repeat("design text", 20000)
			task.Review = strings.Repeat("review text", 20000)
			task.Revision = task.BaseRevision
			worker := &Session{ID: "builder", TaskID: task.ID, ProjectID: "project", Step: 2, Status: "completed"}
			s.sessions[worker.ID] = worker
			token := "foreman"
			body := `{"task_id":"` + task.ID + `"}`
			switch operation {
			case "start_step":
				task.Step = 4
				task.Stage = "Review"
			case "send_message":
				task.Step = 2
				task.Stage = "Build"
				body = `{"task_id":"` + task.ID + `","message":"Continue","request_id":"once"}`
			case "link_pr":
				task.Step = len(task.Steps)
				task.CodeApproved = task.Revision
				task.Stage = "Review"
				worker.Status = "running"
				worker.Delivery = true
				s.active = worker.ID
				s.tokens["delivery"] = worker.ID
				token = "delivery"
				foreman.Status = "interrupted"
				body = `{"task_id":"` + task.ID + `","pr_url":"https://github.com/example/project/pull/1"}`
			case "report":
				task.Step = 2
				task.Stage = "Build"
				worker.Status = "running"
				s.active = worker.ID
				s.tokens["worker"] = worker.ID
				token = "worker"
				foreman.Status = "interrupted"
				body = `{"task_id":"` + task.ID + `","report_id":"once","summary":"Need human answer","outcome":"blocked"}`
			case "create_retry":
				body = createBody
			}
			s.mu.Unlock()
			if operation == "link_pr" {
				bin := t.TempDir()
				script := "#!/bin/sh\nprintf '%s' '{\"state\":\"OPEN\",\"headRefOid\":\"" + task.Revision + "\",\"statusCheckRollup\":[]}'\n"
				if e = os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); e != nil {
					t.Fatal(e)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			}
			tool := operation
			if operation == "create_retry" {
				tool = "create_task"
			}
			w := call(s, "POST", "tools/"+tool, body, token)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			if w.Body.Len() > 16<<10 {
				t.Fatal("mutation response exceeds useful bounded acknowledgement", w.Body.Len())
			}
			var response struct{ Task map[string]any }
			if e = json.Unmarshal(w.Body.Bytes(), &response); e != nil {
				t.Fatal(e)
			}
			for _, key := range []string{"checks", "design", "review"} {
				if _, found := response.Task[key]; found {
					t.Fatal("mutation returns saved artifact", key)
				}
			}
			s.mu.Lock()
			same := task.Checks[0].Output == payload && response.Task["id"] == task.ID && response.Task["status"] == task.Status && response.Task["step"] == float64(task.Step) && response.Task["version"] == float64(task.Version)
			s.active = ""
			s.mu.Unlock()
			if !same {
				t.Fatal("mutation state or saved artifacts changed by projection")
			}
			switch operation {
			case "start_step":
				if response.Task["status"] != "awaiting_approval" || response.Task["step"] != float64(5) {
					t.Fatal("review transition missing")
				}
			case "cancel_task":
				if response.Task["status"] != "cancelled" {
					t.Fatal("cancellation missing")
				}
			case "link_pr":
				if response.Task["pr_url"] != "https://github.com/example/project/pull/1" {
					t.Fatal("PR link missing")
				}
			case "report":
				if !strings.Contains(response.Task["activity"].(string), "Blocked:") {
					t.Fatal("report acknowledgement missing")
				}
			}
		})
	}
}
