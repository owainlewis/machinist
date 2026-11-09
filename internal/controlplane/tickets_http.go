package controlplane

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/mcpserver"
	"github.com/owainlewis/machinist/internal/sources"
	"github.com/owainlewis/machinist/internal/tickets"
)

// ticketView is a ticket with the question it is waiting on.
type ticketView struct {
	tickets.Ticket
	Question string `json:"question,omitempty"`
}

func (s *Server) view(r *http.Request, ticket tickets.Ticket) (ticketView, error) {
	question, err := s.store.WaitingQuestion(r.Context(), ticket)
	return ticketView{Ticket: ticket, Question: question}, err
}

func (s *Server) writeTicket(w http.ResponseWriter, r *http.Request, status int, ticket tickets.Ticket) {
	view, err := s.view(r, ticket)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	writeJSON(w, status, view)
}

func writeTicketError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sources.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, tickets.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, tickets.ErrNotWaiting), errors.Is(err, tickets.ErrTicketEnded):
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
	views := make([]ticketView, 0, len(found))
	for _, ticket := range found {
		view, err := s.view(r, ticket)
		if err != nil {
			writeTicketError(w, err)
			return
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tickets": views})
}

func (s *Server) getTicket(w http.ResponseWriter, r *http.Request, _ string) {
	id := r.PathValue("id")
	ticket, err := s.store.Ticket(r.Context(), id)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	view, err := s.view(r, ticket)
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
	writeJSON(w, http.StatusOK, map[string]any{"ticket": view, "entries": entries, "runs": runs, "figures": figures})
}

func (s *Server) answerTicket(w http.ResponseWriter, r *http.Request, tokenName string) {
	var request struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, maxRequestBytes, &request) {
		return
	}
	if strings.TrimSpace(request.Text) == "" {
		writeError(w, http.StatusBadRequest, errors.New("text is required"))
		return
	}
	ticket, err := s.store.Answer(r.Context(), r.PathValue("id"), tokenName, request.Text)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	s.writeTicket(w, r, http.StatusOK, ticket)
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

func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request, tokenName string) {
	s.mcp.ServeHTTP(w, r.WithContext(mcpserver.WithCaller(r.Context(), tokenName)))
}
