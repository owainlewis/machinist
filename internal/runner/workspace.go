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

	"github.com/owainlewis/machinist/internal/githubauth"
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

// RunInWorkspace launches the fixed harness command in a prepared workspace.
func RunInWorkspace(ctx context.Context, request RunRequest, workspace Workspace, dataDirectory string) RunResult {
	command, err := HarnessCommand(request.Harness, request.Model, time.Duration(request.TimeoutMillis)*time.Millisecond)
	if err != nil {
		return RunResult{State: StateFailed, Error: err.Error()}
	}
	result, err := Execute(ctx, Options{
		Environment:   workspace.Environment,
		Prompt:        request.Prompt,
		RunID:         request.RunID,
		Command:       command,
		Repository:    workspace.Repository,
		DataDirectory: dataDirectory,
		Stdout:        io.Discard,
		Stderr:        io.Discard,
	})
	output := RunResult{State: result.State, Report: result.Report, Tokens: result.TokenUsage, Cost: result.Cost, CredentialSource: result.CredentialSource}
	if err != nil {
		output.Error = err.Error()
	}
	if output.State == "" {
		output.State = StateFailed
	}
	return output
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
