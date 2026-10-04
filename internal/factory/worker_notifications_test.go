package factory

import (
	"context"
	"errors"
	"github.com/owainlewis/machinist/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewFeedbackReachesBusyForemanAtTurnBoundary(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Design", "Review this change", "")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	task.Design = "Initial design"
	s.advance(task)
	_ = s.saveTask(task)
	s.mu.Unlock()
	release := make(chan struct{})
	prompts := make(chan string, 3)
	s.SetRunner(func(ctx context.Context, r RunRequest, emit func(Event), permission func(context.Context, string) (bool, error)) (string, error) {
		prompts <- r.Prompt
		if r.Prompt == "Discuss current work" {
			select {
			case <-release:
			case <-ctx.Done():
				return "foreman", ctx.Err()
			}
		}
		return "foreman", nil
	})
	w := call(s, "POST", "projects/project/messages", `{"request_id":"discussion","message":"Discuss current work"}`, "")
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	select {
	case <-prompts:
	case <-time.After(time.Second):
		t.Fatal("foreman did not start")
	}
	w = call(s, "POST", "tasks/"+task.ID+"/changes", `{"version":1,"message":"Make the design smaller"}`, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	s.mu.Lock()
	foreman, _ := s.foreman("project")
	queued := append([]string(nil), foreman.ReportQueue...)
	s.mu.Unlock()
	if len(queued) != 1 || !strings.Contains(queued[0], "Make the design smaller") {
		t.Fatal("feedback lost while foreman was busy")
	}
	close(release)
	select {
	case prompt := <-prompts:
		if strings.Count(prompt, "Make the design smaller") != 1 {
			t.Fatal("feedback omitted or duplicated")
		}
	case <-time.After(time.Second):
		t.Fatal("busy foreman did not receive queued feedback")
	}
	idle(t, s)
}

func TestWorkerExitNotifiesForemanWithoutRepeatingAcceptedReport(t *testing.T) {
	for _, outcome := range []string{"missing", "error", "complete", "blocked", "blocked-error", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			s, _ := fixture(t)
			s.mu.Lock()
			task, e := s.create("project", "Worker result", "Report the result", "")
			s.mu.Unlock()
			if e != nil {
				t.Fatal(e)
			}
			foremanPrompts := make(chan string, 3)
			workerRuns := make(chan struct{}, 3)
			s.SetRunner(func(ctx context.Context, r RunRequest, emit func(Event), permission func(context.Context, string) (bool, error)) (string, error) {
				if strings.HasPrefix(r.SystemPrompt, "Foreman") {
					foremanPrompts <- r.Prompt
					return "foreman", nil
				}
				workerRuns <- struct{}{}
				if outcome == "cancelled" {
					w := call(s, "POST", "tasks/"+task.ID+"/cancel", `{}`, "")
					if w.Code != 200 {
						t.Error(w.Body.String())
					}
					return "worker", ctx.Err()
				}
				if outcome == "complete" || strings.HasPrefix(outcome, "blocked") {
					result := "blocked"
					if outcome == "complete" {
						result = "complete"
					}
					w := call(s, "POST", "tools/report", `{"task_id":"`+task.ID+`","report_id":"result","summary":"Accepted result","outcome":"`+result+`","design":"Design"}`, r.Token)
					if w.Code != 200 {
						t.Error(w.Body.String())
					}
				}
				if outcome == "error" || outcome == "blocked-error" {
					return "worker", errors.New("provider disconnected")
				}
				return "worker", nil
			})
			s.mu.Lock()
			worker, e := s.start(task)
			s.mu.Unlock()
			if e != nil {
				t.Fatal(e)
			}
			idle(t, s)
			if len(workerRuns) != 1 {
				t.Fatal("worker automatically retried")
			}
			if outcome == "cancelled" {
				if len(foremanPrompts) != 0 {
					t.Fatal("cancelled task notified foreman")
				}
				return
			}
			if len(foremanPrompts) != 1 {
				t.Fatalf("foreman received %d notifications", len(foremanPrompts))
			}
			prompt := <-foremanPrompts
			s.mu.Lock()
			status, activity, step, workerStatus := task.Status, task.Activity, task.Step, worker.Status
			s.mu.Unlock()
			if outcome == "missing" || outcome == "error" {
				if status != "interrupted" || !strings.Contains(prompt, "needs human attention") || !strings.Contains(prompt, "Do not automatically retry") {
					t.Fatalf("unreported exit not surfaced: status=%s prompt=%s", status, prompt)
				}
				if workerStatus != "interrupted" {
					t.Fatal("unreported exit did not require explicit recovery")
				}
				s.mu.Lock()
				foreman, _ := s.foreman("project")
				foreman.Status = "running"
				s.active = foreman.ID
				s.tokens["repair-token"] = foreman.ID
				_, startErr := s.start(task)
				s.mu.Unlock()
				response := call(s, "POST", "tools/send_message", `{"request_id":"retry","task_id":"`+task.ID+`","message":"Retry now"}`, "repair-token")
				s.mu.Lock()
				s.active = ""
				foreman.Status = "completed"
				delete(s.tokens, "repair-token")
				s.mu.Unlock()
				if startErr == nil || response.Code != 409 {
					t.Fatal("foreman bypassed explicit recovery")
				}

			} else {
				if strings.Contains(prompt, "needs human attention") {
					t.Fatal("accepted report was notified again as missing")
				}
				if outcome == "complete" && (step != 1 || status != "awaiting_approval") {
					t.Fatal("accepted completion changed")
				}
				if strings.HasPrefix(outcome, "blocked") && (step != 0 || !strings.HasPrefix(activity, "Blocked:")) {
					t.Fatal("accepted blocked report changed")
				}
			}
		})
	}
}

func TestBlockedWorkerFollowupWithoutReportNeedsExplicitRecovery(t *testing.T) {
	s, _ := fixture(t)
	s.mu.Lock()
	task, e := s.create("project", "Blocked worker", "Inspect blockage", "")
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	workerRuns := make(chan struct{}, 3)
	notifications := make(chan string, 3)
	s.SetRunner(func(ctx context.Context, r RunRequest, emit func(Event), permission func(context.Context, string) (bool, error)) (string, error) {
		if strings.HasPrefix(r.SystemPrompt, "Foreman") {
			notifications <- r.Prompt
			if strings.Contains(r.Prompt, "(blocked)") {
				w := call(s, "POST", "tools/send_message", `{"request_id":"followup","task_id":"`+task.ID+`","message":"Look again"}`, r.Token)
				if w.Code != 200 {
					t.Error(w.Body.String())
				}
			}
			return "foreman", nil
		}
		workerRuns <- struct{}{}
		if !strings.Contains(r.Prompt, "Look again") {
			w := call(s, "POST", "tools/report", `{"task_id":"`+task.ID+`","report_id":"blocked","summary":"Need clarification","outcome":"blocked"}`, r.Token)
			if w.Code != 200 {
				t.Error(w.Body.String())
			}
		}
		return "worker", nil
	})
	s.mu.Lock()
	worker, e := s.start(task)
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	idle(t, s)
	if len(workerRuns) != 2 || len(notifications) != 2 {
		t.Fatalf("unexpected turn counts: workers=%d notices=%d", len(workerRuns), len(notifications))
	}
	first, second := <-notifications, <-notifications
	if !strings.Contains(first, "(blocked)") || !strings.Contains(second, "needs human attention") {
		t.Fatal("unreported followup hidden by stale blocked activity")
	}
	s.mu.Lock()
	taskStatus, workerStatus := task.Status, worker.Status
	s.mu.Unlock()
	if taskStatus != "interrupted" || workerStatus != "interrupted" {
		t.Fatal("unreported followup can repeat without human recovery")
	}
}

func TestReportedRemoteSessionOwnsWorkspaceUntilConfirmedStopped(t *testing.T) {
	s, _ := fixture(t)
	// Run remote Git validation against the disposable local workspace, while
	// preserving the remote ownership and disconnect behavior in the runner.
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "ssh"), []byte("#!/bin/sh\n[ \"$1\" = '-o' ] && [ \"$2\" = 'BatchMode=yes' ] && [ \"$3\" = 'vm' ] || exit 9\nexec sh -c \"$4\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	s.mu.Lock()
	task, e := s.create("project", "Remote report", "Preserve workspace ownership", "")
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	project := s.cfg.Projects["project"]
	project.Host = "vm"
	s.cfg.Projects["project"] = project
	s.cfg.Hosts["vm"] = config.FactoryHost{Name: "VM", SSH: "vm"}
	task.ProjectSnapshot = project
	task.HostSnapshot = s.cfg.Hosts["vm"]
	s.mu.Unlock()
	planningRuns := make(chan struct{}, 2)
	buildStarted := make(chan struct{}, 1)
	s.SetRunner(func(ctx context.Context, r RunRequest, emit func(Event), permission func(context.Context, string) (bool, error)) (string, error) {
		if strings.HasPrefix(r.SystemPrompt, "Foreman") {
			return "foreman", nil
		}
		if r.SystemPrompt == "Builder" {
			buildStarted <- struct{}{}
			<-ctx.Done()
			return "builder", ctx.Err()
		}
		planningRuns <- struct{}{}
		w := call(s, "POST", "tools/report", `{"task_id":"`+task.ID+`","report_id":"design","summary":"Design ready","outcome":"complete","design":"Approved plan"}`, r.Token)
		if w.Code != 200 {
			t.Error(w.Body.String())
		}
		return "planner", errors.New("SSH connection lost after report")
	})
	s.mu.Lock()
	planner, e := s.start(task)
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	idle(t, s)
	s.mu.Lock()
	step, sessionStatus := task.Step, planner.Status
	s.mu.Unlock()
	if step != 1 || sessionStatus != "interrupted" {
		t.Fatalf("reported disconnect not interrupted: step=%d status=%s", step, sessionStatus)
	}
	w := call(s, "POST", "tasks/"+task.ID+"/approve", `{"version":2,"subject":"design"}`, "")
	if w.Code != 409 {
		t.Fatal("approval bypassed uncertain remote ownership", w.Body.String())
	}
	idle(t, s)
	s.mu.Lock()
	_, startErr := s.start(task)
	s.mu.Unlock()
	if startErr == nil {
		t.Fatal("later step started while prior remote process was uncertain")
	}
	select {
	case <-buildStarted:
		t.Fatal("builder started before remote confirmation")
	default:
	}
	w = call(s, "POST", "sessions/"+planner.ID+"/resume", `{"confirmed_stopped":false}`, "")
	if w.Code != 409 {
		t.Fatal("confirmation was bypassed")
	}
	w = call(s, "POST", "sessions/"+planner.ID+"/resume", `{"confirmed_stopped":true}`, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	idle(t, s)
	w = call(s, "POST", "tasks/"+task.ID+"/approve", `{"version":2,"subject":"design"}`, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	idle(t, s)
	s.mu.Lock()
	status, step := planner.Status, task.Step
	builder, startErr := s.start(task)
	s.mu.Unlock()
	if status != "completed" || step != 2 || startErr != nil {
		t.Fatalf("confirmed reported turn did not release next step: status=%s step=%d err=%v", status, step, startErr)
	}
	select {
	case <-buildStarted:
	case <-time.After(time.Second):
		t.Fatal("confirmed recovery did not allow builder")
	}
	if len(planningRuns) != 1 {
		t.Fatal("confirmed recovery replayed already reported planning")
	}
	w = call(s, "POST", "sessions/"+builder.ID+"/cancel", `{}`, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	idle(t, s)
}

func TestHumanReviewWaitsForAnyEarlierWorkspaceOwner(t *testing.T) {
	for _, status := range []string{"running", "queued", "awaiting_permission", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			s, _ := fixture(t)
			s.mu.Lock()
			task, e := s.create("project", "Earlier owner", "Review shared workspace", "")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			task.Step, task.Stage, task.Status = 4, "Review", "active"
			previous := &Session{ID: "previous-build", ProjectID: task.ProjectID, TaskID: task.ID, Step: 2, Status: status}
			s.sessions[previous.ID] = previous
			_, e = s.start(task)
			step, review := task.Step, task.Review
			s.mu.Unlock()
			if e == nil || step != 4 || review != "" {
				t.Fatal("human-review stage advanced while earlier session still owned workspace")
			}
		})
	}
}

func TestDeliveryExitRequiresLinkedPRAndPreservesAcceptedPublication(t *testing.T) {
	for _, outcome := range []string{"unlinked", "error", "linked", "linked-remote-error"} {
		t.Run(outcome, func(t *testing.T) {
			s, _ := fixture(t)
			s.mu.Lock()
			task, e := s.create("project", "Deliver", "Publish the approved revision", "")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			task.Step = len(task.Steps)
			task.Stage = "Review"
			task.Revision = task.BaseRevision
			task.CodeApproved = task.Revision
			task.CodeApprovalVersion = task.Version
			if outcome == "linked-remote-error" {
				p := s.cfg.Projects["project"]
				p.Host = "vm"
				s.cfg.Projects["project"] = p
				s.cfg.Hosts["vm"] = config.FactoryHost{Name: "VM", SSH: "vm"}
				task.ProjectSnapshot = p
				task.HostSnapshot = s.cfg.Hosts["vm"]
			}
			builder := &Session{ID: "delivery-builder", ProjectID: task.ProjectID, TaskID: task.ID, Role: "builder", Status: "completed", Step: 2, Directory: task.Directory}
			s.sessions[builder.ID] = builder
			foreman, e := s.foreman("project")
			if e != nil {
				s.mu.Unlock()
				t.Fatal(e)
			}
			foreman.Status = "running"
			s.active = foreman.ID
			s.tokens["delivery-foreman"] = foreman.ID
			s.mu.Unlock()
			tools := t.TempDir()
			gh := "#!/bin/sh\nprintf '%s\\n' " + shellQuote(`{"state":"OPEN","headRefOid":"`+task.Revision+`","statusCheckRollup":[]}`) + "\n"
			if e = os.WriteFile(filepath.Join(tools, "gh"), []byte(gh), 0700); e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(tools, "ssh"), []byte("#!/bin/sh\nexec sh -c \"$4\"\n"), 0700); e != nil {
				t.Fatal(e)
			}
			t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
			notifications := make(chan string, 3)
			deliveries := make(chan struct{}, 2)
			s.SetRunner(func(ctx context.Context, r RunRequest, emit func(Event), permission func(context.Context, string) (bool, error)) (string, error) {
				if strings.HasPrefix(r.SystemPrompt, "Foreman") {
					notifications <- r.Prompt
					return "foreman", nil
				}
				deliveries <- struct{}{}
				if strings.HasPrefix(outcome, "linked") {
					w := call(s, "POST", "tools/link_pr", `{"task_id":"`+task.ID+`","pr_url":"https://github.com/example/project/pull/7"}`, r.Token)
					if w.Code != 200 {
						t.Error(w.Body.String())
					}
				}
				if outcome == "error" || outcome == "linked-remote-error" {
					return "builder", errors.New("provider disconnected")
				}
				return "builder", nil
			})
			w := call(s, "POST", "tools/send_message", `{"request_id":"deliver","task_id":"`+task.ID+`","message":"Publish approved work"}`, "delivery-foreman")
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			s.mu.Lock()
			s.active = ""
			foreman.Status = "completed"
			delete(s.tokens, "delivery-foreman")
			s.next()
			s.mu.Unlock()
			idle(t, s)
			s.mu.Lock()
			status, workerStatus, linked := task.Status, builder.Status, task.PRURL
			s.mu.Unlock()
			if len(deliveries) != 1 || len(notifications) != 1 {
				t.Fatalf("unexpected delivery/notice counts: %d/%d", len(deliveries), len(notifications))
			}
			notice := <-notifications
			if strings.HasPrefix(outcome, "linked") {
				if linked == "" || strings.Contains(notice, "without linking") {
					t.Fatal("accepted PR publication mistaken for missing result")
				}
				if outcome == "linked" && (status != "active" || workerStatus != "completed" || strings.Contains(notice, "needs human attention")) {
					t.Fatal("successful delivery did not remain in Review")
				}
				if outcome == "linked-remote-error" && (status != "interrupted" || workerStatus != "interrupted" || !strings.Contains(notice, "Confirm the remote delivery process")) {
					t.Fatal("remote publication hid uncertain process ownership")
				}
			} else if status != "interrupted" || workerStatus != "interrupted" || linked != "" || !strings.Contains(notice, "needs human attention") {
				t.Fatal("unlinked delivery silently stalled")
			}
		})
	}
}
