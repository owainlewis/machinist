package managedworker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	instanceID     string
	client         *Client
	stdout         io.Writer
	stderr         io.Writer
	heartbeatTicks <-chan time.Time
	executeRun     func(context.Context, protocol.RunSpec) protocol.Completion
}

const heartbeatInterval = 10 * time.Second

func New(workerConfig config.Worker, stdout, stderr io.Writer) (*Worker, error) {
	if strings.TrimSpace(workerConfig.Name) == "" {
		return nil, errors.New("worker name is required")
	}
	if len(workerConfig.Executors) == 0 || len(workerConfig.Repositories) == 0 {
		return nil, errors.New("managed worker requires at least one executor and repository")
	}
	client, err := NewClient(workerConfig)
	if err != nil {
		return nil, err
	}
	instanceID, err := randomID("worker", 16)
	if err != nil {
		return nil, err
	}
	return &Worker{
		config:     workerConfig,
		instanceID: instanceID,
		client:     client,
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
	execute := w.executeRun
	if execute == nil {
		execute = w.execute
	}
	return withHeartbeats(ctx, w, spec, "", func() protocol.Completion { return execute(ctx, spec) })
}

func (w *Worker) deliverWithHeartbeats(ctx context.Context, spec protocol.RunSpec, completion protocol.Completion) error {
	if err := w.heartbeat(ctx, spec); err != nil {
		fmt.Fprintf(w.stderr, "machinist: heartbeat run %s before completion: %v\n", spec.ID, err)
	}
	return withHeartbeats(ctx, w, spec, " during completion", func() error { return w.deliver(ctx, spec.ID, completion) })
}

// withHeartbeats runs work in the background and keeps the run lease alive
// until it returns. Cancellation does not abandon the work; the work observes
// ctx itself and its result is always returned.
func withHeartbeats[T any](ctx context.Context, w *Worker, spec protocol.RunSpec, phase string, work func() T) T {
	ticks := w.heartbeatTicks
	if ticks == nil {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	done := make(chan T, 1)
	go func() { done <- work() }()
	for {
		select {
		case result := <-done:
			return result
		case <-ticks:
			if err := w.heartbeat(ctx, spec); err != nil {
				fmt.Fprintf(w.stderr, "machinist: heartbeat run %s%s: %v\n", spec.ID, phase, err)
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
	if err := w.client.Post(ctx, "/api/v1/workers/poll", request, &response); err != nil {
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

func (w *Worker) deliver(ctx context.Context, runID string, completion protocol.Completion) error {
	backoff := 250 * time.Millisecond
	for {
		err := w.client.Post(ctx, "/api/v1/runs/"+url.PathEscape(runID)+"/complete", completion, nil)
		if err == nil {
			return nil
		}
		var responseErr *ResponseError
		if errors.As(err, &responseErr) && !responseErr.Retryable() {
			return err
		}
		fmt.Fprintf(w.stderr, "machinist: report run %s: %v\n", runID, err)
		if !wait(ctx, backoff) {
			return ctx.Err()
		}
		backoff = min(backoff*2, 5*time.Second)
	}
}

func (w *Worker) heartbeat(ctx context.Context, spec protocol.RunSpec) error {
	return w.client.Post(ctx, "/api/v1/runs/"+url.PathEscape(spec.ID)+"/heartbeat", protocol.Heartbeat{
		InstanceID: w.instanceID,
		LeaseToken: spec.LeaseToken,
	}, nil)
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

func randomID(prefix string, byteCount int) (string, error) {
	body := make([]byte, byteCount)
	if _, err := rand.Read(body); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(body), nil
}
