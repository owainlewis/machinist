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
	return runner.RunRequest{RunID: fixtureRun, Repository: "owner/repo", Branch: "machinist/t_1", Harness: "claude", TimeoutMillis: time.Minute.Milliseconds()}
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

func TestSandboxRequiresGHToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	result := runRequest(t.Context(), fixtureRequest(), t.TempDir(), t.TempDir(), t.TempDir(), githubCloneURL)
	if result.State != runner.StateFailed || !strings.Contains(result.Error, "GH_TOKEN") {
		t.Fatalf("result = %+v", result)
	}
}

func TestAgentEnvironmentKeepsOnlyWhatThisHarnessNeeds(t *testing.T) {
	environ := []string{"PATH=/bin", "HOME=/x", "GH_TOKEN=g", "OPENAI_API_KEY=o", "ANTHROPIC_API_KEY=a", "AWS_REGION=r", "KUBERNETES_SERVICE_HOST=10.0.0.1", "GIT_DIR=/other"}
	claude := strings.Join(agentEnvironment(environ, runner.HarnessClaude), " ")
	if claude != "PATH=/bin ANTHROPIC_API_KEY=a AWS_REGION=r" {
		t.Fatalf("claude = %s", claude)
	}
	codex := strings.Join(agentEnvironment(environ, runner.HarnessCodex), " ")
	if codex != "PATH=/bin AWS_REGION=r" {
		t.Fatalf("codex = %s", codex)
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
