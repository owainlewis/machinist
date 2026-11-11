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
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/engine"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/ghclient"
	"github.com/owainlewis/machinist/internal/mcpserver"
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
	github   *sources.GitHub
	intake   *sources.Intake
	schedule *sources.Schedule
	mcp      http.Handler
	version  string
	log      io.Writer
}

func New(machinistConfig config.Config, store *tickets.Store, loaded *factory.Factory, version string, log io.Writer) (*Server, error) {
	if machinistConfig.GitHub.Handle == "" {
		return nil, errors.New("github.handle is required")
	}
	if err := loaded.Validate(machinistConfig.Server.DefaultAgent, machinistConfig.GitHub.Repositories); err != nil {
		return nil, err
	}
	client := ghclient.New()
	ticketEngine := engine.New(engine.Options{
		Store:      store,
		Factory:    loaded,
		MaxTurns:   machinistConfig.Server.MaxTurnsWithoutHuman,
		RunTimeout: machinistConfig.Server.RunDeadline(),
		Handle:     machinistConfig.GitHub.Handle,
		FindPR:     findPR(client),
		Post:       client.PostComment,
		Log:        log,
	})
	intake := &sources.Intake{
		Client:       client,
		Store:        store,
		DefaultAgent: machinistConfig.Server.DefaultAgent,
		Repositories: machinistConfig.GitHub.Repositories,
	}
	return &Server{
		config:   machinistConfig,
		store:    store,
		engine:   ticketEngine,
		client:   client,
		intake:   intake,
		schedule: sources.NewSchedule(store, loaded.Automations, log),
		mcp:      mcpserver.New(store, intake, version),
		version:  version,
		log:      log,
		github: sources.NewGitHub(sources.Options{
			Client:       client,
			Store:        store,
			Engine:       ticketEngine,
			Handle:       machinistConfig.GitHub.Handle,
			Repositories: machinistConfig.GitHub.Repositories,
			DefaultAgent: machinistConfig.Server.DefaultAgent,
			Log:          log,
		}),
	}, nil
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

// SetPollInterval changes how often GitHub and schedules are polled. Tests use it.
func (s *Server) SetPollInterval(interval time.Duration) {
	s.github.Interval, s.schedule.Interval = interval, interval
}

// checkGitHub refuses a token that is not the bot's, because Machinist would
// then read its own messages as human replies and start runs on them. Other
// findings only warn, so that a GitHub outage cannot stop a restart.
func (s *Server) checkGitHub(ctx context.Context) error {
	login, err := s.client.CurrentUser(ctx)
	if err != nil {
		fmt.Fprintf(s.log, "machinist: warning: could not check the GitHub login: %v\n", err)
	} else if !strings.EqualFold(login, s.config.GitHub.Handle) {
		return fmt.Errorf("the GitHub token belongs to %s, but github.handle is %s; set GH_TOKEN to a token for the bot account", login, s.config.GitHub.Handle)
	}
	for _, repository := range s.config.GitHub.Repositories {
		access, err := s.client.RepositoryAccess(ctx, repository)
		if err != nil {
			fmt.Fprintf(s.log, "machinist: warning: could not check %s: %v\n", repository, err)
			continue
		}
		if !access.Protected {
			fmt.Fprintf(s.log, "machinist: warning: %s branch %s has no protection\n", repository, access.DefaultBranch)
		}
		if access.Admin {
			fmt.Fprintf(s.log, "machinist: warning: the GitHub login has admin rights on %s\n", repository)
		}
	}
	return nil
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
	mux.HandleFunc("POST /api/v1/tickets/{id}/answer", s.requireToken(s.answerTicket))
	mux.HandleFunc("POST /api/v1/tickets/{id}/cancel", s.requireToken(s.cancelTicket))
	mux.HandleFunc("GET /api/v1/runs/{id}/events", s.requireToken(s.runEvents))
	mux.HandleFunc("GET /api/v1/ledger", s.requireToken(s.ledger))
	mux.HandleFunc("/mcp", s.requireToken(s.serveMCP))
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
	if err := s.checkGitHub(ctx); err != nil {
		return err
	}
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
	githubDone := make(chan struct{})
	go func() { s.github.Run(engineContext); close(githubDone) }()
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
	<-githubDone
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
