package controlplane

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

// The runner records up to 64 MiB before base64 encoding. The JSON envelope remains
// below this limit, including per-event and string-escaping overhead.
const maxCompletionBytes = 96 << 20

const maxRequestBytes = 1 << 20

const workerAvailabilityWindow = 15 * time.Second

//go:embed web/dist/* web/dist/assets/*
var webAssets embed.FS

type Server struct {
	store             *Store
	definitionPath    string
	schedules         []config.ResolvedShepherdSchedule
	triggers          []config.ResolvedTrigger
	github            githubTriggerClient
	schedulerEvery    time.Duration
	now               func() time.Time
	schedulerError    func(error)
	shutdownTimeout   time.Duration
	maxConcurrentJobs int
	workerToken       string
	csrfToken         string
	handler           http.Handler
}

type statusResponse struct {
	Snapshot
	Agents       []string `json:"agents"`
	Pipelines    []string `json:"pipelines"`
	Repositories []string `json:"repositories"`
	CSRFToken    string   `json:"csrf_token"`
}

type submitRequest struct {
	Prompt     string `json:"prompt"`
	Repository string `json:"repository"`
	Agent      string `json:"agent"`
	Pipeline   string `json:"pipeline"`
	Model      string `json:"model"`
}

type agentDefinitionResponse struct {
	Name     string `json:"name"`
	Executor string `json:"executor"`
	Timeout  string `json:"timeout"`
	Hash     string `json:"hash"`
	Prompt   string `json:"prompt"`
}

type pipelineDefinitionResponse struct {
	Name   string   `json:"name"`
	Agents []string `json:"agents"`
}

type definitionsResponse struct {
	Agents    []agentDefinitionResponse    `json:"agents"`
	Pipelines []pipelineDefinitionResponse `json:"pipelines"`
}

type catalogResponse struct {
	Agents       []string `json:"agents"`
	Pipelines    []string `json:"pipelines"`
	Repositories []string `json:"repositories"`
}

func NewServer(store *Store, definitionPath, workerToken string, maxConcurrentJobs int) (*Server, error) {
	if maxConcurrentJobs < 0 {
		return nil, errors.New("max concurrent jobs cannot be negative")
	}
	csrfToken, err := randomID("csrf", 24)
	if err != nil {
		return nil, err
	}
	schedules, err := config.LoadShepherdSchedules(definitionPath)
	if err != nil {
		return nil, err
	}
	managedTriggers, err := config.LoadTriggers(definitionPath)
	if err != nil {
		return nil, err
	}
	startup := time.Now().UTC()
	definitions := make([]TriggerDefinition, 0, len(managedTriggers))
	for _, trigger := range managedTriggers {
		definitions = append(definitions, TriggerDefinition{
			Identity: trigger.Identity, Family: trigger.Family,
			ConfigSignature: trigger.Signature, NextDueAt: trigger.FirstDue(startup),
		})
	}
	if err := store.SyncTriggers(context.Background(), definitions); err != nil {
		return nil, fmt.Errorf("restore managed triggers: %w", err)
	}
	server := &Server{
		store: store, definitionPath: definitionPath, schedules: schedules, triggers: managedTriggers,
		github: NewGitHubCLI("gh", 30*time.Second), now: time.Now,
		schedulerEvery: 30 * time.Second, shutdownTimeout: 5 * time.Second,
		schedulerError:    func(err error) { log.Printf("scheduler: %v", err) },
		maxConcurrentJobs: maxConcurrentJobs, workerToken: workerToken, csrfToken: csrfToken,
	}
	server.handler, err = server.routes()
	if err != nil {
		return nil, err
	}
	return server, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) Serve(ctx context.Context, listen string, onListening func(net.Addr)) error {
	if err := validateLoopbackListen(listen); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listen, err)
	}
	defer listener.Close()
	if onListening != nil {
		onListening(listener.Addr())
	}
	httpServer := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	done := make(chan error, 1)
	go func() {
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	schedulerCtx, stopScheduler := context.WithCancel(ctx)
	defer stopScheduler()
	schedulerDone := make(chan error, 1)
	go func() { schedulerDone <- s.runScheduler(schedulerCtx) }()
	stopHTTP := func() error {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer cancel()
		shutdownErr := httpServer.Shutdown(shutdownCtx)
		var forceCloseErr error
		if shutdownErr != nil {
			forceCloseErr = httpServer.Close()
		}
		// Shutdown can run before Serve registers the listener when the
		// callback cancels the context. Close it explicitly and wait for the
		// serving goroutine so every cancellation path releases the socket.
		closeErr := listener.Close()
		<-done
		var shutdownFailure error
		if shutdownErr != nil {
			shutdownFailure = fmt.Errorf("stop control plane: %w", shutdownErr)
		}
		if forceCloseErr != nil && !errors.Is(forceCloseErr, http.ErrServerClosed) {
			shutdownFailure = errors.Join(shutdownFailure, fmt.Errorf("force close control plane: %w", forceCloseErr))
		}
		if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			shutdownFailure = errors.Join(shutdownFailure, fmt.Errorf("stop control plane listener: %w", closeErr))
		}
		return shutdownFailure
	}
	select {
	case err := <-done:
		stopScheduler()
		return errors.Join(err, <-schedulerDone)
	case err := <-schedulerDone:
		if shutdownErr := stopHTTP(); shutdownErr != nil && err == nil {
			return shutdownErr
		}
		return err
	case <-ctx.Done():
		stopScheduler()
		shutdownErr := stopHTTP()
		return errors.Join(shutdownErr, <-schedulerDone)
	}
}

func (s *Server) runScheduler(ctx context.Context) error {
	var schedulers sync.WaitGroup
	start := func(work func(context.Context) error) {
		schedulers.Add(1)
		go func() {
			defer schedulers.Done()
			for {
				s.reportSchedulerError(work(ctx))
				timer := time.NewTimer(s.schedulerEvery)
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					return
				case <-timer.C:
				}
			}
		}()
	}
	if len(s.schedules) > 0 {
		start(s.enqueueScheduledRuns)
	}
	for _, trigger := range s.triggers {
		trigger := trigger
		start(func(ctx context.Context) error {
			if err := s.processManagedTrigger(ctx, trigger); err != nil {
				return fmt.Errorf("trigger %q: %w", trigger.Identity, err)
			}
			return nil
		})
	}
	schedulers.Add(1)
	go func() {
		defer schedulers.Done()
		for {
			timer := time.NewTimer(s.schedulerEvery)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			case <-timer.C:
				s.reportSchedulerError(s.maintainState(ctx))
			}
		}
	}()
	<-ctx.Done()
	schedulers.Wait()
	return nil
}

func (s *Server) maintainState(ctx context.Context) error {
	_, reclaimErr := s.store.ReclaimExpiredLeases(ctx)
	_, pruneErr := s.store.PruneSupersededWorkers(ctx, s.store.now().UTC().Add(-workerAvailabilityWindow))
	return errors.Join(reclaimErr, pruneErr)
}

func (s *Server) enqueueScheduledRuns(ctx context.Context) error {
	var failures []error
	for _, schedule := range s.schedules {
		if _, _, err := s.store.CreateScheduledJob(ctx, schedule); err != nil {
			failures = append(failures, fmt.Errorf("queue shepherd schedule %q: %w", schedule.Name, err))
		}
	}
	return errors.Join(failures...)
}

func (s *Server) reportSchedulerError(err error) {
	if err != nil && !errors.Is(err, context.Canceled) && s.schedulerError != nil {
		s.schedulerError(err)
	}
}

func (s *Server) routes() (http.Handler, error) {
	dist, err := fs.Sub(webAssets, "web/dist")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("GET /api/v1/catalog", s.catalog)
	mux.HandleFunc("GET /api/v1/definitions", s.definitions)
	mux.HandleFunc("POST /api/v1/jobs", s.authorizeSubmission(s.submit))
	mux.HandleFunc("DELETE /api/v1/jobs/{id}", s.authorizeSubmission(s.deleteJob))
	mux.HandleFunc("POST /api/v1/workers/poll", s.authorizeWorker(s.poll))
	mux.HandleFunc("POST /api/v1/runs/{id}/heartbeat", s.authorizeWorker(s.heartbeat))
	mux.HandleFunc("POST /api/v1/runs/{id}/complete", s.authorizeWorker(s.complete))
	mux.Handle("/", http.FileServer(http.FS(dist)))
	return securityHeaders(mux), nil
}

func (s *Server) definitions(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	definition, err := config.LoadDefinitions(s.definitionPath)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	agents := make([]agentDefinitionResponse, 0, len(definition.Agents))
	for _, name := range mapKeys(definition.Agents) {
		agent, err := config.LoadAgent(s.definitionPath, name)
		if err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
		agents = append(agents, agentDefinitionResponse{Name: agent.Name, Executor: agent.Executor, Timeout: agent.Timeout.String(), Hash: agent.Hash, Prompt: agent.Prompt})
	}
	pipelines := make([]pipelineDefinitionResponse, 0, len(definition.Pipelines))
	for _, name := range mapKeys(definition.Pipelines) {
		resolved, err := config.LoadPipeline(s.definitionPath, name)
		if err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
		agentNames := make([]string, 0, len(resolved))
		for _, agent := range resolved {
			agentNames = append(agentNames, agent.Name)
		}
		pipelines = append(pipelines, pipelineDefinitionResponse{Name: name, Agents: agentNames})
	}
	writeJSON(response, http.StatusOK, definitionsResponse{Agents: agents, Pipelines: pipelines})
}

func (s *Server) status(response http.ResponseWriter, request *http.Request) {
	if err := s.maintainState(request.Context()); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	snapshot, err := s.store.Snapshot(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	definition, err := config.LoadDefinitions(s.definitionPath)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	now := s.store.now().UTC()
	for index := range snapshot.Workers {
		snapshot.Workers[index].Connected = !snapshot.Workers[index].LastSeenAt.Before(now.Add(-workerAvailabilityWindow))
	}
	repositories, repositoryErr := s.store.AvailableRepositories(request.Context(), now.Add(-workerAvailabilityWindow))
	if repositoryErr != nil {
		writeError(response, http.StatusInternalServerError, repositoryErr)
		return
	}
	writeJSON(response, http.StatusOK, statusResponse{
		Snapshot:     snapshot,
		Agents:       mapKeys(definition.Agents),
		Pipelines:    mapKeys(definition.Pipelines),
		Repositories: repositories,
		CSRFToken:    s.csrfToken,
	})
}

func (s *Server) catalog(response http.ResponseWriter, request *http.Request) {
	definition, err := config.LoadDefinitions(s.definitionPath)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	repositories, repositoryErr := s.store.KnownRepositories(request.Context())
	if repositoryErr != nil {
		writeError(response, http.StatusInternalServerError, repositoryErr)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, catalogResponse{
		Agents:       mapKeys(definition.Agents),
		Pipelines:    mapKeys(definition.Pipelines),
		Repositories: repositories,
	})
}

func (s *Server) submit(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxRequestBytes) {
		return
	}
	var input submitRequest
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	if strings.TrimSpace(input.Repository) == "" {
		writeError(response, http.StatusBadRequest, errors.New("repository is required"))
		return
	}
	repositories, repositoryErr := s.store.KnownRepositories(request.Context())
	if repositoryErr != nil {
		writeError(response, http.StatusInternalServerError, repositoryErr)
		return
	}
	if !containsString(repositories, input.Repository) {
		writeError(response, http.StatusBadRequest, fmt.Errorf("repository %q is not defined in the control plane", input.Repository))
		return
	}
	input.Model = strings.TrimSpace(input.Model)
	if len(input.Model) > 128 || strings.ContainsAny(input.Model, "\x00\r\n") {
		writeError(response, http.StatusBadRequest, errors.New("model must be at most 128 characters on one line"))
		return
	}
	if (input.Agent == "") == (input.Pipeline == "") {
		writeError(response, http.StatusBadRequest, errors.New("exactly one agent or pipeline is required"))
		return
	}
	var err error
	var (
		agents []config.ResolvedAgent
		kind   string
		name   string
	)
	if input.Agent != "" {
		kind, name = "agent", input.Agent
		var agent config.ResolvedAgent
		agent, err = config.LoadAgent(s.definitionPath, input.Agent)
		agents = []config.ResolvedAgent{agent}
	} else {
		kind, name = "pipeline", input.Pipeline
		agents, err = config.LoadPipeline(s.definitionPath, input.Pipeline)
	}
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	if err := config.ValidateModelSelection(agents, input.Model); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	for index := range agents {
		agents[index], err = config.RenderPrompt(agents[index], input.Prompt)
		if err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		agents[index].Model = input.Model
	}
	jobID, err := s.store.CreateJob(request.Context(), input.Prompt, input.Repository, kind, name, agents)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]string{"id": jobID})
}

func (s *Server) deleteJob(response http.ResponseWriter, request *http.Request) {
	err := s.store.DeleteJob(request.Context(), request.PathValue("id"))
	if errors.Is(err, ErrJobActive) {
		writeError(response, http.StatusConflict, err)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusNotFound, errors.New("job not found"))
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) poll(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxRequestBytes) {
		return
	}
	var input protocol.PollRequest
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	if strings.TrimSpace(input.InstanceID) == "" || strings.TrimSpace(input.Name) == "" {
		writeError(response, http.StatusBadRequest, errors.New("worker instance_id and name are required"))
		return
	}
	run, err := s.store.poll(request.Context(), input, s.maxConcurrentJobs)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, protocol.PollResponse{Run: run})
}

func (s *Server) complete(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxCompletionBytes) {
		return
	}
	var input protocol.Completion
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	if input.InstanceID == "" || input.LeaseToken == "" {
		writeError(response, http.StatusBadRequest, errors.New("instance_id and lease_token are required"))
		return
	}
	err := s.store.Complete(request.Context(), request.PathValue("id"), input)
	if errors.Is(err, ErrLeaseConflict) || errors.Is(err, ErrRunState) {
		writeError(response, http.StatusConflict, err)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusNotFound, errors.New("run not found"))
		return
	}
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) heartbeat(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxRequestBytes) {
		return
	}
	var input protocol.Heartbeat
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	if input.InstanceID == "" || input.LeaseToken == "" {
		writeError(response, http.StatusBadRequest, errors.New("instance_id and lease_token are required"))
		return
	}
	err := s.store.Heartbeat(request.Context(), request.PathValue("id"), input)
	if errors.Is(err, ErrLeaseConflict) || errors.Is(err, ErrRunState) {
		writeError(response, http.StatusConflict, err)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusNotFound, errors.New("run not found"))
		return
	}
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) authorizeWorker(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if !s.validBearerRequest(request) {
			writeUnauthorized(response)
			return
		}
		next(response, request)
	}
}

func (s *Server) authorizeSubmission(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "" {
			if !s.validBearerRequest(request) {
				writeUnauthorized(response)
				return
			}
		} else if !s.validBrowserRequest(request) {
			writeError(response, http.StatusForbidden, errors.New("invalid submission origin or CSRF token"))
			return
		}
		next(response, request)
	}
}

func (s *Server) validBearerRequest(request *http.Request) bool {
	provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	return provided != request.Header.Get("Authorization") && subtle.ConstantTimeCompare([]byte(provided), []byte(s.workerToken)) == 1
}

func writeUnauthorized(response http.ResponseWriter) {
	response.Header().Set("WWW-Authenticate", "Bearer")
	writeError(response, http.StatusUnauthorized, errors.New("invalid worker token"))
}

func (s *Server) validBrowserRequest(request *http.Request) bool {
	if subtle.ConstantTimeCompare([]byte(request.Header.Get("X-Machinist-CSRF")), []byte(s.csrfToken)) != 1 {
		return false
	}
	origin, err := url.Parse(request.Header.Get("Origin"))
	if err != nil || origin.Scheme != "http" || !strings.EqualFold(origin.Host, request.Host) {
		return false
	}
	hostname := origin.Hostname()
	return hostname == "localhost" || net.ParseIP(hostname) != nil && net.ParseIP(hostname).IsLoopback()
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func validateLoopbackListen(listen string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", listen, err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen address %q must use a loopback host", listen)
	}
	return nil
}

func decodeJSON(request *http.Request, target any) error {
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("decode trailing JSON: %w", err)
		}
		return errors.New("request contains multiple JSON values")
	}
	return nil
}

func limitRequestBody(response http.ResponseWriter, request *http.Request, limit int64) bool {
	if request.ContentLength > limit {
		writeError(response, http.StatusRequestEntityTooLarge, errors.New("request body is too large"))
		return false
	}
	request.Body = http.MaxBytesReader(response, request.Body, limit)
	return true
}

func writeJSON(response http.ResponseWriter, status int, body any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}

func writeError(response http.ResponseWriter, status int, err error) {
	writeJSON(response, status, map[string]string{"error": err.Error()})
}

func writeDecodeError(response http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		writeError(response, http.StatusRequestEntityTooLarge, errors.New("request body is too large"))
		return
	}
	writeError(response, http.StatusBadRequest, err)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(response, request)
	})
}

func mapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
