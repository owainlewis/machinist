package factory

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type taskSummary struct {
	ID                 string `json:"id"`
	ProjectID          string `json:"project_id"`
	Title              string `json:"title"`
	Brief              string `json:"brief"`
	Stage              string `json:"stage"`
	Status             string `json:"status"`
	Activity           string `json:"activity"`
	ApprovalSubject    string `json:"approval_subject,omitempty"`
	PendingPermissions int    `json:"pending_permissions"`
	Version            int    `json:"version"`
	CreatedAt          string `json:"created_at"`
	Step               int    `json:"step"`
	Revision           string `json:"revision,omitempty"`
	PRURL              string `json:"pr_url,omitempty"`
	GitHubError        string `json:"github_error,omitempty"`
	CheckState         string `json:"check_state"`
}

func summaryText(value string) string {
	if len(value) > 512 {
		return strings.ToValidUTF8(value[:512], "") + "..."
	}
	return value
}
func (s *Service) summarize(t *Task) taskSummary {
	pending := 0
	for _, p := range s.permissions {
		if session := s.sessions[p.SessionID]; p.Status == "pending" && session != nil && session.TaskID == t.ID {
			pending++
		}
	}
	checks := sha256.New()
	fmt.Fprintf(checks, "%s:%t", t.GitHubHead, t.GitHubChecksPass)
	for _, check := range t.Checks {
		fmt.Fprintf(checks, "\n%q:%q:%t:%q", summaryText(check.StepID), summaryText(check.Name), check.Passed, summaryText(check.Revision))
	}
	return taskSummary{ID: t.ID, ProjectID: t.ProjectID, Title: summaryText(t.Title), Brief: summaryText(t.Brief), Stage: t.Stage, Status: t.Status, Activity: summaryText(t.Activity), ApprovalSubject: t.ApprovalSubject, PendingPermissions: pending, Version: t.Version, CreatedAt: t.CreatedAt, Step: t.Step, Revision: summaryText(t.Revision), PRURL: summaryText(t.PRURL), GitHubError: summaryText(t.GitHubError), CheckState: fmt.Sprintf("%x", checks.Sum(nil))}
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serve) }
func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/factory/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) > 1 && parts[0] == "tools" {
		s.tool(w, r, parts[1])
		return
	}
	if len(parts) == 3 && parts[0] == "sessions" && parts[2] == "events" {
		s.stream(w, r, parts[1])
		return
	}
	if path == "configuration" {
		s.configuration(w, r)
		return
	}
	if path == "folders" && r.Method == "GET" {
		s.folders(w, r)
		return
	}
	if path == "projects" && r.Method == "POST" {
		s.addProject(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if path == "status" && r.Method == "GET" {
		projects := []map[string]any{}
		for key, p := range s.cfg.Projects {
			projects = append(projects, map[string]any{"id": key, "name": p.Name, "github": p.GitHub, "host": p.Host, "path": p.Path, "source": projectSource(p)})
		}
		sort.Slice(projects, func(i, j int) bool { return projects[i]["id"].(string) < projects[j]["id"].(string) })
		hosts := []map[string]any{}
		for key, h := range s.cfg.Hosts {
			hosts = append(hosts, map[string]any{"id": key, "name": h.Name})
		}
		sort.Slice(hosts, func(i, j int) bool { return hosts[i]["id"].(string) < hosts[j]["id"].(string) })
		agents := []map[string]any{}
		for key, a := range s.cfg.Agents {
			agents = append(agents, map[string]any{"id": key, "name": a.Name, "description": a.Description, "runtime": a.Runtime, "model": a.Model})
		}
		pipelines := []map[string]any{}
		for key, p := range s.cfg.Pipelines {
			steps := []map[string]any{}
			for _, v := range p.Steps {
				steps = append(steps, map[string]any{"id": v.ID, "name": v.Name, "stage": v.Stage, "type": v.Type, "agent": v.Agent, "subject": v.Subject})
			}
			pipelines = append(pipelines, map[string]any{"id": key, "name": p.Name, "steps": steps})
		}
		tasks := []taskSummary{}
		for _, t := range s.tasks {
			tasks = append(tasks, s.summarize(t))
		}
		profile := s.cfg.Agents[s.cfg.Foreman]
		name := profile.Name
		if name == "" {
			name = s.cfg.Foreman
		}
		runtime := profile.Runtime
		if runtime == "" {
			runtime = "claude"
		}
		foreman := map[string]any{"id": s.cfg.Foreman, "name": name, "runtime": runtime, "model": profile.Model}
		jsonReply(w, 200, map[string]any{"enabled": s.cfg.Enabled, "foreman": foreman, "projects": projects, "hosts": hosts, "agents": agents, "pipelines": pipelines, "tasks": tasks, "csrf_token": s.csrf})
		return
	}
	if !s.cfg.Enabled {
		jsonReply(w, 409, map[string]string{"error": "Factory is not configured"})
		return
	}
	if len(parts) >= 2 && parts[0] == "projects" {
		project := parts[1]
		if _, ok := s.cfg.Projects[project]; !ok {
			http.NotFound(w, r)
			return
		}
		v, e := s.foreman(project)
		if e != nil {
			fail(w, e)
			return
		}
		if len(parts) == 2 && r.Method == "GET" {
			tasks := []taskSummary{}
			for _, t := range s.tasks {
				if t.ProjectID == project {
					tasks = append(tasks, s.summarize(t))
				}
			}
			jsonReply(w, 200, map[string]any{"session": v, "tasks": tasks})
			return
		}
		if len(parts) == 3 && parts[2] == "messages" && r.Method == "POST" {
			var in struct {
				RequestID string `json:"request_id"`
				Message   string `json:"message"`
				TaskID    string `json:"task_id"`
			}
			if !decode(w, r, &in) {
				return
			}
			if in.RequestID == "" || strings.TrimSpace(in.Message) == "" {
				fail(w, errors.New("request_id and message are required"))
				return
			}
			key := "message:" + project + ":" + in.RequestID
			if old := s.requests[key]; old != "" {
				jsonReply(w, 200, map[string]any{"session": s.sessions[old]})
				return
			}
			if in.TaskID != "" {
				t := s.tasks[in.TaskID]
				if t == nil || t.ProjectID != project {
					fail(w, errors.New("task does not belong to this project"))
					return
				}
				in.Message = "Regarding task " + t.ID + ": " + in.Message
			}
			if e = s.acceptTurn(v, in.Message, in.RequestID, key); e != nil {
				fail(w, e)
				return
			}

			jsonReply(w, 202, map[string]any{"session": v})
			return
		}
	}
	if len(parts) >= 2 && parts[0] == "sessions" {
		v := s.sessions[parts[1]]
		if v == nil {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 2 && r.Method == "GET" {
			cursor, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
			events, e := s.events(v.ID, cursor)
			if e != nil {
				fail(w, e)
				return
			}
			permissions := []*Permission{}
			for _, p := range s.permissions {
				if p.SessionID == v.ID && p.Status == "pending" {
					permissions = append(permissions, p)
				}
			}
			jsonReply(w, 200, map[string]any{"session": v, "events": events, "permissions": permissions})
			return
		}
		if len(parts) == 3 && r.Method == "POST" {
			switch parts[2] {
			case "cancel":
				copySession := *v
				copySession.Status = "cancelled"
				host := s.cfg.Projects[v.ProjectID].Host
				if s.active == v.ID && !v.isForeman() && host != "" && host != "local" {
					copySession.Status = "interrupted"
					copySession.Error = "Confirm the previous remote process has stopped before resuming."
				}
				if err := s.saveSession(&copySession); err != nil {
					fail(w, err)
					return
				}
				*v = copySession
				if s.active == v.ID && s.cancel != nil {
					s.cancel()
				}
				s.signal()
				jsonReply(w, 200, map[string]any{"session": v})
				return
			case "resume":
				var in struct {
					ConfirmedStopped bool `json:"confirmed_stopped"`
				}
				if !decode(w, r, &in) {
					return
				}
				if !in.ConfirmedStopped {
					fail(w, errors.New("confirm the previous agent process has stopped before resuming"))
					return
				}
				if v.Status != "interrupted" && v.Status != "failed" {
					fail(w, errors.New("only failed or interrupted turns can resume"))
					return
				}
				if s.active == v.ID {
					fail(w, errors.New("old process has not stopped"))
					return
				}
				if task := s.tasks[v.TaskID]; task != nil {
					if task.Status == "cancelled" || task.Status == "done" {
						fail(w, errors.New("task is no longer active"))
						return
					}
					if e := s.validateTask(task); e != nil {
						fail(w, e)
						return
					}
					if task.Repairs >= 3 {
						copySession := *v
						copySession.Status = "completed"
						copySession.Error = ""
						copyTask := *task
						copyTask.Status = "failed"
						copyTask.Activity = "Repair limit reached. Review the task before continuing."
						if err := s.commitRecords([]recordWrite{{"task", task.ID, diskTask(&copyTask)}, {"session", v.ID, diskSession(&copySession)}}, "", Event{}); err != nil {
							fail(w, err)
							return
						}
						*task = copyTask
						*v = copySession
						s.signal()
						jsonReply(w, 200, map[string]any{"session": v})
						return
					}
				}
				if task := s.tasks[v.TaskID]; task != nil && task.Step != v.Step && !v.Delivery {
					copySession := *v
					copySession.Status = "completed"
					copySession.Error = ""
					copyTask := *task
					copyTask.Status = "active"
					copyTask.Activity = "Previous reported turn confirmed stopped"
					if copyTask.Step < len(copyTask.Steps) && copyTask.Steps[copyTask.Step].Type == "approval" {
						copyTask.Status = "awaiting_approval"
						copyTask.ApprovalSubject = copyTask.Steps[copyTask.Step].Subject
						copyTask.Activity = "Needs your approval"
					}
					if err := s.commitTransition(task, &copyTask, "Previous reported task turn confirmed stopped. Inspect task "+task.ID+" and continue its eligible pipeline.", map[*Session]Session{v: copySession}); err != nil {
						fail(w, err)
						return
					}
					jsonReply(w, 200, map[string]any{"session": v})
					return
				}
				var copyTask *Task
				if task := s.tasks[v.TaskID]; task != nil && task.Status == "interrupted" {
					candidate := *task
					candidate.Status = "active"
					candidate.Activity = "Resuming"
					copyTask = &candidate
				}
				request := id("resume_")
				if err := s.acceptConfirmedTurn(v, "Resume the interrupted instruction. Inspect saved files before repeating work.\n"+v.Pending, request, "turn:"+v.ID+":"+request, true, copyTask); err != nil {
					fail(w, err)
					return
				}
				jsonReply(w, 202, map[string]any{"session": v})
				return
			}
		}
	}
	if len(parts) == 2 && parts[0] == "permissions" && r.Method == "POST" {
		p := s.permissions[parts[1]]
		if p == nil || p.Status != "pending" {
			fail(w, errors.New("permission is no longer pending"))
			return
		}
		var in struct {
			Allow bool `json:"allow"`
		}
		if !decode(w, r, &in) {
			return
		}
		p.Status = "resolved"
		p.answer <- in.Allow
		s.signal()
		jsonReply(w, 200, p)
		return
	}
	if len(parts) >= 2 && parts[0] == "tasks" {
		t := s.tasks[parts[1]]
		if t == nil {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 2 && r.Method == "GET" {
			sessions := []*Session{}
			for _, v := range s.sessions {
				if v.TaskID == t.ID {
					sessions = append(sessions, v)
				}
			}
			project, directory, base := t.ProjectID, t.Directory, t.BaseRevision
			version, status := t.Version, t.Status
			s.mu.Unlock()
			diff, diffTruncated, fileNames, filesTruncated, reviewErr := s.reviewChanges(project, directory, base)
			s.mu.Lock()
			if s.closed || s.tasks[t.ID] != t || t.Version != version || t.Status != status || t.BaseRevision != base || t.Directory != directory {
				fail(w, errors.New("task changed while reading changes; reload before continuing"))
				return
			}
			if reviewErr != nil {
				fail(w, reviewErr)
				return
			}
			jsonReply(w, 200, map[string]any{"task": t, "sessions": sessions, "diff": diff, "diff_truncated": diffTruncated, "files": fileNames, "files_truncated": filesTruncated, "checks": t.Checks})
			return
		}
		if len(parts) == 3 && r.Method == "POST" {
			switch parts[2] {
			case "approve":
				var in struct {
					Version int    `json:"version"`
					Subject string `json:"subject"`
				}
				if !decode(w, r, &in) {
					return
				}
				if e := s.approve(t, in.Version, in.Subject); e != nil {
					fail(w, e)
					return
				}
				jsonReply(w, 200, map[string]any{"task": t})
				return
			case "changes":
				var in struct {
					Version int    `json:"version"`
					Message string `json:"message"`
				}
				if !decode(w, r, &in) {
					return
				}
				if e := s.changes(t, in.Version, in.Message); e != nil {
					fail(w, e)
					return
				}
				jsonReply(w, 200, map[string]any{"task": t})
				return
			case "continue":
				var in struct {
					Version int `json:"version"`
				}
				if !decode(w, r, &in) {
					return
				}
				if in.Version != t.Version || t.Repairs < 3 || s.taskBusy(t.ID) || t.Status == "cancelled" || t.Status == "done" {
					fail(w, errors.New("task must be paused at the repair limit with its current version and stopped ownership"))
					return
				}
				copyTask := *t
				copyTask.Repairs = 0
				copyTask.Version++
				copyTask.Status = "active"
				copyTask.Activity = "Human authorized another repair pass"
				if err := s.commitTransition(t, &copyTask, "Human reviewed task "+t.ID+" and authorized another repair pass. Inspect and start the eligible step.", nil); err != nil {
					fail(w, err)
					return
				}
				jsonReply(w, 200, map[string]any{"task": t})
				return
			case "refresh":
				if e := s.refresh(t); e != nil {
					if !errors.Is(e, errStalePR) {
						t.GitHubError = e.Error()
						_ = s.saveTask(t)
					}
					fail(w, e)
					return
				}
				jsonReply(w, 200, map[string]any{"task": t})
				return
			case "cancel":
				if err := s.cancelTask(t); err != nil {
					fail(w, err)
					return
				}
				jsonReply(w, 200, map[string]any{"task": t})
				return
			}
		}
	}
	http.NotFound(w, r)
}
func (s *Service) stream(w http.ResponseWriter, r *http.Request, key string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unavailable", 500)
		return
	}
	s.mu.Lock()
	_, ok = s.sessions[key]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	cursor, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if q := r.URL.Query().Get("cursor"); q != "" && r.Header.Get("Last-Event-ID") == "" {
		cursor, _ = strconv.ParseInt(q, 10, 64)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		events, e := s.events(key, cursor)
		changed := s.changed
		s.mu.Unlock()
		if e != nil {
			return
		}
		for _, event := range events {
			cursor = event.ID
			fmt.Fprintf(w, "id: %d\nevent: changed\ndata: {}\n\n", cursor)
		}
		flusher.Flush()
		if len(events) == 100 {
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-changed:
			fmt.Fprint(w, "event: changed\ndata: {}\n\n")
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}
