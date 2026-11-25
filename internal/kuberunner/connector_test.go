package kuberunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/runner"
)

const fixtureRun = "run_0123456789abcdef01234567"

func configuration() config.Kubernetes {
	return config.Kubernetes{Context: "test", Namespace: "runners", Image: "example.test/runner@sha256:" + strings.Repeat("a", 64), Secret: "machinist-agent", ProxyURL: "http://egress:3128"}
}

func fixtureRequest() runner.RunRequest {
	return runner.RunRequest{RunID: fixtureRun, Repository: "owner/repo", Branch: "machinist/t_1", TimeoutMillis: time.Minute.Milliseconds()}
}

func TestReceiveAcceptsOneBoundedJSONRequest(t *testing.T) {
	directory := t.TempDir()
	if err := Receive(strings.NewReader(`{"run_id":"x"}`), directory); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(filepath.Join(directory, "request.json")); string(body) != `{"run_id":"x"}` {
		t.Fatalf("body = %s", body)
	}
	if _, err := os.Stat(filepath.Join(directory, "ready")); err != nil {
		t.Fatal(err)
	}
	if Receive(strings.NewReader(`{}`), directory) == nil {
		t.Fatal("a second request overwrote the first")
	}
	for name, input := range map[string]string{"not json": "tar bytes", "too large": `"` + strings.Repeat("x", maxRequest) + `"`} {
		fresh := t.TempDir()
		if Receive(strings.NewReader(input), fresh) == nil {
			t.Fatalf("%s accepted", name)
		}
		if _, err := os.Stat(filepath.Join(fresh, "ready")); err == nil {
			t.Fatalf("%s marked ready", name)
		}
	}
}

func TestJobGivesTheSecretOnlyToTheAgentContainer(t *testing.T) {
	object := (Connector{Config: configuration()}).manifest("test-job", 300)
	text := string(mustJSON(object))
	for _, forbidden := range []string{"hostPath", "privileged", "hostNetwork", "control_plane", "codex-auth", "/credentials", "readOnly\":true,\"name\":\"source"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("unexpected in Job: %s", forbidden)
		}
	}
	spec := object["spec"].(map[string]any)
	pod := spec["template"].(map[string]any)["spec"].(map[string]any)
	if pod["runtimeClassName"] != "gvisor" || pod["automountServiceAccountToken"] != false || spec["backoffLimit"] != 0 || pod["restartPolicy"] != "Never" || spec["activeDeadlineSeconds"] != int64(300) {
		t.Fatal("unsafe lifecycle")
	}
	agent := pod["containers"].([]any)[0].(map[string]any)
	if got := string(mustJSON(agent["envFrom"])); got != `[{"secretRef":{"name":"machinist-agent"}}]` {
		t.Fatalf("envFrom = %s", got)
	}
	if init := string(mustJSON(pod["initContainers"])); strings.Contains(init, "envFrom") || strings.Contains(init, "machinist-agent") {
		t.Fatal("the init container received credentials")
	}
	if !strings.Contains(string(mustJSON(agent["env"])), "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC") {
		t.Fatal("Claude Code can try hosts the proxy blocks")
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestSandboxWithoutAnyGitHubTokenFails(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	result := runRequest(t.Context(), fixtureRequest(), t.TempDir(), t.TempDir(), t.TempDir())
	if result.State != runner.StateFailed || !strings.Contains(result.Error, "GH_TOKEN") {
		t.Fatalf("result = %+v", result)
	}
}

func TestStepEnvironmentDropsHomeTokensAndClusterDetails(t *testing.T) {
	environ := []string{"PATH=/bin", "HOME=/x", "GH_TOKEN=g", "GITHUB_TOKEN=g", "ANTHROPIC_API_KEY=a", "AWS_REGION=r", "KUBERNETES_SERVICE_HOST=10.0.0.1", "GIT_DIR=/other"}
	if got := strings.Join(stepEnvironment(environ), " "); got != "PATH=/bin ANTHROPIC_API_KEY=a AWS_REGION=r" {
		t.Fatalf("environment = %s", got)
	}
}

func TestProxyDeniesUnapprovedAndInternalDestinations(t *testing.T) {
	h := ProxyHandler([]string{"chatgpt.com"})
	for _, host := range []string{"example.com:443", "169.254.169.254:443", "chatgpt.com:80", "chatgpt.com.evil.test:443"} {
		r := httptest.NewRequest("CONNECT", "http://"+host, nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("allowed %s", host)
		}
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1", "fc00::1", "::ffff:192.168.1.1"} {
		if PublicAddress(netip.MustParseAddr(ip)) {
			t.Fatal("accepted internal address", ip)
		}
	}
	if !PublicAddress(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("denied public address")
	}
}

func TestInstallAllowsModelsGitHubAndConfiguredHosts(t *testing.T) {
	cfg := configuration()
	cfg.AllowedHosts = []string{"registry.example.test"}
	text := string(mustJSON((Connector{Config: cfg}).InstallObjects()))
	for _, host := range []string{"api.anthropic.com", "storage.googleapis.com", "github.com", "api.github.com", "uploads.github.com", "registry.example.test"} {
		if !strings.Contains(text, host) {
			t.Fatalf("%s is blocked", host)
		}
	}
	if defaults := strings.Join(defaultAllowedHosts, ","); strings.Contains(defaults, "registry.example.test") {
		t.Fatal("configured hosts changed the defaults")
	}
}

// fakeCluster answers kubectl calls for one Job.
func fakeCluster(c *Connector, deleted *bool, podGone bool, onWait func(context.Context) error) {
	c.run = func(ctx context.Context, input io.Reader, limit int, args ...string) ([]byte, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		line := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(line, "get runtimeclass "), strings.HasPrefix(line, "get networkpolicy "):
			return []byte(`{}`), nil
		case strings.HasPrefix(line, "get secret "):
			if !strings.HasSuffix(line, "-o name") {
				return nil, fmt.Errorf("the runner read the Secret's values: %s", line)
			}
			return []byte("secret/machinist-agent"), nil
		case strings.HasPrefix(line, "get deployment "):
			return []byte(`{"status":{"availableReplicas":1}}`), nil
		case args[0] == "create":
			_, _ = io.ReadAll(input)
			return []byte("created"), nil
		case strings.HasPrefix(line, "get pods "):
			if *deleted {
				if podGone {
					return []byte(`{"items":[]}`), nil
				}
				return []byte(`{"items":[{}]}`), nil
			}
			return []byte(`{"items":[{"metadata":{"name":"test-pod"},"status":{"initContainerStatuses":[{"state":{"running":{}}}]}}]}`), nil
		case strings.Contains(line, "test -f /exchange/done"):
			return nil, onWait(ctx)
		case strings.HasPrefix(line, "get pod "):
			return []byte(`{"status":{"phase":"Running"}}`), nil
		case strings.Contains(line, "/exchange/result.json"):
			return []byte(`{"State":"succeeded"}`), nil
		case strings.HasPrefix(line, "delete job "):
			if !strings.Contains(line, "--cascade=foreground") {
				return nil, errors.New("background deletion")
			}
			*deleted = true
			return nil, nil
		case args[0] == "exec" || args[0] == "logs":
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected command %s", line)
	}
}

func TestCancellationDeletesTheJobAndWaitsForThePod(t *testing.T) {
	for _, podGone := range []bool{false, true} {
		t.Run(fmt.Sprint(podGone), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			deleted := false
			c := Connector{Config: configuration()}
			fakeCluster(&c, &deleted, podGone, func(context.Context) error {
				cancel()
				return context.Canceled
			})
			result := c.Execute(ctx, fixtureRequest(), t.TempDir())
			if result.State != runner.StateCancelled || !deleted {
				t.Fatalf("result = %+v, deleted = %v", result, deleted)
			}
			if unconfirmed := strings.Contains(result.Error, "may still be running"); unconfirmed == podGone {
				t.Fatalf("error = %q", result.Error)
			}
		})
	}
}

func TestTransientKubernetesErrorsDoNotEndTheRun(t *testing.T) {
	deleted := false
	c := Connector{Config: configuration()}
	calls := 0
	fakeCluster(&c, &deleted, true, func(context.Context) error {
		calls++
		if calls < 2 {
			return &commandFailure{cause: errors.New("connection reset"), hint: "try again"}
		}
		return nil
	})
	result := c.Execute(t.Context(), fixtureRequest(), t.TempDir())
	if result.State != runner.StateSucceeded || !deleted || result.Error != "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestAFailedPodReportsWhyPreparationStopped(t *testing.T) {
	deleted := false
	c := Connector{Config: configuration()}
	fakeCluster(&c, &deleted, true, func(context.Context) error { return errors.New("no agent container") })
	base := c.run
	c.run = func(ctx context.Context, input io.Reader, limit int, args ...string) ([]byte, error) {
		line := strings.Join(args, " ")
		if strings.HasPrefix(line, "get pod ") {
			return []byte(`{"status":{"phase":"Failed"}}`), nil
		}
		if args[0] == "logs" {
			return []byte("machinist: sandbox egress isolation is not enforced\n"), nil
		}
		return base(ctx, input, limit, args...)
	}
	result := c.Execute(t.Context(), fixtureRequest(), t.TempDir())
	if result.State != runner.StateFailed || !strings.Contains(result.Error, "egress isolation") {
		t.Fatalf("result = %+v", result)
	}
}

func TestPreflightFailureStartsNothing(t *testing.T) {
	request := fixtureRequest()
	request.RunID = "../escape"
	if result := (Connector{Config: configuration()}).Execute(t.Context(), request, t.TempDir()); result.State != runner.StateFailed {
		t.Fatalf("result = %+v", result)
	}
	if result := (Connector{}).Execute(t.Context(), fixtureRequest(), t.TempDir()); result.State != runner.StateFailed || !strings.Contains(result.Error, "explicit context") {
		t.Fatalf("result = %+v", result)
	}
}

func TestMissingSecretSaysWhatToCreate(t *testing.T) {
	c := Connector{Config: configuration()}
	deleted := false
	fakeCluster(&c, &deleted, true, func(context.Context) error { return nil })
	base := c.run
	c.run = func(ctx context.Context, input io.Reader, limit int, args ...string) ([]byte, error) {
		if args[0] == "get" && args[1] == "secret" {
			return nil, &commandFailure{cause: errors.New("exit 1"), hint: "missing", missing: true}
		}
		return base(ctx, input, limit, args...)
	}
	result := c.Execute(t.Context(), fixtureRequest(), t.TempDir())
	if !strings.Contains(result.Error, "GH_TOKEN") {
		t.Fatalf("result = %+v", result)
	}
}

func TestTheRunsGitHubTokenReachesOnlyTheAgentContainer(t *testing.T) {
	deleted := false
	c := Connector{Config: configuration()}
	fakeCluster(&c, &deleted, true, func(context.Context) error { return nil })
	base := c.run
	var prepared, sent string
	tries := 0
	c.run = func(ctx context.Context, input io.Reader, limit int, args ...string) ([]byte, error) {
		line := strings.Join(args, " ")
		switch {
		case strings.HasSuffix(line, "-c prepare -- machinist sandbox receive"):
			body, _ := io.ReadAll(input)
			prepared = string(body)
			return nil, nil
		case strings.HasSuffix(line, "-c agent -- machinist sandbox token"):
			if tries++; tries < 2 {
				return nil, errors.New("container agent is waiting to start")
			}
			body, _ := io.ReadAll(input)
			sent = string(body)
			return nil, nil
		}
		return base(ctx, input, limit, args...)
	}
	request := fixtureRequest()
	request.GitHubToken = "ghs_run_token"
	if result := c.Execute(t.Context(), request, t.TempDir()); result.State != runner.StateSucceeded {
		t.Fatalf("result = %+v", result)
	}
	if prepared == "" || strings.Contains(prepared, "ghs_run_token") {
		t.Fatalf("the init container got %s", prepared)
	}
	if sent != "ghs_run_token" {
		t.Fatalf("the agent container got %q", sent)
	}
}

func TestTheSandboxReadsTheTokenAndRemovesIt(t *testing.T) {
	exchange := t.TempDir()
	if err := ReceiveToken(strings.NewReader("first"), exchange); err != nil {
		t.Fatal(err)
	}
	if err := ReceiveToken(strings.NewReader("ghs_run_token\n"), exchange); err != nil {
		t.Fatal(err)
	}
	token, err := readToken(t.Context(), exchange)
	if err != nil || token != "ghs_run_token" {
		t.Fatalf("token = %q, err = %v", token, err)
	}
	if entries, _ := os.ReadDir(exchange); len(entries) != 0 {
		t.Fatalf("left in the exchange volume: %v", entries)
	}
	if ReceiveToken(strings.NewReader(strings.Repeat("x", 5000)), exchange) == nil {
		t.Fatal("an oversized token was accepted")
	}
}

// A pod keeps the GitHub App token it started with, so a run that could
// outlast it is refused before a Job exists, not an hour in.
func TestARunLongerThanItsAppTokenStartsNothing(t *testing.T) {
	c := Connector{Config: configuration()}
	deleted := false
	fakeCluster(&c, &deleted, true, func(context.Context) error { return nil })
	base := c.run
	created := false
	c.run = func(ctx context.Context, input io.Reader, limit int, args ...string) ([]byte, error) {
		if args[0] == "create" {
			created = true
		}
		return base(ctx, input, limit, args...)
	}
	request := fixtureRequest()
	request.GitHubToken = "ghs_run_token"
	request.TimeoutMillis = (90 * time.Minute).Milliseconds()
	result := c.Execute(t.Context(), request, t.TempDir())
	if result.State != runner.StateFailed || !strings.Contains(result.Error, "run timeout of 1h30m0s") || !strings.Contains(result.Error, "1h0m0s") {
		t.Fatalf("result = %+v", result)
	}
	if created {
		t.Fatal("a Job was created")
	}
	// Without a run token the Secret's GH_TOKEN applies, and it has no such limit.
	request.GitHubToken = ""
	if result := c.Execute(t.Context(), request, t.TempDir()); result.State != runner.StateSucceeded {
		t.Fatalf("without a run token: result = %+v", result)
	}
}

func TestConnectorTailsEventsWhileJobRuns(t *testing.T) {
	line1 := "{\"sequence\":1,\"type\":\"step.started\",\"message\":\"name=build\"}\n"
	line2Part1 := "{\"sequence\":2,\"type\":\"process.output\""
	line2Part2 := ",\"stream\":\"stdout\",\"encoding\":\"base64\",\"data\":\"aGkK\"}\n"
	line3 := "{\"sequence\":3,\"type\":\"step.started\",\"message\":\"name=test\"}\n"
	line4 := "{\"sequence\":4,\"type\":\"run.completed\"}\n"

	dataDir := t.TempDir()
	localEvents := filepath.Join(dataDir, "runs", fixtureRun, "events.jsonl")
	remoteContent := line1 + line2Part1
	deleted := false
	waits := 0
	var seenLive1, seenLive2 string

	c := Connector{Config: configuration()}
	fakeCluster(&c, &deleted, true, func(context.Context) error {
		waits++
		switch waits {
		case 1:
			return errors.New("still running")
		case 2:
			body, _ := os.ReadFile(localEvents)
			seenLive1 = string(body)
			remoteContent += line2Part2 + line3
			return errors.New("still running")
		default:
			body, _ := os.ReadFile(localEvents)
			seenLive2 = string(body)
			remoteContent += line4
			return nil
		}
	})
	base := c.run
	var tailOffsets []string
	c.run = func(ctx context.Context, input io.Reader, limit int, args ...string) ([]byte, error) {
		line := strings.Join(args, " ")
		if strings.Contains(line, "-- tail -c +") {
			offsetArg := args[len(args)-2]
			tailOffsets = append(tailOffsets, offsetArg)
			var start int
			if _, err := fmt.Sscanf(offsetArg, "+%d", &start); err != nil || start < 1 {
				return nil, fmt.Errorf("bad offset %q", offsetArg)
			}
			if start-1 >= len(remoteContent) {
				return nil, nil
			}
			out := []byte(remoteContent[start-1:])
			if len(out) > limit {
				return nil, errOutputLimit
			}
			return out, nil
		}
		return base(ctx, input, limit, args...)
	}

	result := c.Execute(t.Context(), fixtureRequest(), dataDir)
	if result.State != runner.StateSucceeded || !deleted {
		t.Fatalf("result = %+v, deleted = %v", result, deleted)
	}
	if seenLive1 != line1 {
		t.Fatalf("live events after poll 1 = %q, want %q", seenLive1, line1)
	}
	wantLive2 := line1 + line2Part1 + line2Part2 + line3
	if seenLive2 != wantLive2 {
		t.Fatalf("live events after poll 2 = %q, want %q", seenLive2, wantLive2)
	}
	finalBody, err := os.ReadFile(localEvents)
	if err != nil {
		t.Fatal(err)
	}
	wantFinal := wantLive2 + line4
	if string(finalBody) != wantFinal {
		t.Fatalf("final events = %q, want %q", string(finalBody), wantFinal)
	}
	wantOffsets := []string{
		"+1",
		fmt.Sprintf("+%d", len(line1)+1),
		fmt.Sprintf("+%d", len(wantLive2)+1),
	}
	if strings.Join(tailOffsets, ",") != strings.Join(wantOffsets, ",") {
		t.Fatalf("tail offsets = %v, want %v", tailOffsets, wantOffsets)
	}
}

func TestEventTailerIgnoresTransientErrorsAndRespectsMaxLog(t *testing.T) {
	line1 := "{\"sequence\":1,\"type\":\"step.started\",\"message\":\"name=first\"}\n"
	line2 := "{\"sequence\":2,\"type\":\"step.started\",\"message\":\"name=second\"}\n"
	dataDir := t.TempDir()
	localEvents := filepath.Join(dataDir, "runs", fixtureRun, "events.jsonl")
	remoteContent := ""
	deleted := false
	waits := 0
	tailCalls := 0

	c := Connector{Config: configuration()}
	fakeCluster(&c, &deleted, true, func(context.Context) error {
		waits++
		switch waits {
		case 1:
			// events.jsonl does not exist yet
			return errors.New("still running")
		case 2:
			remoteContent = line1
			return errors.New("still running")
		case 3:
			// transient exec error on tail
			return errors.New("still running")
		default:
			remoteContent = line1 + line2
			return nil
		}
	})
	base := c.run
	c.run = func(ctx context.Context, input io.Reader, limit int, args ...string) ([]byte, error) {
		line := strings.Join(args, " ")
		if strings.Contains(line, "-- tail -c +") {
			tailCalls++
			if tailCalls == 1 {
				return nil, errors.New("tail: cannot open: No such file or directory")
			}
			if tailCalls == 3 {
				return nil, &commandFailure{cause: errors.New("connection reset"), hint: "retry"}
			}
			var start int
			_, _ = fmt.Sscanf(args[len(args)-2], "+%d", &start)
			if start-1 >= len(remoteContent) {
				return nil, nil
			}
			return []byte(remoteContent[start-1:]), nil
		}
		return base(ctx, input, limit, args...)
	}

	result := c.Execute(t.Context(), fixtureRequest(), dataDir)
	if result.State != runner.StateSucceeded {
		t.Fatalf("result = %+v", result)
	}
	body, err := os.ReadFile(localEvents)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != line1+line2 {
		t.Fatalf("events = %q, want %q", string(body), line1+line2)
	}

	// Verify the cumulative byte limit stops reading once reached.
	calls := 0
	limited := Connector{Config: configuration(), run: func(_ context.Context, _ io.Reader, limit int, _ ...string) ([]byte, error) {
		calls++
		if limit < len(line1) {
			return nil, errOutputLimit
		}
		return []byte(line1), nil
	}}
	tailer := limited.newEventTailer("test-pod", fixtureRun, t.TempDir())
	tailer.limit = int64(len(line1))
	tailer.sync(t.Context())
	tailer.sync(t.Context())
	if calls != 1 {
		t.Fatalf("tailer called kubectl %d times after reaching limit, want 1", calls)
	}
}

func TestEventTailerStopsWhenRemoteExceedsLimit(t *testing.T) {
	line1 := "{\"sequence\":1,\"type\":\"step.started\",\"message\":\"name=first\"}\n"
	line2 := "{\"sequence\":2,\"type\":\"step.started\",\"message\":\"name=second\"}\n"

	t.Run("incremental overflow stops polling", func(t *testing.T) {
		dataDir := t.TempDir()
		localEvents := filepath.Join(dataDir, "runs", fixtureRun, "events.jsonl")
		remoteContent := line1
		calls := 0
		c := Connector{Config: configuration(), run: func(_ context.Context, _ io.Reader, limit int, args ...string) ([]byte, error) {
			calls++
			var start int
			if _, err := fmt.Sscanf(args[len(args)-2], "+%d", &start); err != nil || start < 1 {
				return nil, fmt.Errorf("bad offset %q", args[len(args)-2])
			}
			if start-1 >= len(remoteContent) {
				return nil, nil
			}
			out := []byte(remoteContent[start-1:])
			if len(out) > limit {
				return nil, errOutputLimit
			}
			return out, nil
		}}

		tailer := c.newEventTailer("test-pod", fixtureRun, dataDir)
		// Allow line1, then overflow remaining bytes when line2 arrives.
		tailer.limit = int64(len(line1) + len(line2)/2)

		tailer.sync(t.Context())
		remoteContent += line2
		tailer.sync(t.Context())
		tailer.sync(t.Context())

		if calls != 2 {
			t.Fatalf("tailer called kubectl %d times, want 2", calls)
		}
		if tailer.offset != tailer.limit {
			t.Fatalf("offset = %d, want %d", tailer.offset, tailer.limit)
		}
		body, err := os.ReadFile(localEvents)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != line1 {
			t.Fatalf("events = %q, want %q", string(body), line1)
		}
	})

	t.Run("command reports output limit even on nonzero exit or sigpipe", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			script string
		}{
			{
				name:   "nonzero",
				script: "#!/bin/sh\nprintf 'x\\n' >> \"$CALL_LOG\"\nprintf '%s' '{\"sequence\":1,\"type\":\"step.started\"}\n'\nexit 1\n",
			},
			{
				name:   "sigpipe",
				script: "#!/bin/sh\nprintf 'x\\n' >> \"$CALL_LOG\"\ndd if=/dev/zero bs=4096 count=64 2>/dev/null\n",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				bin := t.TempDir()
				callLog := filepath.Join(bin, "calls.log")
				if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(tc.script), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("CALL_LOG", callLog)

				c := Connector{Config: configuration()}
				_, err := c.command(t.Context(), nil, 16, "exec", "test-pod", "-c", "agent", "--", "tail", "-c", "+1", "/exchange/runtime/runs/x/events.jsonl")
				if !errors.Is(err, errOutputLimit) {
					t.Fatalf("command error = %v, want errOutputLimit", err)
				}

				// Reset call log and verify eventTailer stops after one overflow.
				if err := os.WriteFile(callLog, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				tailer := c.newEventTailer("test-pod", fixtureRun, t.TempDir())
				tailer.limit = 16
				tailer.sync(t.Context())
				tailer.sync(t.Context())

				raw, err := os.ReadFile(callLog)
				if err != nil {
					t.Fatal(err)
				}
				if got := strings.Count(string(raw), "\n"); got != 1 {
					t.Fatalf("kubectl invoked %d times, want 1", got)
				}
			})
		}
	})
}
