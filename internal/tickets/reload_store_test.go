package tickets

import (
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestQueuedTicketIDsAndDeleteToken(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := t.Context()

	// Create three tickets: one awaiting QueueRun, one queued, one running, and one cancelled.
	awaiting, _, err := store.CreateTicket(ctx, NewTicket{Source: "run", Repo: "owner/repo", Title: "A", Body: "A", Agent: "default", CreatedBy: "alice", SourceKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	queued, _, err := store.CreateTicket(ctx, NewTicket{Source: "run", Repo: "owner/repo", Title: "Q", Body: "Q", Agent: "default", CreatedBy: "alice", SourceKey: "k2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueRun(ctx, queued.ID, "steps"); err != nil {
		t.Fatal(err)
	}
	running, _, err := store.CreateTicket(ctx, NewTicket{Source: "run", Repo: "owner/repo", Title: "R", Body: "R", Agent: "default", CreatedBy: "alice", SourceKey: "k3"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueRun(ctx, running.ID, "steps"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRun(ctx, "runner-1", []string{"steps"}, time.Now().Add(time.Minute), func(tk Ticket) bool { return tk.ID == running.ID }); err != nil {
		t.Fatal(err)
	}
	cancelled, _, err := store.CreateTicket(ctx, NewTicket{Source: "run", Repo: "owner/repo", Title: "C", Body: "C", Agent: "default", CreatedBy: "alice", SourceKey: "k4"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cancel(ctx, cancelled.ID, "alice"); err != nil {
		t.Fatal(err)
	}

	got, err := store.QueuedTicketIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{awaiting.ID, queued.ID}
	if !slices.Equal(got, want) {
		t.Fatalf("QueuedTicketIDs = %v, want %v", got, want)
	}

	// DeleteToken removes the token completely so it cannot authenticate and does not appear in ListTokens.
	raw, err := store.CreateToken(ctx, "reload-temp", RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteToken(ctx, "reload-temp"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(ctx, raw); err == nil {
		t.Fatal("deleted token still authenticates")
	}
	listed, err := store.ListTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("ListTokens = %+v, want empty", listed)
	}
}
