package kuberunner

import (
	"archive/tar"
	"bufio"
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

type Connector struct {
	Config   config.Kubernetes
	Progress func(string)
	run      func(context.Context, io.Reader, int, ...string) ([]byte, error)
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

// Login performs a fresh native device login in a private temporary directory.
// It never copies a desktop credential cache or places tokens in arguments.
func (c Connector) Login(ctx context.Context, in io.Reader, out, stderr io.Writer) error {
	if err := c.Config.Validate(); err != nil {
		return err
	}
	if _, err := c.get(ctx, "secret", c.Config.AuthSecret); err == nil {
		return fmt.Errorf("account Secret already exists; stop its Jobs before deliberately reseeding it")
	} else if !resourceMissing(err) {
		return fmt.Errorf("could not verify the cluster account Secret: %w", err)
	}
	if c.claude() {
		return c.loginClaude(ctx, in, out)
	}
	directory, err := os.MkdirTemp("", "machinist-codex-login-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	cmd := exec.CommandContext(ctx, "codex", "login", "--device-auth", "-c", "cli_auth_credentials_store=\"file\"")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+directory)
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = stderr
	if err = cmd.Run(); err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(directory, "auth.json"))
	if err != nil {
		return err
	}
	if err = ValidateAuth(body); err != nil {
		return err
	}
	return c.json(ctx, "create", map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": c.Config.AuthSecret}, "type": "Opaque", "data": map[string][]byte{"auth.json": body}})
}

func (c Connector) claude() bool { return c.Config.AgentName() == "claude" }

// loginClaude stores a token the operator generated with `claude setup-token`.
// The token is read from stdin, never from arguments, and is not echoed.
func (c Connector) loginClaude(ctx context.Context, in io.Reader, out io.Writer) error {
	fmt.Fprintln(out, "Run `claude setup-token` as the account owner, then paste the token here:")
	line, err := bufio.NewReader(io.LimitReader(in, 8<<10)).ReadString('\n')
	if err != nil && err != io.EOF {
		return err
	}
	token := []byte(strings.TrimSpace(line))
	if err = ValidateClaudeToken(token); err != nil {
		return err
	}
	return c.json(ctx, "create", map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": c.Config.AuthSecret}, "type": "Opaque", "data": map[string][]byte{ClaudeTokenFile: token}})
}

func (c Connector) Execute(ctx context.Context, options runner.Options, readOnly bool) (result runner.Result, candidate string, resultErr error) {
	created, stopped := false, false
	// Cleanup below runs first. Only a verified absent Job/Pod can acknowledge
	// shutdown; preserve that evidence even when cancellation returned no result.
	defer func() {
		result.ProcessStopped = !created || stopped
		result.CleanupUnconfirmed = created && !stopped
		if created && result.ID == "" {
			result.ID, result.State, result.ExitCode = options.RunID, runner.StateFailed, 1
			if errors.Is(ctx.Err(), context.Canceled) {
				result.State = runner.StateCancelled
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				result.State = runner.StateTimedOut
			}
		}
		if created && !stopped && resultErr == nil {
			resultErr = fmt.Errorf("Kubernetes Job shutdown is unconfirmed; inspect the assigned Job before retrying")
		}
	}()
	c.progress("starting_sandbox")
	if err := c.Config.Validate(); err != nil {
		return runner.Result{}, "", err
	}
	if err := c.ready(ctx); err != nil {
		return runner.Result{}, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, options.Command.Timeout+10*time.Minute)
	defer cancel()
	directory := filepath.Join(options.DataDirectory, "runs", options.RunID, options.ArtifactKey)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return runner.Result{}, "", err
	}
	name := objectName("attempt-", options.RunID)
	lock := objectName("codex-", c.Config.AuthSecret)
	// Claude setup-tokens never refresh, so jobs sharing one need no stream lock.
	locked := !c.claude()
	// No expiry or lock stealing: a lost connector must not race a live pod's
	// native token refresh. An operator must stop it before clearing the lock.
	if locked {
		if err := c.json(ctx, "create", map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": lock, "labels": map[string]string{"app.kubernetes.io/managed-by": "machinist"}}, "data": map[string]string{"job": name}}); err != nil {
			return runner.Result{}, "", fmt.Errorf("Codex account stream is locked or cluster unavailable; confirm the prior Job stopped before recovery: %w", err)
		}
	}
	pod := ""
	authSaved := !locked
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 45*time.Second)
		defer done()
		if created {
			if pod != "" && !authSaved {
				_, _ = c.command(cleanup, nil, 128, "exec", pod, "-c", "agent", "--", "touch", "/exchange/stop")
				if c.awaitResult(cleanup, pod) == nil {
					authSaved = c.retainAuth(cleanup, pod) == nil
				}
			}
			if authSaved && pod != "" {
				_, _ = c.command(cleanup, nil, 128, "exec", pod, "-c", "agent", "--", "touch", "/exchange/ack")
			}
			_, err := c.command(cleanup, nil, 1<<20, "delete", "job", name, "--cascade=foreground", "--wait=true", "--timeout=30s", "--ignore-not-found")
			if err == nil {
				body, e := c.command(cleanup, nil, 1<<20, "get", "pods", "-l", "job-name="+name, "-o", "json")
				var list struct {
					Items []json.RawMessage `json:"items"`
				}
				stopped = e == nil && json.Unmarshal(body, &list) == nil && len(list.Items) == 0
			}
		}
		if locked && (!created || (stopped && authSaved)) {
			_, _ = c.command(cleanup, nil, 1<<20, "delete", "configmap", lock, "--wait=true")
		}
	}()
	base, err := git(ctx, options.Repository, "rev-parse", "HEAD")
	if err != nil {
		return runner.Result{}, "", err
	}
	branch, err := git(ctx, options.Repository, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		branch = []byte("sandbox-review")
	}
	r := Request{RunID: options.RunID, JobID: options.JobID, Base: strings.TrimSpace(string(base)), Branch: strings.TrimSpace(string(branch)), ReadOnly: readOnly, Command: options.Command, Factory: options.Factory, Task: options.Task, Revision: options.Revision, ReviewEvidence: options.ReviewEvidence, Agent: c.Config.AgentName()}
	bundle := filepath.Join(directory, "input.bundle")
	if _, err = git(ctx, options.Repository, "bundle", "create", bundle, "HEAD"); err != nil {
		return runner.Result{}, "", err
	}
	info, err := os.Stat(bundle)
	if err != nil {
		return runner.Result{}, "", err
	}
	if info.Size() > MaxBundle {
		return runner.Result{}, "", fmt.Errorf("repository snapshot exceeds 100 MiB")
	}
	created = true // Even a lost create response may have started a Job.
	if err = c.json(ctx, "create", c.manifest(name, readOnly, int64(options.Command.Timeout.Seconds())+600)); err != nil {
		if _, getErr := c.get(ctx, "job", name); getErr != nil {
			return runner.Result{}, "", fmt.Errorf("Job creation uncertain; keep credential lock and inspect %s", name)
		}
	}
	created = true
	assignment := map[string]string{"job": name, "context": c.Config.Context, "namespace": c.Config.Namespace, "agent": c.Config.AgentName()}
	if locked {
		assignment["credential_lock"] = lock
	}
	assignmentBody, _ := json.Marshal(assignment)
	if err = os.WriteFile(filepath.Join(directory, "kubernetes.json"), assignmentBody, 0600); err != nil {
		return runner.Result{}, "", err
	}
	pod, err = c.awaitPod(ctx, name)
	if err != nil {
		return runner.Result{}, "", err
	}
	request, _ := json.Marshal(r)
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	for filename, data := range map[string][]byte{"request.json": request} {
		_ = tw.WriteHeader(&tar.Header{Name: filename, Mode: 0600, Size: int64(len(data))})
		_, _ = tw.Write(data)
	}
	file, err := os.Open(bundle)
	if err != nil {
		return runner.Result{}, "", err
	}
	_ = tw.WriteHeader(&tar.Header{Name: "input.bundle", Mode: 0600, Size: info.Size()})
	_, copyErr := io.Copy(tw, file)
	file.Close()
	closeErr := tw.Close()
	if copyErr != nil {
		return runner.Result{}, "", copyErr
	}
	if closeErr != nil {
		return runner.Result{}, "", closeErr
	}
	if _, err = c.command(ctx, &archive, 1<<20, "exec", "-i", pod, "-c", "prepare", "--", "machinist", "sandbox", "receive"); err != nil {
		return runner.Result{}, "", err
	}
	if err = c.awaitResult(ctx, pod); err != nil {
		// The init container has no credentials. Preserve its bounded diagnostics
		// before cleanup removes the Pod, without collecting native auth or prompts.
		if log, e := c.command(ctx, nil, 64<<10, "logs", pod, "-c", "prepare", "--tail=100"); e == nil {
			_ = os.WriteFile(filepath.Join(directory, "prepare.log"), log, 0600)
		}
		return runner.Result{}, "", err
	}
	// Retrieve auth separately, never in result.json, event logs or public files.
	c.progress("collecting_result")
	if locked {
		if err = c.retainAuth(ctx, pod); err != nil {
			return runner.Result{}, "", err
		}
		authSaved = true
	}
	body, err := c.command(ctx, nil, 1<<20, "exec", pod, "-c", "agent", "--", "cat", "/exchange/result.json")
	if err != nil {
		return runner.Result{}, "", err
	}
	var output Output
	if err = json.Unmarshal(body, &output); err != nil {
		return runner.Result{}, "", err
	}
	if output.Result.ID != options.RunID {
		return runner.Result{}, "", fmt.Errorf("sandbox returned a result for another attempt")
	}
	if output.Result.State != runner.StateSucceeded || output.Result.ExitCode != 0 {
		if output.Error == "" {
			output.Error = "agent process did not complete successfully"
		}
	}
	output.Result.EventsPath = filepath.Join(directory, "events.jsonl")
	for _, f := range []string{"stdout.log", "stderr.log"} {
		data, e := c.command(ctx, nil, MaxLog, "exec", pod, "-c", "agent", "--", "cat", "/exchange/"+f)
		if e != nil {
			return runner.Result{}, "", e
		}
		if e = os.WriteFile(filepath.Join(directory, f), data, 0600); e != nil {
			return runner.Result{}, "", e
		}
	}
	events, e := c.command(ctx, nil, MaxLog, "exec", pod, "-c", "agent", "--", "cat", "/exchange/runtime/runs/"+options.RunID+"/events.jsonl")
	if e != nil {
		return runner.Result{}, "", e
	}
	if e = os.WriteFile(output.Result.EventsPath, events, 0600); e != nil {
		return runner.Result{}, "", e
	}
	resultBody, _ := json.Marshal(output.Result)
	if err = os.WriteFile(filepath.Join(directory, "result.json"), resultBody, 0600); err != nil {
		return runner.Result{}, "", err
	}
	candidate = ""
	if output.Dirty || (readOnly && output.Head != r.Base) {
		return output.Result, "", fmt.Errorf("sandbox changed source without valid committed delivery")
	}
	if !readOnly && output.Head != r.Base {
		data, e := c.command(ctx, nil, MaxBundle, "exec", pod, "-c", "agent", "--", "cat", "/exchange/output.bundle")
		if e != nil {
			return output.Result, "", e
		}
		candidate = filepath.Join(directory, "candidate.bundle")
		if e = os.WriteFile(candidate, data, 0600); e != nil {
			return output.Result, "", e
		}
	}
	_, _ = c.command(ctx, nil, 1<<20, "exec", pod, "-c", "agent", "--", "touch", "/exchange/ack")
	if output.Error != "" {
		return output.Result, "", fmt.Errorf("sandbox agent: %s", output.Error)
	}
	return output.Result, candidate, nil
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
	if _, err := c.get(ctx, "secret", c.Config.AuthSecret); err != nil {
		if resourceMissing(err) {
			return fmt.Errorf("cluster account login is missing; run machinist kubernetes login: %w", err)
		}
		return fmt.Errorf("could not verify the cluster account Secret: %w", err)
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

func (c Connector) awaitPod(ctx context.Context, name string) (string, error) {
	for {
		body, err := c.command(ctx, nil, 1<<20, "get", "pods", "-l", "job-name="+name, "-o", "json")
		if err != nil {
			return "", err
		}
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
			p := list.Items[0]
			if p.Status.Phase == "Failed" {
				return "", fmt.Errorf("sandbox preparation failed")
			}
			if len(p.Status.Init) > 0 && p.Status.Init[0].State.Running != nil {
				return p.Metadata.Name, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
func (c Connector) awaitResult(ctx context.Context, pod string) error {
	for {
		if _, err := c.command(ctx, nil, 128, "exec", pod, "-c", "agent", "--", "test", "-f", "/exchange/done"); err == nil {
			return nil
		}
		object, err := c.get(ctx, "pod", pod)
		if err != nil {
			return err
		}
		status, _ := object["status"].(map[string]any)
		phase, _ := status["phase"].(string)
		if containers, ok := status["containerStatuses"].([]any); ok {
			for _, raw := range containers {
				container, _ := raw.(map[string]any)
				state, _ := container["state"].(map[string]any)
				if container["name"] == "agent" && state["running"] != nil {
					c.progress("running_agent")
				}
			}
		}
		if phase == "Failed" || phase == "Succeeded" {
			return fmt.Errorf("sandbox terminated before acknowledgement; inspect the Job and reseed auth before clearing its lock")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (c Connector) progress(phase string) {
	if c.Progress != nil {
		c.Progress(phase)
	}
}

func (c Connector) manifest(name string, readOnly bool, deadline int64) map[string]any {
	security := map[string]any{"runAsNonRoot": true, "runAsUser": 1000, "runAsGroup": 1000, "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}, "seccompProfile": map[string]string{"type": "RuntimeDefault"}}
	resources := map[string]any{"requests": map[string]string{"cpu": "1", "memory": "4Gi", "ephemeral-storage": "8Gi"}, "limits": map[string]string{"cpu": "2", "memory": "4Gi", "ephemeral-storage": "8Gi"}}
	container := func(containerName, subcommand string) map[string]any {
		return map[string]any{"name": containerName, "image": c.Config.Image, "command": []string{"machinist", "sandbox", subcommand}, "securityContext": security, "resources": resources, "volumeMounts": []any{map[string]any{"name": "exchange", "mountPath": "/exchange"}, map[string]any{"name": "source", "mountPath": "/source", "readOnly": containerName == "agent" && readOnly}, map[string]any{"name": "home", "mountPath": "/home/agent"}, map[string]any{"name": "tmp", "mountPath": "/tmp"}}}
	}
	prepare := container("prepare", "prepare")
	agent := container("agent", "run")
	agent["workingDir"] = SourceDirectory
	agent["volumeMounts"] = append(agent["volumeMounts"].([]any), map[string]any{"name": "codex-auth", "mountPath": "/credentials", "readOnly": true})
	env := []any{map[string]string{"name": "HTTPS_PROXY", "value": c.Config.ProxyURL}, map[string]string{"name": "HTTP_PROXY", "value": c.Config.ProxyURL}, map[string]string{"name": "HOME", "value": "/home/agent"}, map[string]string{"name": "CODEX_HOME", "value": "/home/agent/.codex"}, map[string]string{"name": "GIT_CONFIG_GLOBAL", "value": "/dev/null"}, map[string]string{"name": "GIT_CONFIG_NOSYSTEM", "value": "1"}}
	if c.claude() {
		// Only the model API is on the proxy allowlist; keep Claude Code from
		// reaching telemetry, error reporting or auto-update hosts.
		env = append(env, map[string]string{"name": "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "value": "1"}, map[string]string{"name": "DISABLE_AUTOUPDATER", "value": "1"})
	}
	agent["env"] = env
	prepare["env"] = agent["env"]
	volumes := []any{}
	for _, v := range []string{"exchange", "source", "home", "tmp"} {
		volumes = append(volumes, map[string]any{"name": v, "emptyDir": map[string]string{"sizeLimit": "2Gi"}})
	}
	volumes = append(volumes, map[string]any{"name": "codex-auth", "secret": map[string]any{"secretName": c.Config.AuthSecret, "defaultMode": 0440}})
	return map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": name, "labels": map[string]string{"app.kubernetes.io/managed-by": "machinist"}}, "spec": map[string]any{"backoffLimit": 0, "podReplacementPolicy": "Failed", "activeDeadlineSeconds": deadline, "ttlSecondsAfterFinished": 86400, "template": map[string]any{"metadata": map[string]any{"labels": map[string]string{"app": "machinist-agent"}}, "spec": map[string]any{"runtimeClassName": "gvisor", "automountServiceAccountToken": false, "restartPolicy": "Never", "securityContext": map[string]any{"fsGroup": 1000}, "initContainers": []any{prepare}, "containers": []any{agent}, "volumes": volumes}}}}
}

func (c Connector) retainAuth(ctx context.Context, pod string) error {
	auth, err := c.command(ctx, nil, 64<<10, "exec", pod, "-c", "agent", "--", "cat", "/home/agent/.codex/auth.json")
	if err != nil {
		return fmt.Errorf("could not retain refreshed Codex authentication; credential stream remains locked")
	}
	if err = ValidateAuth(auth); err != nil {
		return err
	}
	secret, err := c.get(ctx, "secret", c.Config.AuthSecret)
	if err != nil {
		return err
	}
	secret["data"] = map[string][]byte{"auth.json": auth}
	if err = c.json(ctx, "replace", secret); err != nil {
		return fmt.Errorf("could not persist refreshed Codex authentication; stream remains locked")
	}
	return nil
}
