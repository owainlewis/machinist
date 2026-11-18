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
	"net"
	"net/http"
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
	if app != nil {
		client.WithToken(app.CachedToken)
		tokens = app.Token
	}
	ticketEngine := engine.New(engine.Options{
		Store:       store,
		Factory:     loaded,
		RunTimeout:  machinistConfig.Server.RunDeadline(),
		Handle:      machinistConfig.GitHub.Handle,
		FindPR:      findPR(client),
		Post:        client.PostComment,
		GitHubToken: tokens,
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
	return &Server{
		config:   machinistConfig,
		store:    store,
		engine:   ticketEngine,
		client:   client,
		intake:   intake,
		schedule: sources.NewSchedule(store, loaded.Automations, log),
		version:  version,
		log:      log,
	}, nil
}

// loadApp loads the GitHub App, when one is configured. Its tokens last an
// hour, so no run may be allowed longer than that.
func loadApp(machinistConfig config.Config, loaded *factory.Factory) (*githubapp.App, error) {
	settings := machinistConfig.GitHub.App
	if settings == nil {
		return nil, nil
	}
	if machinistConfig.Server.RunDeadline() > githubapp.TokenLifetime {
		return nil, fmt.Errorf("run_timeout is %s, but GitHub App tokens last %s; set run_timeout to %s or less", machinistConfig.Server.RunDeadline(), githubapp.TokenLifetime, githubapp.TokenLifetime)
	}
	for name, preset := range loaded.Presets {
		if preset.Timeout > githubapp.TokenLifetime {
			return nil, fmt.Errorf("preset %q has a timeout of %s, but GitHub App tokens last %s; set it to %s or less", name, preset.Timeout, githubapp.TokenLifetime, githubapp.TokenLifetime)
		}
	}
	return githubapp.Load(settings.ID, settings.PrivateKeyFile)
}

func findPR(client *ghclient.Client) engine.PRFinder {
	return func(ctx context.Context, repository, branch string) (*engine.PR, error) {
		found, err := client.FindPR(ctx, repository, branch)
		if err != nil || found == nil {
			return nil, err
		}
		return &engine.PR{Number: found.Number, Closed: found.State == ghclient.StateClosed}, nil
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
	mux.HandleFunc("GET /api/v1/runs/{id}/events", s.requireToken(s.runEvents))
	mux.HandleFunc("GET /api/v1/ledger", s.requireToken(s.ledger))
	mux.HandleFunc("GET /api/v1/usage", s.requireToken(s.usage))
	mux.HandleFunc("POST /machinist/run", s.requireToken(s.runJob))
	mux.HandleFunc("GET /machinist/jobs", s.requireToken(s.listJobs))
	mux.HandleFunc("GET /machinist/jobs/{id}", s.requireToken(s.getJob))
	mux.HandleFunc("POST /machinist/jobs/{id}/cancel", s.requireToken(s.cancelJob))
	mux.HandleFunc("POST /api/v1/runners/poll", s.requireToken(s.poll))
	mux.HandleFunc("GET /api/v1/runners", s.requireToken(s.listRunners))
	mux.HandleFunc("POST /api/v1/runs/{id}/heartbeat", s.requireToken(s.heartbeat))
	mux.HandleFunc("POST /api/v1/runs/{id}/events", s.requireToken(s.ingestEvents))
	mux.HandleFunc("POST /api/v1/runs/{id}/complete", s.requireToken(s.complete))
	mux.HandleFunc("GET /api/v1/repositories", s.requireToken(s.listRepositories))
	mux.HandleFunc("GET /api/v1/automations", s.requireToken(s.listAutomations))
	mux.HandleFunc("POST /api/v1/automations/{name}/run", s.requireToken(s.runAutomation))
	mux.Handle("/", http.FileServer(http.FS(mustSub(webAssets, "web/dist"))))
	return securityHeaders(mux)
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
