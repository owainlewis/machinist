package sources

import (
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/tickets"
)

func pauseLinearTicketAtGate(t *testing.T, p *polling, ticket tickets.Ticket, message, details string) {
	t.Helper()
	run, err := p.store.QueueRun(t.Context(), ticket.ID, protocol.StepsHarness)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := p.store.ClaimRun(t.Context(), "runner-1", []string{protocol.StepsHarness}, p.now.Add(time.Hour), nil)
	if err != nil || claim == nil || claim.Run.ID != run.ID {
		t.Fatalf("claim = (%+v, %v)", claim, err)
	}
	if _, _, err := p.store.CompleteRun(t.Context(), tickets.RunCompletion{
		RunID:      run.ID,
		Status:     tickets.RunWaiting,
		Checkpoint: &protocol.Checkpoint{FailedStep: "approve-spec", HeadSHA: "abc123"},
	}, func(tickets.Ticket, string) tickets.Transition {
		return tickets.Transition{
			State: tickets.StateWaiting,
			Gate: &tickets.Gate{
				Step:    "approve-spec",
				Message: message,
				Details: details,
			},
		}
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLinearDefersRepliesWhileWaitingAtGate(t *testing.T) {
	p := newPolling(t)
	p.poll(t) // initialise cursors

	p.now = p.now.Add(time.Minute)
	p.linear.assign("1", "ENG", "owain", p.now)
	p.poll(t)

	listed := p.tickets(t)
	if len(listed) != 1 {
		t.Fatalf("tickets = %+v", listed)
	}
	ticket := listed[0]
	pauseLinearTicketAtGate(t, p, ticket, "Build this spec?", "SECRET AGENT SPEC OUTPUT")

	// Person comments on Linear while the job is waiting at a gate.
	p.now = p.now.Add(time.Minute)
	p.linear.reply("1", "owain", "Also add metrics", p.now)
	p.poll(t)

	// Reply must be deferred: ticket stays waiting and gate is untouched.
	got, err := p.store.Ticket(t.Context(), ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != tickets.StateWaiting || got.Gate == nil || got.Gate.Step != "approve-spec" {
		t.Fatalf("ticket after reply while waiting = %+v", got)
	}

	// Approve and finish the job; next poll delivers the deferred reply.
	p.now = p.now.Add(time.Minute)
	if _, err := p.store.ApproveGate(t.Context(), ticket.ID, "owain", "ok", "implement"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.store.Apply(t.Context(), ticket.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateDone, Message: "Finished."}); err != nil {
		t.Fatal(err)
	}

	p.now = p.now.Add(time.Minute)
	p.poll(t)

	got, err = p.store.Ticket(t.Context(), ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != tickets.StateWorking {
		t.Fatalf("state after deferred reply = %q, want working", got.State)
	}
	entries, err := p.store.Entries(t.Context(), ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Kind == tickets.EntryComment && entry.Body == "Also add metrics" {
			found = true
		}
	}
	if !found {
		t.Fatalf("deferred reply not added to entries: %+v", entries)
	}
}

func TestLinearCancelsWaitingJobWhenUnassignedOrMovedOutOfInProgress(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fakeIssue)
	}{
		{name: "unassigned", change: func(i *fakeIssue) { i.assignee = "" }},
		{name: "moved to backlog", change: func(i *fakeIssue) { i.state = "s-backlog" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPolling(t)
			p.poll(t)

			p.now = p.now.Add(time.Minute)
			p.linear.assign("1", "ENG", "owain", p.now)
			p.poll(t) // starts job, moves issue to In Progress (ReportStarted)

			ticket := p.tickets(t)[0]
			pauseLinearTicketAtGate(t, p, ticket, "Promote to prod?", "internal details")

			// Poll once while still assigned & In Progress: ticket stays waiting.
			p.now = p.now.Add(time.Minute)
			p.poll(t)
			got, err := p.store.Ticket(t.Context(), ticket.ID)
			if err != nil || got.State != tickets.StateWaiting {
				t.Fatalf("ticket before move = (%+v, %v), want waiting", got, err)
			}

			// Unassign or move out of In Progress -> waiting job is cancelled.
			p.linear.set("1", tc.change)
			p.now = p.now.Add(time.Minute)
			p.poll(t)
			got, err = p.store.Ticket(t.Context(), ticket.ID)
			if err != nil || got.State != tickets.StateCancelled {
				t.Fatalf("ticket after %s = (%+v, %v), want cancelled", tc.name, got, err)
			}
		})
	}
}

func TestLinearReportsGateOnceWithMessageAndLinkNeverDetails(t *testing.T) {
	p := newPolling(t)
	p.poller.PublicURL = "https://machinist.example.com"
	p.poll(t)

	p.now = p.now.Add(time.Minute)
	p.linear.assign("1", "ENG", "owain", p.now)
	p.poll(t)

	ticket := p.tickets(t)[0]
	secretDetails := "CONFIDENTIAL_AGENT_OUTPUT_DO_NOT_LEAK"
	pauseLinearTicketAtGate(t, p, ticket, "Build this spec?", secretDetails)

	// First poll while waiting posts the gate comment once.
	p.now = p.now.Add(time.Minute)
	p.poll(t)

	comments := p.linear.news("1")
	if len(comments) != 1 {
		t.Fatalf("comments after first waiting poll = %q, want 1", comments)
	}
	wantLink := "https://machinist.example.com/#/jobs/" + ticket.ID
	if !strings.Contains(comments[0], "Build this spec?") || !strings.Contains(comments[0], wantLink) {
		t.Fatalf("gate comment = %q, want message and link %q", comments[0], wantLink)
	}
	if strings.Contains(comments[0], secretDetails) {
		t.Fatalf("gate comment leaked gate.details: %q", comments[0])
	}

	// Subsequent poll while still waiting must NOT duplicate the gate comment.
	p.now = p.now.Add(time.Minute)
	p.poll(t)
	if got := p.linear.news("1"); len(got) != 1 {
		t.Fatalf("comments after second waiting poll = %q, want still 1", got)
	}

	// Approving gate 1 and pausing at gate 2 posts a new comment for gate 2.
	p.now = p.now.Add(time.Minute)
	if _, err := p.store.ApproveGate(t.Context(), ticket.ID, "owain", "ok", "implement"); err != nil {
		t.Fatal(err)
	}
	pauseLinearTicketAtGate(t, p, ticket, "Promote to production?", "MORE_SECRET_DETAILS")
	p.now = p.now.Add(time.Minute)
	p.poll(t)

	comments = p.linear.news("1")
	if len(comments) != 2 || !strings.Contains(comments[1], "Promote to production?") || strings.Contains(comments[1], "MORE_SECRET_DETAILS") {
		t.Fatalf("comments after second gate = %q", comments)
	}
}
