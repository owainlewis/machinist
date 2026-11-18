package config

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
)

const (
	defaultRunnerData  = StateDir + "/runner"
	defaultRunnerToken = StateDir + "/runner.token"
)

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Runner is the configuration of one machine or cluster that runs steps.
type Runner struct {
	Kubernetes *Kubernetes `yaml:"kubernetes"`
	// Env names the variables passed from the runner's own environment to
	// every step, such as ANTHROPIC_API_KEY or LINEAR_API_KEY.
	Env []string `yaml:"env"`
	// Login gives steps the OS user's home, so a tool can use an existing login.
	Login         bool   `yaml:"login"`
	ControlPlane  string `yaml:"control_plane"`
	TokenFile     string `yaml:"token_file"`
	DataDirectory string `yaml:"data_directory"`
	Slots         int    `yaml:"slots"`
	configDir     string
}

func LoadRunner(path string) (Runner, error) {
	if path == "" {
		defaultPath, err := defaultConfigPath("runner.yaml")
		if err != nil {
			return Runner{}, err
		}
		path = defaultPath
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return Runner{}, fmt.Errorf("resolve runner config: %w", err)
	}
	runner := Runner{}
	if err := decodeFile(absPath, &runner); err != nil {
		return Runner{}, err
	}
	runner.configDir = filepath.Dir(absPath)
	return applyRunnerDefaults(runner)
}

// Token reads the bearer token this runner presents to the control plane.
func (r Runner) Token() (string, error) { return readToken(r.TokenFile) }

// Kind says where this runner's runs happen.
func (r Runner) Kind() string {
	if r.Kubernetes != nil {
		return "kubernetes"
	}
	return "local"
}

// Harnesses lists what this runner runs: a preset's steps.
func (r Runner) Harnesses() []string { return []string{"steps"} }

func applyRunnerDefaults(runner Runner) (Runner, error) {
	address, err := url.Parse(runner.ControlPlane)
	if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.Host == "" {
		return Runner{}, errors.New("control_plane must be an http or https URL")
	}
	if runner.TokenFile == "" {
		runner.TokenFile = filepath.FromSlash(defaultRunnerToken)
	}
	runner.TokenFile, err = resolveConfigPath(runner.TokenFile, runner.configDir)
	if err != nil {
		return Runner{}, fmt.Errorf("resolve token_file: %w", err)
	}
	if runner.Slots == 0 {
		runner.Slots = 1
	}
	if runner.Slots < 0 {
		return Runner{}, errors.New("slots must be positive")
	}
	if runner.DataDirectory == "" {
		runner.DataDirectory = filepath.FromSlash(defaultRunnerData)
	}
	runner.DataDirectory, err = resolveConfigPath(runner.DataDirectory, runner.configDir)
	if err != nil {
		return Runner{}, fmt.Errorf("resolve data_directory: %w", err)
	}
	for _, name := range runner.Env {
		if !environmentName.MatchString(name) {
			return Runner{}, fmt.Errorf("env: invalid environment variable name %q", name)
		}
	}
	if runner.Login && runner.Kubernetes != nil {
		return Runner{}, errors.New("login: Kubernetes runners do not support login")
	}
	if runner.Kubernetes != nil && runner.Kubernetes.Kubeconfig != "" {
		runner.Kubernetes.Kubeconfig, err = resolveConfigPath(runner.Kubernetes.Kubeconfig, runner.configDir)
		if err != nil {
			return Runner{}, fmt.Errorf("resolve kubeconfig: %w", err)
		}
	}
	return runner, nil
}
