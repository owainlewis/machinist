package controlplane

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/sources"
)

// maxLinearBodyBytes bounds a webhook body. Linear's issue payloads are a few
// kilobytes.
const maxLinearBodyBytes = 256 << 10

// hourlyCap allows at most limit events in any hour.
type hourlyCap struct {
	mu    sync.Mutex
	limit int
	times []time.Time
}

func (c *hourlyCap) allow(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := c.times[:0]
	for _, at := range c.times {
		if now.Sub(at) < time.Hour {
			kept = append(kept, at)
		}
	}
	c.times = kept
	if len(c.times) >= c.limit {
		return false
	}
	c.times = append(c.times, now)
	return true
}

// linearWebhook starts a job from a Linear issue. The route is public, so
// every request must carry Linear's signature, be fresh, come from a connected
// team and match a trigger. It answers 200 for events it ignores, so Linear does
// not retry them.
func (s *Server) linearWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLinearBodyBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("the request body is too large"))
		return
	}
	event, err := sources.VerifyLinear(s.linearRules.Secret, body, r.Header.Get("Linear-Signature"), s.now())
	if errors.Is(err, sources.ErrLinearUnsigned) {
		writeError(w, http.StatusUnauthorized, sources.ErrLinearUnsigned)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	job, reason := s.linearRules.Decide(event)
	if reason != "" {
		writeJSON(w, http.StatusOK, map[string]string{"ignored": reason})
		return
	}
	ticket, found, err := s.intake.LinearExisting(r.Context(), job)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	if found {
		writeJSON(w, http.StatusOK, map[string]string{"job": ticket.ID})
		return
	}
	if !s.linearCap.allow(s.now()) {
		fmt.Fprintf(s.log, "machinist: Linear issue %s did not start a job: %d jobs started in the last hour\n", event.Data.Identifier, s.linearCap.limit)
		writeError(w, http.StatusTooManyRequests, fmt.Errorf("linear has started %d jobs in the last hour", s.linearCap.limit))
		return
	}
	ticket, _, err = s.intake.Linear(r.Context(), job)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	fmt.Fprintf(s.log, "machinist: Linear issue %s started job %s\n", event.Data.Identifier, ticket.ID)
	writeJSON(w, http.StatusCreated, map[string]string{"job": ticket.ID})
}
