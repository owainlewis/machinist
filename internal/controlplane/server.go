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
	"mime"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/engine"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/ghclient"
	"github.com/owainlewis/machinist/internal/githubapp"
	"github.com/owainlewis/machinist/internal/ship"
	"github.com/owainlewis/machinist/internal/sources"
	"github.com/owainlewis/machinist/internal/tickets"
)

//go:embed all:web/dist
var webAssets embed.FS

const (
	maxRequestBytes           = 1 << 20
	httpShutdownTimeout       = 6 * time.Second
	backgroundShutdownTimeout = 2 * time.Second
	httpReadHeaderTimeout     = 5 * time.Second
	httpReadTimeout           = 30 * time.Second
	httpWriteTimeout          = 120 * time.Second
	httpIdleTimeout           = 60 * time.Second
)

// Server is the one control plane of a deployment. It never starts an agent.
type Server struct {
	mu       sync.RWMutex
	reloadMu sync.Mutex
	config   config.Config
	store    *tickets.Store
	engine   *engine.Engine
	client   *ghclient.Client
	intake   *sources.Intake
	schedule *sources.Schedule
	version  string
	log      io.Writer
	// linear takes work from Linear, when it is on.
	linear        *sources.Poller
	linearChanged chan struct{}
	now           func() time.Time
	presets       []presetView
	factory       *factory.Factory
	// app is the GitHub App, or nil when there is none.
	app             githubApp
	appKeyHash      [32]byte
	csrf            *http.CrossOriginProtection
	authLimiter     *ipRateLimiter
	auditLog        *auditLogger
	githubOAuthBase string
	githubAPIBase   string
	oauthHTTPClient *http.Client
	githubCache     githubLookupCache
	githubSem       chan struct{}
	leaderCfg       leaderConfig
	bgWorkers       func(context.Context)
}

type serverState struct {
	config     config.Config
	factory    *factory.Factory
	presets    []presetView
	app        githubApp
	appKeyHash [32]byte
	linear     *sources.Poller
}

func (s *Server) state() serverState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return serverState{
		config:     s.config,
		factory:    s.factory,
		presets:    s.presets,
		app:        s.app,
		appKeyHash: s.appKeyHash,
		linear:     s.linear,
	}
}

func (s *Server) notifyLinear() {
	if s.linearChanged == nil {
		return
	}
	select {
	case s.linearChanged <- struct{}{}:
	default:
	}
}

func (s *Server) runLinear(ctx context.Context) {
	for {
		poller := s.state().linear
		if poller == nil {
			select {
			case <-ctx.Done():
				return
			case <-s.linearChanged:
				continue
			}
		}
		pollCtx, cancelPoll := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			poller.Run(pollCtx)
			close(done)
		}()
		select {
		case <-ctx.Done():
			cancelPoll()
			<-done
			return
		case <-s.linearChanged:
			cancelPoll()
			<-done
		}
	}
}

func New(machinistConfig config.Config, store *tickets.Store, loaded *factory.Factory, version string, log io.Writer) (*Server, error) {
	if machinistConfig.GitHub.Handle == "" {
		return nil, errors.New("github.handle is required")
	}
	if err := validateAuthStartup(machinistConfig); err != nil {
		return nil, err
	}
	if err := loaded.Validate(machinistConfig.Server.DefaultPreset, machinistConfig.GitHub.Repositories); err != nil {
		return nil, err
	}
	app, appKeyHash, err := loadAppWithHash(machinistConfig, loaded)
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
	var notifier engine.Notifier
	if machinistConfig.Notify.Webhook != "" {
		notifier = engine.NewWebhookNotifier(machinistConfig.Notify.Webhook, machinistConfig.Server.PublicURL, log)
	}
	ticketEngine := engine.New(engine.Options{
		Store:          store,
		Factory:        loaded,
		RunTimeout:     machinistConfig.Server.RunDeadline(),
		Handle:         machinistConfig.GitHub.Handle,
		AutoMerge:      machinistConfig.GitHub.AutoMerges(),
		StewardRetries: machinistConfig.GitHub.StewardRetryLimit(),
		FindPR:         findPR(client),
		PullRequests: func(repository string, number int) ship.GitHub {
			return pullRequest{client: client, repository: repository, number: number}
		},
		Reviews:     humanReviews(client),
		Post:        client.PostComment,
		Notify:      notifier,
		GitHubToken: tokens,
		Identity:    identity,
		Log:         log,
	})
	intake := &sources.Intake{
		Client:        client,
		Store:         store,
		DefaultPreset: machinistConfig.Server.DefaultPreset,
		Repositories:  machinistConfig.GitHub.Repositories,
		Presets:       loaded.Preset,
	}
	linear, err := linearPoller(context.Background(), machinistConfig, loaded, intake, store, log)
	if err != nil {
		return nil, err
	}
	schedule := sources.NewSchedule(store, loaded.Automations, log)
	schedule.Intake = intake
	server := &Server{
		linear:          linear,
		linearChanged:   make(chan struct{}, 1),
		now:             time.Now,
		config:          machinistConfig,
		store:           store,
		engine:          ticketEngine,
		client:          client,
		intake:          intake,
		schedule:        schedule,
		version:         version,
		presets:         presetViews(loaded),
		factory:         loaded,
		appKeyHash:      appKeyHash,
		log:             log,
		csrf:            http.NewCrossOriginProtection(),
		authLimiter:     newIPRateLimiter(authRateLimitMaxEntries, authRateLimitPerSec, authRateLimitBurst, time.Now),
		auditLog:        newAuditLogger(log),
		githubOAuthBase: defaultGitHubOAuthBase,
		githubAPIBase:   defaultGitHubAPIBase,
		oauthHTTPClient: &http.Client{Timeout: 10 * time.Second},
		githubSem:       make(chan struct{}, maxConcurrentGitHubLookups),
	}
	if app != nil {
		server.app = app
	}
	if log != nil {
		if config.IsPostgresURL(machinistConfig.Server.Database) {
			fmt.Fprintf(log, "machinist: storage backend: postgres (%s)\n", config.RedactDatabaseURL(machinistConfig.Server.Database))
		} else if machinistConfig.Server.Database != "" {
			fmt.Fprintf(log, "machinist: storage backend: sqlite (%s)\n", machinistConfig.Server.Database)
		}
	}
	if warning := factory.WarningLine(loaded.Updates); warning != "" {
		fmt.Fprintf(log, "machinist: warning: %s\n", warning)
	}
	if store != nil && publicAuthActive(machinistConfig) {
		if hasLegacy, err := store.HasActiveRoleAllToken(context.Background()); err == nil && hasLegacy {
			fmt.Fprintf(log, "machinist: warning: active legacy role-less token(s) exist and will be rejected on this public deployment; replace them with typed operator or --runner tokens\n")
		}
	}
	return server, nil
}

// validateAuthStartup enforces fail-closed public deployment guardrails:
//   - Non-loopback server.public_url requires auth.github.enabled: true unless
//     auth.allow_token_only_public: true is explicitly set.
//   - auth.github.enabled: true requires server.public_url and OAuth env secrets.
func validateAuthStartup(cfg config.Config) error {
	if svc := strings.TrimSpace(os.Getenv("K_SERVICE")); svc != "" {
		if !config.IsPostgresURL(cfg.Server.Database) {
			return fmt.Errorf("refusing to start on Cloud Run (K_SERVICE=%q) with SQLite database; set %s to a PostgreSQL connection URL", svc, config.DatabaseURLEnv)
		}
		if cfg.Server.PublicURL == "" && !cfg.Auth.AllowTokenOnlyPublic {
			return fmt.Errorf("refusing to start on Cloud Run (K_SERVICE=%q) without server.public_url (set server.public_url or auth.allow_token_only_public: true)", svc)
		}
	}
	if isPublicNonLoopback(cfg.Server.PublicURL) && !cfg.Auth.GitHub.Enabled && !cfg.Auth.AllowTokenOnlyPublic {
		return fmt.Errorf("refusing to start on non-loopback server.public_url %q without auth.github.enabled: true (set auth.allow_token_only_public: true only if you intentionally want token-only public auth)", cfg.Server.PublicURL)
	}
	if cfg.Auth.GitHub.Enabled {
		if cfg.Server.PublicURL == "" {
			return errors.New("server.public_url is required when auth.github.enabled is true")
		}
		if err := cfg.Auth.CheckCredentials(); err != nil {
			return err
		}
	}
	return nil
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
	loadedPreset, ok := loaded.Preset(preset)
	if !ok {
		return nil, fmt.Errorf("linear.preset %q is not in the factory", preset)
	}
	if required := loadedPreset.RequiredInputs(); len(required) > 0 {
		return nil, fmt.Errorf("linear.preset %q declares required inputs (%s) and cannot be used with Linear", preset, strings.Join(required, ", "))
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
		pr := &engine.PR{Number: found.Number, Closed: found.State == ghclient.StateClosed, Merged: found.State == ghclient.StateMerged, OpenedAt: found.CreatedAt}
		if pr.Merged {
			pr.ChangedAt = found.MergedAt
		} else if pr.Closed {
			pr.ChangedAt = found.ClosedAt
		}
		return pr, nil
	}
}

// pullRequest is one pull request the engine watches until its checks pass.
type pullRequest struct {
	client     *ghclient.Client
	repository string
	number     int
}

func (p pullRequest) PullRequest(ctx context.Context) (ship.PullRequest, error) {
	found, err := p.client.PullRequest(ctx, p.repository, p.number)
	return ship.PullRequest(found), err
}

func (p pullRequest) Checks(ctx context.Context) ([]ship.Check, error) {
	found, err := p.client.Checks(ctx, p.repository, p.number)
	checks := make([]ship.Check, len(found))
	for i, check := range found {
		checks[i] = ship.Check(check)
	}
	return checks, err
}

func (p pullRequest) Merge(ctx context.Context, head string) error {
	return githubAnswer(p.client.MergePR(ctx, p.repository, p.number, head))
}

func (p pullRequest) UpdateBranch(ctx context.Context) error {
	return githubAnswer(p.client.UpdateBranch(ctx, p.repository, p.number))
}

// githubAnswer turns a ghclient failure from Merge or UpdateBranch into either
// a ship.TransientError (so the watcher keeps waiting and retries) or GitHub's
// refusal message, which the person reads in why their pull request stays open.
func githubAnswer(err error) error {
	if err == nil {
		return nil
	}
	if ghclient.IsTransient(err) {
		return &ship.TransientError{Err: err}
	}
	var failure *ghclient.Error
	if errors.As(err, &failure) && failure.Detail != "" {
		return errors.New(failure.Detail)
	}
	return err
}

// SetPRInterval changes how often open pull requests are checked. Tests use it.
func (s *Server) SetPRInterval(interval time.Duration) {
	s.engine.PRInterval = interval
}

// humanReviews counts the reviews people submitted on a pull request up to
// until (when non-zero). Bots' reviews and post-merge reviews are left out.
func humanReviews(client *ghclient.Client) engine.ReviewReader {
	return func(ctx context.Context, repository string, number int, until time.Time) (tickets.Reviews, error) {
		found, err := client.Reviews(ctx, repository, number)
		if err != nil {
			return tickets.Reviews{}, err
		}
		return countHumanReviews(found, until), nil
	}
}

func countHumanReviews(found []ghclient.Review, until time.Time) tickets.Reviews {
	var reviews tickets.Reviews
	for _, review := range found {
		if review.Bot {
			continue
		}
		if !until.IsZero() && review.SubmittedAt.After(until) {
			continue
		}
		if reviews.First == nil || review.SubmittedAt.Before(*reviews.First) {
			at := review.SubmittedAt
			reviews.First = &at
		}
		reviews.Count++
	}
	return reviews
}

// SetPollInterval changes how often schedules are checked. Tests use it.
func (s *Server) SetPollInterval(interval time.Duration) {
	s.schedule.Interval = interval
}

// Handler returns the routes of the control plane.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /api/v1/auth/status", s.authStatus)
	mux.HandleFunc("GET /auth/github/start", s.githubOAuthStart)
	mux.HandleFunc("GET /auth/github/callback", s.githubOAuthCallback)
	mux.HandleFunc("POST /api/v1/invites/prepare", s.prepareInvite)
	mux.HandleFunc("/mcp", s.handleMCP)
	mux.HandleFunc("POST /api/v1/session", s.signIn)
	mux.HandleFunc("DELETE /api/v1/session", s.requireScope(tickets.ScopeRead, s.signOut))
	mux.HandleFunc("GET /api/v1/me", s.requireScope(tickets.ScopeRead, s.getMe))
	mux.HandleFunc("GET /api/v1/me/sessions", s.requireScope(tickets.ScopeRead, s.listMySessions))
	mux.HandleFunc("DELETE /api/v1/me/sessions/{id}", s.requireScope(tickets.ScopeRead, s.revokeMySession))
	mux.HandleFunc("POST /api/v1/tickets", s.requireScope(tickets.ScopeRun, s.createTicket))
	mux.HandleFunc("GET /api/v1/tickets", s.requireScope(tickets.ScopeRead, s.listTickets))
	mux.HandleFunc("GET /api/v1/tickets/{id}", s.requireScope(tickets.ScopeRead, s.getTicket))
	mux.HandleFunc("DELETE /api/v1/tickets/{id}", s.requireToken(s.deleteTicket))
	mux.HandleFunc("POST /api/v1/tickets/{id}/cancel", s.requireScope(tickets.ScopeRun, s.cancelTicket))
	mux.HandleFunc("POST /api/v1/tickets/{id}/rerun", s.requireScope(tickets.ScopeRun, s.rerunTicket))
	mux.HandleFunc("GET /api/v1/runs/{id}/events", s.requireScope(tickets.ScopeRead, s.runEvents))
	mux.HandleFunc("GET /api/v1/runs/{id}/patch", s.requireScope(tickets.ScopeRead, s.runPatch))
	mux.HandleFunc("GET /api/v1/ledger", s.requireScope(tickets.ScopeRead, s.ledger))
	mux.HandleFunc("GET /api/v1/usage", s.requireScope(tickets.ScopeRead, s.usage))
	mux.HandleFunc("GET /api/v1/stats", s.requireScope(tickets.ScopeRead, s.stats))
	mux.HandleFunc("POST /machinist/run", s.requireScope(tickets.ScopeRun, s.runJob))
	mux.HandleFunc("GET /machinist/jobs", s.requireScope(tickets.ScopeRead, s.listJobs))
	mux.HandleFunc("GET /machinist/jobs/{id}", s.requireScope(tickets.ScopeRead, s.getJob))
	mux.HandleFunc("DELETE /machinist/jobs/{id}", s.requireToken(s.deleteJob))
	mux.HandleFunc("POST /machinist/jobs/{id}/cancel", s.requireScope(tickets.ScopeRun, s.cancelJob))
	mux.HandleFunc("POST /machinist/jobs/{id}/rerun", s.requireScope(tickets.ScopeRun, s.rerunJob))
	mux.HandleFunc("POST /api/v1/runners/poll", s.requireRunner(s.poll))
	mux.HandleFunc("GET /api/v1/runners", s.requireScope(tickets.ScopeRead, s.listRunners))
	mux.HandleFunc("DELETE /api/v1/runners/{name}", s.requireToken(s.removeRunner))
	mux.HandleFunc("POST /api/v1/runs/{id}/heartbeat", s.requireRunner(s.heartbeat))
	mux.HandleFunc("POST /api/v1/runs/{id}/events", s.requireRunner(s.ingestEvents))
	mux.HandleFunc("POST /api/v1/runs/{id}/complete", s.requireRunner(s.complete))
	mux.HandleFunc("POST /api/v1/runs/{id}/github-token", s.requireRunner(s.runToken))
	mux.HandleFunc("GET /api/v1/repositories", s.requireScope(tickets.ScopeRead, s.listRepositories))
	mux.HandleFunc("GET /api/v1/setup", s.requireToken(s.getSetup))
	mux.HandleFunc("POST /api/v1/projects", s.requireToken(s.addProject))
	mux.HandleFunc("DELETE /api/v1/projects/{owner}/{name}", s.requireToken(s.removeProject))
	mux.HandleFunc("GET /api/v1/presets", s.requireScope(tickets.ScopeRead, s.listPresets))
	mux.HandleFunc("GET /api/v1/agents", s.requireScope(tickets.ScopeRead, s.listAgents))
	mux.HandleFunc("GET /api/v1/settings/github", s.requireToken(s.githubSettings))
	mux.HandleFunc("GET /api/v1/settings/linear", s.requireToken(s.linearSettings))
	mux.HandleFunc("GET /api/v1/tokens", s.requireToken(s.listTokens))
	mux.HandleFunc("POST /api/v1/tokens", s.requireToken(s.createToken))
	mux.HandleFunc("POST /api/v1/tokens/{name}/regenerate", s.requireToken(s.regenerateToken))
	mux.HandleFunc("DELETE /api/v1/tokens/{name}", s.requireToken(s.revokeToken))
	mux.HandleFunc("GET /api/v1/team/members", s.requireToken(s.listTeamMembers))
	mux.HandleFunc("PATCH /api/v1/team/members/{id}", s.requireToken(s.updateTeamMember))
	mux.HandleFunc("GET /api/v1/team/invites", s.requireToken(s.listTeamInvites))
	mux.HandleFunc("POST /api/v1/team/invites", s.requireToken(s.createTeamInvite))
	mux.HandleFunc("POST /api/v1/team/invites/{id}/regenerate", s.requireToken(s.regenerateTeamInvite))
	mux.HandleFunc("DELETE /api/v1/team/invites/{id}", s.requireToken(s.revokeTeamInvite))
	mux.HandleFunc("GET /api/v1/automations", s.requireScope(tickets.ScopeRead, s.listAutomations))
	mux.HandleFunc("POST /api/v1/automations", s.requireScope(tickets.ScopeRun, s.createAutomation))
	mux.HandleFunc("PUT /api/v1/automations/{name}", s.requireScope(tickets.ScopeRun, s.updateAutomation))
	mux.HandleFunc("DELETE /api/v1/automations/{name}", s.requireScope(tickets.ScopeRun, s.deleteAutomation))
	mux.HandleFunc("POST /api/v1/automations/{name}/run", s.requireScope(tickets.ScopeRun, s.runAutomation))
	mux.HandleFunc("POST /api/v1/reload", s.requireToken(s.reload))
	mux.Handle("/", http.FileServer(http.FS(mustSub(webAssets, "web/dist"))))
	var handler http.Handler = s.csrfMiddleware(mux)
	cfg := s.state().config
	loopback := isLoopbackListen(cfg.Server.Listen)
	if cfg.Server.PublicURL != "" {
		handler = canonicalHostOnly(cfg.Server.PublicURL, handler)
	}
	if loopback {
		handler = loopbackHostOnly(publicURLHost(cfg.Server.PublicURL), handler)
	}
	return s.securityHeaders(handler)
}

func mustSub(files fs.FS, directory string) fs.FS {
	sub, err := fs.Sub(files, directory)
	if err != nil {
		panic(err)
	}
	return sub
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' blob: https://avatars.githubusercontent.com; font-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if isHTTPS(s.state().config, r) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	if isPublicNonLoopback(s.state().config.Server.PublicURL) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"version": s.version})
}

// Serve runs the API and the engine until ctx ends.
func (s *Server) Serve(ctx context.Context, onListening func(net.Addr)) error {
	cfg := s.state().config
	listener, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Server.Listen, err)
	}
	defer listener.Close()
	if onListening != nil {
		onListening(listener.Addr())
	}
	httpServer := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		WriteTimeout:      httpWriteTimeout,
		IdleTimeout:       httpIdleTimeout,
	}
	served := make(chan error, 1)
	go func() {
		if cfg.Server.TLSCert != "" {
			served <- httpServer.ServeTLS(listener, cfg.Server.TLSCert, cfg.Server.TLSKey)
			return
		}
		served <- httpServer.Serve(listener)
	}()
	bgCtx, stopBG := context.WithCancel(ctx)
	defer stopBG()
	drainCtx, cancelDrain := context.WithCancel(context.Background())
	defer cancelDrain()
	bgDone := make(chan struct{})
	go func() {
		defer close(bgDone)
		s.runLeaderSupervisorWithDrain(bgCtx, drainCtx)
	}()

	select {
	case err = <-served:
	case <-ctx.Done():
		// Parallel SIGTERM shutdown (Cloud Run 10s budget): cancel background
		// loops immediately while draining active HTTP requests (6s max).
		stopBG()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), httpShutdownTimeout)
		defer cancel()
		err = errors.Join(httpServer.Shutdown(shutdown), <-served)
	}
	stopBG()
	drainBudget := s.leaderCfg.drainTimeout
	if drainBudget <= 0 {
		drainBudget = backgroundShutdownTimeout
	}
	drainTimer := time.AfterFunc(drainBudget, cancelDrain)
	defer drainTimer.Stop()
	<-bgDone
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) runAuthPrune(ctx context.Context) {
	if s.store == nil {
		return
	}
	_ = s.store.PruneExpiredAuth(ctx)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.store.PruneExpiredAuth(ctx)
		}
	}
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
	if !hasBearerHeader(r) {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, errors.New("Content-Type must be application/json"))
			return false
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return false
	}
	return true
}
