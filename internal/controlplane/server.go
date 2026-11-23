// Package controlplane serves the HTTP API and owns the ticket engine.
package controlplane

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/engine"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/ghclient"
	"github.com/owainlewis/machinist/internal/githubapp"
	"github.com/owainlewis/machinist/internal/sources"
	"github.com/owainlewis/machinist/internal/tickets"
)

//go:embed all:web/dist
var webAssets embed.FS

const (
	maxRequestBytes = 1 << 20
	shutdownTimeout = 10 * time.Second
)

// Server is the one control plane of a deployment. It never starts an agent.
type Server struct {
	config   config.Config
	store    *tickets.Store
	engine   *engine.Engine
	client   *ghclient.Client
	intake   *sources.Intake
	schedule *sources.Schedule
	version  string
	log      io.Writer
	// linear takes work from Linear, when it is on.
	linear  *sources.Poller
	now     func() time.Time
	presets []presetView
	factory *factory.Factory
	// app is the GitHub App, or nil when there is none.
	app githubApp
}

func New(machinistConfig config.Config, store *tickets.Store, loaded *factory.Factory, version string, log io.Writer) (*Server, error) {
	if machinistConfig.GitHub.Handle == "" {
		return nil, errors.New("github.handle is required")
	}
	if err := loaded.Validate(machinistConfig.Server.DefaultPreset, machinistConfig.GitHub.Repositories); err != nil {
		return nil, err
	}
	app, err := loadApp(machinistConfig, loaded)
	if err != nil {
		return nil, err
	}
	client := ghclient.New()
	var tokens engine.TokenMinter
	var identity engine.IdentityFinder
	if app != nil {
		client.WithToken(app.CachedToken)
		tokens = app.Token
		identity = func(ctx context.Context) (string, string, error) {
			found, err := app.Identity(ctx)
			return found.Name, found.Email, err
		}
	}
	ticketEngine := engine.New(engine.Options{
		Store:       store,
		Factory:     loaded,
		RunTimeout:  machinistConfig.Server.RunDeadline(),
		Handle:      machinistConfig.GitHub.Handle,
		AutoMerge:   machinistConfig.GitHub.AutoMerges(),
		FindPR:      findPR(client),
		Post:        client.PostComment,
		GitHubToken: tokens,
		Identity:    identity,
		Log:         log,
	})
	intake := &sources.Intake{
		Client:        client,
		Store:         store,
		DefaultPreset: machinistConfig.Server.DefaultPreset,
		Repositories:  machinistConfig.GitHub.Repositories,
		Presets: func(name string) bool {
			_, ok := loaded.Preset(name)
			return ok
		},
	}
	linear, err := linearPoller(context.Background(), machinistConfig, loaded, intake, store, log)
	if err != nil {
		return nil, err
	}
	server := &Server{
		linear:   linear,
		now:      time.Now,
		config:   machinistConfig,
		store:    store,
		engine:   ticketEngine,
		client:   client,
		intake:   intake,
		schedule: sources.NewSchedule(store, loaded.Automations, log),
		version:  version,
		presets:  presetViews(loaded),
		factory:  loaded,
		log:      log,
	}
	if app != nil {
		server.app = app
	}
	return server, nil
}

// linearPoller checks the Linear section against the factory and the
// connected repositories, so a mistake stops startup instead of dropping work.
// A connected repository is one in github.repositories or one added in
// Settings → Projects.
func linearPoller(ctx context.Context, machinistConfig config.Config, loaded *factory.Factory, intake *sources.Intake, store *tickets.Store, log io.Writer) (*sources.Poller, error) {
	linear := machinistConfig.Linear
	if !linear.Enabled() {
		return nil, nil
	}
	if err := linear.CheckCredentials(); err != nil {
		return nil, err
	}
	preset := linear.Preset
	if preset == "" {
		preset = machinistConfig.Server.DefaultPreset
	}
	if _, ok := loaded.Preset(preset); !ok {
		return nil, fmt.Errorf("linear.preset %q is not in the factory", preset)
	}
	connected, err := intake.Connected(ctx)
	if err != nil {
		return nil, fmt.Errorf("list connected repositories: %w", err)
	}
	for _, mapping := range []struct {
		key   string
		repos map[string]string
	}{{"projects", linear.Projects}, {"teams", linear.Teams}} {
		names := slices.Sorted(maps.Keys(mapping.repos))
		for _, name := range names {
			repository := mapping.repos[name]
			if !slices.ContainsFunc(connected, func(listed string) bool { return strings.EqualFold(listed, repository) }) {
				return nil, fmt.Errorf("linear.%s.%s: %s is not a connected repository; add it to github.repositories or in Settings → Projects", mapping.key, name, repository)
			}
		}
	}
	tracker := &sources.Linear{Token: linear.Token, AppUserID: linear.AppUserID}
	if linear.UsesClientCredentials() {
		// The first poll, at startup, fetches the token.
		tracker.Credentials = &sources.LinearCredentials{ClientID: linear.ClientID, ClientSecret: linear.ClientSecret}
	}
	return &sources.Poller{
		Tracker:        tracker,
		Rules:          sources.TrackerRules{AllowedUsers: linear.AllowedUsers, Preset: preset, Projects: linear.Projects, Teams: linear.Teams},
		Intake:         intake,
		Store:          store,
		Interval:       linear.Poll,
		MaxJobsPerHour: linear.MaxJobsPerHour,
		Log:            log,
	}, nil
}

// loadApp loads the GitHub App, when one is configured. Its tokens last an
// hour. A local runner asks for a new one before each step, so no step may be
// allowed longer than that; the run as a whole may be. A Kubernetes run keeps
// the token it started with, and its connector refuses a run longer than one
// token.
func loadApp(machinistConfig config.Config, loaded *factory.Factory) (*githubapp.App, error) {
	settings := machinistConfig.GitHub.App
	if settings == nil {
		return nil, nil
	}
	for _, name := range slices.Sorted(maps.Keys(loaded.Presets)) {
		if err := stepsFitInAToken(name, loaded.Presets[name], machinistConfig.Server.RunDeadline()); err != nil {
			return nil, err
		}
	}
	return githubapp.Load(settings.ID, settings.PrivateKeyFile)
}

// stepsFitInAToken reports the first step of a preset that could run longer
// than a GitHub App token lasts. A step runs for its own timeout or, without
// one, for the rest of the run: the preset's timeout, else run_timeout.
func stepsFitInAToken(name string, preset factory.Preset, runDeadline time.Duration) error {
	run, source, fix := preset.Timeout, "the preset's timeout", "set the preset's timeout"
	if run == 0 {
		run, source, fix = runDeadline, "run_timeout", "set run_timeout"
	}
	for _, step := range preset.Steps {
		limit, from, change := run, source, fix+" or the step's timeout"
		if own := time.Duration(step.TimeoutMillis) * time.Millisecond; own > 0 && own < run {
			limit, from, change = own, "its own timeout", "set its timeout"
		}
		if limit > githubapp.TokenLifetime {
			return fmt.Errorf("preset %q step %q may run %s (%s), but GitHub App tokens last %s; %s to %s or less", name, step.Name, limit, from, githubapp.TokenLifetime, change, githubapp.TokenLifetime)
		}
	}
	return nil
}

func findPR(client *ghclient.Client) engine.PRFinder {
	return func(ctx context.Context, repository, branch string) (*engine.PR, error) {
		found, err := client.FindPR(ctx, repository, branch)
		if err != nil || found == nil {
			return nil, err
		}
		pr := &engine.PR{Number: found.Number, Closed: found.State == ghclient.StateClosed, Merged: found.State == ghclient.StateMerged}
		if pr.Merged {
			pr.ChangedAt = found.MergedAt
		} else if pr.Closed {
			pr.ChangedAt = found.ClosedAt
		}
		return pr, nil
	}
}

// SetPollInterval changes how often schedules are checked. Tests use it.
func (s *Server) SetPollInterval(interval time.Duration) {
	s.schedule.Interval = interval
}

// Handler returns the routes of the control plane.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("POST /api/v1/session", s.signIn)
	mux.HandleFunc("DELETE /api/v1/session", s.requireToken(s.signOut))
	mux.HandleFunc("POST /api/v1/tickets", s.requireToken(s.createTicket))
	mux.HandleFunc("GET /api/v1/tickets", s.requireToken(s.listTickets))
	mux.HandleFunc("GET /api/v1/tickets/{id}", s.requireToken(s.getTicket))
	mux.HandleFunc("POST /api/v1/tickets/{id}/cancel", s.requireToken(s.cancelTicket))
	mux.HandleFunc("POST /api/v1/tickets/{id}/rerun", s.requireToken(s.rerunTicket))
	mux.HandleFunc("GET /api/v1/runs/{id}/events", s.requireToken(s.runEvents))
	mux.HandleFunc("GET /api/v1/runs/{id}/patch", s.requireToken(s.runPatch))
	mux.HandleFunc("GET /api/v1/ledger", s.requireToken(s.ledger))
	mux.HandleFunc("GET /api/v1/usage", s.requireToken(s.usage))
	mux.HandleFunc("GET /api/v1/stats", s.requireToken(s.stats))
	mux.HandleFunc("POST /machinist/run", s.requireToken(s.runJob))
	mux.HandleFunc("GET /machinist/jobs", s.requireToken(s.listJobs))
	mux.HandleFunc("GET /machinist/jobs/{id}", s.requireToken(s.getJob))
	mux.HandleFunc("POST /machinist/jobs/{id}/cancel", s.requireToken(s.cancelJob))
	mux.HandleFunc("POST /machinist/jobs/{id}/rerun", s.requireToken(s.rerunJob))
	mux.HandleFunc("POST /api/v1/runners/poll", s.requireRunner(s.poll))
	mux.HandleFunc("GET /api/v1/runners", s.requireToken(s.listRunners))
	mux.HandleFunc("POST /api/v1/runs/{id}/heartbeat", s.requireRunner(s.heartbeat))
	mux.HandleFunc("POST /api/v1/runs/{id}/events", s.requireRunner(s.ingestEvents))
	mux.HandleFunc("POST /api/v1/runs/{id}/complete", s.requireRunner(s.complete))
	mux.HandleFunc("POST /api/v1/runs/{id}/github-token", s.requireRunner(s.runToken))
	mux.HandleFunc("GET /api/v1/repositories", s.requireToken(s.listRepositories))
	mux.HandleFunc("GET /api/v1/setup", s.requireToken(s.getSetup))
	mux.HandleFunc("POST /api/v1/projects", s.requireToken(s.addProject))
	mux.HandleFunc("DELETE /api/v1/projects/{owner}/{name}", s.requireToken(s.removeProject))
	mux.HandleFunc("GET /api/v1/presets", s.requireToken(s.listPresets))
	mux.HandleFunc("GET /api/v1/agents", s.requireToken(s.listAgents))
	mux.HandleFunc("GET /api/v1/settings/github", s.requireToken(s.githubSettings))
	mux.HandleFunc("GET /api/v1/settings/linear", s.requireToken(s.linearSettings))
	mux.HandleFunc("GET /api/v1/tokens", s.requireToken(s.listTokens))
	mux.HandleFunc("DELETE /api/v1/tokens/{name}", s.requireToken(s.revokeToken))
	mux.HandleFunc("GET /api/v1/automations", s.requireToken(s.listAutomations))
	mux.HandleFunc("POST /api/v1/automations/{name}/run", s.requireToken(s.runAutomation))
	mux.Handle("/", http.FileServer(http.FS(mustSub(webAssets, "web/dist"))))
	var handler http.Handler = mux
	if isLoopbackListen(s.config.Server.Listen) {
		handler = loopbackHostOnly(handler)
	}
	return securityHeaders(handler)
}

func mustSub(files fs.FS, directory string) fs.FS {
	sub, err := fs.Sub(files, directory)
	if err != nil {
		panic(err)
	}
	return sub
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' blob:; font-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": s.version})
}

// Serve runs the API and the engine until ctx ends.
func (s *Server) Serve(ctx context.Context, onListening func(net.Addr)) error {
	listener, err := net.Listen("tcp", s.config.Server.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.config.Server.Listen, err)
	}
	defer listener.Close()
	if onListening != nil {
		onListening(listener.Addr())
	}
	httpServer := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	served := make(chan error, 1)
	go func() {
		if s.config.Server.TLSCert != "" {
			served <- httpServer.ServeTLS(listener, s.config.Server.TLSCert, s.config.Server.TLSKey)
			return
		}
		served <- httpServer.Serve(listener)
	}()
	engineContext, stopEngine := context.WithCancel(ctx)
	defer stopEngine()
	engineDone := make(chan struct{})
	go func() { s.engine.Run(engineContext); close(engineDone) }()
	scheduleDone := make(chan struct{})
	go func() { s.schedule.Run(engineContext); close(scheduleDone) }()
	linearDone := make(chan struct{})
	go func() {
		if s.linear != nil {
			s.linear.Run(engineContext)
		}
		close(linearDone)
	}()

	select {
	case err = <-served:
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		err = errors.Join(httpServer.Shutdown(shutdown), <-served)
	}
	stopEngine()
	<-engineDone
	<-scheduleDone
	<-linearDone
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return false
	}
	return true
}
