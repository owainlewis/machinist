package dockerrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/runner"
)

const fixtureRun = "run_0123456789abcdef01234567"

func fixtureConfig() config.Docker {
	return config.Docker{
		Image: "ghcr.io/owainlewis/machinist-runner:latest",
	}.WithDefaults()
}

func fixtureRequest() runner.RunRequest {
	return runner.RunRequest{
		RunID:         fixtureRun,
		TicketID:      "t_1",
		Preset:        "default",
		Repository:    "owner/repo",
		Branch:        "machinist/t_1",
		Prompt:        "Do the work",
		Steps:         []runner.Step{{Name: "work", Run: "true"}},
		TimeoutMillis: time.Minute.Milliseconds(),
		GitName:       "bot",
		GitEmail:      "bot@example.test",
	}
}

func TestBuildRunArgsHardenedDefaults(t *testing.T) {
	c := Connector{
		Config: fixtureConfig(),
		Env:    []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		getenv: func(k string) string {
			return map[string]string{"ANTHROPIC_API_KEY": "sk-secret-anth", "OPENAI_API_KEY": "sk-secret-oai"}[k]
		},
		hostEnv: func() []string {
			return []string{"PATH=/usr/bin", "DOCKER_HOST=unix:///var/run/docker.sock", "GH_TOKEN=old-host-token", "HOST_ONLY_SECRET=never"}
		},
	}
	dirs := RunDirs{
		Workspace: "/data/work/" + fixtureRun + "/workspace",
		Control:   "/data/work/" + fixtureRun + "/control",
		Home:      "/data/work/" + fixtureRun + "/home",
		Tmp:       "/data/work/" + fixtureRun + "/tmp",
		Exchange:  "/data/work/" + fixtureRun + "/exchange",
	}
	args, env := c.BuildRunArgs(ContainerName(fixtureRun), dirs, "ghs_run_secret", "1001:1001")
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"--rm",
		"--name machinist-" + fixtureRun,
		"--user 1001:1001",
		"--read-only",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		"--pids-limit=512",
		"--memory=4g",
		"--cpus=2",
		"-v /data/work/" + fixtureRun + "/workspace:/work/workspace:rw",
		"-v /data/work/" + fixtureRun + "/control:/work/control:rw",
		"-v /data/work/" + fixtureRun + "/home:/home/agent:rw",
		"-v /data/work/" + fixtureRun + "/tmp:/tmp:rw",
		"-v /data/work/" + fixtureRun + "/exchange:/exchange:rw",
		"-w /work/workspace",
		"-e HOME=/home/agent",
		"-e GH_TOKEN",
		"-e ANTHROPIC_API_KEY",
		"-e OPENAI_API_KEY",
		"--entrypoint machinist ghcr.io/owainlewis/machinist-runner:latest sandbox docker",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in args:\n%s", want, joined)
		}
	}

	// Optional flags must not be emitted when unset, and secret values must never appear in argv.
	for _, forbidden := range []string{
		"--runtime",
		"--network",
		"HTTP_PROXY",
		"HTTPS_PROXY",
		"ghs_run_secret",
		"sk-secret-anth",
		"sk-secret-oai",
		"/home/machinist",
		"HOST_ONLY_SECRET",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("unexpected %q in args:\n%s", forbidden, joined)
		}
	}

	// Secret values must be present in cmdEnv, with run token overriding host GH_TOKEN.
	envMap := map[string]string{}
	for _, entry := range env {
		k, v, _ := strings.Cut(entry, "=")
		envMap[k] = v
	}
	if envMap["GH_TOKEN"] != "ghs_run_secret" || envMap["ANTHROPIC_API_KEY"] != "sk-secret-anth" || envMap["OPENAI_API_KEY"] != "sk-secret-oai" || envMap["DOCKER_HOST"] != "unix:///var/run/docker.sock" {
		t.Fatalf("env = %+v", envMap)
	}
}

func TestBuildRunArgsWithGVisorLimitsNetworkAndProxy(t *testing.T) {
	cfg := config.Docker{
		Image:     "ghcr.io/owainlewis/machinist-runner:v1",
		Runtime:   "runsc",
		Network:   "machinist-internal",
		ProxyURL:  "http://egress:3128",
		Memory:    "8g",
		CPUs:      3.5,
		PIDsLimit: 1024,
	}.WithDefaults()
	c := Connector{
		Config:  cfg,
		hostEnv: func() []string { return nil },
	}
	dirs := RunDirs{Workspace: "/w", Control: "/c", Home: "/h", Tmp: "/t", Exchange: "/e"}
	args, _ := c.BuildRunArgs("machinist-test", dirs, "", "1000:1000")
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"--runtime=runsc",
		"--network=machinist-internal",
		"--memory=8g",
		"--cpus=3.5",
		"--pids-limit=1024",
		"-e HTTP_PROXY=http://egress:3128",
		"-e HTTPS_PROXY=http://egress:3128",
		"-e http_proxy=http://egress:3128",
		"-e https_proxy=http://egress:3128",
		"-e CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"-e DISABLE_AUTOUPDATER=1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in args:\n%s", want, joined)
		}
	}

	// Proxy URLs containing credentials must keep credentials out of argv.
	c.Config.ProxyURL = "http://user:proxysecret@egress:3128"
	args, env := c.BuildRunArgs("machinist-test", dirs, "", "1000:1000")
	if strings.Contains(strings.Join(args, " "), "proxysecret") {
		t.Fatalf("proxy credentials leaked into argv: %v", args)
	}
	if !slices.Contains(env, "HTTPS_PROXY=http://user:proxysecret@egress:3128") {
		t.Fatalf("proxy URL missing from env: %v", env)
	}
}

func TestCheckRuntimeValidatesInstalledRuntimesAndLogsLinuxNote(t *testing.T) {
	t.Run("vanilla runc logs note on linux", func(t *testing.T) {
		var log bytes.Buffer
		c := Connector{
			Config: fixtureConfig(),
			goos:   "linux",
			runCmd: func(_ context.Context, _ []string, _ int, args ...string) ([]byte, error) {
				if strings.Join(args, " ") != "info --format {{json .Runtimes}}" {
					return nil, fmt.Errorf("unexpected args: %v", args)
				}
				return []byte(`{"io.containerd.runc.v2":{"path":"runc"},"runc":{"path":"runc"}}`), nil
			},
		}
		if err := c.CheckRuntime(t.Context(), &log); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(log.String(), "runc") || !strings.Contains(log.String(), "runsc") {
			t.Fatalf("expected Linux runc/runsc note, got %q", log.String())
		}
	})

	t.Run("vanilla docker does not log linux note on darwin", func(t *testing.T) {
		var log bytes.Buffer
		c := Connector{
			Config: fixtureConfig(),
			goos:   "darwin",
			runCmd: func(context.Context, []string, int, ...string) ([]byte, error) {
				return []byte(`{"runc":{"path":"runc"}}`), nil
			},
		}
		if err := c.CheckRuntime(t.Context(), &log); err != nil {
			t.Fatal(err)
		}
		if log.Len() != 0 {
			t.Fatalf("unexpected log on darwin: %q", log.String())
		}
	})

	t.Run("configured runsc succeeds when installed", func(t *testing.T) {
		var log bytes.Buffer
		cfg := fixtureConfig()
		cfg.Runtime = "runsc"
		c := Connector{
			Config: cfg,
			goos:   "linux",
			runCmd: func(context.Context, []string, int, ...string) ([]byte, error) {
				return []byte(`{"runc":{"path":"runc"},"runsc":{"path":"/usr/bin/runsc"}}`), nil
			},
		}
		if err := c.CheckRuntime(t.Context(), &log); err != nil {
			t.Fatal(err)
		}
		if log.Len() != 0 {
			t.Fatalf("unexpected note when runsc is configured: %q", log.String())
		}
	})

	t.Run("configured runsc fails clearly when missing", func(t *testing.T) {
		cfg := fixtureConfig()
		cfg.Runtime = "runsc"
		c := Connector{
			Config: cfg,
			goos:   "linux",
			runCmd: func(context.Context, []string, int, ...string) ([]byte, error) {
				return []byte(`{"runc":{"path":"runc"}}`), nil
			},
		}
		err := c.CheckRuntime(t.Context(), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), `"runsc" is not installed`) || !strings.Contains(err.Error(), "runc") || !strings.Contains(err.Error(), "daemon.json") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("daemon unreachable fails clearly", func(t *testing.T) {
		c := Connector{
			Config: fixtureConfig(),
			runCmd: func(context.Context, []string, int, ...string) ([]byte, error) {
				return nil, errors.New("Cannot connect to the Docker daemon")
			},
		}
		err := c.CheckRuntime(t.Context(), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "docker is not ready") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestProbeHarnessesUsesHardenedContainer(t *testing.T) {
	cfg := fixtureConfig()
	cfg.Runtime = "runsc"
	var gotArgs []string
	c := Connector{
		Config: cfg,
		user:   func() (string, int, int) { return "1000:1000", 1000, 1000 },
		runCmd: func(_ context.Context, _ []string, _ int, args ...string) ([]byte, error) {
			gotArgs = append([]string(nil), args...)
			return []byte("codex\nclaude\nunknown-tool\nclaude\n"), nil
		},
	}
	found, err := c.ProbeHarnesses(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(found, ",") != "claude,codex" {
		t.Fatalf("found = %v", found)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"--rm", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--network=none", "--runtime=runsc", "--entrypoint sh"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in probe args: %s", want, joined)
		}
	}
}

type fakeProc struct {
	waitFn func() error
	killed bool
	mu     sync.Mutex
	stderr string
}

func (f *fakeProc) Wait() error { return f.waitFn() }
func (f *fakeProc) Kill() error {
	f.mu.Lock()
	f.killed = true
	f.mu.Unlock()
	return nil
}
func (f *fakeProc) Stderr() string { return f.stderr }

func TestExecuteStreamsEventsLiveWhileContainerRuns(t *testing.T) {
	dataDir := t.TempDir()
	release := make(chan struct{})
	eventWritten := make(chan struct{})

	c := Connector{
		Config:       fixtureConfig(),
		getenv:       func(k string) string { return map[string]string{"GH_TOKEN": "tok"}[k] },
		tailInterval: 10 * time.Millisecond,
		startContainer: func(_ []string, args ...string) (containerProcess, error) {
			// Locate host exchange dir from -v <host>:/exchange:rw
			var exchangeDir string
			for i := 0; i < len(args)-1; i++ {
				if args[i] == "-v" && strings.HasSuffix(args[i+1], ":/exchange:rw") {
					exchangeDir = strings.TrimSuffix(args[i+1], ":/exchange:rw")
				}
			}
			if exchangeDir == "" {
				return nil, errors.New("missing exchange mount")
			}
			return &fakeProc{
				waitFn: func() error {
					// Ensure request.json never contains the GitHub token.
					reqBody, err := os.ReadFile(filepath.Join(exchangeDir, "request.json"))
					if err != nil {
						return err
					}
					if strings.Contains(string(reqBody), "tok") {
						return errors.New("token leaked into request.json")
					}
					// Write live event while container is still running.
					evDir := filepath.Join(exchangeDir, "runtime", "runs", fixtureRun)
					if err := os.MkdirAll(evDir, 0o700); err != nil {
						return err
					}
					evPath := filepath.Join(evDir, "events.jsonl")
					line1 := `{"sequence":1,"time":"2026-01-01T00:00:00Z","type":"step.started","message":"name=work"}` + "\n"
					if err := os.WriteFile(evPath, []byte(line1), 0o600); err != nil {
						return err
					}
					close(eventWritten)
					<-release
					// Append final event and write result.json before exit.
					line2 := `{"sequence":2,"time":"2026-01-01T00:00:01Z","type":"run.completed","message":"state=succeeded"}` + "\n"
					f, err := os.OpenFile(evPath, os.O_WRONLY|os.O_APPEND, 0o600)
					if err != nil {
						return err
					}
					_, _ = f.WriteString(line2)
					_ = f.Close()
					res, _ := json.Marshal(runner.RunResult{State: runner.StateSucceeded})
					return os.WriteFile(filepath.Join(exchangeDir, "result.json"), res, 0o600)
				},
			}, nil
		},
	}

	done := make(chan runner.RunResult, 1)
	go func() {
		done <- c.Execute(t.Context(), fixtureRequest(), dataDir)
	}()

	<-eventWritten
	// Verify event is forwarded to dataDirectory/runs/<runID>/events.jsonl BEFORE container exits!
	dstEvents := filepath.Join(dataDir, "runs", fixtureRun, "events.jsonl")
	deadline := time.Now().Add(5 * time.Second)
	for {
		body, err := os.ReadFile(dstEvents)
		if err == nil && strings.Contains(string(body), `"step.started"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("live event was not streamed while container ran (err=%v, body=%q)", err, string(body))
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(release)
	result := <-done
	if result.State != runner.StateSucceeded {
		t.Fatalf("result = %+v", result)
	}
	finalEvents, err := os.ReadFile(dstEvents)
	if err != nil || !strings.Contains(string(finalEvents), `"run.completed"`) {
		t.Fatalf("final events = %q, err = %v", string(finalEvents), err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "work", fixtureRun)); !os.IsNotExist(err) {
		t.Fatalf("per-run work directory was not cleaned up: %v", err)
	}
}

func TestExecuteCancellationCallsDockerRmForce(t *testing.T) {
	for _, rmFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("rmFails=%v", rmFails), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stopped := make(chan struct{})
			var rmArgs []string

			c := Connector{
				Config: fixtureConfig(),
				getenv: func(k string) string { return map[string]string{"GH_TOKEN": "tok"}[k] },
				runCmd: func(_ context.Context, _ []string, _ int, args ...string) ([]byte, error) {
					rmArgs = append([]string(nil), args...)
					close(stopped)
					if rmFails {
						return nil, errors.New("daemon unreachable")
					}
					return nil, nil
				},
				startContainer: func(_ []string, _ ...string) (containerProcess, error) {
					return &fakeProc{
						waitFn: func() error {
							<-stopped
							return errors.New("killed")
						},
					}, nil
				},
			}

			done := make(chan runner.RunResult, 1)
			go func() {
				done <- c.Execute(ctx, fixtureRequest(), t.TempDir())
			}()
			time.Sleep(20 * time.Millisecond)
			cancel()
			result := <-done

			if result.State != runner.StateCancelled {
				t.Fatalf("result = %+v", result)
			}
			wantRm := "rm -f " + ContainerName(fixtureRun)
			if strings.Join(rmArgs, " ") != wantRm {
				t.Fatalf("cleanup args = %q, want %q", strings.Join(rmArgs, " "), wantRm)
			}
			if got := strings.Contains(result.Error, "may still be running"); got != rmFails {
				t.Fatalf("result.Error = %q, want unconfirmed=%v", result.Error, rmFails)
			}
		})
	}
}

func TestExecuteRejectsLongGitHubAppRunAndInvalidRunID(t *testing.T) {
	started := false
	c := Connector{
		Config: fixtureConfig(),
		getenv: func(string) string { return "" },
		startContainer: func(_ []string, _ ...string) (containerProcess, error) {
			started = true
			return nil, errors.New("should not start")
		},
	}

	badID := fixtureRequest()
	badID.RunID = "../escape"
	if res := c.Execute(t.Context(), badID, t.TempDir()); res.State != runner.StateFailed || started {
		t.Fatalf("bad ID result = %+v, started = %v", res, started)
	}

	longApp := fixtureRequest()
	longApp.GitHubToken = "ghs_app_token"
	longApp.TimeoutMillis = (90 * time.Minute).Milliseconds()
	res := c.Execute(t.Context(), longApp, t.TempDir())
	if res.State != runner.StateFailed || !strings.Contains(res.Error, "1h30m0s") || !strings.Contains(res.Error, "1h0m0s") || started {
		t.Fatalf("long app run result = %+v, started = %v", res, started)
	}
}

func TestSandboxRunsStepsAndWritesResult(t *testing.T) {
	t.Setenv("GH_TOKEN", "fixture-token")
	t.Setenv("ANTHROPIC_API_KEY", "fixture-anth")
	t.Setenv("GIT_DIR", "/must-be-sanitized")

	root := t.TempDir()
	exchange := filepath.Join(root, "exchange")
	work := filepath.Join(root, "work")
	home := filepath.Join(root, "home")
	for _, dir := range []string{exchange, filepath.Join(work, "workspace"), filepath.Join(work, "control"), home} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	req := fixtureRequest()
	req.Steps = []runner.Step{
		{Name: "work", Run: `echo "All good" > "$MACHINIST_DIR/result.md"`},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(exchange, "request.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Sandbox(t.Context(), exchange, work, home); err != nil {
		t.Fatal(err)
	}
	result, err := readResultFile(filepath.Join(exchange, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runner.StateSucceeded || result.Report == nil || result.Report.Summary != "All good" {
		t.Fatalf("result = %+v", result)
	}
	events, err := os.ReadFile(filepath.Join(exchange, "runtime", "runs", fixtureRun, "events.jsonl"))
	if err != nil || !strings.Contains(string(events), `"step.started"`) {
		t.Fatalf("events = %q, err = %v", string(events), err)
	}
}
