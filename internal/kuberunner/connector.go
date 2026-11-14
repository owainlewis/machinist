package kuberunner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/runner"
)

const (
	// setupAllowance is the time a Job may take beyond the run's own deadline.
	setupAllowance = 10 * time.Minute
	cleanupTimeout = 45 * time.Second
	maxLogTail     = 1 << 10
)

// Connector runs one agent per gVisor Job through kubectl.
type Connector struct {
	Config config.Kubernetes
	run    func(context.Context, io.Reader, int, ...string) ([]byte, error)
}

func (c Connector) command(ctx context.Context, input io.Reader, limit int, args ...string) ([]byte, error) {
	if c.run != nil {
		return c.run(ctx, input, limit, args...)
	}
	flags := []string{"--context", c.Config.Context, "--namespace", c.Config.Namespace, "--request-timeout=30s"}
	if c.Config.Kubeconfig != "" {
		flags = append(flags, "--kubeconfig", c.Config.Kubeconfig)
	}
	cmd := exec.CommandContext(ctx, "kubectl", append(flags, args...)...)
	cmd.Stdin = input
	var out bytes.Buffer
	cmd.Stdout = &limitedWriter{writer: &out, remaining: limit}
	var diagnostics commandDiagnostics
	cmd.Stderr = &diagnostics
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("Kubernetes command interrupted: %w", ctx.Err())
		}
		return nil, diagnostics.failure(err)
	}
	return out.Bytes(), nil
}

func (c Connector) json(ctx context.Context, verb string, object any) error {
	body, err := json.Marshal(object)
	if err != nil {
		return err
	}
	_, err = c.command(ctx, bytes.NewReader(body), 1<<20, verb, "-f", "-", "-o", "name")
	return err
}

func (c Connector) get(ctx context.Context, kind, name string) (map[string]any, error) {
	body, err := c.command(ctx, nil, 1<<20, "get", kind, name, "-o", "json")
	if err != nil {
		return nil, err
	}
	var object map[string]any
	err = json.Unmarshal(body, &object)
	return object, err
}

func objectName(prefix, value string) string {
	sum := sha256.Sum256([]byte(value))
	return prefix + hex.EncodeToString(sum[:12])
}

func stateFor(ctx context.Context) runner.State {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return runner.StateCancelled
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return runner.StateTimedOut
	}
	return runner.StateFailed
}

// Execute runs one request in its own Job and removes the Job afterwards.
// Cancelling ctx stops the run.
func (c Connector) Execute(ctx context.Context, request runner.RunRequest, dataDirectory string) runner.RunResult {
	failure := func(err error) runner.RunResult {
		return runner.RunResult{State: stateFor(ctx), Error: err.Error()}
	}
	if !runner.ValidRunID(request.RunID) {
		return failure(fmt.Errorf("invalid run ID %q", request.RunID))
	}
	if err := c.Config.Validate(); err != nil {
		return failure(err)
	}
	if err := c.ready(ctx); err != nil {
		return failure(err)
	}
	timeout := time.Duration(request.TimeoutMillis) * time.Millisecond
	runContext, cancel := context.WithTimeout(ctx, timeout+setupAllowance)
	defer cancel()
	name := objectName("run-", request.RunID)
	result, err := c.runJob(runContext, name, request, timeout, dataDirectory)
	if err != nil {
		result = runner.RunResult{State: stateFor(runContext), Error: err.Error()}
	}
	if err := c.deleteJob(name); err != nil {
		result.Error = strings.TrimSpace(result.Error + " " + err.Error())
	}
	return result
}

func (c Connector) runJob(ctx context.Context, name string, request runner.RunRequest, timeout time.Duration, dataDirectory string) (runner.RunResult, error) {
	var result runner.RunResult
	deadline := int64((timeout + setupAllowance).Seconds())
	if err := c.json(ctx, "create", c.manifest(name, deadline)); err != nil {
		// A lost response can still have created the Job.
		if _, getErr := c.get(ctx, "job", name); getErr != nil {
			return result, err
		}
	}
	pod, err := c.awaitPod(ctx, name)
	if err != nil {
		return result, err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	if _, err = c.command(ctx, bytes.NewReader(body), 1<<20, "exec", "-i", pod, "-c", "prepare", "--", "machinist", "sandbox", "receive"); err != nil {
		return result, err
	}
	if err = c.awaitResult(ctx, pod); err != nil {
		return result, fmt.Errorf("%w%s", err, c.prepareLog(ctx, pod))
	}
	body, err = c.command(ctx, nil, 1<<20, "exec", pod, "-c", "agent", "--", "cat", "/exchange/result.json")
	if err != nil {
		return result, err
	}
	if err = json.Unmarshal(body, &result); err != nil {
		return result, fmt.Errorf("read sandbox result: %w", err)
	}
	c.saveEvents(ctx, pod, request.RunID, dataDirectory)
	return result, nil
}

// saveEvents copies the activity log where the daemon sends it from. Activity is
// a display aid, so a run that cannot copy it still reports its result.
func (c Connector) saveEvents(ctx context.Context, pod, runID, dataDirectory string) {
	events, err := c.command(ctx, nil, MaxLog, "exec", pod, "-c", "agent", "--", "cat", "/exchange/runtime/runs/"+runID+"/events.jsonl")
	if err != nil {
		return
	}
	directory := filepath.Join(dataDirectory, "runs", runID)
	if os.MkdirAll(directory, 0o700) == nil {
		_ = os.WriteFile(filepath.Join(directory, "events.jsonl"), events, 0o600)
	}
}

// prepareLog returns the end of the init container's log. That container has
// no credentials, and its log says why the isolation checks failed.
func (c Connector) prepareLog(ctx context.Context, pod string) string {
	logs, err := c.command(context.WithoutCancel(ctx), nil, maxLogTail, "logs", pod, "-c", "prepare", "--tail=5")
	if err != nil || len(bytes.TrimSpace(logs)) == 0 {
		return ""
	}
	return ": " + strings.TrimSpace(string(logs))
}

// deleteJob reports success only once no Pod remains.
func (c Connector) deleteJob(name string) error {
	cleanup, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	unconfirmed := fmt.Errorf("Kubernetes Job %s may still be running; delete it by hand", name)
	if _, err := c.command(cleanup, nil, 1<<20, "delete", "job", name, "--cascade=foreground", "--wait=true", "--timeout=30s", "--ignore-not-found"); err != nil {
		return unconfirmed
	}
	body, err := c.command(cleanup, nil, 1<<20, "get", "pods", "-l", "job-name="+name, "-o", "json")
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err != nil || json.Unmarshal(body, &list) != nil || len(list.Items) != 0 {
		return unconfirmed
	}
	return nil
}

func (c Connector) ready(ctx context.Context) error {
	for _, item := range [][2]string{{"runtimeclass", "gvisor"}, {"networkpolicy", "agent-isolation"}, {"networkpolicy", "proxy-isolation"}} {
		if _, err := c.get(ctx, item[0], item[1]); err != nil {
			if resourceMissing(err) {
				if item[0] == "runtimeclass" {
					return fmt.Errorf("the gvisor RuntimeClass is missing; configure gVisor on the cluster: %w", err)
				}
				return fmt.Errorf("runner policy %s is missing; run machinist kubernetes install: %w", item[1], err)
			}
			return fmt.Errorf("could not verify Kubernetes %s %s: %w", item[0], item[1], err)
		}
	}
	// Ask only for the name, so the runner process never holds the Secret's values.
	if _, err := c.command(ctx, nil, 1<<10, "get", "secret", c.Config.Secret, "-o", "name"); err != nil {
		if resourceMissing(err) {
			return fmt.Errorf("the agent Secret %s is missing; create it with GH_TOKEN and the harness credentials: %w", c.Config.Secret, err)
		}
		return fmt.Errorf("could not verify the agent Secret: %w", err)
	}
	proxy, err := c.get(ctx, "deployment", "machinist-egress")
	if err != nil {
		if resourceMissing(err) {
			return fmt.Errorf("the sandbox egress proxy is missing; run machinist kubernetes install: %w", err)
		}
		return fmt.Errorf("could not verify the sandbox egress proxy: %w", err)
	}
	status, _ := proxy["status"].(map[string]any)
	available, _ := status["availableReplicas"].(float64)
	if available < 1 {
		return fmt.Errorf("the sandbox egress proxy is not ready")
	}
	return nil
}

func wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Second):
		return nil
	}
}

func (c Connector) awaitPod(ctx context.Context, name string) (string, error) {
	for {
		body, err := c.command(ctx, nil, 1<<20, "get", "pods", "-l", "job-name="+name, "-o", "json")
		if err == nil {
			var list struct {
				Items []struct {
					Metadata struct {
						Name string `json:"name"`
					} `json:"metadata"`
					Status struct {
						Phase string `json:"phase"`
						Init  []struct {
							State struct {
								Running *struct{} `json:"running"`
							} `json:"state"`
						} `json:"initContainerStatuses"`
					} `json:"status"`
				} `json:"items"`
			}
			if err = json.Unmarshal(body, &list); err != nil {
				return "", err
			}
			if len(list.Items) > 1 {
				return "", fmt.Errorf("refusing replacement or duplicate sandbox pods")
			}
			if len(list.Items) == 1 {
				pod := list.Items[0]
				if pod.Status.Phase == "Failed" {
					return "", fmt.Errorf("sandbox preparation failed")
				}
				if len(pod.Status.Init) > 0 && pod.Status.Init[0].State.Running != nil {
					return pod.Metadata.Name, nil
				}
			}
		}
		if err := wait(ctx); err != nil {
			return "", err
		}
	}
}

// awaitResult waits for the sandbox to finish. A failed Kubernetes call says
// nothing about the agent, so only a gone or stopped Pod ends the wait.
func (c Connector) awaitResult(ctx context.Context, pod string) error {
	for {
		if _, err := c.command(ctx, nil, 128, "exec", pod, "-c", "agent", "--", "test", "-f", "/exchange/done"); err == nil {
			return nil
		}
		object, err := c.get(ctx, "pod", pod)
		if resourceMissing(err) {
			return fmt.Errorf("sandbox Pod %s no longer exists", pod)
		}
		if err == nil {
			status, _ := object["status"].(map[string]any)
			if phase, _ := status["phase"].(string); phase == "Failed" || phase == "Succeeded" {
				return fmt.Errorf("sandbox stopped before it reported a result")
			}
		}
		if err := wait(ctx); err != nil {
			return err
		}
	}
}

// manifest gives the credential Secret to the agent container only. The init
// container checks the network before anything secret enters the Pod.
func (c Connector) manifest(name string, deadline int64) map[string]any {
	security := map[string]any{"runAsNonRoot": true, "runAsUser": 1000, "runAsGroup": 1000, "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}, "seccompProfile": map[string]string{"type": "RuntimeDefault"}}
	resources := map[string]any{"requests": map[string]string{"cpu": "1", "memory": "4Gi", "ephemeral-storage": "8Gi"}, "limits": map[string]string{"cpu": "2", "memory": "4Gi", "ephemeral-storage": "8Gi"}}
	env := []any{map[string]string{"name": "HTTPS_PROXY", "value": c.Config.ProxyURL}, map[string]string{"name": "HTTP_PROXY", "value": c.Config.ProxyURL}, map[string]string{"name": "HOME", "value": "/home/agent"}, map[string]string{"name": "CODEX_HOME", "value": "/home/agent/.codex"}, map[string]string{"name": "GIT_CONFIG_GLOBAL", "value": "/dev/null"}, map[string]string{"name": "GIT_CONFIG_NOSYSTEM", "value": "1"},
		// Only allowed hosts are reachable. Keep Claude Code from trying telemetry
		// and auto-update hosts.
		map[string]string{"name": "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "value": "1"}, map[string]string{"name": "DISABLE_AUTOUPDATER", "value": "1"}}
	container := func(containerName, subcommand string) map[string]any {
		return map[string]any{"name": containerName, "image": c.Config.Image, "command": []string{"machinist", "sandbox", subcommand}, "securityContext": security, "resources": resources, "env": env, "volumeMounts": []any{map[string]any{"name": "exchange", "mountPath": "/exchange"}, map[string]any{"name": "source", "mountPath": "/source"}, map[string]any{"name": "home", "mountPath": "/home/agent"}, map[string]any{"name": "tmp", "mountPath": "/tmp"}}}
	}
	prepare := container("prepare", "prepare")
	agent := container("agent", "run")
	agent["workingDir"] = filepath.Dir(SourceDirectory)
	agent["envFrom"] = []any{map[string]any{"secretRef": map[string]string{"name": c.Config.Secret}}}
	volumes := []any{}
	for _, volume := range []string{"exchange", "source", "home", "tmp"} {
		volumes = append(volumes, map[string]any{"name": volume, "emptyDir": map[string]string{"sizeLimit": "2Gi"}})
	}
	return map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": name, "labels": map[string]string{"app.kubernetes.io/managed-by": "machinist"}}, "spec": map[string]any{"backoffLimit": 0, "podReplacementPolicy": "Failed", "activeDeadlineSeconds": deadline, "ttlSecondsAfterFinished": 86400, "template": map[string]any{"metadata": map[string]any{"labels": map[string]string{"app": "machinist-agent"}}, "spec": map[string]any{"runtimeClassName": "gvisor", "automountServiceAccountToken": false, "restartPolicy": "Never", "securityContext": map[string]any{"fsGroup": 1000}, "initContainers": []any{prepare}, "containers": []any{agent}, "volumes": volumes}}}}
}
