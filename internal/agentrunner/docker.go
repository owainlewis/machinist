package agentrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/dockerrunner"
	"github.com/owainlewis/machinist/internal/runner"
)

// Docker runs each job in its own hardened, ephemeral Docker container.
type Docker struct {
	connector     dockerrunner.Connector
	dataDirectory string
}

// NewDocker validates the Docker configuration, verifies every configured env
// variable is present on the host, and checks that Docker (and any configured
// runtime such as runsc) is available.
func NewDocker(ctx context.Context, runnerConfig config.Runner, log io.Writer) (*Docker, error) {
	if runnerConfig.Docker == nil {
		return nil, errors.New("this runner has no docker configuration")
	}
	cfg := runnerConfig.Docker.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	effectiveEnv := runnerConfig.EffectiveEnv()
	for _, name := range effectiveEnv {
		if os.Getenv(name) == "" {
			return nil, fmt.Errorf("env: %s is not set", name)
		}
	}
	connector := dockerrunner.Connector{
		Config: cfg,
		Env:    effectiveEnv,
	}
	if err := connector.CheckRuntime(ctx, log); err != nil {
		return nil, err
	}
	return &Docker{
		connector:     connector,
		dataDirectory: runnerConfig.DataDirectory,
	}, nil
}

func (d *Docker) Execute(ctx context.Context, request runner.RunRequest) runner.RunResult {
	return d.connector.Execute(ctx, request, d.dataDirectory)
}

func (d *Docker) ProbeHarnesses(ctx context.Context) ([]string, error) {
	return d.connector.ProbeHarnesses(ctx)
}
