package factory

import (
	"context"
	"errors"
	"fmt"
	"github.com/owainlewis/machinist/internal/agent"
	"github.com/owainlewis/machinist/internal/config"
	processrunner "github.com/owainlewis/machinist/internal/runner"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

func shellQuote(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
func shellArgs(v []string) string {
	out := make([]string, len(v))
	for i, a := range v {
		out[i] = shellQuote(a)
	}
	return strings.Join(out, " ")
}
func (s *Service) command(ctx context.Context, project, dir string, args []string) *exec.Cmd {
	s.commandMu.RLock()
	p := s.cfg.Projects[project]
	h := s.cfg.Hosts[p.Host]
	s.commandMu.RUnlock()
	return hostCommand(ctx, p, h, dir, args)
}
func (s *Service) git(project, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := s.command(ctx, project, dir, append([]string{"git"}, args...))
	b, e := cmd.CombinedOutput()
	if e != nil {
		return "", fmt.Errorf("git: %s", strings.TrimSpace(string(b)))
	}
	return strings.TrimSpace(string(b)), nil
}

// gitLimited keeps browser review output bounded without changing revision/status reads.
func (s *Service) gitLimited(project, dir string, limit int, args ...string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.gitLimitedContext(ctx, project, dir, limit, false, args...)
}
func (s *Service) gitLimitedContext(ctx context.Context, project, dir string, limit int, allowDifferences bool, args ...string) (string, bool, error) {
	cmd := s.command(ctx, project, dir, append([]string{"git"}, args...))
	out, stderr := &boundedBuffer{limit: limit}, &boundedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !allowDifferences || !errors.As(err, &exit) || exit.ExitCode() != 1 || (len(out.data) == 0 && !out.truncated) {
			return "", false, fmt.Errorf("git: %s (%w)", stderr.String(), err)
		}
	}
	return string(out.data), out.truncated, nil
}
func (s *Service) foremanDirectory(project string) string {
	p := s.cfg.Projects[project]
	if p.Host == "" || p.Host == "local" {
		return p.Path
	}
	root, e := os.UserCacheDir()
	if e != nil {
		return os.TempDir()
	}
	dir := filepath.Join(root, "machinist", "factory", "foreman", project)
	_ = os.MkdirAll(dir, 0700)
	return dir
}
func (s *Service) runClaude(ctx context.Context, r RunRequest, emit func(Event), permission func(context.Context, string) (bool, error)) (string, error) {
	root, e := os.Getwd()
	if e != nil {
		return "", e
	}
	driver := agent.Default(root)
	if h := s.cfg.Hosts["local"]; len(h.ACPCommand) > 0 {
		driver = &agent.Claude{Command: h.ACPCommand[0], Args: h.ACPCommand[1:]}
	}
	command := r.Executable
	toolURL := r.URL
	if r.Host != "" && r.Host != "local" {
		h := s.cfg.Hosts[r.Host]
		forward, e := reverseForward(r.URL, h.ToolsPort)
		if e != nil {
			return "", e
		}
		driver = &agent.Claude{Command: "ssh", Args: []string{"-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes", "-R", forward, h.SSH, shellArgs(h.ACPCommand)}, Remote: true}
		command = h.MachinistCommand
		toolURL = fmt.Sprintf("http://127.0.0.1:%d", h.ToolsPort)
	}
	return driver.Run(ctx, agent.Request{Directory: r.Directory, SessionID: r.SessionID, Prompt: r.Prompt, SystemPrompt: r.SystemPrompt, Model: r.Model, ReadOnly: r.ReadOnly, MCPServers: []agent.MCPServer{{Name: "machinist", Command: command, Args: []string{"factory", "tools"}, Env: map[string]string{"MACHINIST_FACTORY_URL": toolURL, "MACHINIST_FACTORY_TOKEN": r.Token}}}}, func(e agent.Event) {
		kind := e.Kind
		if kind == "text" {
			kind = "message_delta"
		}
		emit(Event{Kind: kind, Text: e.Text, Title: e.Title, ProviderID: func() string {
			if e.Kind == "session" {
				return e.ID
			}
			return ""
		}()})
	}, func(ctx context.Context, p agent.Permission) (bool, error) {
		if safeFactoryPermission(p) {
			return true, nil
		}
		return permission(ctx, p.Title)
	})
}

func (s *Service) execute(ctx context.Context, v Session, token string, runner RunFunc, url string) {
	s.mu.Lock()
	profile := s.cfg.Agents[s.cfg.Foreman]
	system := profile.Prompt
	prompt := v.Pending
	readOnly := false
	var script []string
	timeout := profile.Timeout
	if t := s.tasks[v.TaskID]; t != nil {
		step := t.Steps[v.Step]
		readOnly = strings.EqualFold(step.Stage, "design") && !v.Delivery
		if step.Type == "script" {
			script = append([]string(nil), step.Command...)
			timeout, _ = time.ParseDuration(step.Timeout)
			if timeout == 0 {
				timeout = 10 * time.Minute
			}
		} else {
			profile = t.Agents[step.Agent]
			timeout = profile.Timeout
			system = profile.Prompt
		}
		prompt = fmt.Sprintf("Task %s: %s\nBrief: %s\nDesign: %s\nFeedback: %s\nCurrent step: %s (%s)\nUse the connected machinist MCP server, not a terminal CLI. When complete call mcp__machinist__report with task_id=%s, a unique report_id, summary, outcome=complete and design text for Design. Do not alter Machinist configuration or spawn other agents yourself. Never merge or publish without human authority.", t.ID, t.Title, t.Brief, t.Design, t.Activity+"\n"+v.Pending, step.Name, step.Stage, t.ID)
		prompt += "\n" + stageInstructions(step.Stage)
		if v.Delivery {
			prompt = "Human approved delivery of commit " + t.CodeApproved + " for task " + t.ID + ". First inspect whether this branch already has a pull request to avoid duplicate publication. Publish only this approved branch as a pull request to " + s.cfg.Projects[t.ProjectID].GitHub + ". Do not edit code or merge. Call mcp__machinist__link_pr with task_id=" + t.ID + " and pr_url once published. Feedback: " + v.Pending
		}
	} else {
		system += "\nYou are the only user-facing foreman. Use the connected machinist MCP server: mcp__machinist__inspect_tasks reads work, mcp__machinist__create_task creates a task, mcp__machinist__start_step runs its eligible step, mcp__machinist__send_message routes feedback to its worker, and mcp__machinist__cancel_task stops queued work. These are connected tools, not terminal CLI commands. Do not search for, install, or configure another Machinist instance. Delegate code changes. After human code delivery approval, use send_message to the existing builder to publish only the approved revision and link its PR. Never grant approval, merge, or bypass a pipeline gate. When a worker reports, inspect its task and start the next eligible step unless a human decision is needed."
	}
	host := "local"
	if !v.isForeman() {
		host = s.cfg.Projects[v.ProjectID].Host
	}
	s.mu.Unlock()
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	recorded := 0
	truncated := false
	emit := func(e Event) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.active == v.ID && s.sessions[v.ID].Status != "cancelled" {
			if e.ProviderID != "" {
				s.sessions[v.ID].ProviderID = e.ProviderID
				_ = s.saveSession(s.sessions[v.ID])
				return
			}

			if recorded >= 32<<20 {
				if !truncated {
					truncated = true
					_ = s.event(v.ID, Event{Kind: "activity", Title: "Output limit reached", Text: "Further output is omitted; final status and structured reports are retained."})
				}
				return
			}
			recorded += len(e.Text) + len(e.Title)

			if len(e.Text) > 256<<10 {
				e.Text = e.Text[:256<<10] + "\n[Output truncated]"
			}
			_ = s.event(v.ID, e)
		}
	}
	permission := func(ctx context.Context, title string) (bool, error) {
		s.mu.Lock()
		p := &Permission{ID: id("p_"), SessionID: v.ID, Title: title, Status: "pending", answer: make(chan bool, 1)}
		s.permissions[p.ID] = p
		if t := s.tasks[v.TaskID]; t != nil {
			t.Activity = "Permission needed"
			_ = s.saveTask(t)
		}
		s.sessions[v.ID].Status = "awaiting_permission"
		_ = s.saveSession(s.sessions[v.ID])
		_ = s.event(v.ID, Event{Kind: "permission", Text: p.ID, Title: title})
		s.mu.Unlock()
		select {
		case allow := <-p.answer:
			s.mu.Lock()
			p.Status = "resolved"
			if t := s.tasks[v.TaskID]; t != nil {
				t.Activity = "Working"
				_ = s.saveTask(t)
			}
			if s.sessions[v.ID].Status == "awaiting_permission" {
				s.sessions[v.ID].Status = "running"
				_ = s.saveSession(s.sessions[v.ID])
			}
			s.mu.Unlock()
			return allow, nil
		case <-ctx.Done():
			s.mu.Lock()
			p.Status = "expired"
			s.mu.Unlock()
			return false, ctx.Err()
		}
	}
	provider := v.ProviderID
	var runErr error
	var output string
	if len(script) > 0 {
		cmd := s.command(ctx, v.ProjectID, v.Directory, script)
		processrunner.ConfigureProcess(cmd)
		cmd.Cancel = func() error { return processrunner.TerminateProcessTree(cmd.Process) }
		cmd.WaitDelay = 2 * time.Second
		limited := &boundedBuffer{limit: 1 << 20}
		cmd.Stdout = limited
		cmd.Stderr = limited
		runErr = cmd.Run()
		output = limited.String()
		emit(Event{Kind: "activity", Title: "Checks", Text: output})
	} else {
		provider, runErr = runner(ctx, RunRequest{Host: host, Directory: v.Directory, SessionID: v.ProviderID, Prompt: prompt, SystemPrompt: system, Model: profile.Model, Token: token, URL: url, Executable: s.executable, ReadOnly: readOnly}, emit, permission)
	}
	if runErr == nil && ctx.Err() != nil {
		runErr = ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
	current := s.sessions[v.ID]
	if current == nil {
		return
	}
	originalSession := current
	copySession := *current
	current = &copySession
	var originalTask, finishedTask *Task
	current.ProviderID = provider
	if current.Status == "cancelled" || (current.Status == "interrupted" && ctx.Err() != nil) || s.closed {
		current.Status = "interrupted"
		if !s.closed && (v.isForeman() || host == "local" || host == "") {
			current.Status = "cancelled"
		}
	} else if runErr != nil {
		current.Status = "failed"
		var exitErr *exec.ExitError
		completedScriptFailure := len(script) > 0 && ctx.Err() == nil && errors.As(runErr, &exitErr) && exitErr.ExitCode() > 0 && exitErr.ExitCode() != 255
		if !v.isForeman() && !completedScriptFailure && s.cfg.Projects[v.ProjectID].Host != "local" && s.cfg.Projects[v.ProjectID].Host != "" {
			current.Status = "interrupted"
		}
		current.Error = runErr.Error()

	} else {
		current.Status = "completed"

	}
	workerAttention := ""
	if t := s.tasks[v.TaskID]; t != nil && (t.Step == v.Step || v.Delivery) && t.Status != "cancelled" && t.Status != "done" {
		originalTask = t
		copyTask := *t
		copyTask.Checks = append([]Check(nil), t.Checks...)
		t = &copyTask
		finishedTask = t
		if v.Delivery && current.Reported && current.Status == "interrupted" {
			t.Status = "interrupted"
			t.Activity = "PR linked. Confirm the remote delivery process has stopped."
			workerAttention = t.Activity
		} else if len(script) > 0 {
			rev, dirty, readErr := s.workspaceState(originalTask)
			if readErr != nil {
				// Preserve cancellation or task changes made while Git reads were unlocked.
				*t = *originalTask
				t.Checks = append([]Check(nil), originalTask.Checks...)
				*current = *originalSession
				current.ProviderID = provider
				if t.Status != "cancelled" && current.Status != "cancelled" {
					t.Status, current.Status = "interrupted", "interrupted"
					t.Activity = "Cannot verify check workspace: " + readErr.Error()
				}
			} else {
				kept := t.Checks[:0]
				for _, c := range t.Checks {
					if c.StepID != t.Steps[v.Step].ID {
						kept = append(kept, c)
					}
				}
				t.Checks = append(kept, Check{StepID: t.Steps[v.Step].ID, Name: t.Steps[v.Step].Name, Passed: runErr == nil, Output: output, Revision: rev})
				if runErr == nil {
					if rev != t.Revision || dirty != "" {
						runErr = errors.New("checks changed the submitted workspace; rebuild and validate")
						t.Checks[len(t.Checks)-1].Passed = false
						t.Status = "failed"
						t.Activity = runErr.Error()
					} else {
						s.advance(t)
					}
				} else {
					t.Status = "failed"
					t.Activity = "Checks failed"
					if current.Status == "interrupted" {
						t.Status = "interrupted"
						t.Activity = "Check process needs attention"
					}
				}
			}
		} else if !current.Reported && runErr != nil {
			t.Status, current.Status = "interrupted", "interrupted"
			t.Activity = "Agent needs attention"
			workerAttention = "Agent exited with an error: " + runErr.Error()
		} else if !current.Reported && t.Status == "active" {
			t.Status, current.Status = "interrupted", "interrupted"
			t.Activity = "Agent finished without a structured report. Review saved work before resuming."
			if v.Delivery {
				t.Activity = "Delivery finished without linking a pull request. Review saved work and GitHub before resuming."
			}
			current.Error = t.Activity
			workerAttention = t.Activity

		}
	}
	message := ""
	if len(script) > 0 && !s.closed && finishedTask != nil && finishedTask.Status != "cancelled" {
		message = "Script result for task " + v.TaskID + ": inspect checks and continue the eligible pipeline."
	}
	if workerAttention != "" && !s.closed {
		message = "Worker task " + v.TaskID + " needs human attention: " + workerAttention + " Ask the human to inspect saved work and explicitly confirm recovery. Do not automatically retry or claim pipeline completion."
	}
	var persistErr error
	if finishedTask != nil && message != "" {
		persistErr = s.commitTransition(originalTask, finishedTask, message, map[*Session]Session{originalSession: *current})
	} else {
		records := []recordWrite{{"session", current.ID, diskSession(current)}}
		if finishedTask != nil {
			records = append(records, recordWrite{"task", finishedTask.ID, diskTask(finishedTask)})
		}
		persistErr = s.commitRecords(records, "", Event{})
		if persistErr == nil {
			*originalSession = *current
			if finishedTask != nil {
				*originalTask = *finishedTask
			}
		}
	}
	if persistErr != nil {
		// A failed result transaction must not advance a pipeline or claim completion.
		recovery := *originalSession
		if provider != "" {
			recovery.ProviderID = provider
		}
		recovery.Status = "interrupted"
		recovery.Error = "Could not persist turn result: " + persistErr.Error() + ". Review saved work before explicitly recovering."
		records := []recordWrite{{"session", recovery.ID, diskSession(&recovery)}}
		var paused *Task
		if originalTask != nil && originalTask.Status != "cancelled" {
			copyTask := *originalTask
			copyTask.Status = "interrupted"
			copyTask.Activity = recovery.Error
			paused = &copyTask
			records = append(records, recordWrite{"task", paused.ID, diskTask(paused)})
		}
		_ = s.commitRecords(records, "", Event{})
		*originalSession = recovery
		if paused != nil {
			*originalTask = *paused
		}
	}
	current = originalSession
	if current.Status == "completed" {
		_ = s.event(v.ID, Event{Kind: "completed", Title: "Turn complete"})
	} else if current.Error != "" {
		_ = s.event(v.ID, Event{Kind: "error", Text: current.Error})
	}
	s.active = ""
	s.cancel = nil
	if len(current.ReportQueue) > 0 && current.Status == "completed" && !s.closed {
		_ = s.enqueue(current, "Inspect the pending worker reports and continue eligible work.", id("reports_"))
	}
	s.signal()
	s.next()
}

type boundedBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n > b.limit-len(b.data) {
		b.truncated = true
	}
	if left := b.limit - len(b.data); left > 0 {
		if len(p) > left {
			p = p[:left]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
func (b *boundedBuffer) String() string {
	out := string(b.data)
	if b.truncated {
		out += "\n[Output truncated]"
	}
	return out
}
func (s *Service) advance(t *Task) {
	t.Step++
	t.Status = "active"
	t.Activity = "Ready for next step"
	t.ApprovalSubject = ""
	if t.Step >= len(t.Steps) {
		t.Stage = "Review"
		t.Activity = "Waiting for verified merge"
		return
	}
	step := t.Steps[t.Step]
	t.Stage = step.Stage
	if step.Type == "approval" {
		t.Status = "awaiting_approval"
		t.ApprovalSubject = step.Subject
		t.Activity = "Needs your approval"
	}
}
func (s *Service) start(t *Task) (*Session, error) {
	if e := s.validateTask(t); e != nil {
		return nil, e
	}
	if s.taskBusy(t.ID) {
		return nil, errors.New("task has a running or interrupted session; confirm stopped and resume before continuing")
	}
	if t.Status == "interrupted" {
		return nil, errors.New("resume the interrupted task session after confirming the previous process stopped")
	}
	if t.Status == "cancelled" || t.Status == "done" {
		return nil, errors.New("task is not active")
	}
	if t.Step >= len(t.Steps) {
		return nil, errors.New("pipeline complete; waiting for merge")
	}
	step := t.Steps[t.Step]
	if step.Type == "approval" {
		return nil, errors.New("human approval is required")
	}
	if t.Status == "failed" && step.Type == "script" {
		return nil, errors.New("checks failed; send the builder corrective feedback before retrying")
	}
	if t.Repairs >= 3 {
		return nil, errors.New("repair limit reached; human attention required")
	}
	if strings.EqualFold(step.Stage, "review") && step.Type == "agent" { // Human review until provider read-only containment is proved.
		copyTask := *t
		copyTask.Review = "Human review required: inspect the submitted diff and checks."
		s.advance(&copyTask)
		if err := s.saveTask(&copyTask); err != nil {
			return nil, err
		}
		*t = copyTask
		s.signal()
		return nil, nil
	}
	var v *Session
	for _, candidate := range s.sessions {
		if candidate.TaskID == t.ID && candidate.Step == t.Step {
			v = candidate
			break
		}
	}
	fresh := v == nil
	if fresh {
		v = &Session{ID: id("s_"), ProjectID: t.ProjectID, TaskID: t.ID, Role: step.Agent, Directory: t.Directory, Step: t.Step, Status: "idle", CreatedAt: now()}
		s.sessions[v.ID] = v
	}
	previous := *v
	v.Delivery = false
	copyTask := *t
	copyTask.Status = "active"
	copyTask.Stage = step.Stage
	copyTask.Activity = "Queued: " + step.Name
	request := id("step_")
	if err := s.acceptConfirmedTurn(v, copyTask.Activity+"\n"+v.Pending, request, "turn:"+v.ID+":"+request, false, &copyTask); err != nil {
		*v = previous
		if fresh {
			delete(s.sessions, v.ID)
		}
		return nil, err
	}
	return v, nil
}
func (s *Service) create(project, title, brief, pipeline string, valid ...func() bool) (*Task, error) {
	return s.createWithRequest(project, title, brief, pipeline, "", valid...)
}

func (s *Service) createWithRequest(project, title, brief, pipeline, requestKey string, valid ...func() bool) (*Task, error) {
	if strings.TrimSpace(title) == "" || strings.TrimSpace(brief) == "" {
		return nil, errors.New("title and brief are required")
	}
	if pipeline == "" {
		pipeline = s.cfg.DefaultPipeline
	}
	definition, ok := s.cfg.Pipelines[pipeline]
	if !ok {
		return nil, errors.New("unknown pipeline")
	}
	count := 0
	for _, t := range s.tasks {
		if t.ProjectID == project && t.Status != "done" && t.Status != "cancelled" {
			count++
		}
	}
	if count >= 4 {
		return nil, errors.New("four active tasks already exist in this project")
	}
	p, ok := s.cfg.Projects[project]
	if !ok {
		return nil, errors.New("unknown project")
	}
	host, ok := s.cfg.Hosts[p.Host]
	if !ok && p.Host != "" {
		return nil, errors.New("project host is no longer configured")
	}
	if s.provisioning {
		return nil, errors.New("repository setup is busy; retry shortly")
	}
	s.provisioning = true
	defer func() { s.provisioning = false }()
	key := id("t_")
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	dir, branch, base, e := workspaceAt(ctx, p, host, key)
	if e == nil {
		var observed string
		observed, e = projectGit(ctx, p, host, dir, "rev-parse", "HEAD")
		if e != nil {
			e = fmt.Errorf("record task base revision: %w", e)
		} else if observed != base {
			e = errors.New("record task base revision: workspace differs from the pinned source revision")
		}
	}
	cancel()
	s.mu.Lock()
	abort := func(cause error) (*Task, error) {
		if dir != "" && branch != "" {
			s.mu.Unlock()
			cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			cleanupErr := cleanupWorkspace(cleanupContext, p, host, dir, branch, base)
			cleanupCancel()
			s.mu.Lock()
			if cleanupErr != nil {
				cause = fmt.Errorf("%w; workspace retained for inspection at %s: %v", cause, dir, cleanupErr)
			}
		}
		return nil, cause
	}
	if e != nil {
		return abort(e)
	}
	if s.closed || !reflect.DeepEqual(s.cfg.Projects[project], p) || !reflect.DeepEqual(s.cfg.Hosts[p.Host], host) {
		return abort(errors.New("factory or project changed while preparing the workspace; retry"))
	}
	for _, check := range valid {
		if !check() {
			return abort(errors.New("conversation changed while preparing the workspace; retry from the current turn"))
		}
	}
	t := &Task{ProjectSnapshot: p, HostSnapshot: host, ID: key, ProjectID: project, Title: title, Brief: brief, Pipeline: pipeline, Stage: "Design", Status: "active", Activity: "Ready for planning", Directory: dir, Branch: branch, BaseRevision: base, Version: 1, CreatedAt: now(), Checks: []Check{}, Agents: map[string]config.ResolvedAgent{}, Steps: append([]config.FactoryStep(nil), definition.Steps...)}
	for i := range t.Steps {
		t.Steps[i].Command = append([]string(nil), t.Steps[i].Command...)
	}
	for key, a := range s.cfg.Agents {
		t.Agents[key] = a
	}
	writes := []recordWrite{{"task", t.ID, diskTask(t)}}
	if requestKey != "" {
		writes = append(writes, recordWrite{"request", requestKey, t.ID})
	}
	if e = s.commitRecords(writes, "", Event{}); e != nil {
		return abort(e)
	}
	s.tasks[t.ID] = t
	if requestKey != "" {
		s.requests[requestKey] = t.ID
	}
	return t, nil
}

func (s *Service) validateTask(t *Task) error {
	p, ok := s.cfg.Projects[t.ProjectID]
	if !ok || !reflect.DeepEqual(p, t.ProjectSnapshot) || !reflect.DeepEqual(s.cfg.Hosts[p.Host], t.HostSnapshot) {
		return errors.New("project or execution host configuration changed; restore this task's original configuration before continuing")
	}
	return nil
}

// Display titles never authorize provider tool calls.
func safeFactoryPermission(p agent.Permission) bool { return safeFactoryTool(p.Name) }

func safeFactoryTool(name string) bool {
	switch name {
	case "mcp__machinist__link_pr", "mcp__machinist__inspect_tasks", "mcp__machinist__create_task", "mcp__machinist__start_step", "mcp__machinist__send_message", "mcp__machinist__cancel_task", "mcp__machinist__report":
		return true
	}
	return false
}

func reverseForward(rawURL string, toolsPort int) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("127.0.0.1:%d:%s", toolsPort, net.JoinHostPort(host, port)), nil
}

// Caller holds mu. Repository reads can wait on SSH, so keep state operations responsive.
func (s *Service) workspaceState(t *Task) (string, string, error) {
	return s.readWorkspaceState(t, false)
}
func (s *Service) designWorkspaceState(t *Task) (string, string, error) {
	return s.readWorkspaceState(t, true)
}
func (s *Service) readWorkspaceState(t *Task, includeIgnored bool) (string, string, error) {
	project, directory := t.ProjectID, t.Directory
	version, step, status, revision, approved, active := t.Version, t.Step, t.Status, t.Revision, t.CodeApproved, s.active
	owner := s.sessions[active]
	ownerStatus, ownerRequest := "", ""
	if owner != nil {
		ownerStatus, ownerRequest = owner.Status, owner.RequestID
	}
	s.mu.Unlock()
	rev, err := s.git(project, directory, "rev-parse", "HEAD")
	var dirty string
	if err == nil {
		args := []string{"status", "--porcelain"}
		if includeIgnored {
			args = append(args, "--ignored")
		}
		dirty, err = s.git(project, directory, args...)
	}
	s.mu.Lock()
	if s.closed || s.tasks[t.ID] != t || t.ProjectID != project || t.Directory != directory || t.Version != version || t.Step != step || t.Status != status || t.Revision != revision || t.CodeApproved != approved || s.active != active || s.sessions[active] != owner || (owner != nil && (owner.Status != ownerStatus || owner.RequestID != ownerRequest)) {
		return "", "", errors.New("task changed while reading workspace; reload before continuing")
	}
	return rev, dirty, err
}

// Stage instructions preserve the human design gate and limit commits to builds.
func stageInstructions(stage string) string {
	switch {
	case strings.EqualFold(stage, "design"):
		return "Planning only: inspect the repository and return the proposed design in your report. Do not change workspace files or create commits. Wait for human design approval before implementation."
	case strings.EqualFold(stage, "build"):
		return "Implement the human-approved design. Commit implementation before reporting."
	default:
		return "Inspect and report this stage's results. Do not change workspace files or create commits."
	}
}
