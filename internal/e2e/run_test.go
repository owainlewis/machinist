// Package e2e proves the whole run path with fake harnesses and a local Git remote.
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/agentrunner"
	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/controlplane"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/fakegh"
	"github.com/owainlewis/machinist/internal/tickets"
)

type system struct {
	store  *tickets.Store
	remote string
	output string
	data   string
	github *fakegh.GitHub
	log    *lockedBuffer
}

// lockedBuffer collects the control plane's log while it runs.
type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

// options changes what start builds.
type options struct {
	claude    func(output string) string
	maxTurns  int
	reviewers bool
}

func git(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=F", "GIT_AUTHOR_EMAIL=f@example.test", "GIT_COMMITTER_NAME=F", "GIT_COMMITTER_EMAIL=f@example.test")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func newRemote(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "origin.git")
	git(t, root, "init", "--quiet", "--bare", "--initial-branch=main", remote)
	seed := filepath.Join(root, "seed")
	git(t, root, "init", "--quiet", "--initial-branch=main", seed)
	git(t, seed, "commit", "--quiet", "--allow-empty", "-m", "init")
	git(t, seed, "push", "--quiet", remote, "main")
	return remote
}

// claudeScript commits and pushes, then reports needs_input until it sees the answer.
func claudeScript(output string) string {
	return `#!/bin/sh
env > ` + output + `/env
cat > ` + output + `/prompt
ls .claude/agents > ` + output + `/agents
git status --porcelain > ` + output + `/status
git log --format=%s > ` + output + `/log-before
if grep -q 'Use Postgres' ` + output + `/prompt; then
  git commit --quiet --allow-empty -m 'second run'
  report='{"outcome":"no_change","summary":"Finished with Postgres"}'
else
  git commit --quiet --allow-empty -m 'first run'
  report='{"outcome":"needs_input","summary":"Stuck","question":"Which database?"}'
fi
git push --quiet origin HEAD
printf '%s\n' '{"type":"system","subtype":"init","apiKeySource":"ANTHROPIC_API_KEY"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"Working"}]}}'
printf '%s\n' "{\"type\":\"result\",\"is_error\":false,\"result\":\"done\",\"total_cost_usd\":0.5,\"usage\":{\"input_tokens\":10,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":0,\"output_tokens\":5},\"structured_output\":$report}"
`
}

func codexScript(output string) string {
	return `#!/bin/sh
if [ "$1" = login ]; then
  cat > ` + output + `/codex-key
  exit 0
fi
env > ` + output + `/env
cp "$CODEX_HOME/config.toml" ` + output + `/codex-config
while IFS= read -r line; do
case "$line" in
*'"id":1,'*) echo '{"id":1,"result":{}}';;
*'"id":2,'*) echo '{"id":2,"result":{"account":{"type":"apiKey"}}}';;
*'"id":3,'*) echo '{"id":3,"result":{"data":[{"id":"fixture","model":"fixture","isDefault":true}]}}';;
*'"id":4,'*) pwd > ` + output + `/pwd; echo '{"id":4,"result":{"thread":{"id":"primary"}}}';;
*'"id":5,'*) git commit --quiet --allow-empty -m 'codex run'; git push --quiet origin HEAD
  echo '{"method":"item/completed","params":{"threadId":"primary","item":{"type":"agentMessage","text":"{\"outcome\":\"no_change\",\"summary\":\"Codex finished\"}"}}}'
  echo '{"method":"turn/completed","params":{"threadId":"primary","turn":{"status":"completed"}}}';;
esac
done
`
}

// start runs a real control plane and a real local runner against fake harnesses.
func start(t *testing.T, harness string) *system {
	t.Helper()
	return startWith(t, harness, options{claude: claudeScript, maxTurns: 5})
}

func startWith(t *testing.T, harness string, chosen options) *system {
	t.Helper()
	root := t.TempDir()
	output := t.TempDir()
	github := fakegh.Install(t, "machinist-bot")
	bin := filepath.Join(root, "bin")
	writeFile(t, filepath.Join(bin, "claude"), chosen.claude(output), 0o700)
	writeFile(t, filepath.Join(bin, "codex"), codexScript(output), 0o700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_TOKEN", "fixture-github")
	t.Setenv("ANTHROPIC_API_KEY", "fixture-anthropic")
	t.Setenv("OPENAI_API_KEY", "fixture-openai")
	t.Setenv("MACHINIST_PARENT_SECRET", "must-not-leak")

	factoryDirectory := filepath.Join(root, "factory")
	writeFile(t, filepath.Join(factoryDirectory, "agents/default/agent.toml"), fmt.Sprintf("harness = %q\n", harness), 0o600)
	writeFile(t, filepath.Join(factoryDirectory, "agents/default/playbook.md"), "THE PLAYBOOK\n", 0o600)
	writeFile(t, filepath.Join(factoryDirectory, "agents/default/.claude/agents/machinist-helper.md"), "helper\n", 0o600)
	if chosen.reviewers {
		writeFile(t, filepath.Join(factoryDirectory, "reviewers/review.md"), "THE REVIEWER PROMPT\n", 0o600)
	}
	configPath := filepath.Join(root, "config.toml")
	writeFile(t, configPath, fmt.Sprintf("[server]\nmax_turns_without_human = %d\n", chosen.maxTurns)+"listen = \"127.0.0.1:0\"\ndatabase = \"tickets.db\"\nfactory = \"factory\"\n\n[github]\nhandle = \"machinist-bot\"\nrepositories = [\"owner/repo\"]\n", 0o600)
	machinistConfig, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := factory.Load(machinistConfig.Server.Factory)
	if err != nil {
		t.Fatal(err)
	}
	store, err := tickets.Open(machinistConfig.Server.Database)
	if err != nil {
		t.Fatal(err)
	}
	log := &lockedBuffer{}
	server, err := controlplane.New(machinistConfig, store, loaded, "test", log)
	if err != nil {
		t.Fatal(err)
	}
	server.SetPollInterval(20 * time.Millisecond)
	ctx, cancel := context.WithCancel(t.Context())
	listening := make(chan net.Addr, 1)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(ctx, func(address net.Addr) { listening <- address }) }()
	address := <-listening

	token, err := store.CreateToken(t.Context(), "runner-1")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "runner.token"), token+"\n", 0o600)
	runnerPath := filepath.Join(root, "runner.toml")
	writeFile(t, runnerPath, fmt.Sprintf("control_plane = \"http://%s\"\ntoken_file = \"runner.token\"\ndata_directory = \"runner-data\"\n\n[credentials.claude]\nenv = [\"ANTHROPIC_API_KEY\"]\n\n[credentials.codex]\nenv = [\"OPENAI_API_KEY\"]\n", address), 0o600)
	runnerConfig, err := config.LoadRunner(runnerPath)
	if err != nil {
		t.Fatal(err)
	}
	local, err := agentrunner.NewLocal(runnerConfig)
	if err != nil {
		t.Fatal(err)
	}
	remote := newRemote(t)
	local.CloneURL = func(string) string { return remote }
	daemon, err := agentrunner.NewDaemon(runnerConfig, local, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	runnerDone := make(chan error, 1)
	go func() { runnerDone <- daemon.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-runnerDone; err != nil {
			t.Error(err)
		}
		if err := <-serverDone; err != nil {
			t.Error(err)
		}
		_ = store.Close()
	})
	return &system{store: store, remote: remote, output: output, data: runnerConfig.DataDirectory, github: github, log: log}
}

func (s *system) createTicket(t *testing.T) tickets.Ticket {
	t.Helper()
	ticket, created, err := s.store.CreateTicket(t.Context(), tickets.NewTicket{Source: "api", Repo: "owner/repo", Title: "Add a thing", Body: "THE REQUEST", Agent: "default", CreatedBy: "alice", SourceKey: "api:1"})
	if err != nil || !created {
		t.Fatalf("created = %v, err = %v", created, err)
	}
	return ticket
}

func (s *system) waitForState(t *testing.T, id, want string) tickets.Ticket {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		ticket, err := s.store.Ticket(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if ticket.State == want {
			return ticket
		}
		if time.Now().After(deadline) {
			entries, _ := s.store.Entries(t.Context(), id)
			runs, _ := s.store.Runs(t.Context(), id)
			t.Fatalf("state = %s, want %s\nentries: %+v\nruns: %+v", ticket.State, want, entries, runs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *system) read(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(s.output, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func (s *system) waitForCleanup(t *testing.T, runID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for _, path := range []string{filepath.Join(s.data, "work", runID), filepath.Join(s.data, "runs", runID)} {
		for {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s was not removed", path)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestTicketRunsToReportAndContinuesAfterAnAnswer(t *testing.T) {
	s := start(t, "claude")
	ticket := s.createTicket(t)

	asked := s.waitForState(t, ticket.ID, tickets.StateNeedsYou)
	runs, err := s.store.Runs(t.Context(), ticket.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	first := runs[0]
	if first.Status != tickets.RunSucceeded || first.Runner != "runner-1" || first.Report.Question != "Which database?" || *first.Cost != 0.5 || first.Tokens == nil {
		t.Fatalf("first run = %+v", first)
	}
	prompt := s.read(t, "prompt")
	for _, want := range []string{"THE PLAYBOOK", asked.Branch(), "THE REQUEST", `"outcome"`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("missing %q in:\n%s", want, prompt)
		}
	}
	if strings.TrimSpace(s.read(t, "agents")) != "machinist-helper.md" || s.read(t, "status") != "" {
		t.Fatalf("agents = %q, status = %q", s.read(t, "agents"), s.read(t, "status"))
	}
	environment := s.read(t, "env")
	for _, want := range []string{"ANTHROPIC_API_KEY=fixture-anthropic", "GH_TOKEN=fixture-github"} {
		if !strings.Contains(environment, want) {
			t.Fatalf("missing %s in:\n%s", want, environment)
		}
	}
	for _, unwanted := range []string{"must-not-leak", "fixture-openai"} {
		if strings.Contains(environment, unwanted) {
			t.Fatalf("%s reached the agent:\n%s", unwanted, environment)
		}
	}
	if author := git(t, s.remote, "log", "-1", "--format=%ae", asked.Branch()); author != "machinist-bot@users.noreply.github.com" {
		t.Fatalf("author = %s", author)
	}
	page, err := s.store.RunEvents(t.Context(), first.ID, 0)
	if err != nil || len(page.Events) == 0 {
		t.Fatalf("no activity arrived: %+v, %v", page, err)
	}
	s.waitForCleanup(t, first.ID)

	if _, _, err := s.store.AddInput(t.Context(), ticket.ID, tickets.Input{Kind: tickets.EntryComment, Author: "alice", Body: "Use Postgres", SourceKey: "api:answer"}); err != nil {
		t.Fatal(err)
	}
	finished := s.waitForState(t, ticket.ID, tickets.StateDone)
	if !strings.Contains(s.read(t, "log-before"), "first run") {
		t.Fatalf("second run did not resume the branch: %s", s.read(t, "log-before"))
	}
	history := git(t, s.remote, "log", "--format=%s", finished.Branch())
	if !strings.HasPrefix(history, "second run\nfirst run") {
		t.Fatalf("history = %q", history)
	}
	prompt = s.read(t, "prompt")
	for _, want := range []string{"THE REQUEST", "Which database?", "Use Postgres"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("missing %q in second prompt:\n%s", want, prompt)
		}
	}
	entries, err := s.store.Entries(t.Context(), ticket.ID)
	if err != nil || entries[len(entries)-1].Body != "Finished with Postgres" {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
}

func TestCodexTicketRunsToReportOnAKeyCredential(t *testing.T) {
	s := start(t, "codex")
	ticket := s.createTicket(t)
	finished := s.waitForState(t, ticket.ID, tickets.StateDone)

	runs, err := s.store.Runs(t.Context(), ticket.ID)
	if err != nil || len(runs) != 1 || runs[0].Report.Summary != "Codex finished" || runs[0].Harness != "codex" {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	if key := s.read(t, "codex-key"); key != "fixture-openai" {
		t.Fatalf("key = %q", key)
	}
	if environment := s.read(t, "env"); strings.Contains(environment, "fixture-openai") {
		t.Fatalf("key reached the agent environment:\n%s", environment)
	}
	if !strings.Contains(s.read(t, "codex-config"), `[projects."`+strings.TrimSpace(s.read(t, "pwd"))+`"]`) {
		t.Fatalf("clone not trusted: %s", s.read(t, "codex-config"))
	}
	if subject := git(t, s.remote, "log", "-1", "--format=%s", finished.Branch()); subject != "codex run" {
		t.Fatalf("subject = %q", subject)
	}
	s.waitForCleanup(t, runs[0].ID)
}
