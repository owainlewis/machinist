// Package kuberunner runs one coding agent per Kubernetes Job.
package kuberunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/runner"
)

const (
	MaxLog     = 10 << 20
	maxRequest = 8 << 20
)

// SourceDirectory is the clone. The emptyDir mount above it is root-owned, so
// the non-root init container creates this child for Git's ownership check.
const SourceDirectory = "/source/repository"

// otherHarness lists credentials that only the other harness needs. One Secret
// serves every run, so each run drops what it has no use for.
var otherHarness = map[string][]string{
	runner.HarnessClaude: {"OPENAI_API_KEY"},
	runner.HarnessCodex:  {"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"},
}

// Receive stores the run request sent on stdin. It carries no secrets.
func Receive(input io.Reader, directory string) error {
	body, err := io.ReadAll(io.LimitReader(input, maxRequest+1))
	if err != nil {
		return err
	}
	if len(body) > maxRequest || !json.Valid(body) {
		return errors.New("invalid sandbox request")
	}
	file, err := os.OpenFile(filepath.Join(directory, "request.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(body)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "ready"), []byte("ready"), 0o600)
}

func waitFile(ctx context.Context, path string) error {
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Prepare runs in the init container, which holds no credentials. It proves the
// network is locked down before the agent container receives the Secret.
func Prepare(ctx context.Context, exchange, source string) error {
	if err := waitFile(ctx, filepath.Join(exchange, "ready")); err != nil {
		return err
	}
	if err := ProbeNetwork(ctx); err != nil {
		return err
	}
	return os.MkdirAll(source, 0o700)
}

func readRequest(exchange string) (runner.RunRequest, error) {
	var request runner.RunRequest
	body, err := os.ReadFile(filepath.Join(exchange, "request.json"))
	if err != nil {
		return request, err
	}
	err = json.Unmarshal(body, &request)
	return request, err
}

// agentEnvironment is the Pod's environment, less what the workspace sets
// itself and what this harness has no use for.
func agentEnvironment(environ []string, harness string) []string {
	skip := append([]string{"HOME", "CODEX_HOME", "GH_TOKEN", "GITHUB_TOKEN"}, otherHarness[harness]...)
	if harness == runner.HarnessCodex {
		skip = append(skip, "OPENAI_API_KEY") // Codex receives it through codex login.
	}
	kept := []string{}
next:
	for _, entry := range runner.SanitizedEnvironment(environ) {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "KUBERNETES_") {
			continue
		}
		for _, unwanted := range skip {
			if name == unwanted {
				continue next
			}
		}
		kept = append(kept, entry)
	}
	return kept
}

func githubCloneURL(repository string) string { return "https://github.com/" + repository + ".git" }

// Sandbox runs in the agent container. It writes the result and then stays
// alive until the connector has read it and deletes the Job.
func Sandbox(ctx context.Context, exchange, root, home string, cloneURL func(string) string) error {
	if cloneURL == nil {
		cloneURL = githubCloneURL
	}
	request, err := readRequest(exchange)
	if err != nil {
		return err
	}
	body, err := json.Marshal(runRequest(ctx, request, exchange, root, home, cloneURL))
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(exchange, "result.json"), body, 0o600); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(exchange, "done"), []byte("done"), 0o600); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func runRequest(ctx context.Context, request runner.RunRequest, exchange, root, home string, cloneURL func(string) string) runner.RunResult {
	failure := func(err error) runner.RunResult {
		state := runner.StateFailed
		if ctx.Err() != nil {
			state = runner.StateCancelled
		}
		return runner.RunResult{State: state, Error: err.Error()}
	}
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		return failure(fmt.Errorf("the agent Secret must hold GH_TOKEN"))
	}
	options := runner.WorkspaceOptions{
		Request:     request,
		Root:        root,
		CloneURL:    cloneURL(request.Repository),
		GitHubToken: token,
		Environment: agentEnvironment(os.Environ(), request.Harness),
		Home:        home,
		CodexHome:   filepath.Join(home, ".codex"),
	}
	if request.Harness == runner.HarnessCodex {
		options.CodexAPIKey = os.Getenv("OPENAI_API_KEY")
	}
	workspace, err := runner.PrepareWorkspace(ctx, options)
	if err != nil {
		return failure(err)
	}
	return runner.RunInWorkspace(ctx, request, workspace, filepath.Join(exchange, "runtime"))
}

type limitedWriter struct {
	writer    io.Writer
	remaining int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, fmt.Errorf("sandbox output limit exceeded")
	}
	n, e := w.writer.Write(p)
	w.remaining -= n
	return n, e
}
