// Package dockerrunner runs the steps of one job per ephemeral Docker container.
package dockerrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/owainlewis/machinist/internal/runner"
)

const (
	// ExchangeDirectory holds request.json, result.json and runtime/runs/<id>/events.jsonl.
	ExchangeDirectory = "/exchange"
	// WorkDirectory holds the isolated per-run workspace and control mounts.
	WorkDirectory = "/work"
	// HomeDirectory is the isolated per-run home directory inside the container.
	HomeDirectory = "/home/agent"

	maxRequestBytes = 8 << 20
)

// Sandbox executes one run inside an ephemeral Docker container. It reads
// request.json from exchange, runs all steps in root with isolated home, writes
// result.json atomically, and returns so the container exits cleanly.
func Sandbox(ctx context.Context, exchange, root, home string) error {
	request, err := readRequest(exchange)
	if err != nil {
		return err
	}
	result := runRequest(ctx, request, exchange, root, home)
	body, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode sandbox result: %w", err)
	}
	return writeResultAtomic(filepath.Join(exchange, "result.json"), body)
}

func readRequest(exchange string) (runner.RunRequest, error) {
	file, err := os.Open(filepath.Join(exchange, "request.json"))
	if err != nil {
		return runner.RunRequest{}, fmt.Errorf("read sandbox request: %w", err)
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxRequestBytes+1))
	if err != nil {
		return runner.RunRequest{}, fmt.Errorf("read sandbox request: %w", err)
	}
	if len(body) > maxRequestBytes {
		return runner.RunRequest{}, errors.New("sandbox request exceeds size limit")
	}
	var request runner.RunRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return runner.RunRequest{}, fmt.Errorf("decode sandbox request: %w", err)
	}
	return request, nil
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

// stepEnvironment keeps the container's environment except variables that
// PrepareWorkspace sets itself.
func stepEnvironment(environ []string) []string {
	skip := []string{"HOME", "GH_TOKEN", "GITHUB_TOKEN"}
	kept := []string{}
next:
	for _, entry := range runner.SanitizedEnvironment(environ) {
		name, _, _ := strings.Cut(entry, "=")
		for _, unwanted := range skip {
			if name == unwanted {
				continue next
			}
		}
		kept = append(kept, entry)
	}
	return kept
}

func writeResultAtomic(path string, body []byte) error {
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create sandbox result: %w", err)
	}
	_, writeErr := file.Write(body)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("write sandbox result: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("publish sandbox result: %w", err)
	}
	return nil
}
