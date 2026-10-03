package factory

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

func (s *Service) tool(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != "POST" {
		http.Error(w, "POST required", 405)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	session := s.sessions[s.tokens[token]]
	if token == "" || session == nil || session.ID != s.active || session.Status == "cancelled" || session.Status == "interrupted" {
		http.Error(w, "Invalid conversation credential", 401)
		return
	}
	var in struct {
		RequestID string `json:"request_id"`
		Message   string `json:"message"`
		Title     string `json:"title"`
		Brief     string `json:"brief"`
		Pipeline  string `json:"pipeline"`
		TaskID    string `json:"task_id"`
		ReportID  string `json:"report_id"`
		Summary   string `json:"summary"`
		Outcome   string `json:"outcome"`
		Design    string `json:"design"`
		Revision  string `json:"revision"`
		PRURL     string `json:"pr_url"`
	}
	if !decode(w, r, &in) {
		return
	}
	switch name {
	case "inspect_tasks":
		tasks := []*Task{}
		for _, t := range s.tasks {
			if t.ProjectID == session.ProjectID && (session.isForeman() || session.TaskID == t.ID) {
				tasks = append(tasks, t)
			}
		}
		jsonReply(w, 200, map[string]any{"tasks": tasks})
		return
	case "create_task":
		if !session.isForeman() {
			http.Error(w, "Foreman only", 403)
			return
		}
		key := "create:" + session.ID + ":" + session.RequestID + ":" + in.Title
		if old := s.requests[key]; old != "" {
			jsonReply(w, 200, map[string]any{"task": s.tasks[old]})
			return
		}
		t, e := s.create(session.ProjectID, in.Title, in.Brief, in.Pipeline)
		if e != nil {
			fail(w, e)
			return
		}
		if e = s.request(key, t.ID); e != nil {
			fail(w, e)
			return
		}
		s.signal()
		jsonReply(w, 200, map[string]any{"task": t})
		return
	case "start_step":
		if !session.isForeman() {
			http.Error(w, "Foreman only", 403)
			return
		}
		t := s.tasks[in.TaskID]
		if t == nil || t.ProjectID != session.ProjectID {
			http.Error(w, "Task outside conversation scope", 403)
			return
		}
		v, e := s.start(t)
		if e != nil {
			fail(w, e)
			return
		}
		jsonReply(w, 200, map[string]any{"task": t, "session": v})
		return
	case "send_message":
		if !session.isForeman() {
			http.Error(w, "Foreman only", 403)
			return
		}
		t := s.tasks[in.TaskID]
		if t == nil || t.ProjectID != session.ProjectID {
			http.Error(w, "Task outside conversation scope", 403)
			return
		}
		if in.RequestID == "" || strings.TrimSpace(in.Message) == "" {
			fail(w, errors.New("request_id and message required"))
			return
		}
		key := "worker-message:" + session.ID + ":" + in.RequestID
		if s.requests[key] != "" {
			jsonReply(w, 200, map[string]any{"task": t})
			return
		}
		delivery := t.Step >= len(t.Steps)
		if t.Status == "done" || t.Status == "cancelled" {
			fail(w, errors.New("task is no longer active"))
			return
		}
		if t.Status == "awaiting_approval" || (delivery && (t.CodeApproved == "" || t.CodeApproved != t.Revision)) {
			fail(w, errors.New("human decision required; cannot message past an approval"))
			return
		}
		for _, v := range s.sessions {
			if v.TaskID == t.ID && (v.Status == "running" || v.Status == "queued" || v.Status == "awaiting_permission" || v.Status == "interrupted") {
				fail(w, errors.New("worker is busy; wait for its current turn"))
				return
			}
		}
		if t.Repairs >= 3 {
			fail(w, errors.New("repair limit reached; human continuation required"))
			return
		}
		if t.Status == "failed" {
			if e := s.repair(t, in.Message); e != nil {
				fail(w, e)
				return
			}
			if e := s.saveTask(t); e != nil {
				fail(w, e)
				return
			}
			if t.Status == "failed" {
				fail(w, errors.New("repair limit reached; human continuation required"))
				return
			}
		}
		target := t.Step
		if delivery {
			target = -1
			for i, step := range t.Steps {
				if step.Type == "agent" && strings.EqualFold(step.Stage, "build") {
					target = i
					break
				}
			}
			if e := s.deliveryReady(t); e != nil {
				fail(w, e)
				return
			}
		}
		var worker *Session
		for _, v := range s.sessions {
			if v.TaskID == t.ID && v.Step == target {
				worker = v
				break
			}
		}
		if worker == nil {
			fail(w, errors.New("start the eligible step first"))
			return
		}
		worker.Delivery = delivery
		if e := s.enqueue(worker, in.Message, in.RequestID); e != nil {
			fail(w, e)
			return
		}
		_ = s.request(key, t.ID)
		jsonReply(w, 200, map[string]any{"task": t, "session": worker})
		return
	case "cancel_task":
		if !session.isForeman() {
			http.Error(w, "Foreman only", 403)
			return
		}
		t := s.tasks[in.TaskID]
		if t == nil || t.ProjectID != session.ProjectID {
			http.Error(w, "Task outside conversation scope", 403)
			return
		}
		t.Status = "cancelled"
		t.Activity = "Cancelled"
		for _, v := range s.sessions {
			if v.TaskID == t.ID && v.Status == "queued" {
				v.Status = "cancelled"
				_ = s.saveSession(v)
			}
		}
		_ = s.saveTask(t)
		s.signal()
		jsonReply(w, 200, map[string]any{"task": t})
		return
	case "link_pr":
		if session.TaskID == "" || session.TaskID != in.TaskID || !session.Delivery {
			http.Error(w, "Approved delivery worker only", 403)
			return
		}
		t := s.tasks[session.TaskID]
		if e := s.deliveryReady(t); e != nil {
			fail(w, e)
			return
		}
		if !validPR(in.PRURL, s.cfg.Projects[t.ProjectID].GitHub) {
			fail(w, errors.New("PR must belong to configured project repository"))
			return
		}
		pr, e := s.readPRSnapshot(r.Context(), t, in.PRURL)
		if e != nil {
			fail(w, e)
			return
		}
		if s.active != session.ID || s.tokens[token] != session.ID || session.Status == "cancelled" || session.Status == "interrupted" {
			http.Error(w, "delivery session stopped while reading GitHub", 403)
			return
		}
		if e := s.deliveryReady(t); e != nil {
			fail(w, e)
			return
		}
		if s.tokens[token] != session.ID || session.Status == "cancelled" || session.Status == "interrupted" {
			http.Error(w, "delivery session stopped while reading workspace", 403)
			return
		}
		if pr.HeadRefOID != t.Revision {
			fail(w, errors.New("PR head differs from the human-approved revision"))
			return
		}
		t.PRURL = in.PRURL
		t.Activity = "Waiting for verified merge"
		t.GitHubHead = pr.HeadRefOID
		t.ObservedAt = now()
		t.GitHubError = ""
		if e = s.saveTask(t); e != nil {
			fail(w, e)
			return
		}
		s.notify(t.ProjectID, "Approved revision published for task "+t.ID+": "+in.PRURL+". It remains in Review until GitHub verifies merge.")
		s.signal()
		jsonReply(w, 200, map[string]any{"task": t})
		return
	case "report":
		if session.TaskID == "" || session.TaskID != in.TaskID {
			http.Error(w, "Worker can report only its own task", 403)
			return
		}
		if in.ReportID == "" || in.Summary == "" {
			fail(w, errors.New("report_id and summary are required"))
			return
		}
		key := "report:" + session.ID + ":" + in.ReportID
		if old := s.requests[key]; old != "" {
			jsonReply(w, 200, map[string]any{"task": s.tasks[old]})
			return
		}
		t := s.tasks[session.TaskID]
		if t == nil || t.Step != session.Step || t.Status == "cancelled" {
			fail(w, errors.New("stale step report"))
			return
		}
		if in.PRURL != "" && !validPR(in.PRURL, s.cfg.Projects[t.ProjectID].GitHub) {
			fail(w, errors.New("PR must belong to configured project repository"))
			return
		}
		step := t.Steps[t.Step]
		switch in.Outcome {
		case "blocked":
			t.Activity = "Blocked: " + in.Summary
		case "changes":
			if e := s.repair(t, in.Summary); e != nil {
				fail(w, e)
				return
			}
		case "complete":
			if strings.EqualFold(step.Stage, "design") {
				if strings.TrimSpace(in.Design) == "" {
					fail(w, errors.New("planning report requires design text"))
					return
				}
				t.Design = in.Design
				t.Version++
			} else if strings.EqualFold(step.Stage, "build") {
				rev, dirty, e := s.workspaceState(t)
				if e != nil {
					fail(w, e)
					return
				}
				if s.tokens[token] != session.ID || session.Status == "cancelled" || session.Status == "interrupted" {
					http.Error(w, "worker session stopped while reading workspace", 403)
					return
				}
				if dirty != "" {
					fail(w, errors.New("commit the implementation before reporting"))
					return
				}
				if in.Revision != "" && in.Revision != rev {
					fail(w, errors.New("reported revision differs from workspace HEAD"))
					return
				}
				t.Revision = rev
				t.CodeApproved = ""
				t.Version++
				t.Checks = []Check{}
				if in.PRURL != "" {
					if !validPR(in.PRURL, s.cfg.Projects[t.ProjectID].GitHub) {
						fail(w, errors.New("PR must belong to the configured project repository"))
						return
					}
					t.PRURL = in.PRURL
				}
			}
			s.advance(t)
		default:
			fail(w, errors.New("outcome must be complete, blocked, or changes"))
			return
		}
		foreman, e := s.foreman(t.ProjectID)
		if e != nil {
			fail(w, e)
			return
		}
		notification := fmt.Sprintf("Worker report for task %s: %s (%s). Inspect the task and continue its eligible pipeline step, or ask for the pending human decision.", t.ID, in.Summary, in.Outcome)
		queue := false
		if foreman.Status == "queued" {
			foreman.Pending += "\n" + notification
		} else if foreman.Status == "running" || foreman.Status == "awaiting_permission" || foreman.Status == "interrupted" || foreman.Status == "failed" || foreman.Status == "cancelled" {
			foreman.ReportQueue = append(foreman.ReportQueue, notification)
		} else {
			foreman.Status = "queued"
			foreman.Pending = notification
			foreman.RequestID = id("report_")
			queue = true
		}
		if e = s.commitRecords([]recordWrite{{"task", t.ID, diskTask(t)}, {"session", foreman.ID, diskSession(foreman)}, {"request", key, t.ID}}, session.ID, Event{Kind: "report", Text: in.Summary, Title: in.Outcome}); e != nil {
			fail(w, e)
			return
		}
		s.requests[key] = t.ID
		if queue {
			s.queue = append(s.queue, foreman.ID)
		}
		s.signal()

		jsonReply(w, 200, map[string]any{"task": t})
		return
	}
	http.NotFound(w, r)
}
func (s *Service) approve(t *Task, version int, subject string) error {
	if version == t.Version && ((subject == "design" && t.DesignApprovalVersion == version) || (subject == "code" && t.CodeApprovalVersion == version && t.CodeApproved == t.Revision)) {
		return nil
	}

	if version != t.Version {
		return errors.New("content changed; reload before approving")
	}
	if t.Status != "awaiting_approval" || t.Step >= len(t.Steps) {
		return errors.New("task is not awaiting approval")
	}
	step := t.Steps[t.Step]
	if step.Type != "approval" || step.Subject != subject {
		return errors.New("approval subject does not match the pending step")
	}
	if subject == "design" && t.Design == "" {
		return errors.New("no design to approve")
	}
	if subject == "code" {
		if t.Revision == "" {
			return errors.New("no submitted revision")
		}
		current, dirty, e := s.workspaceState(t)
		if e != nil {
			return e
		}
		if current != t.Revision || dirty != "" {
			return errors.New("workspace changed; submit and validate its current revision")
		}
		for _, check := range t.Checks {
			if !check.Passed || check.Revision != t.Revision {
				return errors.New("checks do not pass for this revision")
			}
		}
		if len(t.Checks) == 0 {
			return errors.New("run the configured checks before code approval")
		}
		if t.PRURL != "" {
			if e := s.refresh(t); e != nil {
				return e
			}
			if t.GitHubHead != t.Revision {
				return errors.New("GitHub head changed; submit current revision")
			}
			if !t.GitHubChecksPass {
				return errors.New("GitHub checks are pending or failed")
			}
		}
		t.CodeApproved = t.Revision
		t.CodeApprovalVersion = version
	}
	if subject == "design" {
		t.DesignApprovalVersion = version
	}
	s.advance(t)
	if e := s.saveTask(t); e != nil {
		return e
	}
	s.notify(t.ProjectID, "Human approved "+subject+" for task "+t.ID+". Inspect and continue the next eligible step.")
	s.signal()
	return nil
}
func (s *Service) repair(t *Task, feedback string) error {
	if strings.EqualFold(t.Stage, "design") {
		return errors.New("design must complete human approval before implementation repair")
	}
	if strings.TrimSpace(feedback) == "" {
		return errors.New("feedback is required")
	}
	target := -1
	for i, step := range t.Steps {
		if step.Type == "agent" && strings.EqualFold(step.Stage, "build") {
			target = i
			break
		}
	}
	if target < 0 {
		return errors.New("pipeline has no implementation step")
	}
	t.Step = target
	t.Stage = "Build"
	t.Status = "active"
	t.Activity = "Changes requested: " + feedback
	t.Repairs++
	t.Version++
	t.Review = ""
	t.CodeApproved = ""
	t.ApprovalSubject = ""
	t.Checks = []Check{}
	if t.Repairs >= 3 {
		t.Status = "failed"
		t.Activity = "Repair limit reached. Review the task before continuing."
	}
	for _, v := range s.sessions {
		if v.TaskID == t.ID && v.Step == target {
			v.Delivery = false
			v.Pending = feedback
			_ = s.saveSession(v)
		}
	}
	return nil
}
func (s *Service) changes(t *Task, version int, message string) error {
	for _, v := range s.sessions {
		if v.TaskID == t.ID && (v.Status == "running" || v.Status == "queued" || v.Status == "awaiting_permission" || v.Status == "interrupted") {
			return errors.New("stop or explicitly recover the task agent before requesting changes")
		}
	}
	if t.Status != "awaiting_approval" && t.Status != "failed" && t.Status != "interrupted" {
		return errors.New("wait for a review or interrupted task before requesting changes")
	}

	if version != t.Version {
		return errors.New("content changed; reload before requesting changes")
	}
	if t.Status == "done" || t.Status == "cancelled" {
		return errors.New("task is no longer active")
	}
	if strings.TrimSpace(message) == "" {
		return errors.New("feedback is required")
	}
	if t.ApprovalSubject == "design" || strings.EqualFold(t.Stage, "design") {
		t.Step = 0
		t.Version++
		t.Design = ""
		t.Status = "active"
		t.Activity = "Design changes requested: " + message
		t.ApprovalSubject = ""
	} else {
		if e := s.repair(t, message); e != nil {
			return e
		}
	}
	if e := s.saveTask(t); e != nil {
		return e
	}
	foreman, e := s.foreman(t.ProjectID)
	if e == nil && foreman.Status != "running" && foreman.Status != "queued" {
		_ = s.enqueue(foreman, "Human requested changes on task "+t.ID+": "+message+". Inspect task and start the eligible step.", id("feedback_"))
	}
	s.signal()
	return nil
}

// notify queues durable reports at a turn boundary instead of interrupting chat.
func (s *Service) notify(project, message string) {
	v, e := s.foreman(project)
	if e != nil {
		return
	}
	if v.Status == "running" || v.Status == "awaiting_permission" || v.Status == "interrupted" || v.Status == "failed" || v.Status == "cancelled" {
		v.ReportQueue = append(v.ReportQueue, message)
		_ = s.saveSession(v)
	} else if v.Status == "queued" {
		v.Pending += "\n" + message
		_ = s.saveSession(v)
	} else {
		_ = s.enqueue(v, message, id("notification_"))
	}
}

func (s *Service) deliveryReady(t *Task) error {
	if t == nil || t.Status == "done" || t.Status == "cancelled" || t.CodeApproved == "" || t.CodeApproved != t.Revision || t.Step < len(t.Steps) {
		return errors.New("human approval of the exact completed pipeline revision is required before publishing")
	}
	if e := s.validateTask(t); e != nil {
		return e
	}
	rev, dirty, e := s.workspaceState(t)
	if e != nil {
		return e
	}
	if rev != t.CodeApproved || dirty != "" {
		return errors.New("workspace differs from the approved revision; rebuild and review it first")
	}
	return nil
}
