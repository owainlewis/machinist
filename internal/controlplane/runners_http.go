package controlplane

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/owainlewis/machinist/internal/engine"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/tickets"
)

func (s *Server) poll(w http.ResponseWriter, r *http.Request, tokenName string) {
	var request protocol.PollRequest
	if !decodeJSON(w, r, maxRequestBytes, &request) {
		return
	}
	if err := s.store.RecordRunner(r.Context(), tokenName, request.Kind, request.Harnesses, request.Slots, request.Probed); err != nil {
		writeRunError(w, err)
		return
	}
	run, err := s.engine.Assign(r.Context(), tokenName, request.Harnesses, request.Probed)
	if err != nil {
		writeRunError(w, err)
		return
	}
	if run == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) listRunners(w http.ResponseWriter, r *http.Request, _ string) {
	runners, queue, err := s.store.Fleet(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for index := range runners {
		for jobIndex := range runners[index].Jobs {
			job := &runners[index].Jobs[jobIndex]
			job.Agents = s.currentAgents(job.Preset, job.Step)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runners": runners, "queue": queue})
}

// ownsRun checks that the run was given to this runner, so one runner cannot
// report on, or stop, another's work.
func (s *Server) ownsRun(w http.ResponseWriter, r *http.Request, tokenName string) bool {
	run, err := s.store.Run(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRunError(w, err)
		return false
	}
	if run.Runner != tokenName {
		writeError(w, http.StatusForbidden, fmt.Errorf("run %s belongs to another runner", run.ID))
		return false
	}
	return true
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request, tokenName string) {
	if !s.ownsRun(w, r, tokenName) {
		return
	}
	if err := s.store.TouchRunner(r.Context(), tokenName); err != nil {
		writeRunError(w, err)
		return
	}
	cancel, err := s.engine.Heartbeat(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRunError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.HeartbeatResponse{Cancel: cancel})
}

// runToken gives a runner a new GitHub token for one of its own active runs.
func (s *Server) runToken(w http.ResponseWriter, r *http.Request, tokenName string) {
	if !s.ownsRun(w, r, tokenName) {
		return
	}
	token, err := s.engine.RunToken(r.Context(), r.PathValue("id"))
	if errors.Is(err, engine.ErrNoGitHubApp) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeRunError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.GitHubToken{Token: token})
}

func (s *Server) ingestEvents(w http.ResponseWriter, r *http.Request, tokenName string) {
	if !s.ownsRun(w, r, tokenName) {
		return
	}
	var batch protocol.RunEventBatch
	if !decodeJSON(w, r, protocol.MaxRunEventBatchBytes, &batch) {
		return
	}
	cursor, err := s.store.IngestRunEvents(r.Context(), r.PathValue("id"), batch.Events)
	if err != nil {
		if errors.Is(err, tickets.ErrRunNotActive) || errors.Is(err, tickets.ErrNotFound) {
			writeRunError(w, err)
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.RunEventPage{Cursor: cursor})
}

func (s *Server) complete(w http.ResponseWriter, r *http.Request, tokenName string) {
	if !s.ownsRun(w, r, tokenName) {
		return
	}
	var completion protocol.Completion
	// A completion can carry a saved patch, base64 in JSON.
	if !decodeJSON(w, r, maxRequestBytes+2*protocol.MaxPatchBytes, &completion) {
		return
	}
	if err := s.engine.Complete(r.Context(), r.PathValue("id"), completion); err != nil {
		// The runner retries a failed completion until it succeeds, so the job
		// shows as running meanwhile. Say why here, where someone can fix it.
		if status := runErrorStatus(err); status >= http.StatusInternalServerError {
			fmt.Fprintf(s.log, "machinist: run %s finished, but recording it failed and the runner will retry: %v\n", r.PathValue("id"), err)
		}
		writeRunError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// currentAgents names the agents a running job's current step runs.
func (s *Server) currentAgents(presetName, step string) []protocol.Agent {
	preset, ok := s.factory.Preset(presetName)
	if !ok {
		return nil
	}
	for _, candidate := range preset.Steps {
		if candidate.Name == step {
			return s.factory.StepAgents(candidate.Run)
		}
	}
	return nil
}

func writeRunError(w http.ResponseWriter, err error) {
	writeError(w, runErrorStatus(err), err)
}

func runErrorStatus(err error) int {
	switch {
	case errors.Is(err, tickets.ErrRunNotActive):
		return http.StatusConflict
	case errors.Is(err, tickets.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, engine.ErrInvalidCompletion):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
