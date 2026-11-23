package controlplane

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/protocol"
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
	case errors.Is(err, tickets.ErrTicketEnded), errors.Is(err, tickets.ErrStillWorking):
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
	query := r.URL.Query()
	found, err := s.store.ListTickets(r.Context(), tickets.TicketFilter{State: query.Get("state"), Repo: query.Get("repo"), Source: query.Get("source")})
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
	detail := map[string]any{"ticket": ticket, "entries": entries, "runs": runs, "figures": figures, "steps": s.presetSteps(ticket.Agent)}
	if len(runs) > 0 && runs[len(runs)-1].Status == tickets.RunQueued {
		runners, _, err := s.store.Fleet(r.Context())
		if err != nil {
			writeTicketError(w, err)
			return
		}
		// waiting_for names the harnesses no online runner has, which keep
		// the queued run from starting.
		if missing := s.waitingFor(s.onlineRunners(runners), ticket.Agent); len(missing) > 0 {
			detail["waiting_for"] = missing
		}
	}
	writeJSON(w, http.StatusOK, detail)
}

// presetSteps lists the steps of a preset, in order, with the agents each
// runs, so the UI can draw a job's pipeline before its first run starts. A
// preset that is gone has none.
func (s *Server) presetSteps(name string) []stepView {
	preset, ok := s.factory.Preset(name)
	if !ok {
		return []stepView{}
	}
	return stepViews(s.factory, preset)
}

func (s *Server) rerunTicket(w http.ResponseWriter, r *http.Request, tokenName string) {
	ticket, err := s.store.Rerun(r.Context(), r.PathValue("id"), tokenName)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ticket)
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

// runPatch downloads the change a run saved as a patch because it could not
// push it, for git am.
func (s *Server) runPatch(w http.ResponseWriter, r *http.Request, _ string) {
	run, err := s.store.Run(r.Context(), r.PathValue("id"))
	if err != nil {
		writeTicketError(w, err)
		return
	}
	body, err := s.store.RunPatch(r.Context(), run.ID)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+patchName(run.TicketID)+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// patchName names a job's patch file after the job, keeping only characters
// that are safe in a header and a file name.
func patchName(ticketID string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, ticketID)
	if safe == "" {
		safe = "change"
	}
	return safe + ".patch"
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

func (s *Server) listRepositories(w http.ResponseWriter, r *http.Request, tokenName string) {
	repositories, err := s.intake.Connected(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
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

// presetView is what the New job form shows of a preset: its name, what it
// does and its steps, in order.
type presetView struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Steps       []stepView `json:"steps"`
}

// stepView is one step and the agents it runs, with their harness and model.
type stepView struct {
	Name   string           `json:"name"`
	Agents []protocol.Agent `json:"agents"`
}

func stepViews(loaded *factory.Factory, preset factory.Preset) []stepView {
	steps := make([]stepView, 0, len(preset.Steps))
	for _, step := range preset.Steps {
		steps = append(steps, stepView{Name: step.Name, Agents: loaded.StepAgents(step.Run)})
	}
	return steps
}

func presetViews(loaded *factory.Factory) []presetView {
	views := make([]presetView, 0, len(loaded.Presets))
	for name, preset := range loaded.Presets {
		views = append(views, presetView{Name: name, Description: preset.Description, Steps: stepViews(loaded, preset)})
	}
	sort.Slice(views, func(a, b int) bool { return views[a].Name < views[b].Name })
	return views
}

func (s *Server) listPresets(w http.ResponseWriter, _ *http.Request, _ string) {
	body := map[string]any{"presets": s.presets, "default": s.config.Server.DefaultPreset}
	if s.factory != nil && len(s.factory.Updates) > 0 {
		body["updates"] = s.factory.Updates
	}
	writeJSON(w, http.StatusOK, body)
}
