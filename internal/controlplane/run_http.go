package controlplane

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/sources"
	"github.com/owainlewis/machinist/internal/tickets"
)

// Job states of the run API.
const (
	JobQueued     = "queued"
	JobRunning    = "running"
	JobNeedsInput = "needs_input"
	JobDone       = "done"
	JobFailed     = "failed"
	JobCancelled  = "cancelled"
)

// job is what the run API returns. It hides the ticket model behind it.
type job struct {
	ID        string            `json:"id"`
	Preset    string            `json:"preset"`
	Vars      map[string]string `json:"vars"`
	Title     string            `json:"title"`
	State     string            `json:"state"`
	Question  string            `json:"question,omitempty"`
	PRNumber  int               `json:"pr_number,omitempty"`
	CreatedBy string            `json:"created_by"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// jobState maps a ticket to a job state. A ticket with an open pull request and
// nothing running is done: Machinist no longer follows the pull request.
func jobState(ticket tickets.Ticket, runs []tickets.Run) string {
	switch ticket.State {
	case tickets.StateWorking:
		for _, run := range runs {
			if run.Status == tickets.RunRunning {
				return JobRunning
			}
		}
		return JobQueued
	case tickets.StateNeedsYou:
		return JobNeedsInput
	case tickets.StatePRReady, tickets.StateDone:
		return JobDone
	case tickets.StateCancelled:
		return JobCancelled
	default:
		return JobFailed
	}
}

func (s *Server) job(r *http.Request, ticket tickets.Ticket) (job, error) {
	runs, err := s.store.Runs(r.Context(), ticket.ID)
	if err != nil {
		return job{}, err
	}
	question, err := s.store.WaitingQuestion(r.Context(), ticket)
	if err != nil {
		return job{}, err
	}
	return job{
		ID:        ticket.ID,
		Preset:    ticket.Agent,
		Vars:      map[string]string{"repo": ticket.Repo},
		Title:     ticket.Title,
		State:     jobState(ticket, runs),
		Question:  question,
		PRNumber:  ticket.PRNumber,
		CreatedBy: ticket.CreatedBy,
		CreatedAt: ticket.CreatedAt,
		UpdatedAt: ticket.UpdatedAt,
	}, nil
}

func (s *Server) writeJob(w http.ResponseWriter, r *http.Request, status int, ticket tickets.Ticket) {
	view, err := s.job(r, ticket)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	writeJSON(w, status, view)
}

func (s *Server) runJob(w http.ResponseWriter, r *http.Request, tokenName string) {
	var request sources.RunRequest
	if !decodeJSON(w, r, maxRequestBytes, &request) {
		return
	}
	ticket, created, err := s.intake.Run(r.Context(), request, tokenName)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	s.writeJob(w, r, status, ticket)
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request, _ string) {
	found, err := s.store.RecentTickets(r.Context(), r.URL.Query().Get("repo"))
	if err != nil {
		writeTicketError(w, err)
		return
	}
	jobs := make([]job, 0, len(found))
	for _, ticket := range found {
		view, err := s.job(r, ticket)
		if err != nil {
			writeTicketError(w, err)
			return
		}
		jobs = append(jobs, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request, _ string) {
	ticket, err := s.store.Ticket(r.Context(), r.PathValue("id"))
	if err != nil {
		writeTicketError(w, err)
		return
	}
	s.writeJob(w, r, http.StatusOK, ticket)
}

func (s *Server) inputJob(w http.ResponseWriter, r *http.Request, tokenName string) {
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
	s.writeJob(w, r, http.StatusOK, ticket)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request, tokenName string) {
	ticket, err := s.store.Cancel(r.Context(), r.PathValue("id"), tokenName)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	s.writeJob(w, r, http.StatusOK, ticket)
}
