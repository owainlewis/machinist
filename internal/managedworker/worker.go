package managedworker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
)

type Worker struct {
	config         config.Worker
	token          string
	instanceID     string
	client         *http.Client
	stdout         io.Writer
	stderr         io.Writer
	heartbeatTicks <-chan time.Time
	executeRun     func(context.Context, protocol.RunSpec) protocol.Completion
}

const heartbeatInterval = 10 * time.Second

type responseError struct {
	status int
	body   string
}

func (err *responseError) Error() string {
	return fmt.Sprintf("control plane returned %s: %s", http.StatusText(err.status), err.body)
}

func New(workerConfig config.Worker, stdout, stderr io.Writer) (*Worker, error) {
	if strings.TrimSpace(workerConfig.Name) == "" {
		return nil, errors.New("worker name is required")
	}
	if len(workerConfig.Executors) == 0 || len(workerConfig.Repositories) == 0 {
		return nil, errors.New("managed worker requires at least one executor and repository")
	}
	endpoint, err := url.Parse(workerConfig.ControlPlane.URL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, fmt.Errorf("invalid control_plane.url %q", workerConfig.ControlPlane.URL)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, errors.New("control_plane.url must use http or https")
	}
	if (endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.User != nil || endpoint.Fragment != "" || strings.Contains(workerConfig.ControlPlane.URL, "#") {
		return nil, errors.New("control_plane.url must not include a path, query, fragment, or userinfo")
	}
	if endpoint.Scheme == "http" && !loopbackHost(endpoint.Hostname()) {
		return nil, errors.New("control_plane.url must use https for a non-loopback host")
	}
	token, err := workerConfig.WorkerToken()
	if err != nil {
		return nil, err
	}
	instanceID, err := randomID("worker", 16)
	if err != nil {
		return nil, err
	}
	return &Worker{
		config:     workerConfig,
		token:      token,
		instanceID: instanceID,
		client:     &http.Client{Timeout: 15 * time.Second},
		stdout:     stdout,
		stderr:     stderr,
	}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	for {
		run, err := w.poll(ctx)
		if err != nil {
			if !wait(ctx, time.Second) {
				return nil
			}
			fmt.Fprintf(w.stderr, "machinist: worker poll: %v\n", err)
			continue
		}
		if run == nil {
			if !wait(ctx, time.Second) {
				return nil
			}
			continue
		}
		completion := w.executeWithHeartbeats(ctx, *run)
		if err := w.deliverWithHeartbeats(ctx, *run, completion); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

func (w *Worker) executeWithHeartbeats(ctx context.Context, spec protocol.RunSpec) protocol.Completion {
	ticks := w.heartbeatTicks
	var ticker *time.Ticker
	if ticks == nil {
		ticker = time.NewTicker(heartbeatInterval)
		ticks = ticker.C
		defer ticker.Stop()
	}
	execute := w.executeRun
	if execute == nil {
		execute = w.execute
	}
	done := make(chan protocol.Completion, 1)
	go func() {
		done <- execute(ctx, spec)
	}()
	for {
		select {
		case completion := <-done:
			return completion
		case <-ticks:
			if err := w.heartbeat(ctx, spec); err != nil {
				fmt.Fprintf(w.stderr, "machinist: heartbeat run %s: %v\n", spec.ID, err)
			}
		case <-ctx.Done():
			return <-done
		}
	}
}

func (w *Worker) poll(ctx context.Context) (*protocol.RunSpec, error) {
	request := protocol.PollRequest{
		InstanceID:   w.instanceID,
		Name:         w.config.Name,
		Executors:    w.config.ExecutorNames(),
		Repositories: w.config.RepositoryNames(),
		Models:       w.config.ModelCapabilities(),
	}
	var response protocol.PollResponse
	if err := w.post(ctx, "/api/v1/workers/poll", request, &response); err != nil {
		return nil, err
	}
	return response.Run, nil
}

func (w *Worker) execute(ctx context.Context, spec protocol.RunSpec) protocol.Completion {
	completion := protocol.Completion{InstanceID: w.instanceID, LeaseToken: spec.LeaseToken, State: "failed", ExitCode: 1}
	repository, err := w.config.ResolveRepository(spec.Repository)
	if err != nil {
		completion.Error = err.Error()
		return completion
	}
	command, err := w.config.ResolveCommandModel(config.ResolvedCommand{
		Name:       spec.Command,
		Executor:   spec.Executor,
		Prompt:     spec.RenderedPrompt,
		Timeout:    time.Duration(spec.TimeoutMillis) * time.Millisecond,
		Definition: "control-plane",
		Hash:       spec.CommandHash,
	}, spec.Model)
	if err != nil {
		completion.Error = err.Error()
		return completion
	}
	result, runErr := runner.Execute(ctx, runner.Options{
		RunID:         spec.ID,
		ArtifactKey:   spec.LeaseToken,
		Command:       command,
		Repository:    repository,
		DataDirectory: w.config.DataDirectory,
		Stdout:        w.stdout,
		Stderr:        w.stderr,
	})
	if result.ID != "" {
		completion.State = string(result.State)
		completion.ExitCode = result.ExitCode
		completion.Result, _ = os.ReadFile(filepath.Join(filepath.Dir(result.EventsPath), "result.json"))
		if events, readErr := os.ReadFile(result.EventsPath); readErr == nil {
			completion.Events = string(events)
		}
	}
	if runErr != nil {
		completion.Error = runErr.Error()
	}
	return completion
}

func (w *Worker) deliverWithHeartbeats(ctx context.Context, spec protocol.RunSpec, completion protocol.Completion) error {
	if err := w.heartbeat(ctx, spec); err != nil {
		fmt.Fprintf(w.stderr, "machinist: heartbeat run %s before completion: %v\n", spec.ID, err)
	}
	ticks := w.heartbeatTicks
	var ticker *time.Ticker
	if ticks == nil {
		ticker = time.NewTicker(heartbeatInterval)
		ticks = ticker.C
		defer ticker.Stop()
	}
	done := make(chan error, 1)
	go func() {
		done <- w.deliver(ctx, spec.ID, completion)
	}()
	for {
		select {
		case err := <-done:
			return err
		case <-ticks:
			if err := w.heartbeat(ctx, spec); err != nil {
				fmt.Fprintf(w.stderr, "machinist: heartbeat run %s during completion: %v\n", spec.ID, err)
			}
		case <-ctx.Done():
			return <-done
		}
	}
}

func (w *Worker) deliver(ctx context.Context, runID string, completion protocol.Completion) error {
	backoff := 250 * time.Millisecond
	for {
		err := w.post(ctx, "/api/v1/runs/"+url.PathEscape(runID)+"/complete", completion, nil)
		if err == nil {
			return nil
		}
		var responseErr *responseError
		if errors.As(err, &responseErr) && responseErr.status >= 400 && responseErr.status < 500 && responseErr.status != http.StatusRequestTimeout && responseErr.status != http.StatusTooManyRequests {
			return err
		}
		fmt.Fprintf(w.stderr, "machinist: report run %s: %v\n", runID, err)
		if !wait(ctx, backoff) {
			return ctx.Err()
		}
		if backoff < 5*time.Second {
			backoff *= 2
			if backoff > 5*time.Second {
				backoff = 5 * time.Second
			}
		}
	}
}

func (w *Worker) heartbeat(ctx context.Context, spec protocol.RunSpec) error {
	return w.post(ctx, "/api/v1/runs/"+url.PathEscape(spec.ID)+"/heartbeat", protocol.Heartbeat{
		InstanceID: w.instanceID,
		LeaseToken: spec.LeaseToken,
	}, nil)
}

func (w *Worker) post(ctx context.Context, path string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(w.config.ControlPlane.URL, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+w.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := w.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		return &responseError{status: response.StatusCode, body: strings.TrimSpace(string(message))}
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output); err != nil {
		return fmt.Errorf("decode control plane response: %w", err)
	}
	return nil
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func randomID(prefix string, byteCount int) (string, error) {
	body := make([]byte, byteCount)
	if _, err := rand.Read(body); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(body), nil
}
