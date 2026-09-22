package runner

import (
	"fmt"
	"github.com/owainlewis/machinist/internal/protocol"
	"io"
	"os"
)

func readStepResult(path string) (*protocol.StepResult, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow result: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("workflow result must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow result: %w", err)
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil {
		return nil, err
	}
	result, err := protocol.ParseStepResult(body)
	if err != nil {
		return nil, fmt.Errorf("invalid workflow result: %w", err)
	}
	return result, nil
}
