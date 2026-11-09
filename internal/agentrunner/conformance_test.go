package agentrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/kuberunner"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
)

// TestMain lets this test binary stand in for kubectl and for the process a
// Kubernetes Job runs, so the suite exercises the real connector and sandbox.
func TestMain(m *testing.M) {
	switch os.Getenv("MACHINIST_TEST_ROLE") {
	case "kubectl":
		os.Exit(fakeKubectl(os.Args[1:]))
	case "sandbox":
		os.Exit(fakeSandbox())
	}
	os.Exit(m.Run())
}

func clusterPath(state, name string) string { return filepath.Join(state, name) }

// exchangePath maps a path inside the Pod to the directory that stands for it.
func exchangePath(state, path string) string {
	return filepath.Join(clusterPath(state, "exchange"), strings.TrimPrefix(path, "/exchange/"))
}

func fakeKubectl(args []string) int {
	state := os.Getenv("FAKE_KUBE_STATE")
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		if strings.Contains(args[0], "=") {
			args = args[1:]
		} else {
			args = args[2:]
		}
	}
	line := strings.Join(args, " ")
	fail := func(message string) int { fmt.Fprintln(os.Stderr, message); return 1 }
	_, gone := os.Stat(clusterPath(state, "deleted"))
	deleted := gone == nil
	switch {
	case strings.HasPrefix(line, "get runtimeclass"), strings.HasPrefix(line, "get networkpolicy"), strings.HasPrefix(line, "get job"):
		fmt.Print(`{}`)
	case strings.HasPrefix(line, "get secret"):
		fmt.Print("secret/agent")
	case strings.HasPrefix(line, "get deployment"):
		fmt.Print(`{"status":{"availableReplicas":1}}`)
	case args[0] == "create":
		if err := startFakeJob(state); err != nil {
			return fail(err.Error())
		}
		fmt.Print("job/created")
	case strings.HasPrefix(line, "get pods"):
		if deleted {
			fmt.Print(`{"items":[]}`)
		} else {
			fmt.Print(`{"items":[{"metadata":{"name":"pod"},"status":{"phase":"Pending","initContainerStatuses":[{"state":{"running":{}}}]}}]}`)
		}
	case strings.HasPrefix(line, "get pod"):
		fmt.Print(`{"status":{"phase":"Running"}}`)
	case args[0] == "logs":
	case strings.Contains(line, "sandbox receive"):
		if err := kuberunner.Receive(os.Stdin, clusterPath(state, "exchange")); err != nil {
			return fail(err.Error())
		}
	case strings.Contains(line, "-- test -f "):
		if _, err := os.Stat(exchangePath(state, args[len(args)-1])); err != nil {
			return fail("not yet")
		}
	case strings.Contains(line, "-- cat "):
		body, err := os.ReadFile(exchangePath(state, args[len(args)-1]))
		if err != nil {
			return fail(err.Error())
		}
		_, _ = os.Stdout.Write(body)
	case strings.HasPrefix(line, "delete job"):
		stopFakeJob(state)
		_ = os.WriteFile(clusterPath(state, "deleted"), nil, 0o600)
	default:
		return fail("fake kubectl: unknown call: " + line)
	}
	return 0
}

// startFakeJob reads the manifest and starts the sandbox with what Kubernetes
// would give it. The Secret reaches it only through the manifest's envFrom.
func startFakeJob(state string) error {
	var job struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						EnvFrom []struct {
							SecretRef struct {
								Name string `json:"name"`
							} `json:"secretRef"`
						} `json:"envFrom"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&job); err != nil {
		return err
	}
	environment := []string{"MACHINIST_TEST_ROLE=sandbox", "FAKE_KUBE_STATE=" + state, "PATH=" + os.Getenv("PATH"), "FAKE_REMOTE=" + os.Getenv("FAKE_REMOTE")}
	for _, source := range job.Spec.Template.Spec.Containers[0].EnvFrom {
		body, err := os.ReadFile(clusterPath(state, "secret-"+source.SecretRef.Name))
		if err != nil {
			return err
		}
		environment = append(environment, strings.Split(strings.TrimSpace(string(body)), "\n")...)
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := os.Create(clusterPath(state, "sandbox.log"))
	if err != nil {
		return err
	}
	sandbox := exec.Command(binary)
	sandbox.Env = environment
	sandbox.Stdout, sandbox.Stderr = log, log
	sandbox.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := sandbox.Start(); err != nil {
		return err
	}
	return os.WriteFile(clusterPath(state, "pid"), []byte(strconv.Itoa(sandbox.Process.Pid)), 0o600)
}

func stopFakeJob(state string) {
	body, err := os.ReadFile(clusterPath(state, "pid"))
	if err != nil {
		return
	}
	pid, _ := strconv.Atoi(string(body))
	_ = syscall.Kill(pid, syscall.SIGTERM)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func fakeSandbox() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	state := os.Getenv("FAKE_KUBE_STATE")
	exchange := clusterPath(state, "exchange")
	for {
		if _, err := os.Stat(filepath.Join(exchange, "ready")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The init container leaves this empty directory for the clone.
	if err := os.MkdirAll(clusterPath(state, "source/repository"), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	remote := os.Getenv("FAKE_REMOTE")
	err := kuberunner.Sandbox(ctx, exchange, clusterPath(state, "source"), clusterPath(state, "home"), func(string) string { return remote })
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// harness installs fake claude and codex commands that behave as the prompt says.
func harness(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	output := t.TempDir()
	claude := `#!/bin/sh
prompt=$(cat)
env > ` + output + `/env
ls -a .claude/agents > ` + output + `/agents
printf '%s\n' '{"type":"system","subtype":"init","apiKeySource":"ANTHROPIC_API_KEY"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"Working"}]}}'
case "$prompt" in
*SLEEP*) echo $$ > ` + output + `/pid; exec sleep 120;;
*CRASH*) echo broken >&2; exit 3;;
esac
git commit --quiet --allow-empty -m 'conformance run'
git push --quiet origin HEAD
printf '%s\n' '{"type":"result","is_error":false,"result":"done","total_cost_usd":0.5,"usage":{"input_tokens":3,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":4},"structured_output":{"outcome":"no_change","summary":"Finished"}}'
`
	codex := `#!/bin/sh
if [ "$1" = login ]; then cat > ` + output + `/codex-key; exit 0; fi
env > ` + output + `/env
while IFS= read -r line; do
case "$line" in
*'"id":1,'*) echo '{"id":1,"result":{}}';;
*'"id":2,'*) echo '{"id":2,"result":{"account":{"type":"apiKey"}}}';;
*'"id":3,'*) echo '{"id":3,"result":{"data":[{"id":"fixture","model":"fixture","isDefault":true}]}}';;
*'"id":4,'*) echo '{"id":4,"result":{"thread":{"id":"primary"}}}';;
*'"id":5,'*) git commit --quiet --allow-empty -m 'codex conformance'; git push --quiet origin HEAD
  echo '{"method":"item/completed","params":{"threadId":"primary","item":{"type":"agentMessage","text":"{\"outcome\":\"no_change\",\"summary\":\"Codex finished\"}"}}}'
  echo '{"method":"turn/completed","params":{"threadId":"primary","turn":{"status":"completed"}}}';;
esac
done
`
	for name, body := range map[string]string{"claude": claude, "codex": codex} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return output
}

type runnerType struct {
	name  string
	build func(t *testing.T, remote, data string) Executor
}

const secretValues = "GH_TOKEN=fixture-github\nANTHROPIC_API_KEY=fixture-anthropic\nOPENAI_API_KEY=fixture-openai\n"

func buildLocal(t *testing.T, remote, data string) Executor {
	t.Helper()
	t.Setenv("GH_TOKEN", "fixture-github")
	t.Setenv("ANTHROPIC_API_KEY", "fixture-anthropic")
	t.Setenv("OPENAI_API_KEY", "fixture-openai")
	t.Setenv("MACHINIST_PARENT_SECRET", "must-not-leak")
	local, err := NewLocal(config.Runner{DataDirectory: data, Slots: 1, Credentials: map[string]config.Credential{"claude": {Env: []string{"ANTHROPIC_API_KEY"}}, "codex": {Env: []string{"OPENAI_API_KEY"}}}})
	if err != nil {
		t.Fatal(err)
	}
	local.CloneURL = func(string) string { return remote }
	return local
}

func buildKubernetes(t *testing.T, remote, data string) Executor {
	t.Helper()
	bin := t.TempDir()
	state := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nMACHINIST_TEST_ROLE=kubectl FAKE_KUBE_STATE=%q FAKE_REMOTE=%q exec %q \"$@\"\n", state, remote, binary)
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MACHINIST_PARENT_SECRET", "must-not-leak")
	for _, directory := range []string{"exchange", "source", "home"} {
		if err := os.MkdirAll(filepath.Join(state, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(state, "secret-machinist-agent"), []byte(secretValues), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopFakeJob(state) })
	executor, err := NewKubernetes(config.Runner{DataDirectory: data, Credentials: map[string]config.Credential{"claude": {Env: []string{"ANTHROPIC_API_KEY"}}, "codex": {Env: []string{"OPENAI_API_KEY"}}}, Kubernetes: &config.Kubernetes{Context: "test", Namespace: "runners", Image: "example.test/runner@sha256:" + strings.Repeat("a", 64), Secret: "machinist-agent", ProxyURL: "http://egress:3128"}})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

var runnerTypes = []runnerType{{"local", buildLocal}, {"kubernetes", buildKubernetes}}

func conformanceRequest(prompt, harnessName string) runner.RunRequest {
	return runner.RunRequest{
		RunID: localRun, TicketID: "t_1", Repository: "owner/repo", Branch: "machinist/t_0123456789abcdef", Prompt: prompt, Harness: harnessName,
		AgentFiles:    []runner.AgentFile{{Path: ".claude/agents/machinist-helper.md", Body: "helper"}},
		TimeoutMillis: time.Minute.Milliseconds(), GitName: "bot", GitEmail: "bot@example.test",
	}
}

func eachRunner(t *testing.T, name string, test func(t *testing.T, executor Executor, remote, data, output string)) {
	for _, target := range runnerTypes {
		t.Run(target.name+"/"+name, func(t *testing.T) {
			output := harness(t)
			remote := bareRemote(t)
			data := t.TempDir()
			test(t, target.build(t, remote, data), remote, data, output)
		})
	}
}

func readOutput(t *testing.T, output, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(output, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func remoteLog(t *testing.T, remote, branch, format string) string {
	t.Helper()
	command := exec.Command("git", "log", "-1", "--format="+format, branch)
	command.Dir = remote
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestConformance(t *testing.T) {
	eachRunner(t, "run and report", func(t *testing.T, executor Executor, remote, data, output string) {
		request := conformanceRequest("Do the work", "claude")
		result := executor.Execute(t.Context(), request)
		if result.State != runner.StateSucceeded || result.Report == nil || result.Report.Outcome != protocol.OutcomeNoChange || result.Report.Summary != "Finished" {
			t.Fatalf("result = %+v", result)
		}
		if result.Tokens == nil || result.Cost == nil || *result.Cost != 0.5 || result.CredentialSource != "ANTHROPIC_API_KEY" {
			t.Fatalf("result = %+v", result)
		}
		if subject := remoteLog(t, remote, request.Branch, "%s %ae"); subject != "conformance run bot@example.test" {
			t.Fatalf("pushed commit = %q", subject)
		}
		if agents := readOutput(t, output, "agents"); !strings.Contains(agents, "machinist-helper.md") {
			t.Fatalf("agents = %q", agents)
		}
		environment := readOutput(t, output, "env")
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
		if events, err := os.ReadFile(filepath.Join(data, "runs", request.RunID, "events.jsonl")); err != nil || len(events) == 0 {
			t.Fatalf("events = %q, err = %v", events, err)
		}
	})

	eachRunner(t, "codex reports on a key", func(t *testing.T, executor Executor, remote, _, output string) {
		request := conformanceRequest("Do the work", "codex")
		result := executor.Execute(t.Context(), request)
		if result.State != runner.StateSucceeded || result.Report == nil || result.Report.Summary != "Codex finished" {
			t.Fatalf("result = %+v", result)
		}
		if key := readOutput(t, output, "codex-key"); key != "fixture-openai" {
			t.Fatalf("key = %q", key)
		}
		if environment := readOutput(t, output, "env"); strings.Contains(environment, "fixture-openai") || strings.Contains(environment, "fixture-anthropic") {
			t.Fatalf("a key reached the agent:\n%s", environment)
		}
		if subject := remoteLog(t, remote, request.Branch, "%s"); subject != "codex conformance" {
			t.Fatalf("pushed commit = %q", subject)
		}
	})

	// CUJ 7: one real run through this runner type, printing the credential source.
	eachRunner(t, "runner check", func(t *testing.T, executor Executor, _, data, _ string) {
		var printed bytes.Buffer
		settings := config.Runner{DataDirectory: data, Credentials: map[string]config.Credential{"claude": {Env: []string{"ANTHROPIC_API_KEY"}}}}
		if err := Check(t.Context(), executor, settings, "owner/repo", &printed); err != nil {
			t.Fatalf("%v: %s", err, printed.String())
		}
		if got := printed.String(); got != "claude: ok, credential source: ANTHROPIC_API_KEY\n" {
			t.Fatalf("printed = %q", got)
		}
	})

	eachRunner(t, "harness failure", func(t *testing.T, executor Executor, _, _, _ string) {
		result := executor.Execute(t.Context(), conformanceRequest("CRASH", "claude"))
		if result.State != runner.StateFailed || result.Error == "" || result.Report != nil {
			t.Fatalf("result = %+v", result)
		}
	})

	eachRunner(t, "clone failure", func(t *testing.T, executor Executor, remote, _, _ string) {
		if err := os.RemoveAll(remote); err != nil {
			t.Fatal(err)
		}
		result := executor.Execute(t.Context(), conformanceRequest("Do the work", "claude"))
		if result.State != runner.StateFailed || !strings.Contains(result.Error, "git clone") {
			t.Fatalf("result = %+v", result)
		}
	})

	eachRunner(t, "cancel", func(t *testing.T, executor Executor, _, _, output string) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		finished := make(chan runner.RunResult, 1)
		go func() { finished <- executor.Execute(ctx, conformanceRequest("SLEEP", "claude")) }()
		pidPath := filepath.Join(output, "pid")
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			if body, err := os.ReadFile(pidPath); err == nil && len(strings.TrimSpace(string(body))) > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the agent never started")
			}
		}
		cancel()
		var result runner.RunResult
		select {
		case result = <-finished:
		case <-time.After(30 * time.Second):
			t.Fatal("run did not stop")
		}
		if result.State != runner.StateCancelled {
			t.Fatalf("result = %+v", result)
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(readOutput(t, output, "pid")))
		for deadline := time.Now().Add(10 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("agent process %d is still running", pid)
			}
		}
	})

	eachRunner(t, "timeout", func(t *testing.T, executor Executor, _, _, _ string) {
		request := conformanceRequest("SLEEP", "claude")
		request.TimeoutMillis = (3 * time.Second).Milliseconds()
		result := executor.Execute(t.Context(), request)
		if result.State != runner.StateTimedOut || result.Report != nil {
			t.Fatalf("result = %+v", result)
		}
	})
}
