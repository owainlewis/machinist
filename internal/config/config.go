package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const (
	defaultListen        = "127.0.0.1:7331"
	defaultAgent         = "default"
	defaultMaxTurns      = 5
	defaultRunTimeout    = 60 * time.Minute
	maxConfigBytes       = 1 << 20
	maxTokenBytes        = 8 << 10
	defaultServerDirName = ".machinist/server"
	defaultFactoryDir    = ".machinist/factory"
)

type Server struct {
	Listen               string `toml:"listen"`
	Database             string `toml:"database"`
	Factory              string `toml:"factory"`
	DefaultAgent         string `toml:"default_agent"`
	MaxTurnsWithoutHuman int    `toml:"max_turns_without_human"`
	RunTimeout           string `toml:"run_timeout"`
	TLSCert              string `toml:"tls_cert"`
	TLSKey               string `toml:"tls_key"`
	runTimeout           time.Duration
	configDir            string
}

// RunDeadline is the longest one run may last.
func (s Server) RunDeadline() time.Duration { return s.runTimeout }

type GitHub struct {
	Handle       string   `toml:"handle"`
	Repositories []string `toml:"repositories"`
}

type Config struct {
	Server Server `toml:"server"`
	GitHub GitHub `toml:"github"`
	path   string
}

func (c Config) Path() string { return c.path }

// ResolvedCommand is the process and timeout for one run.
type ResolvedCommand struct {
	Adapter    string
	Name       string
	Executor   string
	Command    []string
	Model      string
	Timeout    time.Duration
	Definition string
	Hash       string
}

func LoadConfig(path string) (Config, error) {
	if path == "" {
		defaultPath, err := defaultConfigPath("config.toml")
		if err != nil {
			return Config{}, err
		}
		path = defaultPath
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return Config{}, fmt.Errorf("resolve Machinist config: %w", err)
	}
	machinistConfig := Config{path: absPath}
	if err := decodeFile(absPath, &machinistConfig); err != nil {
		return Config{}, err
	}
	machinistConfig.Server.configDir = filepath.Dir(absPath)
	machinistConfig.Server, err = applyServerDefaults(machinistConfig.Server)
	if err != nil {
		return Config{}, err
	}
	return machinistConfig, nil
}

// decodeFile rejects keys the target does not define and names each one, so an
// old configuration fails at startup with the key to remove.
func decodeFile(path string, target any) error {
	body, err := readBoundedFile(path, maxConfigBytes)
	if err != nil {
		return fmt.Errorf("read config %q: %w", path, err)
	}
	decoder := toml.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(target)
	var missing *toml.StrictMissingError
	if errors.As(err, &missing) {
		keys := make([]string, 0, len(missing.Errors))
		for _, unknown := range missing.Errors {
			keys = append(keys, strings.Join(unknown.Key(), "."))
		}
		return fmt.Errorf("parse config %q: unknown key %s", path, strings.Join(keys, ", "))
	}
	if err != nil {
		return fmt.Errorf("parse config %q: %w", path, err)
	}
	return nil
}

func applyServerDefaults(server Server) (Server, error) {
	if server.Listen == "" {
		server.Listen = defaultListen
	}
	if server.DefaultAgent == "" {
		server.DefaultAgent = defaultAgent
	}
	if server.MaxTurnsWithoutHuman == 0 {
		server.MaxTurnsWithoutHuman = defaultMaxTurns
	}
	if server.MaxTurnsWithoutHuman < 0 {
		return Server{}, errors.New("max_turns_without_human must be positive")
	}
	server.runTimeout = defaultRunTimeout
	if server.RunTimeout != "" {
		timeout, err := time.ParseDuration(server.RunTimeout)
		if err != nil {
			return Server{}, fmt.Errorf("run_timeout: %w", err)
		}
		if timeout <= 0 {
			return Server{}, errors.New("run_timeout must be positive")
		}
		server.runTimeout = timeout
	}
	if (server.TLSCert == "") != (server.TLSKey == "") {
		return Server{}, errors.New("tls_cert and tls_key must be set together")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Server{}, fmt.Errorf("find user home directory: %w", err)
	}
	if server.Database == "" {
		server.Database = filepath.Join(home, filepath.FromSlash(defaultServerDirName), "tickets.db")
	}
	if server.Factory == "" {
		server.Factory = filepath.Join(home, filepath.FromSlash(defaultFactoryDir))
	}
	for _, path := range []*string{&server.Database, &server.Factory, &server.TLSCert, &server.TLSKey} {
		if *path == "" {
			continue
		}
		*path, err = resolveConfigPath(*path, server.configDir)
		if err != nil {
			return Server{}, err
		}
	}
	return server, nil
}

func resolveConfigPath(path, base string) (string, error) {
	path, err := expandHome(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) && base != "" {
		path = filepath.Join(base, path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absPath), nil
}

func readToken(path string) (string, error) {
	body, err := readBoundedFile(path, maxTokenBytes)
	if err != nil {
		return "", fmt.Errorf("read token file %q: %w", path, err)
	}
	token := strings.TrimSpace(string(body))
	if token == "" {
		return "", fmt.Errorf("token file %q is empty", path)
	}
	return token, nil
}

func defaultConfigPath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find user home directory: %w", err)
	}
	return filepath.Join(home, ".machinist", name), nil
}

func expandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find user home directory: %w", err)
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(path, "~/"))), nil
	}
	return path, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return body, nil
}
