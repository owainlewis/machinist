package controlplane

import (
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/sources"
	"github.com/owainlewis/machinist/internal/tickets"
)

// Job states of the run API.
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobDone      = "done"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

// job is what the run API returns. It hides the ticket model behind it.
type job struct {
	ID string `json:"id"`
	// Key is the key the job was started with. A job from another source shows
	// that source's key.
	Key string `json:"key"`
	// Source is where the job came from: web, api, schedule, or a tracker
	// such as linear. SourceURL links to the original issue or message.
	Source    string            `json:"source"`
	SourceURL string            `json:"source_url,omitempty"`
	Preset    string            `json:"preset"`
	Harness   string            `json:"harness,omitempty"`
	Model     string            `json:"model,omitempty"`
	Vars      map[string]string `json:"vars"`
	RunsOn    []string          `json:"runs_on,omitempty"`
	Title     string            `json:"title"`
	State     string            `json:"state"`
	PRNumber  int               `json:"pr_number,omitempty"`
	// PRState is open, merged or closed, once the job has a pull request.
	PRState string `json:"pr_state,omitempty"`
	// MergeGateDecision, MergeGateRisk and MergeGateReason are the merge
	// gate's judgment, once the merge-gate step ran.
	MergeGateDecision string `json:"merge_gate_decision,omitempty"`
	MergeGateRisk     string `json:"merge_gate_risk,omitempty"`
	MergeGateReason   string `json:"merge_gate_reason,omitempty"`
	// CurrentStep is the preset step a running job is on, or empty.
	CurrentStep string `json:"current_step"`
	// Tokens and CostUSD add up what the job's ended runs reported. CostUSD is
	// null until a run reports a cost.
	Tokens    int64     `json:"tokens"`
	CostUSD   *float64  `json:"cost_usd"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// jobState maps a ticket to a job state. A ticket with an open pull request and
// nothing running is done. Its pull request's own state is pr_state.
func jobState(ticket tickets.Ticket, runs []tickets.Run) string {
	switch ticket.State {
	case tickets.StateWorking:
		for _, run := range runs {
			if run.Status == tickets.RunRunning {
				return JobRunning
			}
		}
		return JobQueued
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
	key, err := s.store.RequestKey(r.Context(), ticket.ID)
	if err != nil {
		return job{}, err
	}
	if ticket.Source == "run" {
		key = strings.TrimPrefix(key, "run:")
	}
	vars := make(map[string]string, len(ticket.Vars)+1)
	maps.Copy(vars, ticket.Vars)
	vars["repo"] = ticket.Repo
	view := job{
		ID:                ticket.ID,
		Key:               key,
		Source:            ticket.Origin,
		SourceURL:         ticket.SourceURL,
		Preset:            ticket.Agent,
		Harness:           ticket.Harness,
		Model:             ticket.Model,
		Vars:              vars,
		RunsOn:            effectiveRunsOnFrom(s.factoryForTicket(ticket), ticket),
		Title:             ticket.Title,
		State:             jobState(ticket, runs),
		PRNumber:          ticket.PRNumber,
		PRState:           ticket.PRState,
		MergeGateDecision: ticket.MergeGateDecision,
		MergeGateRisk:     ticket.MergeGateRisk,
		MergeGateReason:   ticket.MergeGateReason,
		CreatedBy:         ticket.CreatedBy,
		CreatedAt:         ticket.CreatedAt,
		UpdatedAt:         ticket.UpdatedAt,
	}
	if view.State == JobRunning {
		if view.CurrentStep, err = s.store.CurrentStep(r.Context(), ticket.ID); err != nil {
			return job{}, err
		}
	}
	for _, run := range runs {
		if run.Tokens != nil {
			view.Tokens += *run.Tokens
		}
		if run.Cost != nil {
			total := *run.Cost
			if view.CostUSD != nil {
				total += *view.CostUSD
			}
			view.CostUSD = &total
		}
	}
	return view, nil
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
	request.Origin = requestOrigin(r)
	ticket, created, err := s.intake.Run(r.Context(), request, tokenName)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		actorID, actorName := s.actorInfo(r, tokenName)
		s.audit(r, "job.started", actorID, actorName, ticket.ID, "succeeded")
	}
	s.writeJob(w, r, status, ticket)
}

// requestOrigin is web for a request signed in with the web app's session
// cookie, and api for one that sends its token in the Authorization header.
func requestOrigin(r *http.Request) string {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return tickets.OriginAPI
	}
	return tickets.OriginWeb
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request, _ string) {
	query := r.URL.Query()
	found, err := s.store.ListTickets(r.Context(), tickets.TicketFilter{Repo: query.Get("repo"), Source: query.Get("source")})
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

func (s *Server) rerunJob(w http.ResponseWriter, r *http.Request, tokenName string) {
	ticket, ok := s.rerunTicketWithBody(w, r, r.PathValue("id"), tokenName)
	if !ok {
		return
	}
	if s.engine != nil {
		s.engine.UnpinBefore(ticket.ID, ticket.UpdatedAt)
	}
	actorID, actorName := s.actorInfo(r, tokenName)
	s.audit(r, "job.rerun", actorID, actorName, ticket.ID, "succeeded")
	s.writeJob(w, r, http.StatusOK, ticket)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request, tokenName string) {
	ticket, err := s.store.Cancel(r.Context(), r.PathValue("id"), tokenName)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	if s.engine != nil {
		s.engine.UnpinThrough(ticket.ID, ticket.UpdatedAt)
	}
	actorID, actorName := s.actorInfo(r, tokenName)
	s.audit(r, "job.cancelled", actorID, actorName, ticket.ID, "succeeded")
	s.writeJob(w, r, http.StatusOK, ticket)
}

func (s *Server) deleteJob(w http.ResponseWriter, r *http.Request, tokenName string) {
	s.deleteTicket(w, r, tokenName)
}
