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

const checkPrompt = `This is a setup check. Do not change anything. End this run with exactly one JSON object and nothing else:

{"outcome":"no_change","summary":"ok"}`

// Check makes one real run per harness and prints the credential source each
// harness reported, or the error.
func Check(ctx context.Context, executor Executor, runnerConfig config.Runner, repository string, output io.Writer) error {
	failed := false
	for _, harness := range runnerConfig.Harnesses() {
		random := make([]byte, 12)
		if _, err := rand.Read(random); err != nil {
			return err
		}
		runID := "run_" + hex.EncodeToString(random)
		result := executor.Execute(ctx, runner.RunRequest{
			RunID:         runID,
			Repository:    repository,
			Branch:        "machinist/check-" + hex.EncodeToString(random[:4]),
			Prompt:        checkPrompt,
			Harness:       harness,
			TimeoutMillis: (5 * time.Minute).Milliseconds(),
			GitName:       "machinist-check",
			GitEmail:      "machinist-check@users.noreply.github.com",
		})
		_ = os.RemoveAll(filepath.Join(runnerConfig.DataDirectory, "runs", runID))
		if result.State != runner.StateSucceeded || result.Report == nil {
			failed = true
			fmt.Fprintf(output, "%s: failed: %s\n", harness, result.Error)
			continue
		}
		fmt.Fprintf(output, "%s: ok, credential source: %s\n", harness, result.CredentialSource)
	}
	if failed {
		return errors.New("runner check failed")
	}
	return nil
}
