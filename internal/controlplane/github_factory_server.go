package controlplane

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	machinistexamples "github.com/owainlewis/machinist/examples"
	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

// GitHubFactoryServer stores execution history locally and links published work
// to GitHub. Workers may run on the local host or a remote VM.
type GitHubFactoryServer struct {
	setup          *onboarding
	dataDirectory  string
	mu             sync.Mutex
	liveMu         sync.Mutex // Only leases and worker liveness; never held during GitHub requests.
	auth           *Server
	client         GitHubFactoryClient
	local          *factoryLocalStore
	definitionPath string
	repositories   map[string]string
	actor          string
	tasks          map[string]*githubTask
	workers        map[string]Worker
	leases         map[string]*githubLease
	loaded         time.Time
	maxConcurrent  int
	handler        http.Handler
}
type githubTask struct {
	issue   GitHubIssue
	state   *GitHubFactoryState
	repo    string
	receipt *factoryReceipt
}
type githubLease struct {
	phase    string
	spec     protocol.RunSpec
	instance string
	last     time.Time
}

func NewGitHubFactoryServer(path, token string, maxConcurrent int) (*GitHubFactoryServer, error) {
	definition, err := config.LoadConfig(path)
	if err != nil {
		return nil, err
	}
	s, err := newGitHubFactoryServer(path, token, maxConcurrent, map[string]string{}, NewGitHubFactoryClient(), filepath.Dir(definition.Server.WorkerTokenFile))
	if err != nil {
		return nil, err
	}
	s.setup, err = newOnboarding(s)
	if err != nil {
		s.Close()
		return nil, err
	}
	s.client = &ghFactoryClient{api: s.setup.connection.API}
	return s, nil
}
func newGitHubFactoryServer(path, token string, maxConcurrent int, repositories map[string]string, client GitHubFactoryClient, dataDirectories ...string) (*GitHubFactoryServer, error) {
	csrf, err := randomID("csrf", 24)
	if err != nil {
		return nil, err
	}
	s := &GitHubFactoryServer{auth: &Server{workerToken: token, csrfToken: csrf}, client: client, definitionPath: path, repositories: repositories, tasks: map[string]*githubTask{}, workers: map[string]Worker{}, leases: map[string]*githubLease{}, maxConcurrent: maxConcurrent}
	for alias, repo := range repositories {
		if alias == "" {
			return nil, errors.New("repository alias is required")
		}
		if _, err := githubRepositoryPath(repo); err != nil {
			return nil, err
		}
	}
	if s.repositories == nil {
		s.repositories = map[string]string{}
	}
	s.actor = "local"
	s.dataDirectory = filepath.Dir(path)
	if len(dataDirectories) > 0 {
		s.dataDirectory = dataDirectories[0]
	}
	s.local, err = openLocalJobs(s.dataDirectory)
	if err != nil {
		return nil, err
	}
	s.handler, err = s.routes()
	return s, err
}
func (s *GitHubFactoryServer) Close() error {
	if s.setup != nil {
		s.setup.stopWorker()
	}
	err := s.local.db.Close()
	if s.local.lock != nil {
		_ = s.local.lock.Close()
	}
	return err
}
func (s *GitHubFactoryServer) Handler() http.Handler { return s.handler }
func (s *GitHubFactoryServer) Serve(ctx context.Context, listen string, onListening func(net.Addr)) error {
	if err := validateLoopbackListen(listen); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer s.Close()
	if s.setup != nil {
		s.setup.listening(listener.Addr())
	}
	if onListening != nil {
		onListening(listener.Addr())
	}
	scheduleCtx, stopSchedules := context.WithCancel(ctx)
	schedulesDone := make(chan struct{})
	go func() { defer close(schedulesDone); s.serveAutomations(scheduleCtx) }()
	defer func() { stopSchedules(); <-schedulesDone }()
	server := &http.Server{Handler: s.handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := server.Shutdown(shutdown)
		if err != nil {
			_ = server.Close()
		}
		<-done
		return err
	}
}
func (s *GitHubFactoryServer) routes() (http.Handler, error) {
	dist, err := fs.Sub(webAssets, "web/dist")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("GET /api/v1/onboarding", s.onboardingStatus)
	mux.HandleFunc("GET /api/v1/onboarding/repositories", s.onboardingRepositories)
	mux.HandleFunc("GET /api/v1/onboarding/worker-config", s.onboardingWorkerConfig)
	mux.HandleFunc("POST /api/v1/onboarding/{action...}", s.auth.authorizeSubmission(s.onboardingAction))
	mux.HandleFunc("POST /api/v1/projects", s.auth.authorizeSubmission(s.addProject))
	mux.HandleFunc("GET /api/v1/catalog", s.catalog)
	mux.HandleFunc("GET /api/v1/definitions", s.definitions)
	mux.HandleFunc("PUT /api/v1/factory/agents/{name}", s.auth.authorizeSubmission(s.savePrompt))
	mux.HandleFunc("GET /api/v1/automations", s.listAutomations)
	mux.HandleFunc("POST /api/v1/automations", s.auth.authorizeSubmission(s.saveAutomation))
	mux.HandleFunc("PUT /api/v1/automations/{id}", s.auth.authorizeSubmission(s.saveAutomation))
	mux.HandleFunc("DELETE /api/v1/automations/{id}", s.auth.authorizeSubmission(s.deleteAutomation))
	mux.HandleFunc("POST /api/v1/automations/{id}/run", s.auth.authorizeSubmission(s.runAutomation))
	mux.HandleFunc("POST /api/v1/jobs", s.auth.authorizeSubmission(s.submit))
	mux.HandleFunc("POST /api/v1/jobs/{id}/{action}", s.auth.authorizeSubmission(s.action))
	mux.HandleFunc("POST /api/v1/workers/poll", s.auth.authorizeWorker(s.poll))
	mux.HandleFunc("POST /api/v1/runs/{id}/heartbeat", s.auth.authorizeWorker(s.heartbeat))
	mux.HandleFunc("POST /api/v1/runs/{id}/complete", s.auth.authorizeWorker(s.complete))
	mux.HandleFunc("GET /api/v1/jobs/{id}/artifacts", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, []any{}) })
	mux.Handle("/", http.FileServer(http.FS(dist)))
	return securityHeaders(mux), nil
}
func (s *GitHubFactoryServer) refresh(ctx context.Context, force bool) error {
	if !force && !s.loaded.IsZero() && time.Since(s.loaded) < 15*time.Second {
		return nil
	}
	tasks := map[string]*githubTask{}
	records, err := s.local.tasks(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		comments, err := s.local.comments(ctx, record.issue.HTMLURL)
		if err != nil {
			return err
		}
		state, err := s.reduceLocal(record.issue, comments, record.alias)
		if err != nil {
			return err
		}
		task := &githubTask{issue: record.issue, state: state, repo: record.repository}
		if state.LastEventID == 0 {
			if err = s.event(ctx, task, GitHubFactoryEvent{Kind: "created", Role: "triage"}); err != nil {
				return err
			}
		}
		if task.state.Job.State == "running" {
			run := task.state.Job.Runs[len(task.state.Job.Runs)-1]
			if !s.leaseAlive(run.ID) {
				if err = s.event(ctx, task, GitHubFactoryEvent{Kind: "interrupted", RunID: run.ID, Role: task.state.Role}); err != nil {
					return err
				}
				s.deleteLease(run.ID)
			}
		}
		tasks[task.state.Job.ID] = task
	}
	s.tasks = tasks
	if err := s.seedPublicationHolds(ctx); err != nil {
		return err
	}
	for id, lease := range s.liveLeases() {
		task := s.tasks[lease.spec.JobID]
		if lease.spec.HealthCheck {
			continue
		}
		if task == nil || task.state.Job.State != "running" || !s.leaseAlive(id) {
			s.deleteLease(id)
		}
	}
	s.loaded = time.Now()
	return nil
}

// reduceLocal reuses the transition rules without exposing internal journal links.
func (s *GitHubFactoryServer) reduceLocal(issue GitHubIssue, comments []GitHubComment, alias string) (*GitHubFactoryState, error) {
	state, err := ReduceGitHubFactory(issue, comments, "local", alias)
	if err != nil {
		return nil, err
	}
	state.Job.StateBackend = "local"
	state.Job.Maintenance = issue.Maintenance != nil
	for i := range state.Job.Runs {
		state.Job.Runs[i].GitHubURL = ""
	}
	if strings.HasPrefix(issue.HTMLURL, "local:") {
		state.Job.Task.SourceURL = ""
		state.Job.TriggerSubject = ""
		state.Job.GitHubIssueTitle = ""
	}
	return state, nil
}

type factoryStatusWriter interface {
	UpsertTaskStatus(context.Context, string, int, string) error
}

func (s *GitHubFactoryServer) event(ctx context.Context, task *githubTask, event GitHubFactoryEvent) error {
	event.Version = 1
	// New completions permit Triage answers in the standard pipeline. Historical
	// v1 completions retain their rules so later retries and cancellations replay.
	if event.Kind == "completed" || event.Kind == "completion" {
		event.Version = 2
	}
	event.Previous = task.state.LastEventID
	comments, err := s.local.comments(ctx, task.issue.HTMLURL)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if len(raw) > 1024*1024 {
		return errors.New("execution event exceeds 1 MiB")
	}
	synthetic := GitHubComment{ID: event.Previous + 1, Body: GitHubFactoryMarker + string(raw), User: GitHubActor{Login: "local"}, CreatedAt: time.Now().UTC()}
	if _, err = s.reduceLocal(task.issue, append(comments, synthetic), task.state.Job.Repository); err != nil {
		return err
	}
	if err = s.local.append(ctx, task.issue.HTMLURL, event, task.receipt); err != nil {
		return err
	}
	comments, err = s.local.comments(ctx, task.issue.HTMLURL)
	if err != nil {
		return err
	}
	task.state, err = s.reduceLocal(task.issue, comments, task.state.Job.Repository)
	if err != nil {
		return err
	}
	if event.Kind != "clear_inbox" {
		s.publishTaskStatus(ctx, task)
	}
	return nil
}

func (s *GitHubFactoryServer) publishTaskStatus(ctx context.Context, task *githubTask) {
	// A remote status update is best effort. Execution receipts are already durable.
	if task.issue.Number > 0 && task.state.Job.PublicationHold == "" {
		if labels, ok := s.client.(factoryLabelClient); ok {
			_ = labels.SyncTaskLabels(ctx, task.repo, task.issue.Number, task.state.SuggestedLabels, factoryRoutingLabel(task.state))
		}
		if writer, ok := s.client.(factoryStatusWriter); ok {
			body := fmt.Sprintf("Machinist: **%s**\n\nStage: %s", task.state.Job.State, task.state.Role)
			if runs := task.state.Job.Runs; len(runs) > 0 {
				summary := runs[len(runs)-1].Summary
				if len(summary) > 2000 {
					summary = summary[:2000] + "…"
				}
				if summary != "" {
					body += "\n\n" + summary
				}
			}
			if f := task.state.Job.Workflow.Factory; f.PRURL != "" {
				body += "\n\n[Open pull request](" + f.PRURL + ")"
			}
			_ = writer.UpsertTaskStatus(ctx, task.repo, task.issue.Number, body)
		}
	}
}
func (s *GitHubFactoryServer) repositoryNames() []string {
	names := make([]string, 0, len(s.repositories))
	for name := range s.repositories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func (s *GitHubFactoryServer) status(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	if err := s.refresh(r.Context(), false); err != nil {
		writeError(w, 502, err)
		return
	}
	jobs := []Job{}
	leases := s.liveLeases()
	for _, task := range s.tasks {
		job := task.state.Job
		job.Runs = append([]Run(nil), job.Runs...)
		for i := range job.Runs {
			if lease, ok := leases[job.Runs[i].ID]; ok && job.Runs[i].State == "running" {
				job.Runs[i].Phase = lease.phase
				last := lease.last
				job.Runs[i].HeartbeatAt = &last
			}
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].UpdatedAt.After(jobs[j].UpdatedAt) })
	workers := []Worker{}
	for _, worker := range s.liveWorkers() {
		worker.Connected = time.Since(worker.LastSeenAt) < workerAvailabilityWindow
		workers = append(workers, worker)
	}
	onboardingState := map[string]any{"required": false}
	if s.setup != nil {
		if data, err := s.setup.status(r.Context()); err == nil {
			onboardingState = data
		}
	}
	automations, err := s.automationStatus(r.Context())
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"automations": automations, "onboarding": onboardingState, "jobs": jobs, "workers": workers, "commands": []string{}, "workflows": []string{"factory"}, "repositories": s.repositoryNames(), "repository_mappings": s.repositories, "triggers": []any{}, "csrf_token": s.auth.csrfToken, "state_backend": "local"})
}
func (s *GitHubFactoryServer) catalog(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, 200, catalogResponse{Workflows: []string{"factory"}, Commands: []string{}, Repositories: s.repositoryNames()})
}
func (s *GitHubFactoryServer) command(ctx context.Context, repo, role string) (config.ResolvedCommand, error) {
	definition, err := config.LoadDefinitions(s.definitionPath)
	if err != nil {
		return config.ResolvedCommand{}, err
	}
	command, err := definition.ResolveCommand(role)
	if err != nil {
		return command, err
	}
	if s.setup != nil {
		selection := s.setup.snapshot().Selection
		if selection.Agent != "" {
			command.Executor = selection.Agent
			command.Model = ""
			if role == "review" {
				command.Executor += "-review"
			}
		}
	}
	prompt, hash, localErr := s.local.prompt(ctx, repo, role)
	if localErr == nil {
		command.Prompt = prompt
		command.Hash = hash
		return command, nil
	}
	if !errors.Is(localErr, sql.ErrNoRows) {
		return command, localErr
	}
	// Executor definitions include bundled prompts, so sign-in is not required.
	if strings.TrimSpace(command.Prompt) == "" {
		raw, readErr := machinistexamples.Files.ReadFile("prompts/" + role + ".md")
		if readErr != nil {
			return command, readErr
		}
		command.Prompt = string(raw)
	}
	sum := sha256.Sum256([]byte(command.Prompt))
	command.Hash = hex.EncodeToString(sum[:])
	return command, err
}
func (s *GitHubFactoryServer) definitions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, err := s.definitionRepository(r.URL.Query().Get("repository"))
	if err != nil {
		writeError(w, 400, err)
		return
	}
	commands := []commandDefinitionResponse{}
	for _, role := range []string{"triage", "spec", "implement", "review"} {
		command, err := s.command(r.Context(), repo, role)
		if err != nil {
			writeError(w, 502, err)
			return
		}
		commands = append(commands, commandDefinitionResponse{Name: role, Executor: command.Executor, Timeout: command.Timeout.String(), Hash: command.Hash, Prompt: command.Prompt})
	}
	steps := []workflowStepDefinition{}
	for _, role := range []string{"triage", "spec", "implement", "review"} {
		steps = append(steps, workflowStepDefinition{Name: role})
	}
	writeJSON(w, 200, definitionsResponse{Commands: commands, Workflows: map[string][]workflowStepDefinition{"factory": steps}})
}
func (s *GitHubFactoryServer) definitionRepository(alias string) (string, error) {
	if alias != "" {
		repo, ok := s.repositories[alias]
		if !ok {
			return "", errors.New("repository is not configured")
		}
		return repo, nil
	}
	names := s.repositoryNames()
	if len(names) == 0 {
		return "", nil
	}
	return s.repositories[names[0]], nil
}
func (s *GitHubFactoryServer) savePrompt(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !limitRequestBody(w, r, maxRequestBytes) {
		return
	}
	var input struct {
		Prompt       string `json:"prompt"`
		ExpectedHash string `json:"expected_hash"`
		Repository   string `json:"repository"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	role := r.PathValue("name")
	if role != "triage" && role != "spec" && role != "implement" && role != "review" {
		writeError(w, 404, errors.New("unknown specialist"))
		return
	}
	repo, err := s.definitionRepository(input.Repository)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	if len(input.Prompt) > 256<<10 || strings.TrimSpace(input.Prompt) == "" {
		writeError(w, 400, errors.New("prompt must contain text and be at most 256 KiB"))
		return
	}
	if err = config.ValidatePromptParameters(role, input.Prompt); err != nil {
		writeError(w, 400, err)
		return
	}
	base, err := s.command(r.Context(), repo, role)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	hash, err := s.local.savePrompt(r.Context(), repo, role, input.Prompt, input.ExpectedHash, base.Hash)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]string{"hash": hash})
}
func (s *GitHubFactoryServer) submit(w http.ResponseWriter, r *http.Request) {
	if s.setup != nil && !s.setup.snapshot().Finished {
		writeError(w, 409, errors.New("complete onboarding before starting a task"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !limitRequestBody(w, r, maxRequestBytes) {
		return
	}
	var input submitRequest
	if err := decodeJSON(r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	repo, ok := s.repositories[input.Repository]
	if !ok {
		writeError(w, 400, errors.New("configure the repository's GitHub mapping first"))
		return
	}
	if (input.Workflow != "" && input.Workflow != "factory") || input.Command != "" || input.Model != "" {
		writeError(w, 400, errors.New("choose a recipe; executors and models use the configured agent profile"))
		return
	}
	recipe := input.Recipe
	if recipe == "" {
		recipe = "factory"
	}
	if recipe != "factory" && recipe != "single" {
		writeError(w, 400, errors.New("choose the standard factory pipeline or single agent"))
		return
	}
	if len(input.ClientRequestID) > 128 || strings.TrimSpace(input.ClientRequestID) != input.ClientRequestID {
		writeError(w, 400, errors.New("invalid client request ID"))
		return
	}
	raw, _ := json.Marshal(input)
	sum := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(sum[:])
	if input.ClientRequestID != "" {
		previous, url, err := s.local.request(r.Context(), input.ClientRequestID)
		if err == nil {
			if previous != fingerprint {
				writeError(w, 409, errors.New("client request ID was already used for a different request"))
				return
			}
			if err = s.refresh(r.Context(), true); err != nil {
				writeError(w, 500, err)
				return
			}
			for _, task := range s.tasks {
				if task.issue.HTMLURL == url {
					writeJSON(w, 200, map[string]string{"id": task.state.Job.ID})
					return
				}
			}
			writeError(w, 500, errors.New("submission receipt has no job"))
			return
		}
		if !errors.Is(err, sql.ErrNoRows) {
			writeError(w, 500, err)
			return
		}
	}
	body := strings.TrimSpace(input.Spec)
	if body == "" {
		body = strings.TrimSpace(input.Prompt)
	}
	var issue GitHubIssue
	var err error
	if input.SourceURL != "" {
		u, e := url.Parse(input.SourceURL)
		if e != nil {
			writeError(w, 400, errors.New("invalid source URL"))
			return
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if e != nil || u.Scheme != "https" || u.Host != "github.com" || u.RawQuery != "" || u.Fragment != "" || len(parts) != 4 || parts[2] != "issues" || !strings.EqualFold(strings.Join(parts[:2], "/"), repo) {
			writeError(w, 400, errors.New("source must be a GitHub issue in the selected repository"))
			return
		}
		number, e := strconv.Atoi(parts[3])
		if e != nil || number < 1 {
			writeError(w, 400, errors.New("invalid issue number"))
			return
		}
		issue, err = s.client.GetTask(r.Context(), repo, number)
		if err == nil && (issue.Number != number || !strings.EqualFold(issue.HTMLURL, input.SourceURL)) {
			err = errors.New("GitHub returned a different issue")
		}
		if err == nil && issue.State != "open" {
			err = errors.New("issue is closed")
		}

	} else {
		if body == "" {
			writeError(w, 400, errors.New("describe the task or supply an issue"))
			return
		}
		title := strings.TrimSpace(input.Title)
		if title == "" {
			title = strings.SplitN(body, "\n", 2)[0]
			if len(title) > 100 {
				title = title[:100]
			}
		}
		localID, idErr := randomID("task", 12)
		if idErr != nil {
			writeError(w, 500, idErr)
			return
		}
		issue = GitHubIssue{Title: title, Body: body, HTMLURL: "local:" + localID, State: "open", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	}
	if err != nil {
		writeError(w, 502, err)
		return
	}
	s.loaded = time.Time{}
	existing, readErr := s.local.comments(r.Context(), issue.HTMLURL)
	if readErr != nil {
		writeError(w, 500, readErr)
		return
	}
	if len(existing) > 0 {
		current, reduceErr := s.reduceLocal(issue, existing, input.Repository)
		if reduceErr != nil {
			writeError(w, 409, reduceErr)
			return
		}
		if current.Job.Recipe != recipe {
			writeError(w, 409, errors.New("linked issue already has a job with a different recipe"))
			return
		}
	}
	if err = s.local.createRequest(r.Context(), localFactoryTask{issue: issue, repository: repo, alias: input.Repository}, input.ClientRequestID, fingerprint, input.Recipe); err != nil {
		writeError(w, 500, err)
		return
	}
	comments, err := s.local.comments(r.Context(), issue.HTMLURL)
	if err != nil {
		writeError(w, 502, err)
		return
	}
	state, err := s.reduceLocal(issue, comments, input.Repository)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	task := &githubTask{issue: issue, state: state, repo: repo}
	if state.LastEventID == 0 {
		err = s.event(r.Context(), task, GitHubFactoryEvent{Kind: "created", Role: "triage"})
	}
	if err != nil {
		writeError(w, 502, err)
		return
	}
	s.tasks[task.state.Job.ID] = task
	if len(existing) == 0 {
		s.publishTaskStatus(r.Context(), task)
	}
	writeJSON(w, 201, map[string]string{"id": task.state.Job.ID})
}
func (s *GitHubFactoryServer) action(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !limitRequestBody(w, r, maxRequestBytes) {
		return
	}
	var input struct {
		RunID             string `json:"run_id"`
		Feedback          string `json:"feedback"`
		Stopped           bool   `json:"previous_process_stopped"`
		HoldVersion       int64  `json:"hold_version"`
		ExpectedUpdatedAt string `json:"expected_updated_at"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	if len(input.Feedback) > 16000 {
		writeError(w, 400, errors.New("feedback exceeds 16 KiB"))
		return
	}
	if err := s.refresh(r.Context(), true); err != nil {
		writeError(w, 502, err)
		return
	}
	task := s.tasks[r.PathValue("id")]
	if task == nil {
		writeError(w, 404, errors.New("task not found"))
		return
	}
	action := r.PathValue("action")
	if action == "clear_inbox" {
		seen, err := time.Parse(time.RFC3339Nano, input.ExpectedUpdatedAt)
		if err != nil || !seen.Equal(task.state.Job.UpdatedAt) {
			writeError(w, 409, errors.New("task changed; review its latest update before clearing the inbox"))
			return
		}
		if err := s.event(r.Context(), task, GitHubFactoryEvent{Kind: "clear_inbox"}); err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "accepted"})
		return
	}
	if action == "publication_hold" || action == "release_publication" {
		if (task.state.Job.State == "cancelled" || task.state.Job.State == "interrupted") && !input.Stopped {
			writeError(w, 409, errors.New("confirm the previous process stopped before changing publication access"))
			return
		}
		if task.state.Job.State == "running" {
			writeError(w, 409, errors.New("cancel running work and confirm it stopped before changing publication access"))
			return
		}
		if input.HoldVersion != task.state.Job.HoldVersion {
			writeError(w, 409, errors.New("publication hold changed; reload before continuing"))
			return
		}
		if action == "release_publication" && task.state.Job.PublicationHold == "" {
			writeError(w, 409, errors.New("job is not held"))
			return
		}
		if err := s.event(r.Context(), task, GitHubFactoryEvent{Kind: action, Feedback: input.Feedback}); err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "accepted"})
		return
	}
	if task.state.Job.PublicationHold != "" && action != "cancel" {
		writeError(w, 409, errors.New("publication is held; release it explicitly before resuming work"))
		return
	}
	if action == "complete_merge" {
		f := task.state.Job.Workflow.Factory
		var raw []byte
		var err error
		if s.setup != nil {
			u, _ := url.Parse(f.PRURL)
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) != 4 {
				writeError(w, 400, errors.New("invalid pull request URL"))
				return
			}
			raw, err = s.setup.connection.API(r.Context(), "GET", "/repos/"+strings.Join(parts[:2], "/")+"/pulls/"+parts[3], nil)
			if err == nil {
				var pr struct {
					Merged         bool   `json:"merged"`
					MergeCommitSHA string `json:"merge_commit_sha"`
					Head           struct {
						SHA string `json:"sha"`
					} `json:"head"`
				}
				err = json.Unmarshal(raw, &pr)
				if err == nil {
					state := "OPEN"
					if pr.Merged {
						state = "MERGED"
					}
					raw, _ = json.Marshal(map[string]any{"state": state, "headRefOid": pr.Head.SHA, "mergeCommit": map[string]string{"oid": pr.MergeCommitSHA}})
				}
			}
		} else {
			raw, err = readGitHubMerge(r.Context(), f.PRURL)
		}
		if err != nil {
			writeError(w, 502, err)
			return
		}
		if err = validateMergeState(raw, f.HeadSHA); err != nil {
			writeError(w, 409, err)
			return
		}
	}
	switch action {
	case "approve", "retry", "request_changes", "cancel", "complete_merge":
	default:
		writeError(w, 400, errors.New("unknown task action"))
		return
	}
	if action == "retry" || action == "approve" || action == "request_changes" {
		if err := s.guardAutomationRetry(r.Context(), task, input.Stopped); err != nil {
			writeError(w, 409, err)
			return
		}
	}
	if err := s.event(r.Context(), task, GitHubFactoryEvent{Kind: action, RunID: input.RunID, Role: task.state.Role, Feedback: input.Feedback, Stopped: input.Stopped}); err != nil {
		writeError(w, 409, err)
		return
	}
	if task.state.Job.State != "running" {
		for id, lease := range s.liveLeases() {
			if lease.spec.JobID == task.state.Job.ID {
				s.deleteLease(id)
			}
		}
	}
	writeJSON(w, 200, map[string]string{"status": "accepted"})
}
func (s *GitHubFactoryServer) poll(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !limitRequestBody(w, r, maxRequestBytes) {
		return
	}
	var p protocol.PollRequest
	if err := decodeJSON(r, &p); err != nil {
		writeDecodeError(w, err)
		return
	}
	if p.InstanceID == "" || p.Name == "" {
		writeError(w, 400, errors.New("worker identity required"))
		return
	}
	s.liveMu.Lock()
	s.workers[p.InstanceID] = Worker{InstanceID: p.InstanceID, Name: p.Name, LastSeenAt: time.Now().UTC(), Repositories: p.Repositories, Executors: p.Executors, ReadOnlyExecutors: p.ReadOnlyExecutors}
	s.liveMu.Unlock()
	if s.setup != nil {
		if check := s.setup.claimHealth(p); check != nil {
			writeJSON(w, 200, protocol.PollResponse{Run: check})
			return
		}
		if (s.setup.snapshot().Selection.Host == "kubernetes") != (p.Runtime == "kubernetes") {
			writeJSON(w, 200, protocol.PollResponse{})
			return
		}
		if !s.setup.snapshot().Finished {
			writeJSON(w, 200, protocol.PollResponse{})
			return
		}
	}
	if err := s.refresh(r.Context(), false); err != nil {
		writeError(w, 502, err)
		return
	}
	for _, lease := range s.liveLeases() {
		if lease.instance == p.InstanceID {
			writeJSON(w, 200, protocol.PollResponse{Run: &lease.spec})
			return
		}
	}
	if !p.Factory || !p.Workflows {
		writeJSON(w, 200, protocol.PollResponse{})
		return
	}
	if s.maxConcurrent > 0 && len(s.liveLeases()) >= s.maxConcurrent {
		writeJSON(w, 200, protocol.PollResponse{})
		return
	}
	for id, worker := range s.liveWorkers() {
		if id != p.InstanceID && worker.Name == p.Name && time.Since(worker.LastSeenAt) < workerAvailabilityWindow {
			writeJSON(w, 200, protocol.PollResponse{})
			return
		}
	}
	executors := stringSet(p.Executors)
	repositories := stringSet(p.Repositories)
	readonly := stringSet(p.ReadOnlyExecutors)
	ids := make([]string, 0, len(s.tasks))
	for id := range s.tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		task := s.tasks[id]
		state := task.state
		if (state.Job.Recipe == "single" && !p.SingleAgent) || (state.Job.Recipe != "single" && !p.Reviews) {
			continue
		}
		if state.Job.State == "queued" && repositories[state.Job.Repository] && task.issue.Number > 0 {
			issue, err := s.client.GetTask(r.Context(), task.repo, task.issue.Number)
			if err != nil {
				writeError(w, 502, errors.New("could not refresh the linked GitHub issue before dispatch"))
				return
			}
			if issue.Number != task.issue.Number || issue.HTMLURL != task.issue.HTMLURL {
				writeError(w, 502, errors.New("GitHub returned a different issue"))
				return
			}
			if err = s.local.updateIssue(r.Context(), issue); err != nil {
				writeError(w, 500, err)
				return
			}
			task.issue = issue
			comments, err := s.local.comments(r.Context(), issue.HTMLURL)
			if err != nil {
				writeError(w, 500, err)
				return
			}
			task.state, err = s.reduceLocal(issue, comments, state.Job.Repository)
			if err != nil {
				writeError(w, 500, err)
				return
			}
			state = task.state
		}
		if state.Job.State == "queued" && task.issue.State != "open" {
			if err := s.event(r.Context(), task, GitHubFactoryEvent{Kind: "dispatch_blocked", RunID: state.Job.Runs[len(state.Job.Runs)-1].ID, Role: state.Role, Feedback: "The linked GitHub issue is closed. Reopen it before retrying."}); err != nil {
				writeError(w, 500, err)
				return
			}
			continue
		}
		if state.Job.PublicationHold != "" || task.issue.State != "open" || state.Job.State != "queued" || !repositories[state.Job.Repository] {
			continue
		}
		role := state.Role
		capable := true
		for _, stage := range state.Job.Workflow.Steps {
			command, err := s.jobCommand(r.Context(), task, stage)
			if err != nil {
				writeError(w, 502, err)
				return
			}
			if !executors[command.Executor] || !supportsModel(p.Models, command.Executor, command.Model) || ((stage == "review" || stage == "triage" || state.Job.Recipe == "answer") && !readonly[command.Executor]) {
				capable = false
				break
			}
		}
		if !capable {
			continue
		}
		command, err := s.jobCommand(r.Context(), task, role)
		if err != nil {
			writeError(w, 502, err)
			return
		}
		prompt, err := config.RenderTaskTemplate(command.Prompt, *state.Job.Task, "", nil)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		run := state.Job.Runs[len(state.Job.Runs)-1]
		hash := command.Hash
		if hash == "" {
			sum := sha256.Sum256([]byte(command.Prompt))
			hash = "bundled-" + hex.EncodeToString(sum[:])
		}
		encodedPrompt, _ := json.Marshal(prompt)
		if len(encodedPrompt) > 768<<10 {
			if err := s.event(r.Context(), task, GitHubFactoryEvent{Kind: "dispatch_blocked", RunID: state.Job.Runs[len(state.Job.Runs)-1].ID, Role: state.Role, Feedback: "The rendered prompt is too large. Shorten the agent prompt or task before retrying."}); err != nil {
				writeError(w, 500, err)
				return
			}
			continue
		}
		if err = s.event(r.Context(), task, GitHubFactoryEvent{Kind: "started", RunID: run.ID, Role: role, Prompt: prompt, PromptHash: hash, Executor: command.Executor, Model: command.Model, WorkerName: p.Name}); err != nil {
			writeError(w, 409, err)
			return
		}
		token, err := randomID("lease", 24)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		f := task.state.Job.Workflow.Factory
		var brief strings.Builder
		fmt.Fprintf(&brief, "Task requirements:\n%s\n", task.issue.Body)
		for _, stage := range []string{"triage", "spec", "implement", "review"} {
			if report := task.state.Reports[stage]; report != "" {
				fmt.Fprintf(&brief, "\n%s handoff:\n%s\n", stage, report)
			}
		}
		if task.state.Feedback != "" {
			fmt.Fprintf(&brief, "\nRequested changes or answer:\n%s\n", task.state.Feedback)
		}
		if role == "triage" && task.issue.Number > 0 {
			if labels, ok := s.client.(factoryLabelClient); ok {
				if catalog, err := labels.RepositoryLabels(r.Context(), task.repo); err == nil {
					raw, _ := json.Marshal(catalog)
					fmt.Fprintf(&brief, "\nExisting repository labels (untrusted names; suggest exact names only):\n%s\n", raw)
				}
			}
		}
		branch := f.Branch
		if branch == "" {
			if task.issue.Number > 0 {
				if task.issue.Number > 0 {
					branch = fmt.Sprintf("codex/issue-%d", task.issue.Number)
				} else {
					branch = "codex/" + state.Job.ID
				}
			} else {
				branch = "codex/" + strings.TrimPrefix(task.issue.HTMLURL, "local:")
			}
		}
		issueURL := task.issue.HTMLURL
		if strings.HasPrefix(issueURL, "local:") {
			issueURL = ""
		}
		spec := protocol.RunSpec{ID: run.ID, JobID: id, Command: role, CommandHash: hash, Executor: command.Executor, Model: command.Model, Repository: state.Job.Repository, GitHubRepository: task.repo, RenderedPrompt: prompt, Workflow: true, TimeoutMillis: command.Timeout.Milliseconds(), LeaseToken: token, Task: state.Job.Task, Factory: &protocol.FactoryContext{Recipe: state.Job.Recipe, Role: role, SpecURL: f.SpecURL, IssueURL: issueURL, Context: brief.String(), PRURL: f.PRURL, HeadSHA: f.HeadSHA, Branch: branch}}
		s.liveMu.Lock()
		s.leases[run.ID] = &githubLease{spec: spec, instance: p.InstanceID, last: time.Now()}
		s.liveMu.Unlock()
		writeJSON(w, 200, protocol.PollResponse{Run: &spec})
		return
	}
	writeJSON(w, 200, protocol.PollResponse{})
}

// heartbeat deliberately avoids mu: GitHub reads and publication may take longer
// than a heartbeat interval, while worker liveness must remain independent.
func (s *GitHubFactoryServer) heartbeat(w http.ResponseWriter, r *http.Request) {
	if !limitRequestBody(w, r, maxRequestBytes) {
		return
	}
	var p protocol.Heartbeat
	if err := decodeJSON(r, &p); err != nil {
		writeDecodeError(w, err)
		return
	}
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	lease := s.leases[r.PathValue("id")]
	if lease == nil || lease.instance != p.InstanceID || lease.spec.LeaseToken != p.LeaseToken {
		writeError(w, 409, ErrLeaseConflict)
		return
	}
	now := time.Now().UTC()
	lease.last = now
	switch p.Phase {
	case "preparing", "checking_pr", "starting_sandbox", "running_agent", "collecting_result", "publishing":
		lease.phase = p.Phase
	}
	worker := s.workers[p.InstanceID]
	worker.LastSeenAt = now
	s.workers[p.InstanceID] = worker
	w.WriteHeader(204)
}

// Expiration and heartbeat renewal share a lock so a stale snapshot cannot
// discard a heartbeat received while a GitHub request was pending.
func (s *GitHubFactoryServer) leaseAlive(id string) bool {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	lease := s.leases[id]
	if lease == nil {
		return false
	}
	if time.Since(lease.last) > leaseDuration {
		delete(s.leases, id)
		return false
	}
	return true
}

func (s *GitHubFactoryServer) liveLease(id string) *githubLease {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	if lease := s.leases[id]; lease != nil {
		copy := *lease
		return &copy
	}
	return nil
}
func (s *GitHubFactoryServer) liveLeases() map[string]githubLease {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	result := make(map[string]githubLease, len(s.leases))
	for id, lease := range s.leases {
		result[id] = *lease
	}
	return result
}
func (s *GitHubFactoryServer) liveWorkers() map[string]Worker {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	result := make(map[string]Worker, len(s.workers))
	for id, worker := range s.workers {
		result[id] = worker
	}
	return result
}
func (s *GitHubFactoryServer) deleteLease(id string) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	delete(s.leases, id)
}

func (s *GitHubFactoryServer) complete(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !limitRequestBody(w, r, maxCompletionBytes) {
		return
	}
	var p protocol.Completion
	if err := decodeJSON(r, &p); err != nil {
		writeDecodeError(w, err)
		return
	}
	id := r.PathValue("id")
	if s.local.receipt(r.Context(), id, p.InstanceID, p.LeaseToken) {
		w.WriteHeader(204)
		return
	}
	lease := s.liveLease(id)
	if lease == nil || lease.instance != p.InstanceID || lease.spec.LeaseToken != p.LeaseToken {
		writeError(w, 409, ErrLeaseConflict)
		return
	}
	if lease.spec.HealthCheck && s.setup != nil && s.setup.finishHealth(id, p) {
		s.deleteLease(id)
		w.WriteHeader(204)
		return
	}
	task := s.tasks[lease.spec.JobID]
	if task == nil {
		writeError(w, 404, errors.New("task not found"))
		return
	}
	// Refresh local history before reconciling a possibly retried completion.
	comments, err := s.local.comments(r.Context(), task.issue.HTMLURL)
	if err != nil {
		writeError(w, 502, err)
		return
	}
	current, err := s.reduceLocal(task.issue, comments, task.state.Job.Repository)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	task.state = current
	if current.Job.PublicationHold != "" {
		writeError(w, 409, errors.New("job publication is held"))
		return
	}
	for _, run := range current.Job.Runs {
		if run.ID == id && run.Outcome != "" {
			s.deleteLease(id)
			w.WriteHeader(204)
			return
		}
	}
	var result struct {
		StepResult     json.RawMessage `json:"step_result"`
		TokenUsage     *int64          `json:"token_usage"`
		DurationMillis *int64          `json:"duration_millis"`
	}
	step := &protocol.StepResult{Outcome: "failed", Summary: p.Error, Factory: &protocol.FactoryResult{Version: 1, Role: lease.spec.Factory.Role}}
	decodeErr := json.Unmarshal(p.Result, &result)
	if p.State == "succeeded" {
		if err = decodeErr; err == nil {
			step, err = protocol.ParseStepResult(result.StepResult)
		}
		if err != nil {
			writeError(w, 400, err)
			return
		}
	}
	if step.Summary == "" {
		step.Summary = "Execution " + p.State
	}
	if step.Factory == nil || step.Factory.Role != lease.spec.Factory.Role {
		writeError(w, 400, errors.New("factory result role mismatch"))
		return
	}
	if step.Outcome == "complete" && step.Factory.Role != "review" && strings.TrimSpace(step.Factory.Report) == "" {
		writeError(w, 400, errors.New("completed agent must report its GitHub handoff"))
		return
	}
	if task.issue.Maintenance != nil && step.Outcome == "complete" {
		if step.Factory.Role != "triage" || !step.Factory.Resolved || step.Factory.NeedsSpec || step.Factory.PRURL != "" || step.Factory.HeadSHA != "" || step.Factory.Branch != "" || step.Factory.SpecURL != "" {
			writeError(w, 400, errors.New("maintenance must finish as a read-only triage answer"))
			return
		}
		report, applyErr := s.completeMaintenance(r.Context(), task, step.Factory.Report)
		if applyErr != nil {
			step.Outcome = "blocked"
			report = "Maintenance could not proceed: " + applyErr.Error()
		}
		step.Factory.Report = report
		step.Summary = "Repository maintenance finished. See the triage report for actions and decisions."
		if applyErr != nil {
			step.Summary = report
		}
	}
	task.receipt = &factoryReceipt{run: id, instance: p.InstanceID, token: p.LeaseToken}
	defer func() { task.receipt = nil }()
	if err = s.event(r.Context(), task, GitHubFactoryEvent{Kind: "completed", RunID: id, Role: lease.spec.Factory.Role, Result: step, ExitCode: &p.ExitCode, Error: p.Error, TokenUsage: result.TokenUsage, DurationMillis: result.DurationMillis}); err != nil {
		writeError(w, 409, err)
		return
	}
	s.deleteLease(id)
	w.WriteHeader(204)
}

func (s *GitHubFactoryServer) jobCommand(ctx context.Context, task *githubTask, role string) (config.ResolvedCommand, error) {
	command, err := s.command(ctx, task.repo, role)
	if err == nil && (task.state.Job.Recipe == "answer" || role == "triage") {
		review, reviewErr := s.command(ctx, task.repo, "review")
		if reviewErr != nil {
			return command, reviewErr
		}
		command.Executor = review.Executor
		command.Model = review.Model
		if task.state.Job.Recipe == "answer" {
			command.Prompt = "Answer the request without changing files, making commits, or publishing to GitHub. If changes or files are needed, return blocked. Return the factory triage JSON contract with resolved=true only for a complete answer.\nTask: {{machinist.prompt}}"
		}
		sum := sha256.Sum256([]byte(command.Prompt))
		command.Hash = hex.EncodeToString(sum[:])
	}
	return command, err
}
