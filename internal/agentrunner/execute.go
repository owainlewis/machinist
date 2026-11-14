// Package agentrunner takes runs from the control plane and executes them.
package agentrunner

import (
	"context"

	"github.com/owainlewis/machinist/internal/runner"
)

// Executor hides where an agent runs. Cancelling ctx stops the run.
type Executor interface {
	Execute(ctx context.Context, request runner.RunRequest) runner.RunResult
}
