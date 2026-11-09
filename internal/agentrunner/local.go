package agentrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/runner"
)

const codexKeyVariable = "OPENAI_API_KEY"

// The only variables an agent inherits from the runner, besides its credential.
var environmentAllowlist = []string{"PATH", "LANG", "TMPDIR", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS"}

// A login run keeps the OS user's identity, so the harness can find its login.
var loginAllowlist = []string{"USER", "LOGNAME"}

// Local runs each agent as a process on this machine, in a private directory.
type Local struct {
	config config.Runner
	getenv func(string) string
	// CloneURL maps a repository to the URL it is cloned from.
	CloneURL func(string) string
}

// NewLocal checks that every credential this runner offers is present.
func NewLocal(runnerConfig config.Runner) (*Local, error) {
	local := &Local{config: runnerConfig, getenv: os.Getenv, CloneURL: func(repository string) string {
		return "https://github.com/" + repository + ".git"
	}}
	if local.getenv("GH_TOKEN") == "" {
		return nil, errors.New("GH_TOKEN is required")
	}
	for harness, credential := range runnerConfig.Credentials {
		for _, name := range credential.Env {
			if local.getenv(name) == "" {
				return nil, fmt.Errorf("credentials.%s: %s is not set", harness, name)
			}
		}
	}
	return local, nil
}

func (l *Local) environment(names []string, skip string) []string {
	environment := []string{}
	for _, name := range names {
		if value := l.getenv(name); value != "" && name != skip {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

func (l *Local) workspaceOptions(request runner.RunRequest, credential config.Credential, root string) (runner.WorkspaceOptions, error) {
	options := runner.WorkspaceOptions{Request: request, Root: root, CloneURL: l.CloneURL(request.Repository), GitHubToken: l.getenv("GH_TOKEN"), Login: credential.Login}
	options.Environment = l.environment(environmentAllowlist, "")
	if credential.Login {
		options.Environment = append(options.Environment, l.environment(loginAllowlist, "")...)
		home, err := os.UserHomeDir()
		if err != nil {
			return options, fmt.Errorf("find user home directory: %w", err)
		}
		options.Home = home
		options.CodexHome = l.getenv("CODEX_HOME")
		if options.CodexHome == "" {
			options.CodexHome = filepath.Join(home, ".codex")
		}
		return options, nil
	}
	skip := ""
	if request.Harness == runner.HarnessCodex && slices.Contains(credential.Env, codexKeyVariable) {
		skip = codexKeyVariable
		options.CodexAPIKey = l.getenv(codexKeyVariable)
	}
	options.Environment = append(options.Environment, l.environment(credential.Env, skip)...)
	options.Home = filepath.Join(root, "home")
	options.CodexHome = filepath.Join(options.Home, ".codex")
	return options, nil
}

// Execute prepares a fresh clone, runs the harness and removes the clone and
// private home, including any credential written there.
func (l *Local) Execute(ctx context.Context, request runner.RunRequest) runner.RunResult {
	failure := func(err error) runner.RunResult {
		state := runner.StateFailed
		if ctx.Err() != nil {
			state = runner.StateCancelled
		}
		return runner.RunResult{State: state, Error: err.Error()}
	}
	if !runner.ValidRunID(request.RunID) {
		return failure(fmt.Errorf("invalid run ID %q", request.RunID))
	}
	credential, ok := l.config.Credentials[request.Harness]
	if !ok {
		return failure(fmt.Errorf("this runner has no credential for %q", request.Harness))
	}
	root := filepath.Join(l.config.DataDirectory, "work", request.RunID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return failure(err)
	}
	defer os.RemoveAll(root)
	options, err := l.workspaceOptions(request, credential, root)
	if err != nil {
		return failure(err)
	}
	workspace, err := runner.PrepareWorkspace(ctx, options)
	if err != nil {
		return failure(err)
	}
	command, err := runner.HarnessCommand(request.Harness, request.Model, time.Duration(request.TimeoutMillis)*time.Millisecond)
	if err != nil {
		return failure(err)
	}
	result, err := runner.Execute(ctx, runner.Options{
		Environment:   workspace.Environment,
		Prompt:        request.Prompt,
		RunID:         request.RunID,
		Command:       command,
		Repository:    workspace.Repository,
		DataDirectory: l.config.DataDirectory,
		Stdout:        io.Discard,
		Stderr:        io.Discard,
	})
	output := runner.RunResult{State: result.State, Report: result.Report, Tokens: result.TokenUsage, Cost: result.Cost, CredentialSource: result.CredentialSource}
	if err != nil {
		output.Error = err.Error()
	}
	if output.State == "" {
		output.State = runner.StateFailed
	}
	return output
}
