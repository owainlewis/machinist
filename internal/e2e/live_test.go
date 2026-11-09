package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/agentrunner"
	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/controlplane"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/tickets"
)

// The live test spends real provider credits and writes to a real repository.
//
//	MACHINIST_NATIVE_LIVE=1          turn it on
//	MACHINIST_LIVE_REPO              a disposable owner/name with no branch protection
//	MACHINIST_LIVE_HANDLE            the bot's GitHub login
//	GH_TOKEN                         a token for the bot, with write access
//	MACHINIST_LIVE_HUMAN_TOKEN       a token for a person with write access
//	ANTHROPIC_API_KEY                runs Claude Code, skipped if unset
//	OPENAI_API_KEY                   runs Codex, skipped if unset
//	MACHINIST_LIVE_KUBE_CONTEXT      runs Kubernetes, skipped if unset. Also needs
//	MACHINIST_LIVE_KUBE_NAMESPACE, _IMAGE, _SECRET and _PROXY_URL
const (
	livePlaybook = `Create a file named live-check.txt that holds the word "first", commit it, push this branch and open one pull request with gh pr create. If the ticket log asks for a change, make that change on this branch and push it. Do not open a second pull request.`
	liveReviewer = `Post one comment on the pull request with gh pr comment, saying only: live review done.`
	liveDeadline = 20 * time.Minute
)

type liveSettings struct {
	repo, handle, humanToken string
}

func liveHarnesses() map[string]string {
	return map[string]string{"claude": "ANTHROPIC_API_KEY", "codex": "OPENAI_API_KEY"}
}

func TestLiveMentionToMergedPR(t *testing.T) {
	if os.Getenv("MACHINIST_NATIVE_LIVE") != "1" {
		t.Skip("the live scenario is opt-in")
	}
	settings := liveSettings{repo: os.Getenv("MACHINIST_LIVE_REPO"), handle: os.Getenv("MACHINIST_LIVE_HANDLE"), humanToken: os.Getenv("MACHINIST_LIVE_HUMAN_TOKEN")}
	if settings.repo == "" || settings.handle == "" || settings.humanToken == "" || os.Getenv("GH_TOKEN") == "" {
		t.Fatal("MACHINIST_LIVE_REPO, MACHINIST_LIVE_HANDLE, MACHINIST_LIVE_HUMAN_TOKEN and GH_TOKEN are required")
	}
	for harness, key := range liveHarnesses() {
		for _, runnerType := range []string{"local", "kubernetes"} {
			t.Run(harness+"/"+runnerType, func(t *testing.T) {
				if os.Getenv(key) == "" {
					t.Skip(key + " is not set")
				}
				if runnerType == "kubernetes" && os.Getenv("MACHINIST_LIVE_KUBE_CONTEXT") == "" {
					t.Skip("MACHINIST_LIVE_KUBE_CONTEXT is not set")
				}
				runLiveScenario(t, settings, harness, key, runnerType)
			})
		}
	}
}

func liveRunnerConfig(t *testing.T, root, address, harness, key, runnerType string) config.Runner {
	t.Helper()
	body := fmt.Sprintf("control_plane = \"http://%s\"\ntoken_file = \"runner.token\"\ndata_directory = \"runner-data\"\n\n[credentials.%s]\nenv = [%q]\n", address, harness, key)
	if runnerType == "kubernetes" {
		body += fmt.Sprintf("\n[kubernetes]\ncontext = %q\nnamespace = %q\nimage = %q\nsecret = %q\nproxy_url = %q\n", os.Getenv("MACHINIST_LIVE_KUBE_CONTEXT"), os.Getenv("MACHINIST_LIVE_KUBE_NAMESPACE"), os.Getenv("MACHINIST_LIVE_KUBE_IMAGE"), os.Getenv("MACHINIST_LIVE_KUBE_SECRET"), os.Getenv("MACHINIST_LIVE_KUBE_PROXY_URL"))
	}
	path := filepath.Join(root, "runner.toml")
	writeFile(t, path, body, 0o600)
	loaded, err := config.LoadRunner(path)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func runLiveScenario(t *testing.T, settings liveSettings, harness, key, runnerType string) {
	root := t.TempDir()
	factoryDirectory := filepath.Join(root, "factory")
	writeFile(t, filepath.Join(factoryDirectory, "agents/default/agent.toml"), fmt.Sprintf("harness = %q\n", harness), 0o600)
	writeFile(t, filepath.Join(factoryDirectory, "agents/default/playbook.md"), livePlaybook+"\n", 0o600)
	writeFile(t, filepath.Join(factoryDirectory, "reviewers/review.md"), liveReviewer+"\n", 0o600)
	configPath := filepath.Join(root, "config.toml")
	writeFile(t, configPath, fmt.Sprintf("[server]\nlisten = \"127.0.0.1:0\"\ndatabase = \"tickets.db\"\nfactory = \"factory\"\n\n[github]\nhandle = %q\nrepositories = [%q]\n", settings.handle, settings.repo), 0o600)
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
	defer store.Close()
	server, err := controlplane.New(machinistConfig, store, loaded, "live", testLogWriter{t})
	if err != nil {
		t.Fatal(err)
	}
	server.SetPollInterval(10 * time.Second)
	ctx, cancel := context.WithTimeout(t.Context(), liveDeadline)
	defer cancel()
	listening := make(chan net.Addr, 1)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(ctx, func(address net.Addr) { listening <- address }) }()
	address := <-listening

	token, err := store.CreateToken(ctx, "live-runner")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "runner.token"), token+"\n", 0o600)
	runnerConfig := liveRunnerConfig(t, root, address.String(), harness, key, runnerType)
	var executor agentrunner.Executor
	if runnerType == "kubernetes" {
		executor, err = agentrunner.NewKubernetes(runnerConfig)
	} else {
		executor, err = agentrunner.NewLocal(runnerConfig)
	}
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := agentrunner.NewDaemon(runnerConfig, executor, testLogWriter{t})
	if err != nil {
		t.Fatal(err)
	}
	runnerDone := make(chan error, 1)
	go func() { runnerDone <- daemon.Run(ctx) }()
	defer func() {
		cancel()
		<-runnerDone
		<-serverDone
	}()

	// The first poll must set the comment cursor, or the mention counts as old.
	liveEventually(ctx, t, "the first poll", func() bool {
		_, found, err := store.Cursor(ctx, "github:comments", settings.repo)
		return err == nil && found
	})

	human := func(args ...string) string { t.Helper(); return liveGH(t, settings.humanToken, args...) }
	issueURL := human("issue", "create", "--repo", settings.repo, "--title", "Machinist live check "+harness+" "+runnerType, "--body", "Add the live check file.")
	issue, err := strconv.Atoi(issueURL[strings.LastIndex(issueURL, "/")+1:])
	if err != nil {
		t.Fatalf("issue URL = %q", issueURL)
	}
	defer func() {
		_, _ = liveGHRaw(settings.humanToken, "issue", "close", strconv.Itoa(issue), "--repo", settings.repo)
	}()
	human("issue", "comment", strconv.Itoa(issue), "--repo", settings.repo, "--body", "@"+settings.handle+" please do this")

	var ticket tickets.Ticket
	liveEventually(ctx, t, "a ticket for the mention", func() bool {
		ticket, err = store.ActiveTicketFor(ctx, settings.repo, issue)
		return err == nil
	})
	ready := liveWaitForState(ctx, t, store, ticket.ID, tickets.StatePRReady)
	defer func() {
		_, _ = liveGHRaw(settings.humanToken, "api", "--method", "DELETE", "repos/"+settings.repo+"/git/refs/heads/"+ready.Branch())
	}()
	liveEventually(ctx, t, "the reviewer's comment", func() bool {
		out, err := liveGHRaw(settings.humanToken, "pr", "view", strconv.Itoa(ready.PRNumber), "--repo", settings.repo, "--json", "comments")
		return err == nil && strings.Contains(out, "live review done")
	})

	human("pr", "comment", strconv.Itoa(ready.PRNumber), "--repo", settings.repo, "--body", "@"+settings.handle+" change the word in live-check.txt to second")
	liveEventually(ctx, t, "the follow-up to finish", func() bool {
		runs, err := store.Runs(ctx, ticket.ID)
		if err != nil {
			return false
		}
		agentRuns := 0
		for _, run := range runs {
			if run.Kind == tickets.RunKindAgent {
				agentRuns++
			}
		}
		current, err := store.Ticket(ctx, ticket.ID)
		return err == nil && agentRuns >= 2 && current.State == tickets.StatePRReady
	})

	human("pr", "merge", strconv.Itoa(ready.PRNumber), "--repo", settings.repo, "--squash")
	merged := liveWaitForState(ctx, t, store, ticket.ID, tickets.StateDone)
	if merged.PRNumber != ready.PRNumber || merged.MergedAt == nil {
		t.Fatalf("ticket = %+v", merged)
	}
	ledger, err := store.Ledger(ctx, settings.repo, time.Time{})
	if err != nil || ledger.MergedPRs != 1 || ledger.FeedbackRounds != 1 {
		t.Fatalf("ledger = %+v, err = %v", ledger, err)
	}
}

type testLogWriter struct{ t *testing.T }

func (w testLogWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

var _ io.Writer = testLogWriter{}

func liveGHRaw(token string, args ...string) (string, error) {
	command := exec.Command("gh", args...)
	command.Env = append(os.Environ(), "GH_TOKEN="+token)
	output, err := command.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func liveGH(t *testing.T, token string, args ...string) string {
	t.Helper()
	output, err := liveGHRaw(token, args...)
	if err != nil {
		t.Fatalf("gh %s: %v: %s", args[0], err, output)
	}
	return output
}

func liveEventually(ctx context.Context, t *testing.T, what string, condition func() bool) {
	t.Helper()
	for !condition() {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(2 * time.Second):
		}
	}
}

func liveWaitForState(ctx context.Context, t *testing.T, store *tickets.Store, id, want string) tickets.Ticket {
	t.Helper()
	var ticket tickets.Ticket
	liveEventually(ctx, t, "state "+want, func() bool {
		var err error
		ticket, err = store.Ticket(ctx, id)
		return err == nil && ticket.State == want
	})
	return ticket
}
