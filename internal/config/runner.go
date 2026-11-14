package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

const defaultRunnerDirName = ".machinist/runner"

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Credential names the variables a harness already reads, passed through from
// the runner's own environment, or uses the OS user's existing login.
type Credential struct {
	Env   []string `toml:"env"`
	Login bool     `toml:"login"`
}

// Runner is the configuration of one machine or cluster that runs agents.
type Runner struct {
	Kubernetes    *Kubernetes           `toml:"kubernetes"`
	Credentials   map[string]Credential `toml:"credentials"`
	ControlPlane  string                `toml:"control_plane"`
	TokenFile     string                `toml:"token_file"`
	DataDirectory string                `toml:"data_directory"`
	Slots         int                   `toml:"slots"`
	configDir     string
}

func LoadRunner(path string) (Runner, error) {
	if path == "" {
		defaultPath, err := defaultConfigPath("runner.toml")
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

// Harnesses lists the harnesses this runner has a credential for.
func (r Runner) Harnesses() []string {
	names := make([]string, 0, len(r.Credentials))
	for name := range r.Credentials {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func applyRunnerDefaults(runner Runner) (Runner, error) {
	address, err := url.Parse(runner.ControlPlane)
	if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.Host == "" {
		return Runner{}, errors.New("control_plane must be an http or https URL")
	}
	if runner.TokenFile == "" {
		return Runner{}, errors.New("token_file is required")
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
		home, err := os.UserHomeDir()
		if err != nil {
			return Runner{}, fmt.Errorf("find user home directory: %w", err)
		}
		runner.DataDirectory = filepath.Join(home, filepath.FromSlash(defaultRunnerDirName))
	}
	runner.DataDirectory, err = resolveConfigPath(runner.DataDirectory, runner.configDir)
	if err != nil {
		return Runner{}, fmt.Errorf("resolve data_directory: %w", err)
	}
	if len(runner.Credentials) == 0 {
		return Runner{}, errors.New("at least one of [credentials.claude] or [credentials.codex] is required")
	}
	for harness, credential := range runner.Credentials {
		if err := validateCredential(runner, harness, credential); err != nil {
			return Runner{}, err
		}
	}
	if runner.Kubernetes != nil && runner.Kubernetes.Kubeconfig != "" {
		runner.Kubernetes.Kubeconfig, err = resolveConfigPath(runner.Kubernetes.Kubeconfig, runner.configDir)
		if err != nil {
			return Runner{}, fmt.Errorf("resolve kubeconfig: %w", err)
		}
	}
	return runner, nil
}

func validateCredential(runner Runner, harness string, credential Credential) error {
	if harness != "claude" && harness != "codex" {
		return fmt.Errorf("credentials.%s: harness must be claude or codex", harness)
	}
	if credential.Login == (len(credential.Env) > 0) {
		return fmt.Errorf("credentials.%s: set exactly one of env or login", harness)
	}
	for _, name := range credential.Env {
		if !environmentName.MatchString(name) {
			return fmt.Errorf("credentials.%s: invalid environment variable name %q", harness, name)
		}
	}
	if credential.Login && runner.Kubernetes != nil {
		return fmt.Errorf("credentials.%s: Kubernetes runners do not support login", harness)
	}
	// A ChatGPT login refreshes its token on disk, so runs must not share one.
	if credential.Login && harness == "codex" && runner.Slots != 1 {
		return errors.New("credentials.codex: login requires slots = 1")
	}
	return nil
}
