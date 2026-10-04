package factory

import (
	"encoding/json"
	"testing"

	"github.com/owainlewis/machinist/internal/agent"
)

func TestFactoryPermissionUsesProgrammaticToolName(t *testing.T) {
	for _, p := range []agent.Permission{
		{Name: "mcp__machinist__inspect_tasks", Title: "Read project tasks"},
		{Name: "mcp__machinist__report", Title: "Save worker outcome"},
	} {
		if !safeFactoryPermission(p) {
			t.Fatal("known programmatic tool name denied", p)
		}
	}
	for _, p := range []agent.Permission{
		{Name: "Bash", Title: "mcp__machinist__report"},
		{Name: "mcp__machinist__approve", Title: "mcp__machinist__inspect_tasks"},
		{Title: "mcp__machinist__report"},
	} {
		if safeFactoryPermission(p) {
			t.Fatal("display title authorized an unrelated or unnamed tool", p)
		}
	}
}

func TestCancellationPreservesTerminalTasksAcrossAPIAndMCP(t *testing.T) {
	for _, status := range []string{"done", "cancelled"} {
		for _, endpoint := range []string{"api", "mcp"} {
			t.Run(status+endpoint, func(t *testing.T) {
				s, db := fixture(t)
				s.mu.Lock()
				task, e := s.create("project", "Terminal task", "Brief", "")
				if e != nil {
					s.mu.Unlock()
					t.Fatal(e)
				}
				task.Status = status
				if status == "done" {
					task.Stage = "Done"
					task.Activity = "Merged"
				} else {
					task.Activity = "Cancelled"
				}
				_ = s.saveTask(task)
				path := "tasks/" + task.ID + "/cancel"
				body := `{}`
				token := ""
				if endpoint == "mcp" {
					foreman, err := s.foreman("project")
					if err != nil {
						s.mu.Unlock()
						t.Fatal(err)
					}
					foreman.Status = "running"
					s.active = foreman.ID
					s.tokens["terminal"] = foreman.ID
					path = "tools/cancel_task"
					body = `{"task_id":"` + task.ID + `"}`
					token = "terminal"
				}
				before, _ := json.Marshal(diskTask(task))
				signals := 0
				s.cancel = func() { signals++ }
				s.mu.Unlock()
				// Terminal cancellation must reject or return the existing result without writes.
				_, e = db.Exec(`CREATE TRIGGER reject_terminal_write BEFORE INSERT ON factory_records WHEN NEW.kind='task' BEGIN SELECT RAISE(ABORT,'unexpected terminal task write'); END`)
				if e != nil {
					t.Fatal(e)
				}
				for i := 0; i < 2; i++ {
					w := call(s, "POST", path, body, token)
					expected := 200
					if status == "done" {
						expected = 409
					}
					if w.Code != expected {
						t.Fatal("terminal cancellation status", w.Code, w.Body.String())
					}
				}
				s.mu.Lock()
				after, _ := json.Marshal(diskTask(task))
				same := signals == 0 && len(s.queue) == 0
				s.active = ""
				s.cancel = nil
				s.mu.Unlock()
				if string(before) != string(after) || !same {
					t.Fatal("terminal task changed or process signalled")
				}
				var saved string
				if e = db.QueryRow(`SELECT data FROM factory_records WHERE kind='task' AND id=?`, task.ID).Scan(&saved); e != nil || saved != string(before) {
					t.Fatal("terminal state not preserved", e)
				}
			})
		}
	}
}
