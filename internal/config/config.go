package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	defaultListen     = "127.0.0.1:7331"
	defaultPreset     = "default"
	defaultMaxTurns   = 5
	defaultRunTimeout = 60 * time.Minute
	maxConfigBytes    = 1 << 20
	maxTokenBytes     = 8 << 10
	// StateDir holds what Machinist writes at run time, next to the
	// configuration. It is kept out of git.
	StateDir          = ".machinist"
	defaultDatabase   = StateDir + "/tickets.db"
	defaultFactoryDir = "factory"
)

type Server struct {
	Listen               string `yaml:"listen"`
	Database             string `yaml:"database"`
	Factory              string `yaml:"factory"`
	DefaultPreset        string `yaml:"default_preset"`
	MaxTurnsWithoutHuman int    `yaml:"max_turns_without_human"`
	RunTimeout           string `yaml:"run_timeout"`
	TLSCert              string `yaml:"tls_cert"`
	TLSKey               string `yaml:"tls_key"`
	runTimeout           time.Duration
	configDir            string
}

// RunDeadline is the longest one run may last.
func (s Server) RunDeadline() time.Duration { return s.runTimeout }

type GitHub struct {
	Handle       string   `yaml:"handle"`
	Repositories []string `yaml:"repositories"`
}

type Config struct {
	Server Server `yaml:"server"`
	GitHub GitHub `yaml:"github"`
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
		defaultPath, err := defaultConfigPath("config.yaml")
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
	if filepath.Ext(path) == ".toml" {
		return tomlError(path)
	}
	body, err := readBoundedFile(path, maxConfigBytes)
	if err != nil {
		return fmt.Errorf("read config %q: %w", path, err)
	}
	return DecodeStrict(path, body, target)
}

// tomlError says how to move a file from before Machinist read YAML.
func tomlError(path string) error {
	return fmt.Errorf("%s is TOML, and Machinist now reads YAML. Rewrite it as %s", path, strings.TrimSuffix(path, ".toml")+".yaml")
}

var unknownField = regexp.MustCompile(`^line (\d+): field (\S+) not found in type`)

// DecodeStrict decodes one YAML document and names any key the target does not
// define.
func DecodeStrict(path string, body []byte, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	err := decoder.Decode(target)
	if errors.Is(err, io.EOF) {
		return nil
	}
	var typeError *yaml.TypeError
	if errors.As(err, &typeError) {
		problems := make([]string, 0, len(typeError.Errors))
		for _, problem := range typeError.Errors {
			if match := unknownField.FindStringSubmatch(problem); match != nil {
				problem = fmt.Sprintf("unknown key %s on line %s", match[2], match[1])
			}
			problems = append(problems, problem)
		}
		return fmt.Errorf("parse config %q: %s", path, strings.Join(problems, "; "))
	}
	if err != nil {
		return fmt.Errorf("parse config %q: %w", path, err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fmt.Errorf("parse config %q: only one YAML document is allowed", path)
	}
	return nil
}

func applyServerDefaults(server Server) (Server, error) {
	if server.Listen == "" {
		server.Listen = defaultListen
	}
	if server.DefaultPreset == "" {
		server.DefaultPreset = defaultPreset
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
	if server.Database == "" {
		server.Database = filepath.FromSlash(defaultDatabase)
	}
	if server.Factory == "" {
		server.Factory = defaultFactoryDir
	}
	var err error
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

// defaultConfigPath is the named file in the current directory. Machinist runs
// from the directory that holds its configuration, usually a git repository.
func defaultConfigPath(name string) (string, error) {
	path, err := filepath.Abs(name)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", name, err)
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		old := strings.TrimSuffix(path, ".yaml") + ".toml"
		if _, err := os.Stat(old); err == nil {
			return "", tomlError(old)
		}
		return "", missingConfigError(name, filepath.Dir(path))
	}
	return path, nil
}

func missingConfigError(name, directory string) error {
	message := fmt.Sprintf("no %s in %s. Run Machinist from the directory that holds its configuration, create one there with machinist init, or pass --config", name, directory)
	if home, err := os.UserHomeDir(); err == nil {
		old := filepath.Join(home, ".machinist", strings.TrimSuffix(name, ".yaml")+".toml")
		if _, err := os.Stat(old); err == nil {
			message += fmt.Sprintf(". Older versions kept it in %s; move its files here and rewrite them as YAML", filepath.Dir(old))
		}
	}
	return errors.New(message)
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
