package runner

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

// Step is one shell command of a preset. It runs with sh -c in the job's
// workspace. TimeoutMillis of zero means the rest of the run's time.
// IdleTimeoutMillis of zero means DefaultIdleTimeout.
type Step struct {
	Name              string `json:"name"`
	Run               string `json:"run"`
	TimeoutMillis     int64  `json:"timeout_millis,omitempty"`
	IdleTimeoutMillis int64  `json:"idle_timeout_millis,omitempty"`
}

// DefaultIdleTimeout stops a step that has printed nothing for this long. An
// agent prints a line for every message and action, so only a hung one, or a
// single tool call this long, stays quiet for it.
const DefaultIdleTimeout = 15 * time.Minute

// RunRequest is everything a runner needs to start one run. It carries no
// secrets. The steps run in order in one empty workspace.
type RunRequest struct {
	RunID      string            `json:"run_id"`
	TicketID   string            `json:"ticket_id"`
	Preset     string            `json:"preset"`
	Repository string            `json:"repository"`
	Branch     string            `json:"branch"`
	Vars       map[string]string `json:"vars,omitempty"`
	Prompt     string            `json:"prompt"`
	// Files are the factory's prompts and scripts, by path such as
	// prompts/plan.md. They are written to the control directory.
	Files         map[string]string `json:"files,omitempty"`
	Steps         []Step            `json:"steps"`
	TimeoutMillis int64             `json:"timeout_millis"`
	GitName       string            `json:"git_name"`
	GitEmail      string            `json:"git_email"`
	// GitHubToken is a short-lived token for Repository alone, when the
	// control plane has a GitHub App. Without it, the runner uses GH_TOKEN.
	GitHubToken string `json:"github_token,omitempty"`
	// RefreshToken, when set, asks the control plane for a new GitHubToken.
	// A local runner sets it, and each step starts with a fresh token, so no
	// step outlives the hour a token lasts. It is never sent.
	RefreshToken func(ctx context.Context) (string, error) `json:"-"`
}

// RunResult is what one run produced, on either runner type.
type RunResult struct {
	State  State
	Report *protocol.Report
	Error  string
	Stages []protocol.StageResult
	// Patch is the change a step saved in protocol.PatchFile, because it
	// could not push it.
	Patch []byte `json:",omitempty"`
	// Ship is what the merge step left in protocol.ShipFile, for the control
	// plane to see the pull request through its checks.
	Ship *protocol.Ship `json:",omitempty"`
}

// ValidRunID reports whether value has the form run_ and 24 hex characters.
func ValidRunID(value string) bool {
	if len(value) != len("run_")+24 || !strings.HasPrefix(value, "run_") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "run_"))
	return err == nil
}

// ErrNoGitHubToken means a run has no token of its own and the runner has no
// GH_TOKEN to fall back on.
var ErrNoGitHubToken = errors.New("no GitHub token: give the control plane a GitHub App (github.app), or set GH_TOKEN on the runner")

// RunToken is the token a run's steps use: the run's own, or else fallback,
// the runner's GH_TOKEN.
func RunToken(request RunRequest, fallback string) (string, error) {
	if request.GitHubToken != "" {
		return request.GitHubToken, nil
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", ErrNoGitHubToken
}
