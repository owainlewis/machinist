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
	p := s.cfg.Projects[project]
	if p.Host != "" && p.Host != "local" {
		h := s.cfg.Hosts[p.Host]
		return exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", h.SSH, "cd "+shellQuote(dir)+" && "+shellArgs(args))
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	return cmd
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
		if safeFactoryTool(p.Title) {
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
	var script []string
	timeout := profile.Timeout
	if t := s.tasks[v.TaskID]; t != nil {
		step := t.Steps[v.Step]
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
		prompt = fmt.Sprintf("Task %s: %s\nBrief: %s\nDesign: %s\nFeedback: %s\nCurrent step: %s (%s)\nUse the connected machinist MCP server, not a terminal CLI. When complete call mcp__machinist__report with task_id=%s, a unique report_id, summary, outcome=complete and design text for Design. Commit implementation before reporting. Do not alter Machinist configuration or spawn other agents yourself. Never merge or publish without human authority.", t.ID, t.Title, t.Brief, t.Design, t.Activity+"\n"+v.Pending, step.Name, step.Stage, t.ID)
		if v.Delivery {
			prompt = "Human approved delivery of commit " + t.CodeApproved + " for task " + t.ID + ". First inspect whether this branch already has a pull request to avoid duplicate publication. Publish only this approved branch as a pull request to " + s.cfg.Projects[t.ProjectID].GitHub + ". Do not edit code or merge. Call mcp__machinist__link_pr with task_id=" + t.ID + " and pr_url once published. Feedback: " + v.Pending
		}
	} else {
		system += "\nYou are the only user-facing foreman. Use the connected machinist MCP server: mcp__machinist__inspect_tasks reads work, mcp__machinist__create_task creates a task, mcp__machinist__start_step runs its eligible step, mcp__machinist__send_message routes feedback to its worker, and mcp__machinist__cancel_task stops queued work. These are connected tools, not terminal CLI commands. Do not search for, install, or configure another Machinist instance. Delegate code changes. After human code delivery approval, use send_message to the existing builder to publish only the approved revision and link its PR. Never grant approval, merge, or bypass a pipeline gate. When a worker reports, inspect its task and start the next eligible step unless a human decision is needed."
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
		provider, runErr = runner(ctx, RunRequest{Host: func() string {
			if v.isForeman() {
				return "local"
			}
			return s.cfg.Projects[v.ProjectID].Host
		}(), Directory: v.Directory, SessionID: v.ProviderID, Prompt: prompt, SystemPrompt: system, Model: profile.Model, Token: token, URL: url, Executable: s.executable}, emit, permission)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
	current := s.sessions[v.ID]
	if current == nil {
		return
	}
	current.ProviderID = provider
	if current.Status == "cancelled" || s.closed {
		current.Status = "interrupted"
		if !s.closed {
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
		_ = s.event(v.ID, Event{Kind: "error", Text: runErr.Error()})
	} else {
		current.Status = "completed"
		_ = s.event(v.ID, Event{Kind: "completed", Title: "Turn complete"})
	}
	if t := s.tasks[v.TaskID]; t != nil && t.Step == v.Step && t.Status != "cancelled" {
		if len(script) > 0 {
			rev, dirty, readErr := s.workspaceState(t)
			if readErr != nil {
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
		} else if runErr != nil {
			t.Status = "interrupted"
			t.Activity = "Agent needs attention"
		} else if t.Status == "active" && !strings.HasPrefix(t.Activity, "Blocked:") {
			t.Activity = "Waiting for structured report"
		}
		_ = s.saveTask(t)
	}
	_ = s.saveSession(current)
	s.active = ""
	s.cancel = nil
	if len(current.ReportQueue) > 0 && !s.closed {
		messages := strings.Join(current.ReportQueue, "\n")
		current.ReportQueue = nil
		_ = s.enqueue(current, messages, id("reports_"))
	}
	if len(script) > 0 && !s.closed {
		s.notify(v.ProjectID, "Script result for task "+v.TaskID+": inspect checks and continue the eligible pipeline.")
	}
	s.signal()
	s.next()
}

type boundedBuffer struct {
	data  []byte
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
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
	if len(b.data) >= b.limit {
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
		t.Review = "Human review required: inspect the submitted diff and checks."
		s.advance(t)
		if e := s.saveTask(t); e != nil {
			return nil, e
		}
		s.signal()
		return nil, nil
	}
	for _, v := range s.sessions {
		if v.TaskID == t.ID && v.Step == t.Step && (v.Status == "running" || v.Status == "queued" || v.Status == "awaiting_permission" || v.Status == "interrupted") {
			return nil, errors.New("step is running or interrupted; confirm stopped and resume")
		}
	}
	var v *Session
	for _, candidate := range s.sessions {
		if candidate.TaskID == t.ID && candidate.Step == t.Step {
			v = candidate
			break
		}
	}
	if v == nil {
		v = &Session{ID: id("s_"), ProjectID: t.ProjectID, TaskID: t.ID, Role: step.Agent, Directory: t.Directory, Step: t.Step, Status: "idle", CreatedAt: now()}
		s.sessions[v.ID] = v
	}
	v.Delivery = false
	t.Status = "active"
	t.Stage = step.Stage
	t.Activity = "Queued: " + step.Name
	if e := s.saveTask(t); e != nil {
		return nil, e
	}
	if e := s.enqueue(v, t.Activity+"\n"+v.Pending, id("step_")); e != nil {
		return nil, e
	}
	return v, nil
}
func (s *Service) create(project, title, brief, pipeline string) (*Task, error) {
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
	key := id("t_")
	dir, branch, e := s.workspace(project, key)
	if e != nil {
		return nil, e
	}
	t := &Task{ProjectSnapshot: s.cfg.Projects[project], HostSnapshot: s.cfg.Hosts[s.cfg.Projects[project].Host], ID: key, ProjectID: project, Title: title, Brief: brief, Pipeline: pipeline, Stage: "Design", Status: "active", Activity: "Ready for planning", Directory: dir, Branch: branch, Version: 1, CreatedAt: now(), Checks: []Check{}, Agents: map[string]config.ResolvedAgent{}, Steps: append([]config.FactoryStep(nil), definition.Steps...)}
	t.BaseRevision, e = s.git(project, dir, "rev-parse", "HEAD")
	if e != nil {
		return nil, fmt.Errorf("record task base revision: %w", e)
	}
	if t.BaseRevision == "" {
		return nil, errors.New("record task base revision: Git returned an empty revision")
	}
	for i := range t.Steps {
		t.Steps[i].Command = append([]string(nil), t.Steps[i].Command...)
	}
	for key, a := range s.cfg.Agents {
		t.Agents[key] = a
	}
	if e = s.saveTask(t); e != nil {
		return nil, e
	}
	s.tasks[t.ID] = t
	return t, nil
}

func (s *Service) validateTask(t *Task) error {
	p, ok := s.cfg.Projects[t.ProjectID]
	if !ok || !reflect.DeepEqual(p, t.ProjectSnapshot) || !reflect.DeepEqual(s.cfg.Hosts[p.Host], t.HostSnapshot) {
		return errors.New("project or execution host configuration changed; restore this task's original configuration before continuing")
	}
	return nil
}

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
		dirty, err = s.git(project, directory, "status", "--porcelain")
	}
	s.mu.Lock()
	if s.closed || s.tasks[t.ID] != t || t.ProjectID != project || t.Directory != directory || t.Version != version || t.Step != step || t.Status != status || t.Revision != revision || t.CodeApproved != approved || s.active != active || s.sessions[active] != owner || (owner != nil && (owner.Status != ownerStatus || owner.RequestID != ownerRequest)) {
		return "", "", errors.New("task changed while reading workspace; reload before continuing")
	}
	return rev, dirty, err
}
