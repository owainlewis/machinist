package tickets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	if pgURL := strings.TrimSpace(os.Getenv("TEST_POSTGRES_URL")); pgURL != "" {
		return openPostgresTestStore(t, pgURL)
	}
	store, err := Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func request(key string) NewTicket {
	return NewTicket{Source: "api", Repo: "owner/repo", Title: "Title", Body: "Do the work", Agent: "default", CreatedBy: "alice", SourceKey: key}
}

func createTicket(t *testing.T, store *Store, key string) Ticket {
	t.Helper()
	ticket, created, err := store.CreateTicket(t.Context(), request(key))
	if err != nil || !created {
		t.Fatalf("created = %v, err = %v", created, err)
	}
	return ticket
}

func claimRun(t *testing.T, store *Store, ticket Ticket) *Claim {
	t.Helper()
	if _, err := store.QueueRun(t.Context(), ticket.ID, "claude"); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimRun(t.Context(), "runner-1", []string{"claude"}, time.Now().Add(time.Minute), nil)
	if err != nil || claim == nil {
		t.Fatalf("claim = %v, err = %v", claim, err)
	}
	return claim
}

func completeTo(state string) func(Ticket, string) Transition {
	return func(Ticket, string) Transition { return Transition{State: state} }
}

func completeRun(t *testing.T, store *Store, runID, state string) {
	t.Helper()
	if _, _, err := store.CompleteRun(t.Context(), RunCompletion{RunID: runID, Status: RunSucceeded}, completeTo(state)); err != nil {
		t.Fatal(err)
	}
}

func reload(t *testing.T, store *Store, id string) Ticket {
	t.Helper()
	ticket, err := store.Ticket(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func TestCreateTicketHasIDBranchAndFirstEntry(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:1")
	if len(ticket.ID) != 18 || !strings.HasPrefix(ticket.ID, "t_") || ticket.State != StateWorking || ticket.Branch() != "machinist/"+ticket.ID {
		t.Fatalf("ticket = %+v", ticket)
	}
	entries, err := store.Entries(t.Context(), ticket.ID)
	if err != nil || len(entries) != 1 || entries[0].Kind != EntryRequest || entries[0].Body != "Do the work" {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
}

func TestCreateTicketIsIdempotentBySourceKey(t *testing.T) {
	store := openStore(t)
	first := createTicket(t, store, "github:comment:1")
	second, created, err := store.CreateTicket(t.Context(), request("github:comment:1"))
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("second = %+v, created = %v, err = %v", second, created, err)
	}
}

func TestOneActiveTicketPerIssueAndAcknowledgement(t *testing.T) {
	store := openStore(t)
	withIssue := request("api:1")
	withIssue.IssueNumber = 7
	first, created, err := store.CreateTicket(t.Context(), withIssue)
	if err != nil || !created {
		t.Fatal(err)
	}
	entries, _ := store.Entries(t.Context(), first.ID)
	if len(entries) != 2 || entries[1].Kind != EntryMessage || entries[1].PostedAt != nil {
		t.Fatalf("no acknowledgement: %+v", entries)
	}
	withIssue.SourceKey = "api:2"
	second, created, err := store.CreateTicket(t.Context(), withIssue)
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("second ticket for an active issue: %+v, %v", second, err)
	}
	if _, err = store.Cancel(t.Context(), first.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	withIssue.SourceKey = "api:3"
	third, created, err := store.CreateTicket(t.Context(), withIssue)
	if err != nil || !created || third.ID == first.ID {
		t.Fatalf("ended ticket blocked a new one: %+v, %v", third, err)
	}
}

func TestOneActiveAgentRunPerTicket(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:1")
	if _, err := store.QueueRun(t.Context(), ticket.ID, "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueRun(t.Context(), ticket.ID, "claude"); err == nil {
		t.Fatal("second active agent run accepted")
	}
}

func TestClaimRunGivesOldestMatchingHarnessAndConsumesInput(t *testing.T) {
	store := openStore(t)
	first := createTicket(t, store, "api:1")
	second := createTicket(t, store, "api:2")
	if _, err := store.QueueRun(t.Context(), first.ID, "codex"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueRun(t.Context(), second.ID, "claude"); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimRun(t.Context(), "runner-1", []string{"claude"}, time.Now().Add(time.Minute), nil)
	if err != nil || claim == nil || claim.Ticket.ID != second.ID || claim.Run.Runner != "runner-1" || claim.Run.Status != RunRunning {
		t.Fatalf("claim = %+v, err = %v", claim, err)
	}
	if claim.Entries[0].ConsumedByRun != claim.Run.ID {
		t.Fatalf("input not consumed: %+v", claim.Entries[0])
	}
	if none, err := store.ClaimRun(t.Context(), "runner-1", []string{"claude"}, time.Now(), nil); err != nil || none != nil {
		t.Fatalf("none = %v, err = %v", none, err)
	}
	if none, err := store.ClaimRun(t.Context(), "runner-1", nil, time.Now(), nil); err != nil || none != nil {
		t.Fatalf("empty harness list: %v, %v", none, err)
	}
}

func TestCompleteRunAppliesTransitionAndStoresResult(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:1")
	claim := claimRun(t, store, ticket)
	tokens, cost := int64(12), 0.5
	report := &protocol.Report{Outcome: protocol.OutcomeFailed, Summary: "Stuck"}
	_, _, err := store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: RunSucceeded, Report: report, Tokens: &tokens, Cost: &cost}, func(Ticket, string) Transition {
		return Transition{State: StateFailed, Report: "failed: Stuck", Message: "Stuck"}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := reload(t, store, ticket.ID); got.State != StateFailed {
		t.Fatalf("state = %s", got.State)
	}
	run, _ := store.Run(t.Context(), claim.Run.ID)
	if run.Status != RunSucceeded || *run.Tokens != 12 || *run.Cost != 0.5 || run.Report.Summary != "Stuck" || run.EndedAt == nil {
		t.Fatalf("run = %+v", run)
	}
	entries, _ := store.Entries(t.Context(), ticket.ID)
	if entries[1].Kind != EntryReport || entries[2].Kind != EntryMessage || entries[2].Body != "Stuck" {
		t.Fatalf("entries = %+v", entries)
	}
	if _, _, err := store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: RunSucceeded}, completeTo(StateDone)); !errors.Is(err, ErrRunNotActive) {
		t.Fatalf("second completion: %v", err)
	}
}

func TestCompleteRunKeepsTheSavedPatch(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:1")
	claim := claimRun(t, store, ticket)
	if _, err := store.RunPatch(t.Context(), claim.Run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("patch before completion: %v", err)
	}
	patch := []byte("Subject: [PATCH] Use Postgres\n\x00binary")
	if _, _, err := store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: RunFailed, Patch: patch}, completeTo(StateFailed)); err != nil {
		t.Fatal(err)
	}
	if body, err := store.RunPatch(t.Context(), claim.Run.ID); err != nil || string(body) != string(patch) {
		t.Fatalf("patch = %q, %v", body, err)
	}
	runs, err := store.Runs(t.Context(), ticket.ID)
	if err != nil || len(runs) != 1 || !runs[0].Patch {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
}

func TestARunWithoutAPatchSaysSo(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:1")
	claim := claimRun(t, store, ticket)
	if _, _, err := store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: RunFailed}, completeTo(StateFailed)); err != nil {
		t.Fatal(err)
	}
	if run, err := store.Run(t.Context(), claim.Run.ID); err != nil || run.Patch {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	if _, err := store.RunPatch(t.Context(), claim.Run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("patch: %v", err)
	}
}

func TestCompleteRunStoresStagesInOrder(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:1")
	claim := claimRun(t, store, ticket)
	tokens, cost := int64(12), 0.5
	stages := []protocol.StageResult{
		{Name: "build", Kind: "agent", State: "succeeded", Outcome: protocol.OutcomePRReady, Tokens: &tokens, Cost: &cost, DurationMillis: 1500},
		{Name: "test", Kind: "script", State: "failed", DurationMillis: 20, Error: "command exited with status 1"},
		{Name: "review", Kind: "agent", State: protocol.StageSkipped, Tokens: &tokens, CostUnknown: true},
	}
	if _, _, err := store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: RunFailed, Stages: stages}, completeTo(StateFailed)); err != nil {
		t.Fatal(err)
	}
	runs, err := store.Runs(t.Context(), ticket.ID)
	if err != nil || len(runs) != 1 || len(runs[0].Stages) != 3 {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	build, test, review := runs[0].Stages[0], runs[0].Stages[1], runs[0].Stages[2]
	if build.Name != "build" || build.Outcome != protocol.OutcomePRReady || *build.Tokens != 12 || *build.Cost != 0.5 || build.DurationMillis != 1500 {
		t.Fatalf("build = %+v", build)
	}
	if test.Kind != "script" || test.State != "failed" || test.Tokens != nil || test.Cost != nil || test.Error != "command exited with status 1" {
		t.Fatalf("test = %+v", test)
	}
	if review.State != protocol.StageSkipped || review.Cost != nil || !review.CostUnknown || build.CostUnknown {
		t.Fatalf("review = %+v", review)
	}
}

func TestWaitingInputReturnsTicketToWorking(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:1")
	claim := claimRun(t, store, ticket)
	if _, _, err := store.AddInput(t.Context(), ticket.ID, Input{Kind: EntryComment, Author: "bob", Body: "Also this", SourceKey: "github:comment:2"}); err != nil {
		t.Fatal(err)
	}
	completeRun(t, store, claim.Run.ID, StatePRReady)
	if got := reload(t, store, ticket.ID); got.State != StateWorking {
		t.Fatalf("state = %s", got.State)
	}
}

func TestAFailedRunIgnoresWaitingInput(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:1")
	claim := claimRun(t, store, ticket)
	if _, _, err := store.AddInput(t.Context(), ticket.ID, Input{Kind: EntryCI, Author: "github", Body: "CI failed on abc", SourceKey: "github:ci:abc"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: RunSucceeded}, completeTo(StateFailed)); err != nil {
		t.Fatal(err)
	}
	if got := reload(t, store, ticket.ID); got.State != StateFailed {
		t.Fatalf("state = %s", got.State)
	}
}

func TestEndedTicketIgnoresWaitingInputAndCompletion(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:1")
	claim := claimRun(t, store, ticket)
	if _, err := store.Cancel(t.Context(), ticket.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	called := false
	_, _, err := store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: RunCancelled}, func(Ticket, string) Transition {
		called = true
		return Transition{State: StateDone}
	})
	if err != nil || called {
		t.Fatalf("called = %v, err = %v", called, err)
	}
	if got := reload(t, store, ticket.ID); got.State != StateCancelled {
		t.Fatalf("state = %s", got.State)
	}
	if run, _ := store.Run(t.Context(), claim.Run.ID); run.Status != RunCancelled {
		t.Fatalf("status = %s", run.Status)
	}
	if _, added, err := store.AddInput(t.Context(), ticket.ID, Input{Kind: EntryComment, Author: "bob", Body: "late"}); err != nil || added {
		t.Fatalf("added = %v, err = %v", added, err)
	}
}

func TestAddInputFollowsStateRows(t *testing.T) {
	for _, test := range []struct {
		from, kind, want string
	}{
		{StateWorking, EntryComment, StateWorking},
		{StatePRReady, EntryReview, StateWorking},
		{StatePRReady, EntryCI, StateWorking},
		{StatePRReady, EntryComment, StateWorking},
	} {
		store := openStore(t)
		ticket := createTicket(t, store, "api:1")
		if _, _, err := store.Apply(t.Context(), ticket.ID, StateWorking, Transition{State: test.from}); err != nil {
			t.Fatal(err)
		}
		got, added, err := store.AddInput(t.Context(), ticket.ID, Input{Kind: test.kind, Author: "bob", Body: "text", SourceKey: "k"})
		if err != nil || !added || got.State != test.want {
			t.Fatalf("%s + %s: state = %s, added = %v, err = %v", test.from, test.kind, got.State, added, err)
		}
		if _, added, err = store.AddInput(t.Context(), ticket.ID, Input{Kind: test.kind, Author: "bob", Body: "text", SourceKey: "k"}); err != nil || added {
			t.Fatalf("repeat added = %v, err = %v", added, err)
		}
	}
}

func TestTurnsSinceHumanRestartsOnHumanInput(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:1")
	assertTurns := func(want int) {
		t.Helper()
		if got, err := store.TurnsSinceHuman(t.Context(), ticket.ID); err != nil || got != want {
			t.Fatalf("turns = %d, want %d, err = %v", got, want, err)
		}
	}
	assertTurns(0)
	claim := claimRun(t, store, ticket)
	completeRun(t, store, claim.Run.ID, StateWorking)
	assertTurns(1)
	claim = claimRun(t, store, ticket)
	completeRun(t, store, claim.Run.ID, StatePRReady)
	assertTurns(2)
	if _, _, err := store.AddInput(t.Context(), ticket.ID, Input{Kind: EntryComment, Author: "alice", Body: "Answer"}); err != nil {
		t.Fatal(err)
	}
	assertTurns(0)
	claim = claimRun(t, store, ticket)
	assertTurns(1)
	completeRun(t, store, claim.Run.ID, StateWorking)
}

func TestLeasesRenewExpireAndAskRunnersToStop(t *testing.T) {
	store := openStore(t)
	now := time.Now()
	store.now = func() time.Time { return now }
	ticket := createTicket(t, store, "api:1")
	claim := claimRun(t, store, ticket)
	if cancel, err := store.RenewLease(t.Context(), claim.Run.ID, now.Add(10*time.Minute)); err != nil || cancel {
		t.Fatalf("cancel = %v, err = %v", cancel, err)
	}
	now = now.Add(9 * time.Minute)
	if count, err := store.ExpireLeases(t.Context()); err != nil || count != 0 {
		t.Fatalf("count = %d, err = %v", count, err)
	}
	now = now.Add(2 * time.Minute)
	if count, err := store.ExpireLeases(t.Context()); err != nil || count != 1 {
		t.Fatalf("count = %d, err = %v", count, err)
	}
	if run, _ := store.Run(t.Context(), claim.Run.ID); run.Status != RunInterrupted {
		t.Fatalf("status = %s", run.Status)
	}
	if _, err := store.RenewLease(t.Context(), claim.Run.ID, now); !errors.Is(err, ErrRunNotActive) {
		t.Fatalf("expired run renewed: %v", err)
	}
	if _, _, err := store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: RunSucceeded}, completeTo(StateDone)); !errors.Is(err, ErrRunNotActive) {
		t.Fatalf("expired run completed: %v", err)
	}
	if _, err := store.RenewLease(t.Context(), "run_unknown", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown run: %v", err)
	}
}

func TestHeartbeatTellsRunnerToStopAfterCancel(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:1")
	claim := claimRun(t, store, ticket)
	if _, err := store.Cancel(t.Context(), ticket.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if cancel, err := store.RenewLease(t.Context(), claim.Run.ID, time.Now().Add(time.Minute)); err != nil || !cancel {
		t.Fatalf("cancel = %v, err = %v", cancel, err)
	}
	if _, err := store.Cancel(t.Context(), ticket.ID, "alice"); !errors.Is(err, ErrTicketEnded) {
		t.Fatalf("second cancel: %v", err)
	}
}

func TestCancelStopsQueuedRun(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:1")
	run, err := store.QueueRun(t.Context(), ticket.ID, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Cancel(t.Context(), ticket.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Run(t.Context(), run.ID); got.Status != RunCancelled {
		t.Fatalf("status = %s", got.Status)
	}
	if claim, _ := store.ClaimRun(context.Background(), "r", []string{"claude"}, time.Now(), nil); claim != nil {
		t.Fatal("cancelled run was claimed")
	}
}

func TestQueueRunRefusesATicketThatIsNotWorking(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:1")
	if _, err := store.Cancel(t.Context(), ticket.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueRun(t.Context(), ticket.ID, "claude"); !errors.Is(err, ErrNotWorking) {
		t.Fatalf("err = %v", err)
	}
	if claim, _ := store.ClaimRun(t.Context(), "r", []string{"claude"}, time.Now(), nil); claim != nil {
		t.Fatal("a cancelled ticket got a run")
	}
}

func TestTicketsAwaitingRunSkipsActiveRunsAndOtherStates(t *testing.T) {
	store := openStore(t)
	waiting := createTicket(t, store, "api:1")
	busy := createTicket(t, store, "api:2")
	paused := createTicket(t, store, "api:3")
	if _, err := store.QueueRun(t.Context(), busy.ID, "claude"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Apply(t.Context(), paused.ID, StateWorking, Transition{State: StatePRReady}); err != nil {
		t.Fatal(err)
	}
	tickets, err := store.TicketsAwaitingRun(t.Context())
	if err != nil || len(tickets) != 1 || tickets[0].ID != waiting.ID {
		t.Fatalf("tickets = %+v, err = %v", tickets, err)
	}
}

func event(sequence int, kind string) json.RawMessage {
	body, _ := json.Marshal(protocol.RunEvent{Sequence: int64(sequence), Type: kind})
	return body
}

func TestIngestRunEventsIsOrderedAndIdempotent(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:1")
	claim := claimRun(t, store, ticket)
	id := claim.Run.ID
	if cursor, err := store.IngestRunEvents(t.Context(), id, []json.RawMessage{event(1, "a"), event(2, "b")}); err != nil || cursor != 2 {
		t.Fatalf("cursor = %d, err = %v", cursor, err)
	}
	if cursor, err := store.IngestRunEvents(t.Context(), id, []json.RawMessage{event(2, "b"), event(3, "c")}); err != nil || cursor != 3 {
		t.Fatalf("replay cursor = %d, err = %v", cursor, err)
	}
	if _, err := store.IngestRunEvents(t.Context(), id, []json.RawMessage{event(5, "e")}); err == nil {
		t.Fatal("gap accepted")
	}
	if _, err := store.IngestRunEvents(t.Context(), id, []json.RawMessage{event(1, "different")}); err == nil {
		t.Fatal("conflict accepted")
	}
	page, err := store.RunEvents(t.Context(), id, 1)
	if err != nil || page.Cursor != 3 || len(page.Events) != 2 {
		t.Fatalf("page = %+v, err = %v", page, err)
	}
	completeRun(t, store, id, StateDone)
	if _, err := store.IngestRunEvents(t.Context(), id, []json.RawMessage{event(4, "d")}); !errors.Is(err, ErrRunNotActive) {
		t.Fatalf("ended run accepted events: %v", err)
	}
}

func TestTokensAuthenticateByHashOnly(t *testing.T) {
	store := openStore(t)
	token, err := store.CreateToken(t.Context(), "runner-1", RoleAll)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, TokenPrefix) || len(token) != len(TokenPrefix)+64 {
		t.Fatalf("token = %q", token)
	}
	if name, role, err := store.TokenName(t.Context(), token); err != nil || name != "runner-1" || role != RoleAll {
		t.Fatalf("name = %q, err = %v", name, err)
	}
	if _, _, err := store.TokenName(t.Context(), "wrong"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong token: %v", err)
	}
	var stored, prefix string
	if err := store.q().QueryRowContext(t.Context(), `SELECT token_hash, prefix FROM tokens`).Scan(&stored, &prefix); err != nil || stored == token || prefix != token[:tokenPrefixDisplayLength] {
		t.Fatalf("stored = %q, prefix = %q, err = %v", stored, prefix, err)
	}
	if _, err := store.CreateToken(t.Context(), " ", RoleAll); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, err := store.CreateToken(t.Context(), "bad/name", RoleOperator); err == nil {
		t.Fatal("slash in name accepted")
	}
}

func TestLegacyTokensWorkWithoutPrefixAndRegenerateKeepsNameAndRole(t *testing.T) {
	store := openStore(t)
	legacy := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, err := store.q().ExecContext(t.Context(), `INSERT INTO tokens(id,name,token_hash,created_at,role) VALUES('old1','legacy',?,?,?)`, hashToken(legacy), stamp(time.Now().UTC()), RoleAll); err != nil {
		t.Fatal(err)
	}
	if name, role, err := store.TokenName(t.Context(), legacy); err != nil || name != "legacy" || role != RoleAll {
		t.Fatalf("legacy auth = (%q, %q, %v)", name, role, err)
	}
	tokens, err := store.ListTokens(t.Context())
	if err != nil || len(tokens) != 1 || tokens[0].Prefix != "" {
		t.Fatalf("tokens = %+v, err = %v", tokens, err)
	}

	// Regenerating revokes the legacy token and issues a new mch_ token with the same name and role.
	fresh, err := store.RegenerateToken(t.Context(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fresh, TokenPrefix) || fresh == legacy {
		t.Fatalf("fresh = %q", fresh)
	}
	if _, _, err := store.TokenName(t.Context(), legacy); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old legacy token still works: %v", err)
	}
	if name, role, err := store.TokenName(t.Context(), fresh); err != nil || name != "legacy" || role != RoleAll {
		t.Fatalf("fresh auth = (%q, %q, %v)", name, role, err)
	}

	// Regenerating a runner token keeps its runner role and clears its runner row.
	oldRunner, err := store.CreateToken(t.Context(), "vm-1", RoleRunner)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRunner(t.Context(), "vm-1", "local", []string{"claude"}, nil, 1, false); err != nil {
		t.Fatal(err)
	}
	newRunner, err := store.RegenerateToken(t.Context(), "vm-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.TokenName(t.Context(), oldRunner); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old runner token still works: %v", err)
	}
	if name, role, err := store.TokenName(t.Context(), newRunner); err != nil || name != "vm-1" || role != RoleRunner {
		t.Fatalf("new runner auth = (%q, %q, %v)", name, role, err)
	}
	if runners, _ := fleet(t, store); len(runners) != 0 {
		t.Fatalf("runners after regenerate = %+v", runners)
	}

	// Regenerating a missing or revoked token returns ErrNotFound.
	if _, err := store.RegenerateToken(t.Context(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing regenerate = %v", err)
	}
	if err := store.RevokeToken(t.Context(), "vm-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegenerateToken(t.Context(), "vm-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked regenerate = %v", err)
	}
}

func TestOpenRestoresDataAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tickets.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ticket := createTicket(t, store, "api:1")
	claim := claimRun(t, store, ticket)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if run, err := reopened.Run(t.Context(), claim.Run.ID); err != nil || run.Status != RunRunning {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
}

func TestOpenFailsJobsLeftWaitingForAnAnswer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tickets.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ticket := createTicket(t, store, "api:waiting")
	if _, err := store.db.Exec(`UPDATE tickets SET state='needs_you' WHERE id=?`, ticket.ID); err != nil {
		t.Fatal(err)
	}
	store.Close()
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reload(t, reopened, ticket.ID); got.State != StateFailed {
		t.Fatalf("state = %s", got.State)
	}
}

func TestListTicketsLimitsToTheNewest(t *testing.T) {
	store := openStore(t)
	for index := range maxListedTickets + 2 {
		createTicket(t, store, fmt.Sprintf("api:%d", index))
	}
	listed, err := store.ListTickets(t.Context(), TicketFilter{})
	if err != nil || len(listed) != maxListedTickets {
		t.Fatalf("count = %d, err = %v", len(listed), err)
	}
}

func TestListTicketsPutsANewerFinishedTicketFirst(t *testing.T) {
	store := openStore(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return start }
	active := createTicket(t, store, "api:active")
	store.now = func() time.Time { return start.Add(time.Minute) }
	finished := createTicket(t, store, "api:finished")
	if _, err := store.Cancel(t.Context(), finished.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	listed, err := store.ListTickets(t.Context(), TicketFilter{})
	if err != nil || len(listed) != 2 {
		t.Fatalf("listed = %+v, err = %v", listed, err)
	}
	if listed[0].ID != finished.ID || listed[1].ID != active.ID {
		t.Fatalf("order = %s, %s, want %s first", listed[0].ID, listed[1].ID, finished.ID)
	}
}

func TestListTicketsPagePaginatesBeyondDefaultLimit(t *testing.T) {
	store := openStore(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const totalTickets = maxListedTickets + 15
	createdIDs := make([]string, 0, totalTickets)
	for index := range totalTickets {
		at := start.Add(time.Duration(index) * time.Second)
		store.now = func() time.Time { return at }
		ticket := createTicket(t, store, fmt.Sprintf("api:%d", index))
		createdIDs = append(createdIDs, ticket.ID)
	}

	// Default call stays backward compatible at 200 tickets while reporting total and has_more.
	firstDefault, err := store.ListTicketsPage(t.Context(), TicketFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstDefault.Tickets) != DefaultTicketLimit || firstDefault.Total != totalTickets || firstDefault.Limit != DefaultTicketLimit || firstDefault.Offset != 0 || !firstDefault.HasMore {
		t.Fatalf("default page = len=%d total=%d limit=%d offset=%d has_more=%v", len(firstDefault.Tickets), firstDefault.Total, firstDefault.Limit, firstDefault.Offset, firstDefault.HasMore)
	}

	// Walk every ticket 50 at a time and verify we reach past the 200-ticket mark cleanly.
	seen := make([]string, 0, totalTickets)
	for offset := 0; offset < totalTickets; offset += 50 {
		page, err := store.ListTicketsPage(t.Context(), TicketFilter{Limit: 50, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		wantHasMore := offset+len(page.Tickets) < totalTickets
		if page.Total != totalTickets || page.Offset != offset || page.Limit != 50 || page.HasMore != wantHasMore {
			t.Fatalf("offset %d: total=%d limit=%d offset=%d has_more=%v (want %v)", offset, page.Total, page.Limit, page.Offset, page.HasMore, wantHasMore)
		}
		for _, ticket := range page.Tickets {
			seen = append(seen, ticket.ID)
		}
	}
	if len(seen) != totalTickets {
		t.Fatalf("seen = %d, want %d", len(seen), totalTickets)
	}
	// Newest created first.
	for i := range totalTickets {
		want := createdIDs[totalTickets-1-i]
		if seen[i] != want {
			t.Fatalf("position %d = %s, want %s", i, seen[i], want)
		}
	}

	// Past the end returns an empty slice, not nil.
	empty, err := store.ListTicketsPage(t.Context(), TicketFilter{Limit: 50, Offset: totalTickets + 10})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Tickets == nil || len(empty.Tickets) != 0 || empty.HasMore || empty.Total != totalTickets {
		t.Fatalf("empty page = %+v", empty)
	}
}

func TestListTicketsPageFiltersStatesCountsAndSources(t *testing.T) {
	store := openStore(t)
	start := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	step := 0
	nextTime := func() time.Time {
		step++
		return start.Add(time.Duration(step) * time.Minute)
	}
	store.now = nextTime

	newFrom := func(origin, key string) Ticket {
		t.Helper()
		ticket, _, err := store.CreateTicket(t.Context(), NewTicket{
			Source:    origin,
			Origin:    origin,
			Repo:      "owner/repo",
			Title:     key,
			Body:      "Do work",
			Agent:     "default",
			CreatedBy: "alice",
			SourceKey: key,
		})
		if err != nil {
			t.Fatal(err)
		}
		return ticket
	}

	runningTicket := newFrom("linear", "linear:1")
	if _, err := store.QueueRun(t.Context(), runningTicket.ID, "claude"); err != nil {
		t.Fatal(err)
	}
	if claim, err := store.ClaimRun(t.Context(), "runner-1", []string{"claude"}, start.Add(time.Hour), func(cand Ticket) bool { return cand.ID == runningTicket.ID }); err != nil || claim == nil {
		t.Fatalf("claim = %v, err = %v", claim, err)
	}
	queuedTicket := newFrom("linear", "linear:2")
	prReadyTicket := newFrom("linear", "linear:3")
	if _, _, err := store.Apply(t.Context(), prReadyTicket.ID, StateWorking, Transition{State: StatePRReady, PRNumber: 7}); err != nil {
		t.Fatal(err)
	}
	doneTicket := newFrom("api", "api:1")
	if _, _, err := store.Apply(t.Context(), doneTicket.ID, StateWorking, Transition{State: StateDone}); err != nil {
		t.Fatal(err)
	}
	failedTicket := newFrom("web", "web:1")
	if _, _, err := store.Apply(t.Context(), failedTicket.ID, StateWorking, Transition{State: StateFailed}); err != nil {
		t.Fatal(err)
	}
	cancelledTicket := newFrom("web", "web:2")
	if _, err := store.Cancel(t.Context(), cancelledTicket.ID, "alice"); err != nil {
		t.Fatal(err)
	}

	// Synthetic "running" and "queued" state filters distinguish active working tickets.
	runningPage, err := store.ListTicketsPage(t.Context(), TicketFilter{States: []string{"running"}})
	if err != nil || len(runningPage.Tickets) != 1 || runningPage.Tickets[0].ID != runningTicket.ID || runningPage.Total != 1 {
		t.Fatalf("runningPage = %+v, err = %v", runningPage, err)
	}
	queuedPage, err := store.ListTicketsPage(t.Context(), TicketFilter{States: []string{"queued"}})
	if err != nil || len(queuedPage.Tickets) != 1 || queuedPage.Tickets[0].ID != queuedTicket.ID || queuedPage.Total != 1 {
		t.Fatalf("queuedPage = %+v, err = %v", queuedPage, err)
	}

	// Multi-state filter done + pr_ready matches both done jobs and still reports full counts and sources.
	donePage, err := store.ListTicketsPage(t.Context(), TicketFilter{States: []string{StateDone, StatePRReady}, Limit: 1, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(donePage.Tickets) != 1 || donePage.Total != 2 || !donePage.HasMore {
		t.Fatalf("donePage = %+v", donePage)
	}
	wantCounts := TicketCounts{Queued: 1, Running: 1, Done: 2, Failed: 1, NeedsPerson: 1, Cancelled: 1}
	if donePage.Counts != wantCounts {
		t.Fatalf("counts = %+v, want %+v", donePage.Counts, wantCounts)
	}
	if len(donePage.Sources) != 3 || donePage.Sources[0] != "api" || donePage.Sources[1] != "linear" || donePage.Sources[2] != "web" {
		t.Fatalf("sources = %v", donePage.Sources)
	}

	// Filtering by source scopes counts to that source, while sources keeps every available source.
	linearPage, err := store.ListTicketsPage(t.Context(), TicketFilter{Source: "linear"})
	if err != nil {
		t.Fatal(err)
	}
	wantLinearCounts := TicketCounts{Queued: 1, Running: 1, Done: 1, NeedsPerson: 1}
	if linearPage.Total != 3 || linearPage.Counts != wantLinearCounts || len(linearPage.Sources) != 3 {
		t.Fatalf("linearPage = total=%d counts=%+v sources=%v", linearPage.Total, linearPage.Counts, linearPage.Sources)
	}
}

func TestListBoardTicketsCapsDoneTo20AndKeepsActiveUncapped(t *testing.T) {
	store := openStore(t)
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	tick := 0
	advance := func() time.Time {
		tick++
		return start.Add(time.Duration(tick) * time.Minute)
	}
	store.now = advance

	// Create 22 active tickets FIRST (older than all finished tickets) plus 2 in-review
	// (pr_ready) tickets to prove neither queued/running nor in-review jobs are dropped
	// when finished jobs exceed 20.
	activeIDs := map[string]bool{}
	for i := range 22 {
		ticket := createTicket(t, store, fmt.Sprintf("api:active:%d", i))
		activeIDs[ticket.ID] = true
	}
	reviewIDs := map[string]bool{}
	for i := range 2 {
		ticket := createTicket(t, store, fmt.Sprintf("api:review:%d", i))
		if _, _, err := store.Apply(t.Context(), ticket.ID, StateWorking, Transition{State: StatePRReady}); err != nil {
			t.Fatal(err)
		}
		reviewIDs[ticket.ID] = true
	}

	// Create 25 finished tickets afterward.
	finished := make([]Ticket, 0, 25)
	for i := range 25 {
		ticket := createTicket(t, store, fmt.Sprintf("api:done:%d", i))
		state := StateDone
		if i%3 == 1 {
			state = StateFailed
		} else if i%3 == 2 {
			state = StateCancelled
		}
		if _, _, err := store.Apply(t.Context(), ticket.ID, StateWorking, Transition{State: state}); err != nil {
			t.Fatal(err)
		}
		finished = append(finished, ticket)
	}

	page, err := store.ListBoardTickets(t.Context(), TicketFilter{}, DefaultBoardDoneLimit)
	if err != nil {
		t.Fatal(err)
	}
	if page.DoneTotal == nil || *page.DoneTotal != 25 || page.DoneLimit == nil || *page.DoneLimit != 20 || !page.HasMore {
		t.Fatalf("done metadata = total=%v limit=%v has_more=%v", page.DoneTotal, page.DoneLimit, page.HasMore)
	}
	if page.Total != 49 || len(page.Tickets) != 44 {
		t.Fatalf("total=%d len=%d, want total=49 len=44 (22 active + 2 review + 20 done)", page.Total, len(page.Tickets))
	}

	gotActive := 0
	gotReview := 0
	gotDone := make([]string, 0, 20)
	for _, ticket := range page.Tickets {
		if ticket.State == StateWorking {
			if !activeIDs[ticket.ID] {
				t.Fatalf("unexpected active ticket %s", ticket.ID)
			}
			gotActive++
		} else if ticket.State == StatePRReady {
			if !reviewIDs[ticket.ID] {
				t.Fatalf("unexpected review ticket %s", ticket.ID)
			}
			gotReview++
		} else {
			gotDone = append(gotDone, ticket.ID)
		}
	}
	if gotActive != 22 || gotReview != 2 {
		t.Fatalf("active=%d review=%d, want 22 and 2", gotActive, gotReview)
	}
	if len(gotDone) != 20 {
		t.Fatalf("done count = %d, want 20", len(gotDone))
	}
	// The 20 done tickets are the most recently updated ones, newest first.
	for i := range 20 {
		want := finished[24-i].ID
		if gotDone[i] != want {
			t.Fatalf("done[%d] = %s, want %s", i, gotDone[i], want)
		}
	}
}

func TestTokenNamesAreUniqueWhileActive(t *testing.T) {
	store := openStore(t)
	token, err := store.CreateToken(t.Context(), "alice", RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateToken(t.Context(), "alice", RoleOperator); !errors.Is(err, ErrTokenExists) {
		t.Fatalf("error = %v", err)
	}
	if err := store.RevokeToken(t.Context(), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.TokenName(t.Context(), token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
	if err := store.RevokeToken(t.Context(), "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
	if _, err := store.CreateToken(t.Context(), "alice", RoleOperator); err != nil {
		t.Fatal(err)
	}
	tokens, err := store.ListTokens(t.Context())
	if err != nil || len(tokens) != 2 || tokens[0].RevokedAt == nil || tokens[1].RevokedAt != nil {
		t.Fatalf("tokens = %+v, err = %v", tokens, err)
	}
}

func TestAnAutomationTicketWithAPullRequestDoesNotHoldItsSchedule(t *testing.T) {
	store := openStore(t)
	scheduled := func(key string) NewTicket {
		ticket := request(key)
		ticket.Source = "schedule:nightly"
		return ticket
	}
	first, created, err := store.CreateTicket(t.Context(), scheduled("schedule:nightly:1"))
	if err != nil || !created {
		t.Fatalf("created = %v, err = %v", created, err)
	}
	if again, created, err := store.CreateTicket(t.Context(), scheduled("schedule:nightly:2")); err != nil || created || again.ID != first.ID {
		t.Fatalf("a second ticket started while the first works: %+v, %v, %v", again, created, err)
	}
	completeRun(t, store, claimRun(t, store, first).Run.ID, StatePRReady)
	if next, created, err := store.CreateTicket(t.Context(), scheduled("schedule:nightly:3")); err != nil || !created || next.ID == first.ID {
		t.Fatalf("next = %+v, created = %v, err = %v", next, created, err)
	}
}

func TestOpenCancelsQueuedReviewerRunsFromBefore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tickets.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ticket, _, err := store.CreateTicket(t.Context(), request("k"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.QueueRun(t.Context(), ticket.ID, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE runs SET kind='reviewer' WHERE id=?`, run.ID); err != nil {
		t.Fatal(err)
	}
	if claim, err := store.ClaimRun(t.Context(), "runner-1", []string{"claude"}, time.Now().Add(time.Minute), nil); err != nil || claim != nil {
		t.Fatalf("a runner claimed a reviewer run: %+v, %v", claim, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.Run(t.Context(), run.ID); err != nil || got.Status != RunCancelled {
		t.Fatalf("run = %+v, err = %v", got, err)
	}
}

func TestRerunReopensAnEndedTicket(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:rerun")
	if _, err := store.Rerun(t.Context(), ticket.ID, "alice"); err != ErrStillWorking {
		t.Fatalf("working: err = %v", err)
	}
	if _, _, err := store.Apply(t.Context(), ticket.ID, StateWorking, Transition{State: StateFailed}); err != nil {
		t.Fatal(err)
	}
	again, err := store.Rerun(t.Context(), ticket.ID, "alice")
	if err != nil || again.State != StateWorking {
		t.Fatalf("ticket = %+v, err = %v", again, err)
	}
	entries, _ := store.Entries(t.Context(), ticket.ID)
	last := entries[len(entries)-1]
	if last.Kind != EntryComment || last.Author != "alice" {
		t.Fatalf("entry = %+v", last)
	}
	if turns, _ := store.TurnsSinceHuman(t.Context(), ticket.ID); turns != 0 {
		t.Fatalf("turns = %d", turns)
	}
	if _, err := store.Rerun(t.Context(), "t_missing", "alice"); err != ErrNotFound {
		t.Fatalf("missing: err = %v", err)
	}
}

func TestAClaimLooksPastRunsTheRunnerCannotTake(t *testing.T) {
	store := openStore(t)
	previous := claimPage
	claimPage = 2
	t.Cleanup(func() { claimPage = previous })
	var wanted Ticket
	for index := range 5 {
		ticket := createTicket(t, store, fmt.Sprintf("k%d", index))
		if _, err := store.QueueRun(t.Context(), ticket.ID, "steps"); err != nil {
			t.Fatal(err)
		}
		wanted = ticket
	}
	accept := func(ticket Ticket) bool { return ticket.ID == wanted.ID }
	claim, err := store.ClaimRun(t.Context(), "runner-1", []string{"steps"}, time.Now().Add(time.Minute), accept)
	if err != nil || claim == nil || claim.Ticket.ID != wanted.ID {
		t.Fatalf("claim = %+v, err = %v", claim, err)
	}
	if none, err := store.ClaimRun(t.Context(), "runner-1", []string{"steps"}, time.Now().Add(time.Minute), accept); err != nil || none != nil {
		t.Fatalf("claim = %+v, err = %v", none, err)
	}
}

func TestTokenUseIsRecordedAtMostOncePerMinute(t *testing.T) {
	store := openStore(t)
	if store.dialect == dialectPostgres {
		t.Skip("SQLite trigger syntax test")
	}
	token, err := store.CreateToken(t.Context(), "runner-1", RoleAll)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE token_writes (at TEXT)`,
		`CREATE TRIGGER count_token_writes AFTER UPDATE OF last_used_at ON tokens BEGIN INSERT INTO token_writes VALUES (NEW.last_used_at); END`,
	} {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	writes := func() int {
		t.Helper()
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM token_writes`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	start := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	for _, step := range []struct {
		at     time.Duration
		writes int
	}{
		{0, 1},
		{30 * time.Second, 1},
		{59 * time.Second, 1},
		{61 * time.Second, 2},
		{90 * time.Second, 2},
		{122 * time.Second, 3},
	} {
		now := start.Add(step.at)
		store.SetClock(func() time.Time { return now })
		if name, role, err := store.TokenName(t.Context(), token); err != nil || name != "runner-1" || role != RoleAll {
			t.Fatalf("at %v: name = %q, role = %q, err = %v", step.at, name, role, err)
		}
		if got := writes(); got != step.writes {
			t.Fatalf("at %v: writes = %d, want %d", step.at, got, step.writes)
		}
	}
	tokens, err := store.ListTokens(t.Context())
	if err != nil || len(tokens) != 1 || tokens[0].LastUsedAt == nil || !tokens[0].LastUsedAt.Equal(start.Add(122*time.Second)) {
		t.Fatalf("tokens = %+v, err = %v", tokens, err)
	}
}

// ENG-132: a run cancelled while running is only told to stop at its next
// heartbeat. Running the job again before then must not revive it.
func TestACancelledRunNeverLandsOnTheRerun(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:cancel-rerun")
	old := claimRun(t, store, ticket).Run
	if _, err := store.Cancel(t.Context(), ticket.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Rerun(t.Context(), ticket.ID, "alice"); !errors.Is(err, ErrStillWorking) {
		t.Fatalf("rerun while the cancelled run is still going: err = %v", err)
	}
	if stop, err := store.RenewLease(t.Context(), old.ID, time.Now().Add(time.Minute)); err != nil || !stop {
		t.Fatalf("stop = %v, err = %v", stop, err)
	}

	// Even with the ticket working again, the cancelled run stays cancelled.
	if _, err := store.q().ExecContext(t.Context(), `UPDATE tickets SET state=? WHERE id=?`, StateWorking, ticket.ID); err != nil {
		t.Fatal(err)
	}
	if stop, err := store.RenewLease(t.Context(), old.ID, time.Now().Add(time.Minute)); err != nil || !stop {
		t.Fatalf("revived: stop = %v, err = %v", stop, err)
	}
	if _, _, err := store.CompleteRun(t.Context(), RunCompletion{RunID: old.ID, Status: RunSucceeded}, completeTo(StatePRReady)); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Ticket(t.Context(), ticket.ID); got.State != StateWorking {
		t.Fatalf("the cancelled run's result landed: state = %s", got.State)
	}
	if got, _ := store.Run(t.Context(), old.ID); got.Status != RunCancelled {
		t.Fatalf("old run status = %s", got.Status)
	}
}

func TestRerunWaitsForTheCancelledRunToEnd(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:cancel-wait")
	old := claimRun(t, store, ticket).Run
	if _, err := store.Cancel(t.Context(), ticket.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CompleteRun(t.Context(), RunCompletion{RunID: old.ID, Status: RunCancelled}, completeTo(StateFailed)); err != nil {
		t.Fatal(err)
	}
	again, err := store.Rerun(t.Context(), ticket.ID, "alice")
	if err != nil || again.State != StateWorking {
		t.Fatalf("ticket = %+v, err = %v", again, err)
	}
	next := claimRun(t, store, again).Run
	if stop, err := store.RenewLease(t.Context(), next.ID, time.Now().Add(time.Minute)); err != nil || stop {
		t.Fatalf("the rerun was told to stop: stop = %v, err = %v", stop, err)
	}
	completeRun(t, store, next.ID, StatePRReady)
	if got, _ := store.Ticket(t.Context(), ticket.ID); got.State != StatePRReady {
		t.Fatalf("state = %s", got.State)
	}
}

func TestReplyAfterAMergeForgetsTheMergedPullRequest(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:reply-merged")
	run := claimRun(t, store, ticket).Run
	merged := time.Now().Add(-time.Hour)
	if _, _, err := store.CompleteRun(t.Context(), RunCompletion{RunID: run.ID, Status: RunSucceeded}, func(Ticket, string) Transition {
		return Transition{State: StateDone, PRNumber: 7, MergedAt: &merged}
	}); err != nil {
		t.Fatal(err)
	}
	again, err := store.Rerun(t.Context(), ticket.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if again.PRNumber != 0 || again.MergedAt != nil || again.PRState != "" {
		t.Fatalf("stale pull request: number = %d, merged_at = %v, state = %q", again.PRNumber, again.MergedAt, again.PRState)
	}
}

func TestReplyToAnOpenPullRequestKeepsIt(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:reply-open")
	run := claimRun(t, store, ticket).Run
	if _, _, err := store.CompleteRun(t.Context(), RunCompletion{RunID: run.ID, Status: RunSucceeded}, func(Ticket, string) Transition {
		return Transition{State: StatePRReady, PRNumber: 7}
	}); err != nil {
		t.Fatal(err)
	}
	again, err := store.Reply(t.Context(), ticket.ID, "alice", []Comment{{Body: "Rename it."}})
	if err != nil {
		t.Fatal(err)
	}
	if again.PRNumber != 7 {
		t.Fatalf("pr_number = %d", again.PRNumber)
	}
}

func TestUnpostedMessagesOnlyIncludesIssueTickets(t *testing.T) {
	store := openStore(t)
	webTicket := createTicket(t, store, "run:web-1")
	if _, _, err := store.Apply(t.Context(), webTicket.ID, StateWorking, Transition{State: StateFailed, PRNumber: 42, Message: "The run ended without a report: exit 1"}); err != nil {
		t.Fatal(err)
	}
	// Even a legacy row with posted_at IS NULL on a non-issue ticket with a PR must not be posted.
	if _, err := store.q().ExecContext(t.Context(), `UPDATE ticket_entries SET posted_at=NULL WHERE ticket_id=? AND kind=?`, webTicket.ID, EntryMessage); err != nil {
		t.Fatal(err)
	}

	issueReq := request("github:issue:9")
	issueReq.Source = "github"
	issueReq.IssueNumber = 9
	issueTicket, _, err := store.CreateTicket(t.Context(), issueReq)
	if err != nil {
		t.Fatal(err)
	}

	messages, err := store.UnpostedMessages(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Number != 9 {
		t.Fatalf("messages = %+v, want only issue #9", messages)
	}
	if entries, err := store.Entries(t.Context(), issueTicket.ID); err != nil || len(entries) < 2 {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
}

func TestTokenScopesAndRevocation(t *testing.T) {
	store := openStore(t)
	readToken, err := store.CreateScopedToken(t.Context(), "watcher", RoleOperator, ScopeRead)
	if err != nil {
		t.Fatal(err)
	}
	runToken, err := store.CreateScopedToken(t.Context(), "grokbot", RoleOperator, ScopeRun)
	if err != nil {
		t.Fatal(err)
	}
	fullToken, err := store.CreateToken(t.Context(), "alice", RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	runnerToken, err := store.CreateToken(t.Context(), "vm-1", RoleRunner)
	if err != nil {
		t.Fatal(err)
	}

	// Invalid scope or scope on non-operator role is rejected.
	if _, err := store.CreateScopedToken(t.Context(), "bad1", RoleOperator, "admin"); err == nil {
		t.Fatal("unknown scope accepted")
	}
	if _, err := store.CreateScopedToken(t.Context(), "bad2", RoleRunner, ScopeRead); err == nil {
		t.Fatal("runner token with scope accepted")
	}

	// Authenticate returns role and scope.
	if auth, err := store.Authenticate(t.Context(), readToken); err != nil || auth.Name != "watcher" || auth.Role != RoleOperator || auth.Scope != ScopeRead {
		t.Fatalf("read auth = %+v, err = %v", auth, err)
	}
	if auth, err := store.Authenticate(t.Context(), runToken); err != nil || auth.Name != "grokbot" || auth.Role != RoleOperator || auth.Scope != ScopeRun {
		t.Fatalf("run auth = %+v, err = %v", auth, err)
	}
	if auth, err := store.Authenticate(t.Context(), fullToken); err != nil || auth.Name != "alice" || auth.Role != RoleOperator || auth.Scope != ScopeAll {
		t.Fatalf("full auth = %+v, err = %v", auth, err)
	}
	if auth, err := store.Authenticate(t.Context(), runnerToken); err != nil || auth.Name != "vm-1" || auth.Role != RoleRunner || auth.Scope != ScopeAll {
		t.Fatalf("runner auth = %+v, err = %v", auth, err)
	}

	// AllowsScope enforces least privilege while preserving full access for existing tokens.
	for _, tc := range []struct {
		role, scope, want string
		ok                bool
	}{
		{RoleAll, ScopeAll, ScopeRead, true},
		{RoleAll, ScopeAll, ScopeRun, true},
		{RoleAll, ScopeAll, ScopeAll, true},
		{RoleOperator, ScopeAll, ScopeRead, true},
		{RoleOperator, ScopeAll, ScopeRun, true},
		{RoleOperator, ScopeAll, ScopeAll, true},
		{RoleOperator, ScopeRun, ScopeRead, true},
		{RoleOperator, ScopeRun, ScopeRun, true},
		{RoleOperator, ScopeRun, ScopeAll, false},
		{RoleOperator, ScopeRead, ScopeRead, true},
		{RoleOperator, ScopeRead, ScopeRun, false},
		{RoleOperator, ScopeRead, ScopeAll, false},
		{RoleRunner, ScopeAll, ScopeRead, false},
		{RoleRunner, ScopeAll, ScopeRun, false},
		{RoleRunner, ScopeAll, ScopeAll, false},
	} {
		if got := AllowsScope(tc.role, tc.scope, tc.want); got != tc.ok {
			t.Fatalf("AllowsScope(%q, %q, %q) = %v, want %v", tc.role, tc.scope, tc.want, got, tc.ok)
		}
	}

	// Regenerating keeps the scope.
	freshRun, err := store.RegenerateToken(t.Context(), "grokbot")
	if err != nil {
		t.Fatal(err)
	}
	if auth, err := store.Authenticate(t.Context(), freshRun); err != nil || auth.Name != "grokbot" || auth.Scope != ScopeRun {
		t.Fatalf("regenerated auth = %+v, err = %v", auth, err)
	}

	// Revoking one token does not revoke others.
	if err := store.RevokeToken(t.Context(), "grokbot"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(t.Context(), freshRun); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token auth = %v", err)
	}
	if _, err := store.Authenticate(t.Context(), readToken); err != nil {
		t.Fatalf("other token failed after revoking grokbot: %v", err)
	}

	// ListTokens reports each token's scope.
	listed, err := store.ListTokens(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	scopes := map[string]string{}
	for _, info := range listed {
		if info.RevokedAt == nil {
			scopes[info.Name] = info.Scope
		}
	}
	if scopes["watcher"] != ScopeRead || scopes["alice"] != ScopeAll {
		t.Fatalf("active scopes = %+v", scopes)
	}
}
