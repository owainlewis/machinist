// Package controlplane serves the HTTP API and owns the ticket engine.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/engine"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/tickets"
)

const (
	maxRequestBytes = 1 << 20
	shutdownTimeout = 10 * time.Second
)

// Server is the one control plane of a deployment. It never starts an agent.
type Server struct {
	config  config.Config
	store   *tickets.Store
	engine  *engine.Engine
	version string
	log     io.Writer

	runnersMu sync.Mutex
	runners   map[string]runnerStatus
}

func New(machinistConfig config.Config, store *tickets.Store, loaded *factory.Factory, version string, log io.Writer) (*Server, error) {
	if machinistConfig.GitHub.Handle == "" {
		return nil, errors.New("github.handle is required")
	}
	if err := loaded.Validate(machinistConfig.Server.DefaultAgent, machinistConfig.GitHub.Repositories); err != nil {
		return nil, err
	}
	return &Server{
		config:  machinistConfig,
		store:   store,
		version: version,
		log:     log,
		runners: map[string]runnerStatus{},
		engine: engine.New(engine.Options{
			Store:      store,
			Factory:    loaded,
			MaxTurns:   machinistConfig.Server.MaxTurnsWithoutHuman,
			RunTimeout: machinistConfig.Server.RunDeadline(),
			Handle:     machinistConfig.GitHub.Handle,
			Log:        log,
		}),
	}, nil
}

// Handler returns the routes of the control plane.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("POST /api/v1/runners/poll", s.requireToken(s.poll))
	mux.HandleFunc("GET /api/v1/runners", s.requireToken(s.listRunners))
	mux.HandleFunc("POST /api/v1/runs/{id}/heartbeat", s.requireToken(s.heartbeat))
	mux.HandleFunc("POST /api/v1/runs/{id}/events", s.requireToken(s.ingestEvents))
	mux.HandleFunc("POST /api/v1/runs/{id}/complete", s.requireToken(s.complete))
	return mux
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

	select {
	case err = <-served:
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		err = errors.Join(httpServer.Shutdown(shutdown), <-served)
	}
	stopEngine()
	<-engineDone
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
