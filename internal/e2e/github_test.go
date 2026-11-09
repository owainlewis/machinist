package e2e

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/fakegh"
	"github.com/owainlewis/machinist/internal/tickets"
)

// builderScript acts on what the prompt asks, the way a real agent would: it
// pushes the branch, opens one PR, and reviewers comment on that PR.
func builderScript(output string) string {
	return `#!/bin/sh
prompt=$(cat)
printf '%s' "$prompt" >> ` + output + `/prompts
result() { printf '%s\n' "{\"type\":\"result\",\"is_error\":false,\"result\":\"done\",\"usage\":{\"input_tokens\":1,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":0,\"output_tokens\":1},\"structured_output\":$1}"; }
printf '%s\n' '{"type":"system","subtype":"init","apiKeySource":"ANTHROPIC_API_KEY"}'
case "$prompt" in
*'THE REVIEWER PROMPT'*)
  number=$(printf '%s' "$prompt" | sed -n 's/.*PR #\([0-9]*\).*/\1/p')
  gh pr comment "$number" --body 'Reviewed: looks fine'
  result '{"outcome":"no_change","summary":"Posted the review"}'
  exit 0;;
esac
case "$prompt" in
*'Use Postgres'*) ;;
*'which database'*)
  result '{"outcome":"needs_input","summary":"Stuck","question":"Which database?"}'
  exit 0;;
esac
git commit --quiet --allow-empty -m "$(date +%s%N) work"
git push --quiet origin HEAD
branch=$(git branch --show-current)
case "$(gh pr list --head "$branch" --json number)" in
*number*) ;;
*) gh pr create --title Work --body Work > /dev/null;;
esac
result '{"outcome":"pr_ready","summary":"The branch is ready"}'
`
}

func startBuilder(t *testing.T, maxTurns int, reviewers bool) *system {
	t.Helper()
	s := startWith(t, "claude", options{claude: builderScript, maxTurns: maxTurns, reviewers: reviewers})
	s.github.SetPermission("alice", "write")
	s.github.SetPermission("reader", "read")
	s.github.SetIssue(5, "Broken login", "Login fails on Safari")
	s.waitForFirstPoll(t)
	return s
}

// waitForFirstPoll waits for the poll that sets the cursor, because a comment
// made before it counts as old.
func (s *system) waitForFirstPoll(t *testing.T) {
	t.Helper()
	eventually(t, "the first poll", func() bool {
		_, found, err := s.store.Cursor(t.Context(), "github:comments", "owner/repo")
		return err == nil && found
	})
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *system) mention(t *testing.T, body string) tickets.Ticket {
	t.Helper()
	s.github.AddComment(fakegh.Comment{ID: 100, Author: "alice", Body: body, Number: 5})
	var ticket tickets.Ticket
	eventually(t, "the ticket", func() bool {
		var err error
		ticket, err = s.store.ActiveTicketFor(t.Context(), "owner/repo", 5)
		return err == nil
	})
	return ticket
}

func (s *system) posted(text string) int {
	count := 0
	for _, post := range s.github.Posted() {
		if strings.Contains(post.Body, text) {
			count++
		}
	}
	return count
}

func (s *system) waitForPost(t *testing.T, text string) {
	t.Helper()
	eventually(t, "a post containing "+text, func() bool { return s.posted(text) > 0 })
}

func (s *system) agentRuns(t *testing.T, id string) []tickets.Run {
	t.Helper()
	runs, err := s.store.Runs(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	agent := []tickets.Run{}
	for _, run := range runs {
		if run.Kind == tickets.RunKindAgent {
			agent = append(agent, run)
		}
	}
	return agent
}

// waitForRuns waits until a ticket has this many finished agent runs and is back in a state.
func (s *system) waitForRuns(t *testing.T, id string, count int, state string) {
	t.Helper()
	eventually(t, "the runs to finish", func() bool {
		ticket, err := s.store.Ticket(t.Context(), id)
		if err != nil || ticket.State != state {
			return false
		}
		runs := s.agentRuns(t, id)
		return len(runs) == count && runs[count-1].EndedAt != nil
	})
}

// CUJ 1.
func TestMentionBecomesOnePRWithAReview(t *testing.T) {
	s := startBuilder(t, 5, true)
	ticket := s.mention(t, "@machinist-bot please fix this")
	s.waitForPost(t, "Working on this as ticket "+ticket.ID)
	ready := s.waitForState(t, ticket.ID, tickets.StatePRReady)
	if ready.PRNumber != 7 {
		t.Fatalf("PR = %d", ready.PRNumber)
	}
	s.waitForPost(t, "https://github.com/owner/repo/pull/7")
	eventually(t, "the reviewer's comment", func() bool { return len(s.github.PRComments()) == 1 })
	if comment := s.github.PRComments()[0]; comment != "7\tReviewed: looks fine" {
		t.Fatalf("comment = %q", comment)
	}
	if s.posted("Working on this") != 1 || s.posted("pull/7") != 1 {
		t.Fatalf("posted = %+v", s.github.Posted())
	}
	for _, post := range s.github.Posted() {
		if post.Number != 5 {
			t.Fatalf("posted on the wrong issue: %+v", post)
		}
	}
	if len(s.agentRuns(t, ticket.ID)) != 1 {
		t.Fatalf("runs = %+v", s.agentRuns(t, ticket.ID))
	}
	if branch := git(t, s.remote, "rev-parse", "--verify", ready.Branch()); branch == "" {
		t.Fatal("branch was not pushed")
	}
	entries, _ := s.store.Entries(t.Context(), ticket.ID)
	for _, want := range []string{"Broken login", "Login fails on Safari", "please fix this"} {
		if !strings.Contains(entries[0].Body, want) {
			t.Fatalf("missing %q in %q", want, entries[0].Body)
		}
	}
}

// CUJ 2.
func TestMentionWithoutWriteAccessDoesNothing(t *testing.T) {
	s := startBuilder(t, 5, true)
	s.github.AddComment(fakegh.Comment{ID: 100, Author: "reader", Body: "@machinist-bot please fix this", Number: 5})
	eventually(t, "the log line", func() bool { return strings.Contains(s.log.String(), "no write access") })
	if _, err := s.store.ActiveTicketFor(t.Context(), "owner/repo", 5); !errors.Is(err, tickets.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
	if len(s.github.Posted()) != 0 {
		t.Fatalf("posted = %+v", s.github.Posted())
	}
}

// CUJ 3.
func TestFollowUpsContinueTheSamePR(t *testing.T) {
	s := startBuilder(t, 5, false)
	ticket := s.mention(t, "@machinist-bot please fix this")
	s.waitForRuns(t, ticket.ID, 1, tickets.StatePRReady)

	s.github.SetPR(7, fakegh.PR{State: "OPEN", HeadSHA: "aaa111", Reviews: []fakegh.Review{{ID: "r1", Author: "alice", State: "CHANGES_REQUESTED"}}})
	s.waitForRuns(t, ticket.ID, 2, tickets.StatePRReady)

	s.github.SetPR(7, fakegh.PR{State: "OPEN", HeadSHA: "bbb222", Reviews: []fakegh.Review{{ID: "r1", Author: "alice", State: "CHANGES_REQUESTED"}}, Checks: []fakegh.Check{{Name: "test", Status: "COMPLETED", Conclusion: "FAILURE"}}})
	s.waitForRuns(t, ticket.ID, 3, tickets.StatePRReady)

	final, err := s.store.Ticket(t.Context(), ticket.ID)
	if err != nil || final.ID != ticket.ID || final.PRNumber != 7 {
		t.Fatalf("ticket = %+v, err = %v", final, err)
	}
	if history := git(t, s.remote, "rev-list", "--count", final.Branch(), "^main"); history != "3" {
		t.Fatalf("commits on the branch = %s", history)
	}
	prompts := s.read(t, "prompts")
	for _, want := range []string{"alice requested changes", "CI failed on bbb222: test"} {
		if !strings.Contains(prompts, want) {
			t.Fatalf("missing %q in:\n%s", want, prompts)
		}
	}
	created := 0
	for _, call := range s.github.Calls() {
		if strings.HasPrefix(call, "pr create") {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("PRs created = %d", created)
	}

	s.github.SetCommits(7, "machinist-bot@users.noreply.github.com", "alice@example.test")
	s.github.SetPR(7, fakegh.PR{State: "MERGED", MergedAt: "2026-07-01T10:00:00Z", HeadSHA: "ccc333"})
	merged := s.waitForState(t, ticket.ID, tickets.StateDone)
	if merged.HumanCommits == nil || *merged.HumanCommits != 1 || merged.MergedAt == nil {
		t.Fatalf("ticket = %+v", merged)
	}
	ledger, err := s.store.Ledger(t.Context(), "owner/repo", time.Time{})
	if err != nil || ledger.MergedPRs != 1 || ledger.FeedbackRounds != 1 || ledger.HumanCommits != 1 {
		t.Fatalf("ledger = %+v, err = %v", ledger, err)
	}
}

// CUJ 4.
func TestCIThatKeepsFailingStopsAtTheTurnLimit(t *testing.T) {
	s := startBuilder(t, 2, false)
	ticket := s.mention(t, "@machinist-bot please fix this")
	s.waitForRuns(t, ticket.ID, 1, tickets.StatePRReady)
	failing := []fakegh.Check{{Name: "test", Status: "COMPLETED", Conclusion: "FAILURE"}}

	s.github.SetPR(7, fakegh.PR{State: "OPEN", HeadSHA: "aaa111", Checks: failing})
	s.waitForRuns(t, ticket.ID, 2, tickets.StatePRReady)

	s.github.SetPR(7, fakegh.PR{State: "OPEN", HeadSHA: "bbb222", Checks: failing})
	s.waitForState(t, ticket.ID, tickets.StateNeedsYou)
	s.waitForPost(t, "CI failed on bbb222")

	if runs := s.agentRuns(t, ticket.ID); len(runs) != 2 {
		t.Fatalf("runs = %+v", runs)
	}
	time.Sleep(200 * time.Millisecond) // Several polls and ticks pass here.
	if runs := s.agentRuns(t, ticket.ID); len(runs) != 2 {
		t.Fatalf("a run started past the limit: %+v", runs)
	}
	if s.posted("limit of 2 runs") != 1 {
		t.Fatalf("posted = %+v", s.github.Posted())
	}
}

// CUJ 5.
func TestQuestionIsPostedAndAReplyContinues(t *testing.T) {
	s := startBuilder(t, 5, false)
	ticket := s.mention(t, "@machinist-bot which database should this use?")
	s.waitForState(t, ticket.ID, tickets.StateNeedsYou)
	s.waitForPost(t, "Which database?")

	s.github.AddComment(fakegh.Comment{ID: 200, Author: "alice", Body: "Use Postgres", Number: 5})
	ready := s.waitForState(t, ticket.ID, tickets.StatePRReady)
	if ready.ID != ticket.ID {
		t.Fatalf("ticket = %+v", ready)
	}
	if !strings.Contains(s.read(t, "prompts"), "Use Postgres") {
		t.Fatalf("prompts = %s", s.read(t, "prompts"))
	}
}

// CUJ 5, for a ticket that has no issue or PR.
func TestQuestionOnATicketWithNoIssueStaysInMachinist(t *testing.T) {
	s := startBuilder(t, 5, false)
	ticket, _, err := s.store.CreateTicket(t.Context(), tickets.NewTicket{Source: "api", Repo: "owner/repo", Title: "T", Body: "which database?", Agent: "default", CreatedBy: "alice", SourceKey: "api:1"})
	if err != nil {
		t.Fatal(err)
	}
	s.waitForState(t, ticket.ID, tickets.StateNeedsYou)
	entries, _ := s.store.Entries(t.Context(), ticket.ID)
	if last := entries[len(entries)-1]; last.Kind != tickets.EntryMessage || last.Body != "Which database?" {
		t.Fatalf("entries = %+v", entries)
	}
	time.Sleep(1200 * time.Millisecond) // At least one delivery pass.
	if len(s.github.Posted()) != 0 {
		t.Fatalf("posted = %+v", s.github.Posted())
	}
}
