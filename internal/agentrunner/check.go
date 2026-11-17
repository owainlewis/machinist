package agentrunner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/runner"
)

// checkStep proves a step can reach the repository with the runner's token.
const checkStep = `git ls-remote "https://github.com/$MACHINIST_REPO.git" HEAD > /dev/null`

// Check runs one real step on this runner and prints whether it worked.
func Check(ctx context.Context, executor Executor, runnerConfig config.Runner, repository string, output io.Writer) error {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	runID := "run_" + hex.EncodeToString(random)
	result := executor.Execute(ctx, runner.RunRequest{
		RunID:         runID,
		TicketID:      "check",
		Preset:        "check",
		Repository:    repository,
		Branch:        "machinist/check-" + hex.EncodeToString(random[:4]),
		Vars:          map[string]string{"repo": repository},
		Prompt:        "This is a setup check.",
		Steps:         []runner.Step{{Name: "reach-repository", Run: checkStep}},
		TimeoutMillis: time.Minute.Milliseconds(),
		GitName:       "machinist-check",
		GitEmail:      "machinist-check@users.noreply.github.com",
	})
	_ = os.RemoveAll(filepath.Join(runnerConfig.DataDirectory, "runs", runID))
	if result.State != runner.StateSucceeded {
		fmt.Fprintf(output, "failed: %s\n", result.Error)
		return errors.New("runner check failed")
	}
	fmt.Fprintf(output, "ok: a step reached %s, with %d variables from env\n", repository, len(runnerConfig.Env))
	return nil
}
