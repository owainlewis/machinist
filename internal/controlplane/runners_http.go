package controlplane

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/owainlewis/machinist/internal/engine"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/tickets"
)

type runnerStatus struct {
	Name        string    `json:"name"`
	Harnesses   []string  `json:"harnesses"`
	LastContact time.Time `json:"last_contact"`
}

func (s *Server) poll(w http.ResponseWriter, r *http.Request, tokenName string) {
	var request protocol.PollRequest
	if !decodeJSON(w, r, maxRequestBytes, &request) {
		return
	}
	s.runnersMu.Lock()
	s.runners[tokenName] = runnerStatus{Name: tokenName, Harnesses: request.Harnesses, LastContact: time.Now().UTC()}
	s.runnersMu.Unlock()
	run, err := s.engine.Assign(r.Context(), tokenName, request.Harnesses)
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

func (s *Server) listRunners(w http.ResponseWriter, _ *http.Request, _ string) {
	s.runnersMu.Lock()
	runners := make([]runnerStatus, 0, len(s.runners))
	for _, status := range s.runners {
		runners = append(runners, status)
	}
	s.runnersMu.Unlock()
	sort.Slice(runners, func(i, j int) bool { return runners[i].Name < runners[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"runners": runners})
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request, _ string) {
	cancel, err := s.engine.Heartbeat(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRunError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.HeartbeatResponse{Cancel: cancel})
}

func (s *Server) ingestEvents(w http.ResponseWriter, r *http.Request, _ string) {
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

func (s *Server) complete(w http.ResponseWriter, r *http.Request, _ string) {
	var completion protocol.Completion
	if !decodeJSON(w, r, maxRequestBytes, &completion) {
		return
	}
	if err := s.engine.Complete(r.Context(), r.PathValue("id"), completion); err != nil {
		writeRunError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeRunError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tickets.ErrRunNotActive):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, tickets.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, engine.ErrInvalidCompletion):
		writeError(w, http.StatusBadRequest, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}
