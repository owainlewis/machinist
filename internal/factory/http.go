package factory

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

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
	s.mu.Lock()
	defer s.mu.Unlock()
	if path == "status" && r.Method == "GET" {
		projects := []map[string]any{}
		for key, p := range s.cfg.Projects {
			projects = append(projects, map[string]any{"id": key, "name": p.Name, "github": p.GitHub, "host": p.Host})
		}
		sort.Slice(projects, func(i, j int) bool { return projects[i]["id"].(string) < projects[j]["id"].(string) })
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
		tasks := []*Task{}
		for _, t := range s.tasks {
			tasks = append(tasks, t)
		}
		jsonReply(w, 200, map[string]any{"enabled": s.cfg.Enabled, "projects": projects, "agents": agents, "pipelines": pipelines, "tasks": tasks, "csrf_token": s.csrf})
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
			tasks := []*Task{}
			for _, t := range s.tasks {
				if t.ProjectID == project {
					tasks = append(tasks, t)
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
				if s.active == v.ID && s.cancel != nil {
					s.cancel()
				}
				v.Status = "cancelled"
				_ = s.saveSession(v)
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
						v.Status = "completed"
						v.Error = ""
						if e := s.saveSession(v); e != nil {
							fail(w, e)
							return
						}
						task.Status = "failed"
						task.Activity = "Repair limit reached. Review the task before continuing."
						if e := s.saveTask(task); e != nil {
							fail(w, e)
							return
						}
						s.signal()
						jsonReply(w, 200, map[string]any{"session": v})
						return
					}
				}
				if task := s.tasks[v.TaskID]; task != nil && task.Step != v.Step && !v.Delivery {
					v.Status = "completed"
					v.Error = ""
					_ = s.saveSession(v)
					task.Status = "active"
					task.Activity = "Previous reported turn confirmed stopped"
					if task.Step < len(task.Steps) && task.Steps[task.Step].Type == "approval" {
						task.Status = "awaiting_approval"
						task.ApprovalSubject = task.Steps[task.Step].Subject
						task.Activity = "Needs your approval"
					}
					_ = s.saveTask(task)
					s.notify(task.ProjectID, "Previous reported task turn confirmed stopped. Inspect task "+task.ID+" and continue its eligible pipeline.")
					s.signal()
					jsonReply(w, 200, map[string]any{"session": v})
					return
				}
				v.Status = "idle"
				if task := s.tasks[v.TaskID]; task != nil && task.Status == "interrupted" {
					task.Status = "active"
					task.Activity = "Resuming"
					_ = s.saveTask(task)
				}
				if e := s.enqueue(v, "Resume the interrupted instruction. Inspect saved files before repeating work.\n"+v.Pending, id("resume_")); e != nil {
					fail(w, e)
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
			diff, diffErr := s.git(project, directory, "diff", base)
			files, filesErr := s.git(project, directory, "diff", "--name-only", base)
			s.mu.Lock()
			if s.closed || s.tasks[t.ID] != t || t.Version != version || t.Status != status || t.BaseRevision != base || t.Directory != directory {
				fail(w, errors.New("task changed while reading changes; reload before continuing"))
				return
			}
			if diffErr != nil {
				fail(w, diffErr)
				return
			}
			if filesErr != nil {
				fail(w, filesErr)
				return
			}

			jsonReply(w, 200, map[string]any{"task": t, "sessions": sessions, "diff": diff, "files": strings.Fields(files), "checks": t.Checks})
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
				t.Repairs = 0
				t.Version++
				t.Status = "active"
				t.Activity = "Human authorized another repair pass"
				if e := s.saveTask(t); e != nil {
					fail(w, e)
					return
				}
				s.notify(t.ProjectID, "Human reviewed task "+t.ID+" and authorized another repair pass. Inspect and start the eligible step.")
				s.signal()
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
				t.Status = "cancelled"
				t.Activity = "Cancelled"
				for _, v := range s.sessions {
					if v.TaskID == t.ID && (v.Status == "running" || v.Status == "queued" || v.Status == "awaiting_permission") {
						v.Status = "cancelled"
						_ = s.saveSession(v)
						if s.active == v.ID && s.cancel != nil {
							s.cancel()
						}
					}
				}
				_ = s.saveTask(t)
				s.signal()
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
	if q := r.URL.Query().Get("cursor"); q != "" {
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
