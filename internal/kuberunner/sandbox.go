// Package kuberunner runs the steps of one job per Kubernetes Job.
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

// SourceDirectory holds the workspace. The emptyDir mount above it is
// root-owned, so the non-root init container creates this child.
const SourceDirectory = "/source/repository"

// Receive stores the run request sent on stdin. The connector sends it to the
// init container without the run's GitHub token.
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

// tokenFile holds the run's GitHub token, which the connector sends to the
// agent container once it starts. Empty means the Secret's GH_TOKEN applies.
const tokenFile = "token"

// ReceiveToken stores the run's GitHub token sent on stdin. It writes a new
// file and renames it, so a retried send replaces the first.
func ReceiveToken(input io.Reader, directory string) error {
	body, err := io.ReadAll(io.LimitReader(input, 4097))
	if err != nil {
		return err
	}
	if len(body) > 4096 {
		return errors.New("invalid sandbox token")
	}
	temporary, err := os.CreateTemp(directory, ".token-*")
	if err != nil {
		return err
	}
	_, writeErr := temporary.Write(body)
	if err := errors.Join(writeErr, temporary.Close()); err != nil {
		_ = os.Remove(temporary.Name())
		return err
	}
	return os.Rename(temporary.Name(), filepath.Join(directory, tokenFile))
}

// readToken waits for the run's GitHub token and removes it from the exchange
// volume, which outlives the steps.
func readToken(ctx context.Context, exchange string) (string, error) {
	path := filepath.Join(exchange, tokenFile)
	if err := waitFile(ctx, path); err != nil {
		return "", err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), os.Remove(path)
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
// network is locked down before the agent container receives the Secret and
// the run's GitHub token.
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

// stepEnvironment is the Pod's environment, less what the workspace sets
// itself. Every variable of the Secret reaches the steps.
func stepEnvironment(environ []string) []string {
	skip := []string{"HOME", "GH_TOKEN", "GITHUB_TOKEN"}
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

// Sandbox runs in the agent container. It writes the result and then stays
// alive until the connector has read it and deletes the Job.
func Sandbox(ctx context.Context, exchange, root, home string) error {
	request, err := readRequest(exchange)
	if err != nil {
		return err
	}
	if request.GitHubToken, err = readToken(ctx, exchange); err != nil {
		return err
	}
	body, err := json.Marshal(runRequest(ctx, request, exchange, root, home))
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

func runRequest(ctx context.Context, request runner.RunRequest, exchange, root, home string) runner.RunResult {
	failure := func(err error) runner.RunResult {
		state := runner.StateFailed
		if ctx.Err() != nil {
			state = runner.StateCancelled
		}
		return runner.RunResult{State: state, Error: err.Error()}
	}
	token, err := runner.RunToken(request, os.Getenv("GH_TOKEN"))
	if err != nil {
		return failure(err)
	}
	workspace, err := runner.PrepareWorkspace(runner.WorkspaceOptions{
		Request:     request,
		Root:        root,
		GitHubToken: token,
		Environment: stepEnvironment(os.Environ()),
		Home:        home,
	})
	if err != nil {
		return failure(err)
	}
	return runner.RunSteps(ctx, request, workspace, filepath.Join(exchange, "runtime"))
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
