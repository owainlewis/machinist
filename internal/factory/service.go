package factory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/owainlewis/machinist/internal/config"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Service struct {
	workers               sync.WaitGroup
	mu                    sync.Mutex
	commandMu             sync.RWMutex
	provisioning          bool
	db                    *sql.DB
	cfg                   config.ResolvedFactory
	executable, url, csrf string
	tasks                 map[string]*Task
	sessions              map[string]*Session
	tokens                map[string]string
	permissions           map[string]*Permission
	requests              map[string]string
	queue                 []string
	active                string
	cancel                context.CancelFunc
	closed                bool
	runner                RunFunc
	changed               chan struct{}
}

func id(prefix string) string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return prefix + hex.EncodeToString(b)
}
func New(db *sql.DB, cfg config.ResolvedFactory, executable string) (*Service, error) {
	s := &Service{db: db, cfg: cfg, executable: executable, tasks: map[string]*Task{}, sessions: map[string]*Session{}, tokens: map[string]string{}, permissions: map[string]*Permission{}, requests: map[string]string{}, changed: make(chan struct{})}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS factory_records (kind TEXT NOT NULL,id TEXT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(kind,id)); CREATE TABLE IF NOT EXISTS factory_events (id INTEGER PRIMARY KEY AUTOINCREMENT,session_id TEXT NOT NULL,data TEXT NOT NULL); CREATE INDEX IF NOT EXISTS factory_events_session ON factory_events(session_id,id);`); err != nil {
		return nil, err
	}
	if s.cfg.Hosts == nil {
		s.cfg.Hosts = map[string]config.FactoryHost{"local": {Name: "This computer"}}
	}
	projects := make(map[string]config.FactoryProject, len(cfg.Projects))
	for key, p := range cfg.Projects {
		projects[key] = p
	}
	s.cfg.Projects = projects
	rows, err := db.Query(`SELECT kind,id,data FROM factory_records`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, key, raw string
		if err = rows.Scan(&kind, &key, &raw); err != nil {
			return nil, err
		}
		switch kind {
		case "project":
			var p config.FactoryProject
			if err = json.Unmarshal([]byte(raw), &p); err != nil {
				return nil, err
			}
			if _, configured := s.cfg.Projects[key]; !configured {
				s.cfg.Projects[key] = p
			}
		case "task":
			var r taskRecord
			if err = json.Unmarshal([]byte(raw), &r); err != nil {
				return nil, err
			}
			t := r.Task
			t.Directory = r.Directory
			t.Branch = r.Branch
			t.Steps = r.Steps
			t.Agents = r.Agents
			t.CodeApproved = r.CodeApproved
			t.BaseRevision = r.BaseRevision
			t.ProjectSnapshot = r.ProjectSnapshot
			t.HostSnapshot = r.HostSnapshot
			t.DesignApprovalVersion = r.DesignApprovalVersion
			t.CodeApprovalVersion = r.CodeApprovalVersion
			t.GitHubHead = r.GitHubHead
			t.GitHubChecksPass = r.GitHubChecksPass
			s.tasks[key] = &t
		case "session":
			var r sessionRecord
			if err = json.Unmarshal([]byte(raw), &r); err != nil {
				return nil, err
			}
			v := r.Session
			v.ProviderID = r.ProviderID
			v.Directory = r.Directory
			v.Pending = r.Pending
			v.RequestID = r.RequestID
			v.Step = r.Step
			v.ReportQueue = r.ReportQueue
			s.sessions[key] = &v
		case "request":
			var value string
			if err = json.Unmarshal([]byte(raw), &value); err != nil {
				return nil, err
			}
			s.requests[key] = value
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for _, v := range s.sessions {
		if v.Status == "running" || v.Status == "queued" || v.Status == "awaiting_permission" {
			v.Status = "interrupted"
			v.Error = "Server restarted. Review saved work and resume explicitly."
			if err = s.saveSession(v); err != nil {
				return nil, err
			}
			if t := s.tasks[v.TaskID]; t != nil {
				t.Status = "interrupted"
				t.Activity = "Interrupted"
				if err = s.saveTask(t); err != nil {
					return nil, err
				}
			}
		}
	}
	s.runner = s.runClaude
	return s, nil
}
func (s *Service) SetURL(v string)       { s.mu.Lock(); defer s.mu.Unlock(); s.url = v }
func (s *Service) SetCSRFToken(v string) { s.mu.Lock(); defer s.mu.Unlock(); s.csrf = v }

// SetRunner supports deterministic protocol tests without starting a provider.
func (s *Service) SetRunner(v RunFunc) { s.mu.Lock(); defer s.mu.Unlock(); s.runner = v }
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	for _, v := range s.sessions {
		if v.Status == "running" || v.Status == "queued" || v.Status == "awaiting_permission" {
			v.Status = "interrupted"
			_ = s.saveSession(v)
			if t := s.tasks[v.TaskID]; t != nil && t.Status != "cancelled" {
				t.Status = "interrupted"
				t.Activity = "Interrupted"
				_ = s.saveTask(t)
			}
		}
	}
	s.signal()
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

func (s *Service) signal() { close(s.changed); s.changed = make(chan struct{}) }
func (s *Service) save(kind, key string, value any) error {
	b, e := json.Marshal(value)
	if e != nil {
		return e
	}
	_, e = s.db.Exec(`INSERT INTO factory_records(kind,id,data) VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET data=excluded.data`, kind, key, string(b))
	return e
}
func diskTask(t *Task) taskRecord {
	return taskRecord{ProjectSnapshot: t.ProjectSnapshot, HostSnapshot: t.HostSnapshot, DesignApprovalVersion: t.DesignApprovalVersion, CodeApprovalVersion: t.CodeApprovalVersion, GitHubHead: t.GitHubHead, GitHubChecksPass: t.GitHubChecksPass, BaseRevision: t.BaseRevision, Task: *t, Directory: t.Directory, Branch: t.Branch, Steps: t.Steps, Agents: t.Agents, CodeApproved: t.CodeApproved}
}
func (s *Service) saveTask(t *Task) error { return s.save("task", t.ID, diskTask(t)) }
func diskSession(v *Session) sessionRecord {
	return sessionRecord{ReportQueue: v.ReportQueue, Session: *v, ProviderID: v.ProviderID, Directory: v.Directory, Pending: v.Pending, RequestID: v.RequestID, Step: v.Step}
}
func (s *Service) saveSession(v *Session) error { return s.save("session", v.ID, diskSession(v)) }
func (s *Service) event(session string, event Event) error {
	event.At = now()
	b, e := json.Marshal(event)
	if e != nil {
		return e
	}
	_, e = s.db.Exec(`INSERT INTO factory_events(session_id,data) VALUES(?,?)`, session, string(b))
	if e == nil {
		s.signal()
	}
	return e
}
func (s *Service) events(session string, cursor int64) ([]Event, error) {
	rows, e := s.db.Query(`SELECT id,data FROM factory_events WHERE session_id=? AND id>? ORDER BY id LIMIT 100`, session, cursor)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var id int64
		var raw string
		if e = rows.Scan(&id, &raw); e != nil {
			return nil, e
		}
		var v Event
		if e = json.Unmarshal([]byte(raw), &v); e != nil {
			return nil, e
		}
		v.ID = id
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Service) request(key, value string) error {
	if key == "" {
		return errors.New("request_id is required")
	}
	if err := s.save("request", key, value); err != nil {
		return err
	}
	s.requests[key] = value
	return nil
}
func (s *Service) foreman(project string) (*Session, error) {
	if _, ok := s.cfg.Projects[project]; !ok {
		return nil, errors.New("unknown project")
	}
	for _, v := range s.sessions {
		if v.ProjectID == project && v.isForeman() {
			return v, nil
		}
	}
	v := &Session{ID: id("s_"), ProjectID: project, Role: "foreman", Status: "idle", Directory: s.foremanDirectory(project), CreatedAt: now()}
	if e := s.saveSession(v); e != nil {
		return nil, e
	}
	s.sessions[v.ID] = v
	return v, nil
}
func (s *Service) enqueue(v *Session, prompt, request string) error {
	return s.acceptTurn(v, prompt, request, "turn:"+v.ID+":"+request)
}
func (s *Service) acceptTurn(v *Session, prompt, request, key string) error {
	return s.acceptConfirmedTurn(v, prompt, request, key, false, nil)
}

// Recovery includes its task state in the accepted turn transaction.
func (s *Service) acceptConfirmedTurn(v *Session, prompt, request, key string, recovery bool, task *Task) error {
	if s.closed {
		return errors.New("factory is stopping")
	}
	if s.requests[key] != "" {
		return nil
	}
	if v.Status == "interrupted" && !recovery {
		return errors.New("resume interrupted conversation after confirming its previous process stopped")
	}
	if s.active == v.ID || v.Status == "running" || v.Status == "queued" || v.Status == "awaiting_permission" {
		return errors.New("conversation is busy; wait or cancel its current turn")
	}
	previous := *v
	v.Pending = prompt
	v.RequestID = request
	v.Reported = false
	v.Status = "queued"
	v.Error = ""
	if len(v.ReportQueue) > 0 {
		v.Pending += "\nPending reports:\n" + strings.Join(v.ReportQueue, "\n")
		v.ReportQueue = nil
	}
	event := Event{Kind: "context", Title: "Task update", Text: prompt}
	if strings.HasPrefix(key, "message:") {
		event.Kind = "user"
		event.Title = ""
	}
	records := []recordWrite{{"session", v.ID, diskSession(v)}, {"request", key, v.ID}}
	if task != nil {
		records = append(records, recordWrite{"task", task.ID, diskTask(task)})
	}
	e := s.commitRecords(records, v.ID, event)
	if e != nil {
		*v = previous
		return e
	}
	if task != nil {
		*s.tasks[task.ID] = *task
	}
	s.requests[key] = v.ID
	s.queue = append(s.queue, v.ID)
	s.signal()
	s.next()
	return nil
}

type recordWrite struct {
	kind, key string
	value     any
}

func (s *Service) commitRecords(records []recordWrite, session string, event Event) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, r := range records {
		b, e := json.Marshal(r.value)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(`INSERT INTO factory_records(kind,id,data) VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET data=excluded.data`, r.kind, r.key, string(b)); e != nil {
			return e
		}
	}
	if session != "" {
		event.At = now()
		b, e := json.Marshal(event)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(`INSERT INTO factory_events(session_id,data) VALUES(?,?)`, session, string(b)); e != nil {
			return e
		}
	}
	return tx.Commit()
}

func (s *Service) next() {
	if s.closed || s.active != "" || len(s.queue) == 0 {
		return
	}
	key := s.queue[0]
	s.queue = s.queue[1:]
	v := s.sessions[key]
	if v == nil || v.Status != "queued" {
		s.next()
		return
	}
	s.active = key
	v.Status = "running"
	if task := s.tasks[v.TaskID]; task != nil && task.Step == v.Step {
		task.Activity = "Working: " + task.Steps[v.Step].Name
		_ = s.saveTask(task)
	}
	_ = s.saveSession(v)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	copySession := *v
	token := id("")
	s.tokens[token] = v.ID
	runner := s.runner
	url := s.url
	s.workers.Add(1)
	go func() { defer s.workers.Done(); s.execute(ctx, copySession, token, runner, url) }()
}

// workspace uses immutable host/project values and runs without the service lock.
func workspace(ctx context.Context, p config.FactoryProject, h config.FactoryHost, task string) (string, string, error) {
	dir, branch, _, err := workspaceAt(ctx, p, h, task)
	return dir, branch, err
}
func workspaceAt(ctx context.Context, p config.FactoryProject, h config.FactoryHost, task string) (string, string, string, error) {
	if p.Source == "git" {
		if e := prepareProject(ctx, p, h); e != nil {
			return "", "", "", e
		}
	}
	var dir string
	if p.Host != "" && p.Host != "local" {
		dir = path.Join(path.Dir(p.Path), ".machinist-worktrees", task)
	} else {
		root, e := os.UserCacheDir()
		if e != nil {
			return "", "", "", e
		}
		dir = filepath.Join(root, "machinist", "factory", task)
		if e = os.MkdirAll(filepath.Dir(dir), 0700); e != nil {
			return "", "", "", e
		}
	}
	base, e := projectGit(ctx, p, h, p.Path, "rev-parse", "HEAD")
	if e != nil {
		return "", "", "", e
	}
	if base == "" {
		return "", "", "", errors.New("source repository returned an empty revision")
	}
	branch := "codex/factory-" + task
	// Create the branch first so checkout failure still has verified ownership.
	if _, e := projectGit(ctx, p, h, p.Path, "branch", branch, base); e != nil {
		// An existing branch or lost SSH acknowledgement has no verified ownership.
		return dir, branch, "", e
	}
	if _, e := projectGit(ctx, p, h, p.Path, "worktree", "add", dir, branch); e != nil {
		return dir, branch, base, e
	}
	return dir, branch, base, nil
}

// Remove only the worktree just created by this attempt, while preserving any new work.
func cleanupWorkspace(ctx context.Context, p config.FactoryProject, h config.FactoryHost, dir, branch, base string) error {
	if base == "" {
		return errors.New("original revision is unknown")
	}
	// Git may remove a failed checkout while leaving its newly created branch.
	registered, e := projectGit(ctx, p, h, p.Path, "worktree", "list", "--porcelain")
	if e != nil {
		return e
	}
	used := false
	for _, line := range strings.Split(registered, "\n") {
		if line == "branch refs/heads/"+branch {
			used = true
			break
		}
	}
	if !used {
		_, e = projectGit(ctx, p, h, p.Path, "update-ref", "-d", "refs/heads/"+branch, base)
		return e
	}
	actualBranch, e := projectGit(ctx, p, h, dir, "symbolic-ref", "--short", "HEAD")
	if e != nil {
		return e
	}
	head, e := projectGit(ctx, p, h, dir, "rev-parse", "HEAD")
	if e != nil {
		return e
	}
	dirty, e := projectGit(ctx, p, h, dir, "status", "--porcelain", "--untracked-files=all", "--ignored")
	if e != nil {
		return e
	}
	if actualBranch != branch || head != base || dirty != "" {
		return errors.New("workspace changed after creation; saved files and branch were preserved")
	}
	if _, e = projectGit(ctx, p, h, p.Path, "worktree", "remove", "--", dir); e != nil {
		return e
	}
	// Compare-and-delete preserves a concurrently changed branch reference.
	_, e = projectGit(ctx, p, h, p.Path, "update-ref", "-d", "refs/heads/"+branch, base)
	return e
}

func jsonReply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, err error) {
	jsonReply(w, http.StatusConflict, map[string]string{"error": err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		jsonReply(w, 400, map[string]string{"error": err.Error()})
		return false
	}
	return true
}
