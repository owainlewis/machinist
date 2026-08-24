package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type server struct {
	config loadedConfig
	store  *store
	github githubClient
	runner *agentRunner
	log    *log.Logger

	mu      sync.Mutex
	running map[string]struct{}
	workers sync.WaitGroup
}

func newServer(cfg loadedConfig, github githubClient, writes bool, logger *log.Logger) (*server, error) {
	if writes {
		for _, repository := range cfg.Config.Repositories {
			if !strings.HasPrefix(repository.BaseRef, "origin/") || strings.TrimPrefix(repository.BaseRef, "origin/") == "" {
				return nil, fmt.Errorf("repository %s base_ref must use origin/<branch> when GitHub writes are enabled", repository.GitHub)
			}
		}
	}
	executable, err := executablePath()
	if err != nil {
		return nil, err
	}
	authToken, err := newAuthorityToken()
	if err != nil {
		return nil, err
	}
	state := newStore(cfg.Config.StateDirectory)
	runner := &agentRunner{config: cfg, store: state, github: github, githubWrites: writes, executable: executable, authToken: authToken}
	return &server{config: cfg, store: state, github: github, runner: runner, log: logger, running: make(map[string]struct{})}, nil
}

func (s *server) serve(ctx context.Context) error {
	if err := s.store.activate(s.runner.authToken); err != nil {
		return fmt.Errorf("activate local runtime authority: %w", err)
	}
	defer s.store.deactivate(s.runner.authToken)
	if err := s.recoverInterrupted(); err != nil {
		return err
	}
	serverContext, cancel := context.WithCancel(ctx)
	defer cancel()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleHome)
	mux.HandleFunc("GET /api/work", s.handleList)
	mux.HandleFunc("POST /api/run", s.handleRun)
	mux.HandleFunc("POST /api/internal", s.handleInternal)
	mux.HandleFunc("POST /webhooks/github", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "GitHub webhooks are not enabled in this local spike; use the poller", http.StatusNotImplemented)
	})

	httpServer := &http.Server{
		Addr:              s.config.Config.Server.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context {
			return serverContext
		},
	}
	var loops sync.WaitGroup
	loops.Add(2)
	go func() {
		defer loops.Done()
		s.schedule(serverContext)
	}()
	go func() {
		defer loops.Done()
		s.poll(serverContext)
	}()
	go func() {
		<-serverContext.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	s.log.Printf("factory web listening on http://%s", displayAddress(s.config.Config.Server.Listen))
	err := httpServer.ListenAndServe()
	cancel()
	loops.Wait()
	workersDone := make(chan struct{})
	go func() {
		s.workers.Wait()
		close(workersDone)
	}()
	select {
	case <-workersDone:
	case <-time.After(5 * time.Second):
		return fmt.Errorf("timed out waiting for active agent processes to stop")
	}
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *server) handleInternal(w http.ResponseWriter, request *http.Request) {
	expected := "Bearer " + s.runner.authToken
	provided := request.Header.Get("Authorization")
	if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		http.Error(w, "unauthorized internal command", http.StatusUnauthorized)
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	request.Body = http.MaxBytesReader(w, request.Body, 16<<10)
	var input struct {
		WorkID string   `json:"work_id"`
		Action string   `json:"action"`
		Args   []string `json:"args"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil || input.WorkID == "" {
		http.Error(w, "invalid internal command", http.StatusBadRequest)
		return
	}
	var output []byte
	switch input.Action {
	case "delegate":
		if len(input.Args) != 1 {
			http.Error(w, "delegate requires a role", http.StatusBadRequest)
			return
		}
		output, err = s.runner.delegate(request.Context(), input.WorkID, input.Args[0])
	case "publish-plan":
		err = s.runner.publishPlan(request.Context(), input.WorkID)
	case "finish":
		err = s.runner.finish(request.Context(), input.WorkID)
	case "block":
		if len(input.Args) == 0 {
			http.Error(w, "block requires a reason", http.StatusBadRequest)
			return
		}
		err = s.runner.block(request.Context(), input.WorkID, strings.Join(input.Args, " "))
	default:
		http.Error(w, "unknown internal action", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(output)
}

func (s *server) recoverInterrupted() error {
	items, err := s.store.list()
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.State != stateRunning {
			continue
		}
		_, err := s.store.update(item.ID, func(current *work) error {
			current.State = stateFailed
			current.ActiveRole = ""
			current.Failure = "factory web stopped while this attempt was running"
			current.Events = append(current.Events, event{At: time.Now().UTC(), Message: "interrupted attempt marked failed on restart"})
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *server) poll(ctx context.Context) {
	duration, err := time.ParseDuration(s.config.Config.Server.PollEvery)
	if err != nil || duration <= 0 {
		s.log.Printf("poll: invalid interval %q", s.config.Config.Server.PollEvery)
		return
	}
	ticker := time.NewTicker(duration)
	defer ticker.Stop()
	s.pollOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollOnce(ctx)
		}
	}
}

func (s *server) pollOnce(ctx context.Context) {
	for _, repository := range s.config.Config.Repositories {
		pollContext, cancel := context.WithTimeout(ctx, time.Minute)
		issues, err := s.github.LabeledIssues(pollContext, repository.GitHub, s.config.Config.Server.TriggerLabel)
		cancel()
		if err != nil {
			s.log.Printf("poll %s: %v", repository.GitHub, err)
			continue
		}
		for _, value := range issues {
			if _, created, err := s.store.create(value); err != nil {
				s.log.Printf("admit %s#%d: %v", value.Repository, value.Number, err)
			} else if created {
				s.log.Printf("queued %s#%d from label", value.Repository, value.Number)
			}
		}
	}
}

func (s *server) schedule(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.startQueued(ctx)
		}
	}
}

func (s *server) startQueued(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.running) >= s.config.Config.Server.MaxConcurrent {
		return
	}
	items, err := s.store.list()
	if err != nil {
		s.log.Printf("schedule: %v", err)
		return
	}
	for i := len(items) - 1; i >= 0 && len(s.running) < s.config.Config.Server.MaxConcurrent; i-- {
		item := items[i]
		if item.State != stateQueued {
			continue
		}
		if _, active := s.running[item.ID]; active {
			continue
		}
		s.running[item.ID] = struct{}{}
		s.workers.Add(1)
		go s.execute(ctx, item.ID)
	}
}

func (s *server) execute(ctx context.Context, id string) {
	defer s.workers.Done()
	err := s.runner.runWork(ctx, id)
	if err != nil {
		_, _ = s.store.update(id, func(item *work) error {
			if item.State == stateRunning || item.State == stateQueued {
				item.State = stateFailed
				item.ActiveRole = ""
				item.Failure = err.Error()
				item.CompletedAt = time.Now().UTC()
				item.Events = append(item.Events, event{At: time.Now().UTC(), Message: "attempt failed: " + err.Error()})
			}
			return nil
		})
		s.log.Printf("work %s failed: %v", id, err)
	}
	s.mu.Lock()
	delete(s.running, id)
	s.mu.Unlock()
}

func (s *server) handleRun(w http.ResponseWriter, request *http.Request) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	request.Body = http.MaxBytesReader(w, request.Body, 16<<10)
	var input struct {
		Issue string `json:"issue"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	repository, number, err := parseIssueReference(input.Issue)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := repositoryFor(s.config, repository); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	value, err := s.github.Issue(request.Context(), repository, number)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	item, created, err := s.store.create(value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !created {
		if !s.isRunning(item.ID) {
			item, created, err = s.store.retry(item.ID, value)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}
	status := http.StatusAccepted
	if !created {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(item)
}

func (s *server) isRunning(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.running[id]
	return ok
}

func (s *server) handleList(w http.ResponseWriter, _ *http.Request) {
	items, err := s.store.list()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

var homeTemplate = template.Must(template.New("home").Funcs(template.FuncMap{
	"shortTime": func(value time.Time) string {
		if value.IsZero() {
			return ""
		}
		return value.Local().Format("2006-01-02 15:04:05")
	},
}).Parse(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><meta http-equiv="refresh" content="5"><title>Factory</title><style>
body{font:15px system-ui,sans-serif;max-width:1100px;margin:40px auto;padding:0 20px;color:#18202a;background:#f6f7f9}h1{font-size:24px}p{color:#5c6773}.grid{display:grid;gap:14px}.card{background:white;border:1px solid #dde2e7;border-radius:10px;padding:16px}.top{display:flex;justify-content:space-between;gap:16px}.state{font:12px ui-monospace,monospace;text-transform:uppercase;padding:4px 8px;border-radius:999px;background:#edf1f5}.meta{font-size:13px;color:#66717d}.events{margin:12px 0 0;padding-left:20px;color:#48535e}a{color:#075bc7;text-decoration:none}code{font-size:12px}
</style></head><body><h1>Factory</h1><p>Always-on local control plane. Polling every {{.PollEvery}} for <code>{{.Label}}</code>.</p><div class="grid">{{range .Work}}<section class="card"><div class="top"><div><a href="{{.Issue.URL}}"><strong>{{.Issue.Repository}}#{{.Issue.Number}} {{.Issue.Title}}</strong></a><div class="meta">Attempt {{.Attempt}} · updated {{shortTime .UpdatedAt}}{{if .ActiveRole}} · active: {{.ActiveRole}}{{end}}{{if .Branch}} · {{.Branch}}{{end}}</div></div><span class="state">{{.State}}</span></div>{{if .Failure}}<p>{{.Failure}}</p>{{end}}{{if .PRURL}}<p><a href="{{.PRURL}}">Open pull request</a></p>{{end}}<ul class="events">{{range .Events}}<li>{{shortTime .At}} · {{.Message}}</li>{{end}}</ul></section>{{else}}<section class="card">No work yet. Add the configured label or run <code>factory run owner/repo#123</code>.</section>{{end}}</div></body></html>`))

func (s *server) handleHome(w http.ResponseWriter, _ *http.Request) {
	items, err := s.store.list()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := struct {
		PollEvery string
		Label     string
		Work      []work
	}{s.config.Config.Server.PollEvery, s.config.Config.Server.TriggerLabel, items}
	if err := homeTemplate.Execute(w, data); err != nil {
		s.log.Printf("render home: %v", err)
	}
}

func displayAddress(address string) string {
	if strings.HasPrefix(address, ":") {
		return "127.0.0.1" + address
	}
	return address
}

func serverURL(address string) string { return fmt.Sprintf("http://%s", displayAddress(address)) }
