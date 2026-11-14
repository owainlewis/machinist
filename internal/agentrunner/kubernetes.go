package agentrunner

import (
	"context"
	"errors"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/kuberunner"
	"github.com/owainlewis/machinist/internal/runner"
)

// Kubernetes runs each agent in its own gVisor Job. Credentials live in one
// Secret that only the Job reads, so this process never holds them.
type Kubernetes struct {
	connector     kuberunner.Connector
	dataDirectory string
}

func NewKubernetes(runnerConfig config.Runner) (*Kubernetes, error) {
	if runnerConfig.Kubernetes == nil {
		return nil, errors.New("this runner has no [kubernetes] configuration")
	}
	if err := runnerConfig.Kubernetes.Validate(); err != nil {
		return nil, err
	}
	return &Kubernetes{connector: kuberunner.Connector{Config: *runnerConfig.Kubernetes}, dataDirectory: runnerConfig.DataDirectory}, nil
}

func (k *Kubernetes) Execute(ctx context.Context, request runner.RunRequest) runner.RunResult {
	return k.connector.Execute(ctx, request, k.dataDirectory)
}
