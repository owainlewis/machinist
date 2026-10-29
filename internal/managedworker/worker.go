package managedworker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/kuberunner"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
)

type Worker struct {
	githubTokenProvider func(context.Context, string) (string, error)
	config              config.Worker
	instanceID          string
	client              *Client
	stdout              io.Writer
	stderr              io.Writer
	heartbeatTicks      <-chan time.Time
	executeRun          func(context.Context, protocol.RunSpec) protocol.Completion
}

const heartbeatInterval = 10 * time.Second

func New(workerConfig config.Worker, stdout, stderr io.Writer) (*Worker, error) {
	if strings.TrimSpace(workerConfig.Name) == "" {
		return nil, errors.New("worker name is required")
	}
	if len(workerConfig.Executors) == 0 || len(workerConfig.Repositories) == 0 {
		return nil, errors.New("managed worker requires at least one executor and repository")
	}
	if workerConfig.Kubernetes != nil {
		if err := workerConfig.Kubernetes.Validate(); err != nil {
			return nil, err
		}
		agent := workerConfig.Kubernetes.AgentName()
		for name := range workerConfig.Executors {
			if name != agent && name != agent+"-review" {
				return nil, fmt.Errorf("the Kubernetes runner is configured for %s; executor %q does not match", agent, name)
			}
		}
	}
	for _, executor := range workerConfig.Executors {
		if err := runner.CheckRestrictedClaude(context.Background(), executor.Command); err != nil {
			return nil, err
		}
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
			var responseErr *ResponseError
			if errors.As(err, &responseErr) && responseErr.Status == 409 {
				fmt.Fprintf(w.stderr, "machinist: report run %s: %v\n", run.ID, err)
				continue
			}
			return err
		}
	}
}

func (w *Worker) executeWithHeartbeats(ctx context.Context, spec protocol.RunSpec) protocol.Completion {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = withRunProgress(ctx)
	execute := w.executeRun
	if execute == nil {
		execute = w.execute
	}
	if spec.Factory != nil && spec.Factory.Recipe == "native-v1" {
		stop := w.streamRunEvents(ctx, spec)
		defer stop()
	}
	return withHeartbeats(ctx, w, spec, "", func() protocol.Completion { return execute(ctx, spec) }, cancel)
}

func (w *Worker) deliverWithHeartbeats(ctx context.Context, spec protocol.RunSpec, completion protocol.Completion) error {
	if err := w.heartbeat(ctx, spec); err != nil {
		fmt.Fprintf(w.stderr, "machinist: heartbeat run %s before completion: %v\n", spec.ID, err)
	}
	return withHeartbeats(ctx, w, spec, " during completion", func() error {
		if spec.Factory != nil && spec.Factory.Recipe == "native-v1" {
			for {
				err := w.flushRunEvents(ctx, spec)
				if err == nil {
					break
				}
				var response *ResponseError
				if errors.As(err, &response) && !response.Retryable() {
					return err
				}
				if !wait(ctx, time.Second) {
					return ctx.Err()
				}
			}
		}

		return w.deliver(ctx, spec.ID, completion)
	})
}

// withHeartbeats runs work in the background and keeps the run lease alive
// until it returns. Cancellation does not abandon the work; the work observes
// ctx itself and its result is always returned.
func withHeartbeats[T any](ctx context.Context, w *Worker, spec protocol.RunSpec, phase string, work func() T, stop ...context.CancelFunc) T {
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
				var responseErr *ResponseError
				if spec.Workflow && len(stop) > 0 && errors.As(err, &responseErr) && (responseErr.Status == 409 || responseErr.Status == 404) {
					stop[0]()
				}
			}
		case <-ctx.Done():
			return <-done
		}
	}
}

func (w *Worker) poll(ctx context.Context) (*protocol.RunSpec, error) {
	runtime := "host"
	if w.config.Kubernetes != nil {
		runtime = "kubernetes"
	}
	adapters := map[string]string{}
	for name, e := range w.config.Executors {
		adapters[name] = e.Adapter
	}
	request := protocol.PollRequest{
		NativeAgents: w.config.Kubernetes == nil, Adapters: adapters,
		Runtime:           runtime,
		SingleAgent:       true,
		Factory:           true,
		HealthChecks:      true,
		ReadOnlyExecutors: w.readOnlyExecutors(),
		SharedOutputs:     true,
		Reviews:           true,
		Workflows:         true,
		Artifacts:         true,
		InstanceID:        w.instanceID,
		Name:              w.config.Name,
		Executors:         w.config.ExecutorNames(),
		Repositories:      w.config.RepositoryNames(),
		Models:            w.config.ModelCapabilities(),
	}
	var response protocol.PollResponse
	if err := w.client.Post(ctx, "/api/v1/workers/poll", request, &response); err != nil {
		return nil, err
	}
	return response.Run, nil
}

func (w *Worker) execute(ctx context.Context, spec protocol.RunSpec) protocol.Completion {
	completion := protocol.Completion{InstanceID: w.instanceID, LeaseToken: spec.LeaseToken, State: "failed", ExitCode: 1, ProcessStopped: true}
	repository, err := w.config.ResolveRepository(spec.Repository)
	if err != nil {
		completion.Error = err.Error()
		return completion
	}
	if spec.Factory != nil || spec.HealthCheck {
		if w.githubTokenProvider != nil {
			_, remoteRepository, remoteErr := factoryRemote(repositoryCommand(ctx, repository))
			if remoteErr != nil {
				completion.Error = remoteErr.Error()
				return completion
			}
			if spec.GitHubRepository != "" && !strings.EqualFold(remoteRepository, spec.GitHubRepository) {
				completion.Error = "registered checkout no longer matches the selected GitHub repository"
				return completion
			}
			token, tokenErr := w.githubTokenProvider(ctx, spec.Repository)
			if tokenErr != nil {
				completion.Error = "GitHub App connection unavailable"
				return completion
			}
			environment, environmentErr := GitHubEnvironment(token, remoteRepository)
			if environmentErr != nil {
				completion.Error = environmentErr.Error()
				return completion
			}
			ctx = context.WithValue(ctx, commandEnvironmentKey{}, environment)
		}
	}
	if spec.HealthCheck {
		if err := validateGitHubConnection(repositoryCommand(ctx, repository), spec.GitHubRepository); err != nil {
			completion.Error = err.Error()
			return completion
		}
	}
	if w.config.Kubernetes != nil {
		env, _ := ctx.Value(commandEnvironmentKey{}).([]string)
		env = append(append([]string{}, env...), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		ctx = context.WithValue(ctx, commandEnvironmentKey{}, env)
	}
	if spec.Factory != nil {
		factory := *spec.Factory
		spec.Factory = &factory
		if factory.Branch == "" && factory.Role != "review" {
			factory.Branch = "codex/" + spec.JobID
		}
		if spec.Task != nil && spec.Task.SourceURL != "" {
			if err := validateSourceRepository(repositoryCommand(ctx, repository), spec.Task.SourceURL); err != nil {
				completion.Error = err.Error()
				return completion
			}
		}
		if (spec.Factory.Role == "review" || spec.Factory.Role == "triage" || spec.Factory.Recipe == "answer") && !w.config.Executors[spec.Executor].ReadOnly {
			completion.Error = "factory review requires a read-only executor"
			return completion
		}
		if w.config.Kubernetes != nil && (spec.Factory.Role == "spec" || spec.Factory.Role == "implement") {
			if recovered, found := w.resumePublication(ctx, repository, spec); found {
				return recovered
			}
		}
		registeredRepository := repository
		repository, err = w.factoryWorkspace(ctx, repository, spec)
		if err != nil {
			completion.Error = err.Error()
			return completion
		}
		if spec.Factory.Role == "review" {
			defer func() {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				if _, err := repositoryCommand(cleanup, registeredRepository)("git", "worktree", "remove", repository); err != nil {
					fmt.Fprintf(w.stderr, "machinist: review workspace retained: %v\n", err)
				}
			}()
		}
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
	var reviewEvidence string
	if spec.Factory != nil && spec.Factory.Role == "review" {
		setRunProgress(ctx, "checking_pr")
		evidence, err := collectReviewEvidence(repositoryCommand(ctx, repository), spec.Factory)
		if err != nil {
			completion.Error = err.Error()
			return completion
		}
		reviewEvidence = evidence
	}
	prepareInputs := w.prepareInputs(spec)
	if spec.Factory != nil {
		prepareInputs = nil
	}
	answerHead := ""
	if spec.Factory != nil && (spec.Factory.Recipe == "answer" || (spec.Factory.Recipe == "single" || spec.Factory.Recipe == "native-v1") || spec.Factory.Role == "triage") {
		answerHead, err = answerWorkspaceState(repositoryCommand(ctx, repository))
		if err != nil {
			completion.Error = err.Error()
			return completion
		}
	}
	agentEnvironment := commandEnvironment(ctx)
	if spec.Factory != nil && (spec.Factory.Role == "triage" || spec.Factory.Role == "review") {
		agentEnvironment = []string{"GH_TOKEN=", "GITHUB_TOKEN=", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0="}
	}
	options := runner.Options{
		Factory:        spec.Factory,
		Environment:    agentEnvironment,
		ReviewEvidence: reviewEvidence,
		Revision:       spec.Revision,
		Workflow:       spec.Workflow, JobID: spec.JobID,
		Task: spec.Task, PrepareInputs: prepareInputs,
		RunID:         spec.ID,
		ArtifactKey:   spec.LeaseToken,
		Command:       command,
		Repository:    repository,
		DataDirectory: w.config.DataDirectory,
		Stdout:        w.stdout,
		Stderr:        w.stderr,
	}
	var result runner.Result
	var runErr error
	if w.config.Kubernetes == nil {
		setRunProgress(ctx, "running_agent")
		result, runErr = runner.Execute(ctx, options)
	} else {
		if spec.Factory == nil && !spec.HealthCheck {
			completion.Error = "Kubernetes supports factory tasks and connection checks only"
			return completion
		}
		if spec.Factory != nil {
			copy := *spec.Factory
			copy.CommitOnly = true
			copy.SpecPath = "docs/specs/" + spec.JobID + ".md"
			options.Factory = &copy
			spec.Factory = &copy
		}
		options.Environment = nil
		var bundle string
		result, bundle, runErr = (kuberunner.Connector{Config: *w.config.Kubernetes, Progress: func(phase string) { setRunProgress(ctx, phase) }}).Execute(ctx, options, w.config.Executors[spec.Executor].ReadOnly)
		if runErr == nil && spec.Factory != nil && (spec.Factory.Role == "spec" || spec.Factory.Role == "implement") {
			setRunProgress(ctx, "publishing")
			runErr = w.publishCandidate(ctx, repository, bundle, spec, &result)
		}
		if result.ID != "" {
			body, _ := json.Marshal(result)
			_ = os.WriteFile(filepath.Join(filepath.Dir(result.EventsPath), "result.json"), body, 0600)
		}
		if runErr != nil {
			result.State = runner.StateFailed
			result.ExitCode = 1
		}
	}
	completion.CleanupUnconfirmed = result.CleanupUnconfirmed
	completion.ProcessStopped = result.ProcessStopped
	if ctx.Err() != nil && result.ID != "" {
		completion.State = string(result.State)
		completion.ExitCode = result.ExitCode
		if runErr != nil {
			completion.Error = runErr.Error()
		}
		return completion
	}
	if w.config.Kubernetes == nil && spec.Factory != nil && (spec.Factory.Recipe == "answer" || (spec.Factory.Recipe == "single" || spec.Factory.Recipe == "native-v1") || spec.Factory.Role == "triage") {
		after, stateErr := answerWorkspaceState(repositoryCommand(ctx, repository))
		if (stateErr != nil || after != answerHead) && (runErr == nil || !nativeFailureHasPublishedDelivery(repositoryCommand(ctx, repository), spec.Factory)) && ((spec.Factory.Recipe != "single" && spec.Factory.Recipe != "native-v1") || result.StepResult == nil || result.StepResult.Factory == nil || result.StepResult.Factory.PRURL == "") {
			completion.State = "succeeded"
			completion.ExitCode = 0
			blocked := protocol.StepResult{Outcome: "blocked", Summary: "The task changed files without valid PR delivery; preserve the work and deliver the requested changes through the assigned PR", Factory: &protocol.FactoryResult{Version: 1, Role: spec.Factory.Role, Report: "Workspace/delivery validation failed"}}
			completion.Result, _ = json.Marshal(map[string]any{"step_result": blocked})
			return completion
		}
	}
	if w.config.Kubernetes == nil && runErr == nil && spec.Factory != nil && (spec.Factory.Role == "implement" || spec.Factory.Role == "spec" || spec.Factory.Role == "primary") && result.StepResult != nil && result.StepResult.Outcome == "complete" && ((spec.Factory.Recipe != "single" && spec.Factory.Recipe != "native-v1") || (result.StepResult.Factory != nil && result.StepResult.Factory.PRURL != "")) {
		if err := validateImplementationEvidence(repositoryCommand(ctx, repository), result.StepResult.Factory); err != nil {
			runErr = err
			result.State = runner.StateFailed
			result.ExitCode = 1
		}
	}
	if result.ID != "" {
		completion.ProcessStopped = result.ProcessStopped
		completion.State = string(result.State)
		completion.ExitCode = result.ExitCode
		completion.Result, _ = os.ReadFile(filepath.Join(filepath.Dir(result.EventsPath), "result.json"))
		if runErr != nil && spec.Factory != nil {
			completion.Result, _ = json.Marshal(result)
		}
		if events, readErr := os.ReadFile(result.EventsPath); readErr == nil {
			completion.Events = string(events)
		}
	}
	if spec.Factory == nil && spec.Task != nil && result.EventsPath != "" {
		ids, err := w.publishOutputs(ctx, spec, filepath.Join(filepath.Dir(result.EventsPath), "outputs"))
		completion.Artifacts = ids
		if err != nil {
			completion.PublicationError = err.Error()
		}
	}
	if runErr != nil {
		completion.Error = runErr.Error()
	}
	return completion
}

func (w *Worker) deliver(ctx context.Context, runID string, completion protocol.Completion) error {
	// The foreman may reconcile a bounded batch of GitHub actions before recording
	// completion. Keep polling/heartbeat timeouts short and honor shutdown through
	// the request context while allowing this operation the artifact deadline.
	client := *w.client
	completionHTTP := *w.client.http
	completionHTTP.Timeout = 5 * time.Minute
	client.http = &completionHTTP
	backoff := 250 * time.Millisecond
	for {
		err := client.Post(ctx, "/api/v1/runs/"+url.PathEscape(runID)+"/complete", completion, nil)
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
		Phase:      runProgress(ctx),
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

func (w *Worker) readOnlyExecutors() []string {
	var names []string
	for _, name := range w.config.ExecutorNames() {
		if w.config.Executors[name].ReadOnly {
			names = append(names, name)
		}
	}
	return names
}
