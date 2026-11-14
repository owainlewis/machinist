package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/githubauth"
	"github.com/owainlewis/machinist/internal/protocol"
)

const agentFileLimit = 1 << 20

// WorkspaceOptions describes the private directory and credentials of one run.
type WorkspaceOptions struct {
	Request     RunRequest
	Root        string
	CloneURL    string
	GitHubToken string
	// Environment is the base environment: allowlist and credential variables.
	Environment []string
	// Home and CodexHome are private to the run unless Login is set.
	Home      string
	CodexHome string
	Login     bool
	// CodexAPIKey is piped to codex login, because app-server ignores it in the environment.
	CodexAPIKey string
}

// Workspace is a prepared clone and the complete environment of its agent.
type Workspace struct {
	Repository  string
	Environment []string
}

// PrepareWorkspace makes a fresh clone on the ticket's branch and sets up what
// the harness needs to run in it. Both runner types use it.
func PrepareWorkspace(ctx context.Context, options WorkspaceOptions) (Workspace, error) {
	if !options.Login {
		for _, directory := range []string{options.Home, options.CodexHome} {
			if err := os.MkdirAll(directory, 0o700); err != nil {
				return Workspace{}, err
			}
		}
	}
	github, err := githubauth.GitHubEnvironment(options.GitHubToken, options.Request.Repository)
	if err != nil {
		return Workspace{}, err
	}
	environment := append([]string(nil), options.Environment...)
	environment = append(environment, "HOME="+options.Home, "CODEX_HOME="+options.CodexHome, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	environment = append(environment, github...)

	repository := filepath.Join(options.Root, "repository")
	if err := runGit(ctx, options.Root, environment, "clone", "--quiet", options.CloneURL, repository); err != nil {
		return Workspace{}, err
	}
	if err := checkoutBranch(ctx, repository, environment, options.Request.Branch); err != nil {
		return Workspace{}, err
	}
	for _, setting := range [][2]string{{"user.name", options.Request.GitName}, {"user.email", options.Request.GitEmail}} {
		if err := runGit(ctx, repository, environment, "config", setting[0], setting[1]); err != nil {
			return Workspace{}, err
		}
	}
	if err := writeAgentFiles(repository, options.Request.AgentFiles); err != nil {
		return Workspace{}, err
	}
	canonical, err := filepath.EvalSymlinks(repository)
	if err != nil {
		return Workspace{}, err
	}
	if options.Request.Harness == HarnessCodex {
		if err := writeCodexConfig(options.CodexHome, canonical, options.Login); err != nil {
			return Workspace{}, err
		}
		if options.CodexAPIKey != "" {
			if err := codexLogin(ctx, options.Root, environment, options.CodexAPIKey); err != nil {
				return Workspace{}, err
			}
		}
	}
	return Workspace{Repository: canonical, Environment: environment}, nil
}

// RunInWorkspace runs the stages of a request in order in a prepared workspace.
// Each stage is a new process, and only the workspace carries over. The run
// ends at the first stage that fails, or at an agent stage that reports failed
// or needs_input. The run timeout covers all stages together.
func RunInWorkspace(ctx context.Context, request RunRequest, workspace Workspace, dataDirectory string) RunResult {
	stages := request.Stages
	if len(stages) == 0 {
		// PrepareWorkspace already wrote the agent files of the request.
		stages = []Stage{{Name: request.Harness, Kind: StageAgent, Prompt: request.Prompt, Model: request.Model}}
	}
	if !ValidRunID(request.RunID) {
		return RunResult{State: StateFailed, Error: fmt.Sprintf("invalid run ID %q", request.RunID)}
	}
	runDirectory := filepath.Join(dataDirectory, "runs", request.RunID)
	if err := createDurableDirectory(runDirectory); err != nil {
		return RunResult{State: StateFailed, Error: fmt.Sprintf("create run directory: %v", err)}
	}
	// Every stage appends to one event log, so the sequence numbers stay
	// continuous for the whole run.
	log, err := newEventLog(filepath.Join(runDirectory, "events.jsonl"))
	if err != nil {
		return RunResult{State: StateFailed, Error: err.Error()}
	}

	output := RunResult{State: StateSucceeded}
	results := make([]protocol.StageResult, len(stages))
	for index, stage := range stages {
		results[index] = protocol.StageResult{Name: stage.Name, Kind: stage.Kind, State: protocol.StageSkipped}
	}
	deadline := time.Now().Add(time.Duration(request.TimeoutMillis) * time.Millisecond)
	for index, stage := range stages {
		result, err := runStage(ctx, request, stage, workspace, dataDirectory, log, time.Until(deadline))
		if result.State == "" {
			result.State = StateFailed
		}
		entry := &results[index]
		entry.State = string(result.State)
		entry.Tokens = result.TokenUsage
		entry.Cost = result.Cost
		entry.DurationMillis = result.DurationMillis
		if result.TokenUsage != nil {
			if output.Tokens == nil {
				output.Tokens = new(int64)
			}
			*output.Tokens += *result.TokenUsage
		}
		if result.Cost != nil {
			if output.Cost == nil {
				output.Cost = new(float64)
			}
			*output.Cost += *result.Cost
		}
		if result.CredentialSource != "" {
			output.CredentialSource = result.CredentialSource
		}
		if result.State != StateSucceeded {
			output.State = result.State
			output.Report = nil
			if err != nil {
				entry.Error = err.Error()
				output.Error = entry.Error
				if len(stages) > 1 {
					output.Error = fmt.Sprintf("stage %s: %s", stage.Name, entry.Error)
				}
			}
			break
		}
		report := result.Report
		if report == nil {
			continue
		}
		entry.Outcome = report.Outcome
		// A later stage that changed nothing does not hide a pull request an
		// earlier stage made ready.
		if output.Report == nil || report.Outcome != protocol.OutcomeNoChange || output.Report.Outcome == protocol.OutcomeNoChange {
			output.Report = report
		}
		if report.Outcome == protocol.OutcomeFailed || report.Outcome == protocol.OutcomeNeedsInput {
			break
		}
	}
	if err := log.close(); err != nil && output.Error == "" {
		output.Error = err.Error()
	}
	if len(request.Stages) > 0 {
		output.Stages = results
	}
	return output
}

// runStage starts one stage and waits for it. An agent stage is the harness
// command. A script stage is sh, with no prompt and no report.
func runStage(ctx context.Context, request RunRequest, stage Stage, workspace Workspace, dataDirectory string, log *eventLog, remaining time.Duration) (Result, error) {
	if remaining <= 0 {
		return Result{State: StateTimedOut}, errors.New("the run timed out before this stage started")
	}
	var command config.ResolvedCommand
	switch stage.Kind {
	case StageAgent:
		if err := writeAgentFiles(workspace.Repository, stage.AgentFiles); err != nil {
			return Result{}, err
		}
		var err error
		command, err = HarnessCommand(request.Harness, stage.Model, remaining)
		if err != nil {
			return Result{}, err
		}
	case StageScript:
		command = config.ResolvedCommand{Name: stage.Name, Executor: "sh", Command: []string{"sh", "-c", stage.Script}, Timeout: remaining}
	default:
		return Result{}, fmt.Errorf("unknown stage kind %q", stage.Kind)
	}
	if err := log.append("stage.started", "", fmt.Sprintf("name=%s kind=%s", stage.Name, stage.Kind)); err != nil {
		return Result{}, err
	}
	return Execute(ctx, Options{
		Environment:   workspace.Environment,
		Prompt:        stage.Prompt,
		RunID:         request.RunID,
		Command:       command,
		Repository:    workspace.Repository,
		DataDirectory: dataDirectory,
		Stdout:        io.Discard,
		Stderr:        io.Discard,
		log:           log,
	})
}

func runGit(ctx context.Context, directory string, environment []string, args ...string) error {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = directory
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %.2048s", args[0], err, output)
	}
	return nil
}

// checkoutBranch resumes the ticket's branch if it was pushed, or starts it
// from the default branch.
func checkoutBranch(ctx context.Context, repository string, environment []string, branch string) error {
	remote := "refs/remotes/origin/" + branch
	if runGit(ctx, repository, environment, "rev-parse", "--quiet", "--verify", remote) == nil {
		return runGit(ctx, repository, environment, "checkout", "--quiet", "-B", branch, "origin/"+branch)
	}
	return runGit(ctx, repository, environment, "checkout", "--quiet", "-b", branch)
}

func validAgentFilePath(path string) bool {
	if !strings.HasPrefix(path, ".claude/agents/") && !strings.HasPrefix(path, ".codex/agents/") {
		return false
	}
	return filepath.ToSlash(filepath.Clean(path)) == path
}

func writeAgentFiles(repository string, files []AgentFile) error {
	if len(files) == 0 {
		return nil
	}
	excludes := []string{}
	for _, file := range files {
		if !validAgentFilePath(file.Path) || len(file.Body) > agentFileLimit {
			return fmt.Errorf("invalid agent file %q", file.Path)
		}
		path := filepath.Join(repository, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(file.Body), 0o600); err != nil {
			return err
		}
		excludes = append(excludes, "/"+file.Path)
	}
	exclude, err := os.OpenFile(filepath.Join(repository, ".git", "info", "exclude"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := exclude.WriteString(strings.Join(excludes, "\n") + "\n")
	return errors.Join(writeErr, exclude.Close())
}

// writeCodexConfig marks the clone trusted before Codex starts. Otherwise Codex
// sends a configWarning that fails the run. A login run appends to the user's
// own file, which is what Codex itself does when it trusts a folder.
func writeCodexConfig(codexHome, repository string, login bool) error {
	settings := ""
	if !login {
		settings = "cli_auth_credentials_store = \"file\"\n"
	}
	settings += fmt.Sprintf("\n[projects.%q]\ntrust_level = \"trusted\"\n", repository)
	file, err := os.OpenFile(filepath.Join(codexHome, "config.toml"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(settings)
	return errors.Join(writeErr, file.Close())
}

func codexLogin(ctx context.Context, directory string, environment []string, key string) error {
	command := exec.CommandContext(ctx, "codex", "login", "--with-api-key")
	command.Dir = directory
	command.Env = environment
	command.Stdin = strings.NewReader(key)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("codex login: %w: %.2048s", err, output)
	}
	return nil
}
