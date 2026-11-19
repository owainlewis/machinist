package agentrunner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/runner"
)

// The only variables a step inherits from the runner, besides the runner's env.
var environmentAllowlist = []string{"PATH", "LANG", "TMPDIR", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS"}

// A login run keeps the OS user's identity, so a tool can find its login.
var loginAllowlist = []string{"USER", "LOGNAME"}

// Local runs each step as a process on this machine, in a private directory.
type Local struct {
	config config.Runner
	getenv func(string) string
}

// NewLocal checks that every variable this runner passes to steps is set.
// GH_TOKEN is optional: a control plane with a GitHub App sends each run a
// token of its own.
func NewLocal(runnerConfig config.Runner) (*Local, error) {
	local := &Local{config: runnerConfig, getenv: os.Getenv}
	for _, name := range runnerConfig.Env {
		if local.getenv(name) == "" {
			return nil, fmt.Errorf("env: %s is not set", name)
		}
	}
	return local, nil
}

func (l *Local) environment(names []string) []string {
	environment := []string{}
	for _, name := range names {
		if value := l.getenv(name); value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

func (l *Local) workspaceOptions(request runner.RunRequest, root string) (runner.WorkspaceOptions, error) {
	token, err := runner.RunToken(request, l.getenv("GH_TOKEN"))
	if err != nil {
		return runner.WorkspaceOptions{}, err
	}
	options := runner.WorkspaceOptions{Request: request, Root: root, GitHubToken: token, Home: filepath.Join(root, "home")}
	options.Environment = append(l.environment(environmentAllowlist), l.environment(l.config.Env)...)
	if l.config.Login {
		options.Environment = append(options.Environment, l.environment(loginAllowlist)...)
		home, err := os.UserHomeDir()
		if err != nil {
			return options, fmt.Errorf("find user home directory: %w", err)
		}
		options.Home = home
	}
	return options, nil
}

// Execute prepares an empty workspace, runs the steps and removes the
// workspace and private home, including anything a step wrote there.
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
	root := filepath.Join(l.config.DataDirectory, "work", request.RunID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return failure(err)
	}
	defer os.RemoveAll(root)
	options, err := l.workspaceOptions(request, root)
	if err != nil {
		return failure(err)
	}
	workspace, err := runner.PrepareWorkspace(options)
	if err != nil {
		return failure(err)
	}
	return runner.RunSteps(ctx, request, workspace, l.config.DataDirectory)
}
