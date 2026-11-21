// Package e2e proves the whole run path with fake claude and gh commands and a local Git remote.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	machinistexamples "github.com/owainlewis/machinist/examples"
	"github.com/owainlewis/machinist/internal/agentrunner"
	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/controlplane"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/fakegh"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/tickets"
)

type system struct {
	store  *tickets.Store
	remote string
	output string
	data   string
	github *fakegh.GitHub
	log    *lockedBuffer
	// address is where the control plane listens.
	address string
	// restart stops the control plane and starts a new one on the same database.
	restart func(t *testing.T)
	token   string
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
	// claude is the fake claude command that the shipped presets run.
	claude func(output string) string
	// factory, when set, adds files to the factory before it is loaded.
	factory func(t *testing.T, directory string)
	// github is added to the github section of config.yaml.
	github string
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

// fakeLaterSteps is what the fake claude does after the implement step: the
// review writes the verdict in the output directory's verdict file, or
// accepted, the revise step does nothing, and publish does what
// agents/publish.md asks, with the fake gh.
func fakeLaterSteps(output string) string {
	return `case "$*" in
*"Your step is to review the change"*)
  cat > /dev/null
  verdict=accepted; [ -f ` + output + `/verdict ] && verdict=$(cat ` + output + `/verdict)
  echo "{\"verdict\":\"$verdict\",\"summary\":\"Looks right.\",\"findings\":[]}" > "$MACHINIST_DIR/review.json"
  exit 0;;
*"Your step is to address the review"*)
  cat > /dev/null
  exit 0;;
*"Your step is to ship the change"*)
  cat > /dev/null
  if [ -z "$(git log --oneline origin/HEAD..HEAD)" ]; then echo "The work needed no change." > "$MACHINIST_DIR/result.md"; exit 0; fi
  git push --quiet -u origin HEAD
  { echo "## Summary"; echo; echo "Uses Postgres."; echo; echo "## Assumptions"; echo; cat "$MACHINIST_DIR/assumptions.md"; } > "$MACHINIST_DIR/body.md"
  gh pr create --head "$MACHINIST_BRANCH" --title "Use Postgres" --body-file "$MACHINIST_DIR/body.md" > /dev/null
  number=$(gh pr view "$MACHINIST_BRANCH" | sed 's/.*\///')
  gh pr checks "$number" --watch --required
  if [ "$MACHINIST_AUTO_MERGE" = true ] && grep -q '"verdict":"accepted"' "$MACHINIST_DIR/review.json"; then
    gh pr merge "$number" --merge --delete-branch
    gh pr view "$MACHINIST_BRANCH" > "$MACHINIST_DIR/result.md"
  else
    echo "Left open: $(gh pr view "$MACHINIST_BRANCH")" > "$MACHINIST_DIR/result.md"
  fi
  exit 0;;
esac
`
}

// claudeScript cannot ask which database to use, so it picks one, says so in
// assumptions.md and commits.
func claudeScript(output string) string {
	return `#!/bin/sh
` + fakeLaterSteps(output) + `env > ` + output + `/env
printf '%s' "$*" > ` + output + `/args
cat > ` + output + `/prompt
git status --porcelain > ` + output + `/status
echo '- Used Postgres, because the request named no database.' > "$MACHINIST_DIR/assumptions.md"
git commit --quiet --allow-empty -m 'use postgres'
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git commit"}}]}}'
echo '{"type":"result","subtype":"success","num_turns":2,"duration_ms":1000,"total_cost_usd":0.25,"usage":{"input_tokens":100,"output_tokens":20}}'
`
}

// shippedFactory copies the presets that ship in examples/factory.
func shippedFactory(t *testing.T, directory string) {
	t.Helper()
	files, err := fs.Sub(machinistexamples.Files, "factory")
	if err != nil {
		t.Fatal(err)
	}
	for _, shipped := range []string{"agents", "presets", "prompts", "scripts"} {
		err = fs.WalkDir(files, shipped, func(name string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			body, err := fs.ReadFile(files, name)
			if err != nil {
				return err
			}
			writeFile(t, filepath.Join(directory, filepath.FromSlash(name)), string(body), 0o600)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// start runs a real control plane and a real local runner. The shipped default
// preset runs against a fake claude, a fake gh and a local Git remote.
func start(t *testing.T) *system {
	t.Helper()
	return startWith(t, options{claude: claudeScript})
}

func startWith(t *testing.T, chosen options) *system {
	t.Helper()
	root := t.TempDir()
	output := t.TempDir()
	github := fakegh.Install(t, "machinist-bot")
	remote := newRemote(t)
	github.SetRemote(remote)
	bin := filepath.Join(root, "bin")
	if chosen.claude != nil {
		writeFile(t, filepath.Join(bin, "claude"), chosen.claude(output), 0o700)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+machinistBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_TOKEN", "fixture-github")
	t.Setenv("ANTHROPIC_API_KEY", "fixture-anthropic")
	t.Setenv("OPENAI_API_KEY", "fixture-openai")
	t.Setenv("MACHINIST_PARENT_SECRET", "must-not-leak")

	factoryDirectory := filepath.Join(root, "factory")
	shippedFactory(t, factoryDirectory)
	if chosen.factory != nil {
		chosen.factory(t, factoryDirectory)
	}
	configPath := filepath.Join(root, "config.yaml")
	writeFile(t, configPath, "server:\n  listen: 127.0.0.1:0\n  database: tickets.db\n  factory: factory\n\ngithub:\n  handle: machinist-bot\n  repositories: [owner/repo]\n"+chosen.github, 0o600)
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
	serverContext, stopServer := context.WithCancel(ctx)
	go func() { serverDone <- server.Serve(serverContext, func(address net.Addr) { listening <- address }) }()
	address := <-listening

	token, err := store.CreateToken(t.Context(), "runner-1", tickets.RoleAll)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "runner.token"), token+"\n", 0o600)
	runnerPath := filepath.Join(root, "runner.yaml")
	writeFile(t, runnerPath, fmt.Sprintf("control_plane: http://%s\ntoken_file: runner.token\ndata_directory: runner-data\nenv: [ANTHROPIC_API_KEY]\n", address), 0o600)
	runnerConfig, err := config.LoadRunner(runnerPath)
	if err != nil {
		t.Fatal(err)
	}
	local, err := agentrunner.NewLocal(runnerConfig)
	if err != nil {
		t.Fatal(err)
	}
	s := &system{}
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
		_ = s.store.Close()
	})
	s.store, s.remote, s.output, s.data, s.github, s.log, s.address, s.token = store, remote, output, runnerConfig.DataDirectory, github, log, address.String(), token
	s.restart = func(t *testing.T) {
		t.Helper()
		stopServer()
		if err := <-serverDone; err != nil {
			t.Fatal(err)
		}
		if err := s.store.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := tickets.Open(machinistConfig.Server.Database)
		if err != nil {
			t.Fatal(err)
		}
		s.store = reopened
		machinistConfig.Server.Listen = s.address
		next, err := controlplane.New(machinistConfig, reopened, loaded, "test", log)
		if err != nil {
			t.Fatal(err)
		}
		next.SetPollInterval(20 * time.Millisecond)
		serverContext, stopServer = context.WithCancel(ctx)
		go func() { serverDone <- next.Serve(serverContext, nil) }()
	}
	return s
}

// call sends a request to the control plane with the runner's token.
func (s *system) call(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, "http://"+s.address+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+s.token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reply, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, reply
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

type jobView struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	PRNumber int    `json:"pr_number"`
}

func (s *system) job(t *testing.T, id string) jobView {
	t.Helper()
	status, body := s.call(t, http.MethodGet, "/machinist/jobs/"+id, nil)
	var view jobView
	if err := json.Unmarshal(body, &view); err != nil || status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	return view
}

// The whole path: a run request, the shipped default preset and a pull request,
// in one run that never waits for a person.
func TestRunOpensAPullRequestInOneGo(t *testing.T) {
	s := start(t)
	status, body := s.call(t, http.MethodPost, "/machinist/run", map[string]any{
		"key": "linear:ENG-1", "preset": "default", "vars": map[string]string{"repo": "owner/repo"}, "prompt": "Add a thing\n\nTHE REQUEST",
	})
	var created jobView
	if err := json.Unmarshal(body, &created); err != nil || status != http.StatusCreated {
		t.Fatalf("status = %d: %s", status, body)
	}

	finished := s.waitForState(t, created.ID, tickets.StateDone)
	if view := s.job(t, created.ID); view.State != "done" || view.PRNumber != 7 {
		t.Fatalf("job = %+v", view)
	}
	runs, err := s.store.Runs(t.Context(), created.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	run := runs[0]
	if run.Status != tickets.RunSucceeded || run.Runner != "runner-1" || run.Harness != protocol.StepsHarness {
		t.Fatalf("run = %+v", run)
	}
	if got := stepStates(run); got != "checkout=succeeded implement=succeeded review=succeeded revise=succeeded publish=succeeded" {
		t.Fatalf("steps = %s", got)
	}
	// Each agent step records the agent, harness and model that ran it.
	if agents := run.Stages[1].Agents; len(agents) != 1 || agents[0] != (protocol.Agent{Name: "implement", Harness: "claude"}) {
		t.Fatalf("implement step agents = %+v", agents)
	}
	if agents := run.Stages[0].Agents; len(agents) != 0 {
		t.Fatalf("checkout step agents = %+v", agents)
	}
	if work := run.Stages[1]; work.Tokens == nil || *work.Tokens != 120 || work.Cost == nil || *work.Cost != 0.25 {
		t.Fatalf("work step usage = %+v", work)
	}
	if run.Tokens == nil || *run.Tokens != 120 || run.Cost == nil || *run.Cost != 0.25 {
		t.Fatalf("run usage = tokens %v cost %v", run.Tokens, run.Cost)
	}
	if prompt := s.read(t, "prompt"); !strings.Contains(prompt, "THE REQUEST") || !strings.Contains(prompt, "untrusted") {
		t.Fatalf("prompt:\n%s", prompt)
	}
	if args := s.read(t, "args"); !strings.Contains(args, "running headless") || !strings.Contains(args, "Do the work in the prompt") || !strings.Contains(args, "Never push") || !strings.Contains(args, "write a test that fails") || !strings.Contains(args, "--output-format stream-json") {
		t.Fatalf("system prompt:\n%s", args)
	}
	if status := s.read(t, "status"); status != "" {
		t.Fatalf("the workspace was not a clean clone: %q", status)
	}
	environment := s.read(t, "env")
	for _, want := range []string{"ANTHROPIC_API_KEY=fixture-anthropic", "GH_TOKEN=fixture-github", "MACHINIST_REPO=owner/repo", "MACHINIST_BRANCH=" + finished.Branch()} {
		if !strings.Contains(environment, want) {
			t.Fatalf("missing %s in:\n%s", want, environment)
		}
	}
	for _, unwanted := range []string{"must-not-leak", "fixture-openai"} {
		if strings.Contains(environment, unwanted) {
			t.Fatalf("%s reached a step:\n%s", unwanted, environment)
		}
	}
	page, err := s.store.RunEvents(t.Context(), run.ID, 0)
	if err != nil || len(page.Events) == 0 {
		t.Fatalf("no activity arrived: %+v, %v", page, err)
	}
	if author := git(t, s.remote, "log", "-1", "--format=%s %ae", finished.Branch()); author != "use postgres machinist-bot@users.noreply.github.com" {
		t.Fatalf("pushed commit = %s", author)
	}
	if body := s.github.PRBody(7); !strings.Contains(body, "## Summary") || !strings.Contains(body, "## Assumptions") || !strings.Contains(body, "Used Postgres") {
		t.Fatalf("PR body:\n%s", body)
	}
	if merged := s.github.Merged(); len(merged) != 1 || merged[0] != "7" {
		t.Fatalf("merged = %v", merged)
	}
	if figures, err := s.store.TicketFigures(t.Context(), created.ID); err != nil || !figures.Merged {
		t.Fatalf("a job that merged its own PR was not counted as merged: %+v, %v", figures, err)
	}
	for _, call := range s.github.Calls() {
		if strings.Contains(call, "--draft") {
			t.Fatalf("the pull request was a draft: %s", call)
		}
	}
	entries, err := s.store.Entries(t.Context(), created.ID)
	if err != nil || entries[len(entries)-1].Body != "https://github.com/owner/repo/pull/7" {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
	if status, _ := s.call(t, http.MethodPost, "/machinist/jobs/"+created.ID+"/input", map[string]string{"text": "Use Postgres"}); status == http.StatusOK {
		t.Fatal("a job took input")
	}
}

func stepStates(run tickets.Run) string {
	states := []string{}
	for _, stage := range run.Stages {
		states = append(states, stage.Name+"="+stage.State)
	}
	return strings.Join(states, " ")
}

// A job merges its own pull request only when auto-merge is on and the review
// is clean. Otherwise the pull request stays open for a person.
func TestPullRequestStaysOpenWithoutAutoMergeOrACleanReview(t *testing.T) {
	for _, test := range []struct {
		name, github, verdict string
	}{
		{"auto-merge off", "  auto_merge: false\n", ""},
		{"review needs a person", "", "human_decision_needed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := startWith(t, options{claude: claudeScript, github: test.github})
			if test.verdict != "" {
				writeFile(t, filepath.Join(s.output, "verdict"), test.verdict, 0o600)
			}
			ticket := s.createTicket(t)
			s.waitForState(t, ticket.ID, tickets.StatePRReady)
			if merged := s.github.Merged(); len(merged) != 0 {
				t.Fatalf("merged = %v", merged)
			}
			want := "MACHINIST_AUTO_MERGE=" + map[bool]string{true: "false", false: "true"}[test.github != ""]
			if environment := s.read(t, "env"); !strings.Contains(environment, want) {
				t.Fatalf("missing %s in:\n%s", want, environment)
			}
			runs, err := s.store.Runs(t.Context(), ticket.ID)
			if err != nil || len(runs) != 1 || !strings.HasPrefix(runs[0].Report.Summary, "Left open:") {
				t.Fatalf("runs = %+v, err = %v", runs, err)
			}
		})
	}
}
