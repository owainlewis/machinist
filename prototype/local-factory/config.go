package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type config struct {
	Version        int                    `toml:"version"`
	StateDirectory string                 `toml:"state_directory"`
	MaxRevisions   int                    `toml:"max_revisions"`
	Server         serverConfig           `toml:"server"`
	Repositories   []repositoryConfig     `toml:"repositories"`
	Agents         map[string]agentConfig `toml:"agents"`
	Roles          roleConfig             `toml:"roles"`
}

type serverConfig struct {
	Listen        string `toml:"listen"`
	PollEvery     string `toml:"poll_every"`
	TriggerLabel  string `toml:"trigger_label"`
	MaxConcurrent int    `toml:"max_concurrent"`
}

type repositoryConfig struct {
	GitHub  string `toml:"github"`
	Path    string `toml:"path"`
	BaseRef string `toml:"base_ref"`
}

type agentConfig struct {
	Runtime string   `toml:"runtime"`
	Model   string   `toml:"model"`
	Prompt  string   `toml:"prompt"`
	Timeout string   `toml:"timeout"`
	Command []string `toml:"command"`
}

type roleConfig struct {
	Foreman string `toml:"foreman"`
	Plan    string `toml:"plan"`
	Build   string `toml:"build"`
	Verify  string `toml:"verify"`
}

type loadedConfig struct {
	Path      string
	Directory string
	Config    config
	Prompts   map[string]string
}

func loadConfig(path string) (loadedConfig, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return loadedConfig{}, fmt.Errorf("resolve config path: %w", err)
	}

	var value config
	metadata, err := toml.DecodeFile(abs, &value)
	if err != nil {
		return loadedConfig{}, fmt.Errorf("decode config: %w", err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) != 0 {
		return loadedConfig{}, fmt.Errorf("decode config: unknown field %q", undecoded[0])
	}
	for index := range value.Repositories {
		value.Repositories[index].GitHub = strings.TrimSpace(value.Repositories[index].GitHub)
	}
	if err := validateConfig(value); err != nil {
		return loadedConfig{}, err
	}

	directory := filepath.Dir(abs)
	prompts := make(map[string]string, len(value.Agents))
	for name, agent := range value.Agents {
		promptPath, err := resolveBeneath(directory, agent.Prompt)
		if err != nil {
			return loadedConfig{}, fmt.Errorf("agent %q prompt: %w", name, err)
		}
		body, err := os.ReadFile(promptPath)
		if err != nil {
			return loadedConfig{}, fmt.Errorf("read agent %q prompt: %w", name, err)
		}
		if strings.TrimSpace(string(body)) == "" {
			return loadedConfig{}, fmt.Errorf("agent %q prompt is empty", name)
		}
		prompts[name] = string(body)
	}

	if value.StateDirectory == "" {
		value.StateDirectory = filepath.Join(directory, ".state")
	} else if !filepath.IsAbs(value.StateDirectory) {
		value.StateDirectory = filepath.Join(directory, value.StateDirectory)
	}
	value.StateDirectory = filepath.Clean(value.StateDirectory)

	return loadedConfig{
		Path:      abs,
		Directory: directory,
		Config:    value,
		Prompts:   prompts,
	}, nil
}

func validateConfig(value config) error {
	if value.Version != 1 {
		return fmt.Errorf("config version must be 1, got %d", value.Version)
	}
	if value.MaxRevisions < 0 || value.MaxRevisions > 5 {
		return errors.New("max_revisions must be between 0 and 5")
	}
	if value.Server.Listen == "" {
		return errors.New("server.listen is required")
	}
	host, _, err := net.SplitHostPort(value.Server.Listen)
	if err != nil {
		return fmt.Errorf("server.listen: %w", err)
	}
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return errors.New("server.listen must use localhost, 127.0.0.1, or ::1 in this local spike")
	}
	if value.Server.PollEvery == "" {
		return errors.New("server.poll_every is required")
	}
	pollEvery, err := time.ParseDuration(value.Server.PollEvery)
	if err != nil {
		return fmt.Errorf("server.poll_every: %w", err)
	}
	if pollEvery <= 0 {
		return errors.New("server.poll_every must be greater than zero")
	}
	if value.Server.TriggerLabel == "" {
		return errors.New("server.trigger_label is required")
	}
	if value.Server.MaxConcurrent < 1 || value.Server.MaxConcurrent > 16 {
		return errors.New("server.max_concurrent must be between 1 and 16")
	}
	if len(value.Repositories) == 0 {
		return errors.New("at least one [[repositories]] entry is required")
	}
	seenRepositories := make(map[string]struct{}, len(value.Repositories))
	for index, repository := range value.Repositories {
		if !validRepositoryName(repository.GitHub) {
			return fmt.Errorf("repositories[%d].github must use owner/repository format", index)
		}
		if repository.Path == "" {
			return fmt.Errorf("repositories[%d].path is required", index)
		}
		if strings.TrimSpace(repository.BaseRef) == "" || strings.HasPrefix(repository.BaseRef, "-") {
			return fmt.Errorf("repositories[%d].base_ref is required and must be a Git ref", index)
		}
		key := strings.ToLower(repository.GitHub)
		if _, exists := seenRepositories[key]; exists {
			return fmt.Errorf("repository %q is configured more than once", repository.GitHub)
		}
		seenRepositories[key] = struct{}{}
	}
	roles := map[string]string{
		"foreman": value.Roles.Foreman,
		"plan":    value.Roles.Plan,
		"build":   value.Roles.Build,
		"verify":  value.Roles.Verify,
	}
	for role, name := range roles {
		if name == "" {
			return fmt.Errorf("roles.%s is required", role)
		}
		if _, ok := value.Agents[name]; !ok {
			return fmt.Errorf("roles.%s references unknown agent %q", role, name)
		}
	}
	for name, agent := range value.Agents {
		if agent.Prompt == "" {
			return fmt.Errorf("agent %q prompt is required", name)
		}
		if agent.Timeout != "" {
			timeout, err := time.ParseDuration(agent.Timeout)
			if err != nil {
				return fmt.Errorf("agent %q timeout: %w", name, err)
			}
			if timeout <= 0 {
				return fmt.Errorf("agent %q timeout must be greater than zero", name)
			}
		}
		switch agent.Runtime {
		case "claude", "codex":
			if len(agent.Command) != 0 {
				return fmt.Errorf("agent %q command is allowed only for runtime=command", name)
			}
		case "command":
			if len(agent.Command) == 0 || strings.TrimSpace(agent.Command[0]) == "" {
				return fmt.Errorf("agent %q command is required for runtime=command", name)
			}
		default:
			return fmt.Errorf("agent %q runtime must be claude, codex, or command", name)
		}
	}
	if value.Agents[value.Roles.Foreman].Runtime == "codex" {
		return errors.New("roles.foreman cannot use runtime=codex in V1 because the read-only sandbox cannot reach Factory's loopback control API")
	}
	return nil
}

func validRepositoryName(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" &&
		!strings.ContainsAny(value, "#?\\")
}

func resolveBeneath(root, path string) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
	joined := path
	if !filepath.IsAbs(joined) {
		joined = filepath.Join(root, joined)
	}
	abs, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes the config directory")
	}
	return abs, nil
}
