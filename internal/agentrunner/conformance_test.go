package agentrunner

import (
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
	environment := []string{"MACHINIST_TEST_ROLE=sandbox", "FAKE_KUBE_STATE=" + state, "PATH=" + os.Getenv("PATH")}
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
	// The init container leaves this empty directory for the workspace.
	if err := os.MkdirAll(clusterPath(state, "source/repository"), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	err := kuberunner.Sandbox(ctx, exchange, clusterPath(state, "source/repository"), clusterPath(state, "home"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

type runnerType struct {
	name  string
	build func(t *testing.T, data string) Executor
}

const secretValues = "GH_TOKEN=fixture-github\nANTHROPIC_API_KEY=fixture-anthropic\n"

func buildLocal(t *testing.T, data string) Executor {
	t.Helper()
	t.Setenv("GH_TOKEN", "fixture-github")
	t.Setenv("ANTHROPIC_API_KEY", "fixture-anthropic")
	t.Setenv("MACHINIST_PARENT_SECRET", "must-not-leak")
	local, err := NewLocal(config.Runner{DataDirectory: data, Slots: 1, Env: []string{"ANTHROPIC_API_KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	return local
}

func buildKubernetes(t *testing.T, data string) Executor {
	t.Helper()
	bin := t.TempDir()
	state := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nMACHINIST_TEST_ROLE=kubectl FAKE_KUBE_STATE=%q exec %q \"$@\"\n", state, binary)
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
	executor, err := NewKubernetes(config.Runner{DataDirectory: data, Kubernetes: &config.Kubernetes{Context: "test", Namespace: "runners", Image: "example.test/runner@sha256:" + strings.Repeat("a", 64), Secret: "machinist-agent", ProxyURL: "http://egress:3128"}})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

var runnerTypes = []runnerType{{"local", buildLocal}, {"kubernetes", buildKubernetes}}

func conformanceRequest(steps ...runner.Step) runner.RunRequest {
	return runner.RunRequest{
		RunID: localRun, TicketID: "t_1", Preset: "default", Repository: "owner/repo", Branch: "machinist/t_0123456789abcdef",
		Vars: map[string]string{"repo": "owner/repo"}, Prompt: "Do the work", Steps: steps,
		TimeoutMillis: time.Minute.Milliseconds(), GitName: "bot", GitEmail: "bot@example.test",
	}
}

// cloneStep clones the fixture remote. Real presets clone from GitHub.
func cloneStep(remote string) runner.Step {
	return runner.Step{Name: "checkout", Run: fmt.Sprintf("git clone --quiet %q . && git switch --quiet -c \"$MACHINIST_BRANCH\"", remote)}
}

func eachRunner(t *testing.T, name string, test func(t *testing.T, executor Executor, remote, data, output string)) {
	for _, target := range runnerTypes {
		t.Run(target.name+"/"+name, func(t *testing.T) {
			remote := bareRemote(t)
			data := t.TempDir()
			test(t, target.build(t, data), remote, data, t.TempDir())
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
	eachRunner(t, "steps clone, push and report", func(t *testing.T, executor Executor, remote, data, output string) {
		request := conformanceRequest(
			cloneStep(remote),
			runner.Step{Name: "work", Run: "env > " + output + "/env; cat > " + output + "/prompt; git commit --quiet --allow-empty -m 'conformance run' && git push --quiet origin HEAD"},
			runner.Step{Name: "report", Run: `echo Finished > "$MACHINIST_DIR/result.md"`},
		)
		result := executor.Execute(t.Context(), request)
		if result.State != runner.StateSucceeded || result.Report == nil || result.Report.Outcome != protocol.OutcomeNoChange || result.Report.Summary != "Finished" {
			t.Fatalf("result = %+v", result)
		}
		if len(result.Stages) != 3 || result.Stages[2].State != string(runner.StateSucceeded) {
			t.Fatalf("stages = %+v", result.Stages)
		}
		if subject := remoteLog(t, remote, request.Branch, "%s %ae"); subject != "conformance run bot@example.test" {
			t.Fatalf("pushed commit = %q", subject)
		}
		if prompt := readOutput(t, output, "prompt"); prompt != "Do the work" {
			t.Fatalf("prompt = %q", prompt)
		}
		environment := readOutput(t, output, "env")
		for _, want := range []string{"ANTHROPIC_API_KEY=fixture-anthropic", "GH_TOKEN=fixture-github", "MACHINIST_REPO=owner/repo"} {
			if !strings.Contains(environment, want) {
				t.Fatalf("missing %s in:\n%s", want, environment)
			}
		}
		for _, unwanted := range []string{"must-not-leak"} {
			if strings.Contains(environment, unwanted) {
				t.Fatalf("%s reached the step:\n%s", unwanted, environment)
			}
		}
		if events, err := os.ReadFile(filepath.Join(data, "runs", request.RunID, "events.jsonl")); err != nil || len(events) == 0 {
			t.Fatalf("events = %q, err = %v", events, err)
		}
	})

	eachRunner(t, "question", func(t *testing.T, executor Executor, _, _, _ string) {
		result := executor.Execute(t.Context(), conformanceRequest(
			runner.Step{Name: "ask", Run: `echo "Which database?" > "$MACHINIST_DIR/question.md"`},
			runner.Step{Name: "after", Run: "true"},
		))
		if result.State != runner.StateSucceeded || result.Report == nil || result.Report.Outcome != protocol.OutcomeNeedsInput || result.Report.Question != "Which database?" {
			t.Fatalf("result = %+v", result)
		}
	})

	eachRunner(t, "failing step", func(t *testing.T, executor Executor, _, _, _ string) {
		result := executor.Execute(t.Context(), conformanceRequest(
			runner.Step{Name: "work", Run: "echo broken >&2; exit 3"},
			runner.Step{Name: "after", Run: "true"},
		))
		if result.State != runner.StateFailed || !strings.HasPrefix(result.Error, "step work: ") || result.Report != nil {
			t.Fatalf("result = %+v", result)
		}
	})

	eachRunner(t, "clone failure", func(t *testing.T, executor Executor, remote, _, _ string) {
		if err := os.RemoveAll(remote); err != nil {
			t.Fatal(err)
		}
		result := executor.Execute(t.Context(), conformanceRequest(cloneStep(remote)))
		if result.State != runner.StateFailed || !strings.HasPrefix(result.Error, "step checkout: ") {
			t.Fatalf("result = %+v", result)
		}
	})

	eachRunner(t, "cancel", func(t *testing.T, executor Executor, _, _, output string) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		finished := make(chan runner.RunResult, 1)
		go func() {
			finished <- executor.Execute(ctx, conformanceRequest(runner.Step{Name: "wait", Run: "echo $$ > " + output + "/pid; exec sleep 120"}))
		}()
		pidPath := filepath.Join(output, "pid")
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			if body, err := os.ReadFile(pidPath); err == nil && len(strings.TrimSpace(string(body))) > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the step never started")
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
				t.Fatalf("step process %d is still running", pid)
			}
		}
	})

	eachRunner(t, "timeout", func(t *testing.T, executor Executor, _, _, _ string) {
		request := conformanceRequest(runner.Step{Name: "wait", Run: "sleep 120"})
		request.TimeoutMillis = (3 * time.Second).Milliseconds()
		result := executor.Execute(t.Context(), request)
		if result.State != runner.StateTimedOut || result.Report != nil {
			t.Fatalf("result = %+v", result)
		}
	})
}
