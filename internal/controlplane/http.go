package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

type API struct {
	store  *Store
	logger *slog.Logger
}

const workerConnectionTimeout = 12 * time.Second

type workerRegistrationRequest struct {
	protocol.WorkerRegistration
	Runtime        registrationString `json:"runtime"`
	RuntimeVersion registrationString `json:"runtime_version"`
	CodexVersion   registrationString `json:"codex_version"`
}

type registrationString struct {
	Value   string
	Present bool
}

func (value *registrationString) UnmarshalJSON(body []byte) error {
	value.Present = true
	if bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		return nil
	}
	return json.Unmarshal(body, &value.Value)
}

type legacyWorkerResponse struct {
	ID                string                      `json:"id"`
	Name              string                      `json:"name"`
	WorkerVersion     string                      `json:"worker_version"`
	CodexVersion      string                      `json:"codex_version"`
	Capacity          int                         `json:"capacity"`
	ActiveCount       int                         `json:"active_count"`
	Health            string                      `json:"health"`
	Online            bool                        `json:"online"`
	Repositories      []protocol.Repository       `json:"repositories"`
	RetainedWorktrees []protocol.RetainedWorktree `json:"retained_worktrees"`
	CurrentRunTitle   string                      `json:"current_run_title,omitempty"`
	RegisteredAt      time.Time                   `json:"registered_at"`
	LastHeartbeat     time.Time                   `json:"last_heartbeat"`
}

func NewHandler(store *Store, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	api := &API{store: store, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", api.health)
	mux.HandleFunc("POST /api/v1/worker-enrollments", api.createWorkerEnrollment)
	mux.HandleFunc("PUT /api/v1/workers/{worker_id}", api.registerWorker)
	mux.HandleFunc("PUT /api/v1/workers/{worker_id}/heartbeat", api.heartbeatWorker)
	mux.HandleFunc("POST /api/v1/workers/{worker_id}/claims", api.claim)
	mux.HandleFunc("GET /api/v1/workers", api.listWorkers)
	mux.HandleFunc("GET /api/v1/workers/{worker_id}", api.getWorker)
	mux.HandleFunc("POST /api/v1/workers/{worker_id}/test", api.testWorkerConnection)
	mux.HandleFunc("GET /api/v1/workers/{worker_id}/repository-options", api.getWorkerRepositoryOptions)
	mux.HandleFunc("GET /api/v1/repositories", api.listManagedRepositories)
	mux.HandleFunc("POST /api/v1/repositories", api.createManagedRepository)
	mux.HandleFunc("GET /api/v1/repositories/{repository_id}", api.getManagedRepository)
	mux.HandleFunc("GET /api/v1/repositories/{repository_id}/readiness", api.getManagedRepositoryReadiness)
	mux.HandleFunc("PUT /api/v1/repositories/{repository_id}/enabled", api.setManagedRepositoryEnabled)
	mux.HandleFunc("GET /api/v1/execution-profiles", api.listExecutionProfiles)
	mux.HandleFunc("POST /api/v1/execution-profiles", api.createExecutionProfile)
	mux.HandleFunc("GET /api/v1/execution-profiles/{profile_id}", api.getExecutionProfile)
	mux.HandleFunc("PUT /api/v1/execution-profiles/{profile_id}", api.updateExecutionProfile)
	mux.HandleFunc("GET /api/v1/pipelines", api.listPipelines)
	mux.HandleFunc("POST /api/v1/pipelines", api.createPipeline)
	mux.HandleFunc("GET /api/v1/pipelines/{pipeline_id}", api.getPipeline)
	mux.HandleFunc("PUT /api/v1/pipelines/{pipeline_id}", api.updatePipeline)
	mux.HandleFunc("DELETE /api/v1/pipelines/{pipeline_id}", api.deletePipeline)
	mux.HandleFunc("GET /api/v1/tasks", api.listTasks)
	mux.HandleFunc("POST /api/v1/tasks", api.createTask)
	mux.HandleFunc("GET /api/v1/tasks/{task_id}", api.getTask)
	mux.HandleFunc("PUT /api/v1/tasks/{task_id}", api.updateTask)
	mux.HandleFunc("PUT /api/v1/tasks/{task_id}/archived", api.setTaskArchived)
	mux.HandleFunc("POST /api/v1/tasks/{task_id}/run", api.runTask)
	mux.HandleFunc("POST /api/v1/tasks/{task_id}/discard-occurrence", api.discardTaskOccurrence)
	mux.HandleFunc("GET /api/v1/procedures", api.listProcedures)
	mux.HandleFunc("POST /api/v1/procedure-runs", api.admitProcedureRun)
	mux.HandleFunc("POST /api/v1/builds", api.admitBuild)
	mux.HandleFunc("GET /api/v1/runs", api.listRuns)
	mux.HandleFunc("GET /api/v1/runs/{run_id}", api.getRun)
	mux.HandleFunc("POST /api/v1/runs/{run_id}/cancel", api.cancelRun)
	mux.HandleFunc("POST /api/v1/runs/{run_id}/sessions/{session_id}/cancel", api.cancelSession)
	mux.HandleFunc("POST /api/v1/runs/{run_id}/sessions/{session_id}/retry", api.retrySession)
	mux.HandleFunc("GET /api/v1/overview", api.getOverview)
	mux.HandleFunc("GET /api/v1/attempts/{attempt_id}", api.getAttempt)
	mux.HandleFunc("POST /api/v1/attempts/{attempt_id}/start", api.startAttempt)
	mux.HandleFunc("POST /api/v1/attempts/{attempt_id}/stages/{position}/start", api.startStage)
	mux.HandleFunc("POST /api/v1/attempts/{attempt_id}/stages/{position}/complete", api.completeStage)
	mux.HandleFunc("PUT /api/v1/attempts/{attempt_id}/heartbeat", api.heartbeat)
	mux.HandleFunc("GET /api/v1/attempts/{attempt_id}/events", api.getEvents)
	mux.HandleFunc("POST /api/v1/attempts/{attempt_id}/events", api.appendEvents)
	mux.HandleFunc("POST /api/v1/attempts/{attempt_id}/updates", api.appendAgentUpdate)
	mux.HandleFunc("POST /api/v1/attempts/{attempt_id}/complete", api.completeAttempt)
	return api.requestLog(mux, true)
}

// NewRemoteWorkerHandler exposes only the Worker lifecycle over the optional
// TLS listener. Operator, repository, Task, and Run APIs remain local.
func NewRemoteWorkerHandler(store *Store, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	api := &API{store: store, logger: logger}
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", api.requireTLS(http.HandlerFunc(api.health)))
	mux.Handle("POST /api/v1/worker-enrollments/exchange", api.requireTLS(http.HandlerFunc(api.exchangeWorkerEnrollment)))
	mux.Handle("PUT /api/v1/workers/{worker_id}", api.remoteWorkerAuth(http.HandlerFunc(api.registerWorker)))
	mux.Handle("PUT /api/v1/workers/{worker_id}/heartbeat", api.remoteWorkerAuth(http.HandlerFunc(api.heartbeatWorker)))
	mux.Handle("POST /api/v1/workers/{worker_id}/claims", api.remoteWorkerAuth(http.HandlerFunc(api.claim)))
	mux.Handle("GET /api/v1/attempts/{attempt_id}", api.remoteAttemptAuth(http.HandlerFunc(api.getAttempt)))
	mux.Handle("POST /api/v1/attempts/{attempt_id}/start", api.remoteAttemptAuth(http.HandlerFunc(api.startAttempt)))
	mux.Handle("POST /api/v1/attempts/{attempt_id}/stages/{position}/start", api.remoteAttemptAuth(http.HandlerFunc(api.startStage)))
	mux.Handle("POST /api/v1/attempts/{attempt_id}/stages/{position}/complete", api.remoteAttemptAuth(http.HandlerFunc(api.completeStage)))
	mux.Handle("PUT /api/v1/attempts/{attempt_id}/heartbeat", api.remoteAttemptAuth(http.HandlerFunc(api.heartbeat)))
	mux.Handle("POST /api/v1/attempts/{attempt_id}/events", api.remoteAttemptAuth(http.HandlerFunc(api.appendEvents)))
	mux.Handle("POST /api/v1/attempts/{attempt_id}/updates", api.remoteAttemptAuth(http.HandlerFunc(api.appendAgentUpdate)))
	mux.Handle("POST /api/v1/attempts/{attempt_id}/complete", api.remoteAttemptAuth(http.HandlerFunc(api.completeAttempt)))
	return api.requestLog(mux, false)
}

func (a *API) createWorkerEnrollment(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.CreateWorkerEnrollmentRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	enrollment, err := a.store.CreateWorkerEnrollment(r.Context(), input.WorkerID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, enrollment)
}

func (a *API) exchangeWorkerEnrollment(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.ExchangeWorkerEnrollmentRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	credential, err := a.store.ExchangeWorkerEnrollment(r.Context(), input.WorkerID, input.EnrollmentToken, input.Credential)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, credential)
}

func (a *API) requireTLS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			writeError(w, &ServiceError{Code: "tls_required", Message: "the remote Worker API requires TLS", Status: http.StatusUpgradeRequired})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) authenticateRemoteWorker(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.TLS == nil {
		writeError(w, &ServiceError{Code: "tls_required", Message: "the remote Worker API requires TLS", Status: http.StatusUpgradeRequired})
		return "", false
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, unauthorizedWorker())
		return "", false
	}
	workerID, err := a.store.AuthenticateWorkerCredential(r.Context(), parts[1])
	if err != nil {
		var service *ServiceError
		if errors.As(err, &service) && service.Status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		writeError(w, err)
		return "", false
	}
	return workerID, true
}

func (a *API) remoteWorkerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workerID, ok := a.authenticateRemoteWorker(w, r)
		if !ok {
			return
		}
		if workerID != r.PathValue("worker_id") {
			writeError(w, &ServiceError{Code: "worker_forbidden", Message: "Worker credential does not own this resource", Status: http.StatusForbidden})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) remoteAttemptAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workerID, ok := a.authenticateRemoteWorker(w, r)
		if !ok {
			return
		}
		attempt, err := a.store.Attempt(r.Context(), r.PathValue("attempt_id"))
		if err != nil || attempt.WorkerID != workerID {
			if err != nil && !errors.Is(err, ErrNotFound) {
				writeError(w, err)
				return
			}
			writeError(w, &ServiceError{Code: "worker_forbidden", Message: "Worker credential does not own this resource", Status: http.StatusForbidden})
			return
		}
		next.ServeHTTP(w, r)
	})
}

type responseRecorder struct {
	http.ResponseWriter
	status     int
	errorClass string
}

func (w *responseRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseRecorder) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func (a *API) requestLog(next http.Handler, requireLoopbackHost bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID, err := newID()
		if err != nil {
			requestID = "unavailable"
		}
		w.Header().Set("X-Request-ID", requestID)
		recorder := &responseRecorder{ResponseWriter: w}
		start := time.Now()
		if err := validateRequestHost(r.Host); requireLoopbackHost && err != nil {
			writeError(recorder, &ServiceError{Code: "invalid_host", Message: "Host must identify a loopback address", Status: 403})
		} else {
			next.ServeHTTP(recorder, r)
		}
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		attributes := []any{
			"request_id", requestID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
		}
		if recorder.errorClass != "" {
			attributes = append(attributes, "error_class", recorder.errorClass)
		}
		a.logger.Info("http_request", attributes...)
	})
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	if err := a.store.db.PingContext(r.Context()); err != nil {
		writeError(w, unavailable(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) registerWorker(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input workerRegistrationRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	legacyRequest := !input.Runtime.Present && !input.RuntimeVersion.Present
	if input.CodexVersion.Present && !legacyRequest {
		writeError(w, invalid("invalid_runtime", "use codex_version or runtime fields, not both"))
		return
	}
	if legacyRequest {
		input.WorkerRegistration.Runtime = protocol.RuntimeCodex
		input.WorkerRegistration.RuntimeVersion = input.CodexVersion.Value
	} else {
		input.WorkerRegistration.Runtime = input.Runtime.Value
		input.WorkerRegistration.RuntimeVersion = input.RuntimeVersion.Value
	}
	worker, err := a.store.RegisterWorker(r.Context(), r.PathValue("worker_id"), input.WorkerRegistration)
	if err != nil {
		writeError(w, err)
		return
	}
	if legacyRequest {
		writeJSON(w, http.StatusOK, legacyWorkerResponse{
			ID: worker.ID, Name: worker.Name, WorkerVersion: worker.WorkerVersion,
			CodexVersion: worker.RuntimeVersion, Capacity: worker.Capacity,
			ActiveCount: worker.ActiveCount, Health: worker.Health, Online: worker.Online,
			Repositories: worker.Repositories, RetainedWorktrees: worker.RetainedWorktrees,
			CurrentRunTitle: worker.CurrentRunTitle, RegisteredAt: worker.RegisteredAt,
			LastHeartbeat: worker.LastHeartbeat,
		})
		return
	}
	writeJSON(w, http.StatusOK, worker)
}

func (a *API) heartbeatWorker(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) || !decodeEmptyJSON(w, r) {
		return
	}
	worker, err := a.store.HeartbeatWorker(r.Context(), r.PathValue("worker_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, worker)
}

func (a *API) claim(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.ClaimRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	claim, err := a.store.Claim(r.Context(), r.PathValue("worker_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if claim == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	a.logStateChange("execution", claim.Execution.ID, claim.Execution.State, "attempt_id", claim.Attempt.ID)
	writeJSON(w, http.StatusOK, claim)
}

func (a *API) listWorkers(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Query().Get("view") {
	case "":
		workers, err := a.store.Workers(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"workers": workers})
	case "summary":
		page, err := a.store.WorkerSummaries(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	default:
		writeError(w, invalid("invalid_view", "view must be summary when provided"))
	}
}

func (a *API) getWorker(w http.ResponseWriter, r *http.Request) {
	worker, err := a.store.Worker(r.Context(), r.PathValue("worker_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, worker)
}

func (a *API) testWorkerConnection(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) || !decodeEmptyJSON(w, r) {
		return
	}
	worker, err := a.store.Worker(r.Context(), r.PathValue("worker_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	testContext, cancel := context.WithTimeout(r.Context(), workerConnectionTimeout)
	defer cancel()
	worker, err = waitForWorkerRegistration(
		testContext, a.store, worker.ID, worker.LastHeartbeat, 100*time.Millisecond,
	)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, worker)
}

func waitForWorkerRegistration(
	ctx context.Context,
	store *Store,
	workerID string,
	previousHeartbeat time.Time,
	pollInterval time.Duration,
) (protocol.Worker, error) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return protocol.Worker{}, workerConnectionError(ctx.Err())
		case <-ticker.C:
			worker, err := store.Worker(ctx, workerID)
			if err != nil {
				if ctx.Err() != nil {
					return protocol.Worker{}, workerConnectionError(ctx.Err())
				}
				return protocol.Worker{}, err
			}
			if worker.LastHeartbeat.After(previousHeartbeat) {
				return worker, nil
			}
		}
	}
}

func workerConnectionError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &ServiceError{
			Code: "worker_connection_timeout", Message: "Worker did not send a fresh registration", Status: http.StatusGatewayTimeout,
		}
	}
	return unavailable(err)
}

func (a *API) getWorkerRepositoryOptions(w http.ResponseWriter, r *http.Request) {
	options, err := a.store.WorkerRepositoryOptions(r.Context(), r.PathValue("worker_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": options})
}

func (a *API) listManagedRepositories(w http.ResponseWriter, r *http.Request) {
	repositories, err := a.store.ManagedRepositories(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": repositories})
}

func (a *API) createManagedRepository(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.CreateManagedRepositoryRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	repository, created, err := a.store.CreateManagedRepository(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, repository)
}

func (a *API) getManagedRepository(w http.ResponseWriter, r *http.Request) {
	repository, err := a.store.ManagedRepository(r.Context(), r.PathValue("repository_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, repository)
}

func (a *API) getManagedRepositoryReadiness(w http.ResponseWriter, r *http.Request) {
	readiness, err := a.store.ManagedRepositoryReadiness(
		r.Context(), r.PathValue("repository_id"),
	)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, readiness)
}

func (a *API) setManagedRepositoryEnabled(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		writeError(w, invalid("invalid_repository", "enabled is required"))
		return
	}
	repository, err := a.store.SetManagedRepositoryEnabled(
		r.Context(),
		r.PathValue("repository_id"),
		*input.Enabled,
	)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, repository)
}

func (a *API) getAttempt(w http.ResponseWriter, r *http.Request) {
	attempt, err := a.store.Attempt(r.Context(), r.PathValue("attempt_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, attempt)
}

func (a *API) startAttempt(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.StartAttemptRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	attempt, err := a.store.StartAttempt(r.Context(), r.PathValue("attempt_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("attempt", attempt.ID, attempt.State, "execution_id", attempt.ExecutionID)
	writeJSON(w, http.StatusOK, attempt)
}

func stagePosition(r *http.Request) (int, error) {
	position, err := strconv.Atoi(r.PathValue("position"))
	if err != nil || position < 0 || position >= protocol.MaxPipelineStages {
		return 0, invalid("invalid_stage_position", "stage position must be between 0 and 19")
	}
	return position, nil
}

func (a *API) startStage(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	position, err := stagePosition(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input protocol.StartStageRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	stage, err := a.store.StartStage(r.Context(), r.PathValue("attempt_id"), position, input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("stage", r.PathValue("attempt_id")+":"+strconv.Itoa(position), string(stage.State))
	writeJSON(w, http.StatusOK, stage)
}

func (a *API) completeStage(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	position, err := stagePosition(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input protocol.CompleteStageRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	stage, err := a.store.CompleteStage(r.Context(), r.PathValue("attempt_id"), position, input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("stage", r.PathValue("attempt_id")+":"+strconv.Itoa(position), string(stage.State))
	writeJSON(w, http.StatusOK, stage)
}

func (a *API) logStateChange(resourceType, resourceID, state string, extra ...any) {
	attributes := []any{
		"resource_type", resourceType,
		"resource_id", resourceID,
		"new_state", state,
	}
	a.logger.Info("state_change", append(attributes, extra...)...)
}

func (a *API) heartbeat(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.LeaseRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	response, err := a.store.Heartbeat(r.Context(), r.PathValue("attempt_id"), input.LeaseToken)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *API) appendEvents(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxEventBatchBytes) {
		return
	}
	var input protocol.EventBatchRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := a.store.AppendEvents(r.Context(), r.PathValue("attempt_id"), input); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) appendAgentUpdate(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.AttemptUpdateRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	update, err := a.store.AppendAgentUpdate(r.Context(), r.PathValue("attempt_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, update)
}

func (a *API) getEvents(w http.ResponseWriter, r *http.Request) {
	after := int64(-1)
	if raw := r.URL.Query().Get("after"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < -1 {
			writeError(w, invalid("invalid_after", "after must be an integer of at least -1"))
			return
		}
		after = value
	}
	limit, err := pageLimit(r, protocol.DefaultEventPageSize, protocol.MaxEventPageSize)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := a.store.Events(r.Context(), r.PathValue("attempt_id"), after, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events": page.Events, "next_after": page.NextAfter, "has_more": page.HasMore,
	})
}

func pageLimit(r *http.Request, defaultLimit, maxLimit int) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > maxLimit {
		return 0, invalid("invalid_limit", fmt.Sprintf("limit must be an integer between 1 and %d", maxLimit))
	}
	return limit, nil
}

func (a *API) completeAttempt(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.CompleteAttemptRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	attempt, err := a.store.CompleteAttempt(r.Context(), r.PathValue("attempt_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("attempt", attempt.ID, attempt.State, "execution_id", attempt.ExecutionID)
	writeJSON(w, http.StatusOK, attempt)
}

func prepareMutation(w http.ResponseWriter, r *http.Request, limit int64) bool {
	if !validateMutationOrigin(w, r) {
		return false
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeError(w, &ServiceError{Code: "json_required", Message: "Content-Type must be application/json", Status: 415})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	return true
}

func validateMutationOrigin(w http.ResponseWriter, r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "http" || !sameAuthority(parsed.Host, r.Host) {
			writeError(w, &ServiceError{Code: "cross_origin_request", Message: "browser mutations must be same-origin", Status: 403})
			return false
		}
	}
	return true
}

func validateRequestHost(authority string) error {
	host := authority
	if parsed, _, err := net.SplitHostPort(authority); err == nil {
		host = parsed
	} else if strings.Contains(authority, ":") && net.ParseIP(strings.Trim(authority, "[]")) == nil {
		return errors.New("invalid host")
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return nil
		}
		return errors.New("host is not loopback")
	}
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return nil
	}
	return errors.New("host must be a loopback IP or localhost")
}

func sameAuthority(left, right string) bool {
	return strings.EqualFold(strings.TrimSuffix(left, "."), strings.TrimSuffix(right, "."))
}

func validateResolvedLoopback(ips []net.IP) error {
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return errors.New("hostname resolves outside loopback")
		}
	}
	return nil
}

var lookupIP = net.LookupIP

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeDecodeError(w, err)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		writeDecodeError(w, err)
		return false
	}
	return true
}

func decodeEmptyJSON(w http.ResponseWriter, r *http.Request) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var value map[string]any
	if err := decoder.Decode(&value); errors.Is(err, io.EOF) {
		return true
	} else if err != nil {
		writeDecodeError(w, err)
		return false
	}
	if len(value) != 0 {
		writeError(w, invalid("invalid_request", "request body must be an empty JSON object"))
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		writeDecodeError(w, err)
		return false
	}
	return true
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, &ServiceError{Code: "body_too_large", Message: "request body exceeds its size limit", Status: 413})
		return
	}
	writeError(w, invalid("malformed_json", "request body must contain one valid JSON object"))
}

func writeError(w http.ResponseWriter, err error) {
	service := &ServiceError{Code: "internal_error", Message: "internal server error", Status: 500, Err: err}
	var typed *ServiceError
	if errors.As(err, &typed) {
		service = typed
	}
	if recorder, ok := w.(*responseRecorder); ok {
		recorder.errorClass = service.Code
	}
	writeJSON(w, service.Status, protocol.ErrorBody{Error: protocol.APIError{Code: service.Code, Message: service.Message}})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		fmt.Fprint(w, "\n")
	}
}
