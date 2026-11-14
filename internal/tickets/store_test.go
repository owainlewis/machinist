package tickets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

func openStore(t *testing.T) *Store {
	t.Helper()
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
	claim, err := store.ClaimRun(t.Context(), "runner-1", []string{"claude"}, time.Now().Add(time.Minute))
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
	if err := store.CompleteRun(t.Context(), RunCompletion{RunID: runID, Status: RunSucceeded}, 5, completeTo(state)); err != nil {
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
	if _, err := queueRun(t.Context(), store.db, ticket.ID, RunKindReviewer, "review", "claude", time.Now()); err != nil {
		t.Fatalf("reviewer run blocked: %v", err)
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
	claim, err := store.ClaimRun(t.Context(), "runner-1", []string{"claude"}, time.Now().Add(time.Minute))
	if err != nil || claim == nil || claim.Ticket.ID != second.ID || claim.Run.Runner != "runner-1" || claim.Run.Status != RunRunning {
		t.Fatalf("claim = %+v, err = %v", claim, err)
	}
	if claim.Entries[0].ConsumedByRun != claim.Run.ID {
		t.Fatalf("input not consumed: %+v", claim.Entries[0])
	}
	if none, err := store.ClaimRun(t.Context(), "runner-1", []string{"claude"}, time.Now()); err != nil || none != nil {
		t.Fatalf("none = %v, err = %v", none, err)
	}
	if none, err := store.ClaimRun(t.Context(), "runner-1", nil, time.Now()); err != nil || none != nil {
		t.Fatalf("empty harness list: %v, %v", none, err)
	}
}

func TestCompleteRunAppliesTransitionAndStoresResult(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:1")
	claim := claimRun(t, store, ticket)
	tokens, cost := int64(12), 0.5
	report := &protocol.Report{Outcome: protocol.OutcomeNeedsInput, Summary: "Stuck", Question: "Which?"}
	err := store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: RunSucceeded, Report: report, Tokens: &tokens, Cost: &cost}, 5, func(Ticket, string) Transition {
		return Transition{State: StateNeedsYou, Report: "needs_input: Stuck", Message: "Which?"}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := reload(t, store, ticket.ID); got.State != StateNeedsYou {
		t.Fatalf("state = %s", got.State)
	}
	run, _ := store.Run(t.Context(), claim.Run.ID)
	if run.Status != RunSucceeded || *run.Tokens != 12 || *run.Cost != 0.5 || run.Report.Question != "Which?" || run.EndedAt == nil {
		t.Fatalf("run = %+v", run)
	}
	entries, _ := store.Entries(t.Context(), ticket.ID)
	if entries[1].Kind != EntryReport || entries[2].Kind != EntryMessage || entries[2].Body != "Which?" {
		t.Fatalf("entries = %+v", entries)
	}
	if err := store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: RunSucceeded}, 5, completeTo(StateDone)); !errors.Is(err, ErrRunNotActive) {
		t.Fatalf("second completion: %v", err)
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
		{Name: "review", Kind: "agent", State: protocol.StageSkipped},
	}
	if err := store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: RunFailed, Stages: stages}, 5, completeTo(StateNeedsYou)); err != nil {
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
	if review.State != protocol.StageSkipped {
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

func TestWaitingCISignalStopsAtTurnLimit(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:1")
	claim := claimRun(t, store, ticket)
	if _, _, err := store.AddInput(t.Context(), ticket.ID, Input{Kind: EntryCI, Author: "github", Body: "CI failed on abc", SourceKey: "github:ci:abc"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: RunSucceeded}, 1, completeTo(StateNeedsYou)); err != nil {
		t.Fatal(err)
	}
	if got := reload(t, store, ticket.ID); got.State != StateNeedsYou {
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
	err := store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: RunCancelled}, 5, func(Ticket, string) Transition {
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
		{StateNeedsYou, EntryComment, StateWorking},
		{StateNeedsYou, EntryReview, StateNeedsYou},
		{StateNeedsYou, EntryCI, StateNeedsYou},
		{StatePRReady, EntryReview, StateWorking},
		{StatePRReady, EntryCI, StateWorking},
		{StatePRReady, EntryComment, StateWorking},
	} {
		store := openStore(t)
		ticket := createTicket(t, store, "api:1")
		if err := store.Apply(t.Context(), ticket.ID, StateWorking, Transition{State: test.from}); err != nil {
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
	completeRun(t, store, claim.Run.ID, StateNeedsYou)
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
	if err := store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: RunSucceeded}, 5, completeTo(StateDone)); !errors.Is(err, ErrRunNotActive) {
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
	if claim, _ := store.ClaimRun(context.Background(), "r", []string{"claude"}, time.Now()); claim != nil {
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
	if claim, _ := store.ClaimRun(t.Context(), "r", []string{"claude"}, time.Now()); claim != nil {
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
	if err := store.Apply(t.Context(), paused.ID, StateWorking, Transition{State: StateNeedsYou}); err != nil {
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
	token, err := store.CreateToken(t.Context(), "runner-1")
	if err != nil {
		t.Fatal(err)
	}
	if name, err := store.TokenName(t.Context(), token); err != nil || name != "runner-1" {
		t.Fatalf("name = %q, err = %v", name, err)
	}
	if _, err := store.TokenName(t.Context(), "wrong"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong token: %v", err)
	}
	var stored string
	if err := store.db.QueryRow(`SELECT token_hash FROM tokens`).Scan(&stored); err != nil || stored == token {
		t.Fatalf("stored = %q, err = %v", stored, err)
	}
	if _, err := store.CreateToken(t.Context(), " "); err == nil {
		t.Fatal("empty name accepted")
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

func TestAnswerOnlyWhenWaiting(t *testing.T) {
	store := openStore(t)
	ticket := createTicket(t, store, "api:answer")
	if _, err := store.Answer(t.Context(), ticket.ID, "alice", "Too soon"); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("error = %v", err)
	}
	if err := store.Apply(t.Context(), ticket.ID, StateWorking, Transition{State: StateNeedsYou, Message: "Which?"}); err != nil {
		t.Fatal(err)
	}
	waiting := reload(t, store, ticket.ID)
	if question, err := store.WaitingQuestion(t.Context(), waiting); err != nil || question != "Which?" {
		t.Fatalf("question = %q, err = %v", question, err)
	}
	answered, err := store.Answer(t.Context(), ticket.ID, "alice", "Postgres")
	if err != nil || answered.State != StateWorking {
		t.Fatalf("ticket = %+v, err = %v", answered, err)
	}
	if question, err := store.WaitingQuestion(t.Context(), answered); err != nil || question != "" {
		t.Fatalf("question = %q, err = %v", question, err)
	}
	if _, err := store.Answer(t.Context(), "t_missing", "alice", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestListTicketsLimitsToTheNewest(t *testing.T) {
	store := openStore(t)
	for index := range maxListedTickets + 2 {
		createTicket(t, store, fmt.Sprintf("api:%d", index))
	}
	listed, err := store.ListTickets(t.Context(), "", "")
	if err != nil || len(listed) != maxListedTickets {
		t.Fatalf("count = %d, err = %v", len(listed), err)
	}
}

func TestListTicketsKeepsAnOldActiveTicketPastTheLimit(t *testing.T) {
	store := openStore(t)
	oldest := createTicket(t, store, "api:oldest")
	for index := range maxListedTickets + 1 {
		ticket := createTicket(t, store, fmt.Sprintf("api:%d", index))
		if _, err := store.Cancel(t.Context(), ticket.ID, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := store.ListTickets(t.Context(), "", "")
	if err != nil || len(listed) != maxListedTickets {
		t.Fatalf("count = %d, err = %v", len(listed), err)
	}
	if listed[0].ID != oldest.ID {
		t.Fatalf("first = %+v, want the active ticket %s", listed[0], oldest.ID)
	}
}

func TestTokenNamesAreUniqueWhileActive(t *testing.T) {
	store := openStore(t)
	token, err := store.CreateToken(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateToken(t.Context(), "alice"); !errors.Is(err, ErrTokenExists) {
		t.Fatalf("error = %v", err)
	}
	if err := store.RevokeToken(t.Context(), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TokenName(t.Context(), token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
	if err := store.RevokeToken(t.Context(), "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
	if _, err := store.CreateToken(t.Context(), "alice"); err != nil {
		t.Fatal(err)
	}
	tokens, err := store.ListTokens(t.Context())
	if err != nil || len(tokens) != 2 || tokens[0].RevokedAt == nil || tokens[1].RevokedAt != nil {
		t.Fatalf("tokens = %+v, err = %v", tokens, err)
	}
}
