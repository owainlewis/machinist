package controlplane

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/sources"
	"github.com/owainlewis/machinist/internal/tickets"
)

func (s *Server) writeTicket(w http.ResponseWriter, _ *http.Request, status int, ticket tickets.Ticket) {
	writeJSON(w, status, ticket)
}

func writeTicketError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sources.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, tickets.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, tickets.ErrTicketEnded):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func (s *Server) createTicket(w http.ResponseWriter, r *http.Request, tokenName string) {
	var request sources.AssignRequest
	if !decodeJSON(w, r, maxRequestBytes, &request) {
		return
	}
	ticket, created, err := s.intake.Assign(r.Context(), request, tokenName)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	s.writeTicket(w, r, status, ticket)
}

func (s *Server) listTickets(w http.ResponseWriter, r *http.Request, _ string) {
	found, err := s.store.ListTickets(r.Context(), r.URL.Query().Get("state"), r.URL.Query().Get("repo"))
	if err != nil {
		writeTicketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tickets": found})
}

func (s *Server) getTicket(w http.ResponseWriter, r *http.Request, _ string) {
	id := r.PathValue("id")
	ticket, err := s.store.Ticket(r.Context(), id)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	entries, err := s.store.Entries(r.Context(), id)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	runs, err := s.store.Runs(r.Context(), id)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	figures, err := s.store.TicketFigures(r.Context(), id)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ticket": ticket, "entries": entries, "runs": runs, "figures": figures})
}

func (s *Server) cancelTicket(w http.ResponseWriter, r *http.Request, tokenName string) {
	ticket, err := s.store.Cancel(r.Context(), r.PathValue("id"), tokenName)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	s.writeTicket(w, r, http.StatusOK, ticket)
}

func (s *Server) runEvents(w http.ResponseWriter, r *http.Request, _ string) {
	id := r.PathValue("id")
	if _, err := s.store.Run(r.Context(), id); err != nil {
		writeTicketError(w, err)
		return
	}
	var after int64
	if value := r.URL.Query().Get("after"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, errors.New("after must be a non-negative number"))
			return
		}
		after = parsed
	}
	page, err := s.store.RunEvents(r.Context(), id, after)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) ledger(w http.ResponseWriter, r *http.Request, _ string) {
	var since time.Time
	if value := r.URL.Query().Get("since"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("since must be an RFC 3339 time"))
			return
		}
		since = parsed
	}
	ledger, err := s.store.Ledger(r.Context(), r.URL.Query().Get("repo"), since)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ledger)
}

// usageDays is how far back the usage charts on Home look.
const usageDays = 14

func (s *Server) usage(w http.ResponseWriter, r *http.Request, _ string) {
	usage, err := s.store.Usage(r.Context(), usageDays)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

// maxStatsDays is the longest window the dashboard can ask for.
const maxStatsDays = 90

func (s *Server) stats(w http.ResponseWriter, r *http.Request, _ string) {
	days := usageDays
	if value := r.URL.Query().Get("days"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > maxStatsDays {
			writeError(w, http.StatusBadRequest, errors.New("days must be a whole number from 1 to 90"))
			return
		}
		days = parsed
	}
	stats, err := s.store.Stats(r.Context(), days)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) listRepositories(w http.ResponseWriter, _ *http.Request, tokenName string) {
	repositories := s.config.GitHub.Repositories
	if repositories == nil {
		repositories = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": repositories, "name": tokenName})
}

func (s *Server) listAutomations(w http.ResponseWriter, r *http.Request, _ string) {
	automations, err := s.schedule.Status(r.Context())
	if err != nil {
		writeTicketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"automations": automations})
}

func (s *Server) runAutomation(w http.ResponseWriter, r *http.Request, tokenName string) {
	ticket, created, err := s.schedule.RunNow(r.Context(), r.PathValue("name"), tokenName)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	s.writeTicket(w, r, status, ticket)
}

// presetView is what the New job form shows of a preset: its name and the
// names of its steps, in order.
type presetView struct {
	Name  string   `json:"name"`
	Steps []string `json:"steps"`
}

func presetViews(loaded *factory.Factory) []presetView {
	views := make([]presetView, 0, len(loaded.Presets))
	for name, preset := range loaded.Presets {
		steps := make([]string, 0, len(preset.Steps))
		for _, step := range preset.Steps {
			steps = append(steps, step.Name)
		}
		views = append(views, presetView{Name: name, Steps: steps})
	}
	sort.Slice(views, func(a, b int) bool { return views[a].Name < views[b].Name })
	return views
}

func (s *Server) listPresets(w http.ResponseWriter, _ *http.Request, _ string) {
	writeJSON(w, http.StatusOK, map[string]any{"presets": s.presets, "default": s.config.Server.DefaultPreset})
}
