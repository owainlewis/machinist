package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

type serverBootstrapConfig struct {
	Listen                         string `toml:"listen"`
	Database                       string `toml:"database"`
	DefaultBuildRuntime            string `toml:"default_build_runtime"`
	WorkerListen                   string `toml:"worker_listen"`
	WorkerTLSCert                  string `toml:"worker_tls_cert"`
	WorkerTLSKey                   string `toml:"worker_tls_key"`
	RetiredWebhookListen           string `toml:"webhook_listen"`
	RetiredWebhookTLSCert          string `toml:"webhook_tls_cert"`
	RetiredWebhookTLSKey           string `toml:"webhook_tls_key"`
	RetiredGitHubWebhookSecretFile string `toml:"github_webhook_secret_file"`
	path                           string
}

func loadServerBootstrapConfig(dataRoot string) (serverBootstrapConfig, error) {
	path := strings.TrimSpace(os.Getenv("FACTORY_SERVER_CONFIG"))
	explicit := path != ""
	if path == "" {
		path = filepath.Join(dataRoot, "config.toml")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return serverBootstrapConfig{}, fmt.Errorf("resolve Factory server configuration: %w", err)
	}
	info, err := os.Lstat(absolute)
	if errors.Is(err, os.ErrNotExist) && !explicit {
		return serverBootstrapConfig{path: absolute}, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return serverBootstrapConfig{}, fmt.Errorf("Factory server configuration does not exist: %s", absolute)
	}
	if err != nil {
		return serverBootstrapConfig{}, fmt.Errorf("inspect Factory server configuration: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return serverBootstrapConfig{}, errors.New("Factory server configuration must be a regular non-symlink file")
	}
	if info.Size() > 1<<20 {
		return serverBootstrapConfig{}, errors.New("Factory server configuration exceeds 1 MiB")
	}
	var config serverBootstrapConfig
	metadata, err := toml.DecodeFile(absolute, &config)
	if err != nil {
		return serverBootstrapConfig{}, fmt.Errorf("load Factory server configuration: %w", err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) != 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		sort.Strings(keys)
		return serverBootstrapConfig{}, fmt.Errorf("unknown Factory server configuration fields: %s", strings.Join(keys, ", "))
	}
	config.path = absolute
	config.Listen = strings.TrimSpace(config.Listen)
	config.Database = strings.TrimSpace(config.Database)
	config.DefaultBuildRuntime = strings.ToLower(strings.TrimSpace(config.DefaultBuildRuntime))
	config.WorkerListen = strings.TrimSpace(config.WorkerListen)
	config.WorkerTLSCert = resolveConfigPath(absolute, config.WorkerTLSCert)
	config.WorkerTLSKey = resolveConfigPath(absolute, config.WorkerTLSKey)
	if config.Database != "" && !filepath.IsAbs(config.Database) {
		config.Database = filepath.Join(filepath.Dir(absolute), config.Database)
	}
	return config, nil
}

func resolveConfigPath(configPath, value string) string {
	value = strings.TrimSpace(value)
	if value != "" && !filepath.IsAbs(value) {
		return filepath.Join(filepath.Dir(configPath), value)
	}
	return value
}
