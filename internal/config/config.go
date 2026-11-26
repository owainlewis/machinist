package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
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
	defaultRunTimeout = 60 * time.Minute
	maxConfigBytes    = 1 << 20
	maxTokenBytes     = 8 << 10
	// StateDir holds what Machinist writes at run time, next to the
	// configuration. It is kept out of git.
	StateDir          = ".machinist"
	defaultDatabase   = StateDir + "/tickets.db"
	defaultFactoryDir = "factory"

	// DefaultStewardRetries is how many times PR Steward automatically requeues
	// a job when its pull request fails CI or hits a merge conflict.
	DefaultStewardRetries = 2
	// MaxStewardRetries caps github.steward_retries so a broken pull request
	// cannot loop indefinitely.
	MaxStewardRetries = 5
)

type Server struct {
	Listen        string `yaml:"listen"`
	PublicURL     string `yaml:"public_url"`
	Database      string `yaml:"database"`
	Factory       string `yaml:"factory"`
	DefaultPreset string `yaml:"default_preset"`
	RunTimeout    string `yaml:"run_timeout"`
	TLSCert       string `yaml:"tls_cert"`
	TLSKey        string `yaml:"tls_key"`
	runTimeout    time.Duration
	configDir     string
}

// RunDeadline is the longest one run may last.
func (s Server) RunDeadline() time.Duration { return s.runTimeout }

type GitHub struct {
	Handle       string   `yaml:"handle"`
	Repositories []string `yaml:"repositories"`
	// App, when set, gives each run a short-lived token for its repository
	// alone, in place of a token on the runner.
	App *GitHubApp `yaml:"app"`
	// AutoMerge lets the publish step merge a pull request once its checks
	// pass and its review is clean. It is on unless set to false.
	AutoMerge *bool `yaml:"auto_merge"`
	// StewardRetries is how many times PR Steward automatically requeues a job
	// on the same branch when its pull request fails CI or hits a merge
	// conflict. It defaults to DefaultStewardRetries; 0 disables PR Steward.
	StewardRetries *int `yaml:"steward_retries"`
}

// AutoMerges reports whether jobs may merge their own pull requests.
func (g GitHub) AutoMerges() bool { return g.AutoMerge == nil || *g.AutoMerge }

// StewardRetryLimit reports how many automatic PR Steward retries are allowed.
// It defaults to DefaultStewardRetries when omitted; 0 disables PR Steward.
func (g GitHub) StewardRetryLimit() int {
	if g.StewardRetries == nil {
		return DefaultStewardRetries
	}
	return *g.StewardRetries
}

// GitHubApp names a GitHub App and the file that holds its private key. The key
// itself never goes in the configuration.
type GitHubApp struct {
	ID             int64  `yaml:"id"`
	PrivateKeyFile string `yaml:"private_key_file"`
}

// Linear starts jobs from issues assigned to the Machinist app, by asking
// Linear's API for them on an interval. It is off until projects or teams
// maps at least one repository. The app signs in with LINEAR_CLIENT_ID and
// LINEAR_CLIENT_SECRET, or a fixed LINEAR_API_TOKEN, never with anything in
// this file, so the file can live in git.
type Linear struct {
	// AppUserID is the Machinist app's Linear user. Assigning an issue to it
	// starts a job.
	AppUserID string `yaml:"app_user_id"`
	// AllowedUsers are the Linear user IDs whose assignments start jobs. An
	// assignment by anyone else is ignored.
	AllowedUsers []string `yaml:"allowed_users"`
	// Preset is the preset Linear jobs use. It defaults to server.default_preset.
	Preset string `yaml:"preset"`
	// Projects maps a Linear project, by name or ID, to the repository its
	// jobs work in. It wins over the issue's team.
	Projects map[string]string `yaml:"projects"`
	// Teams maps a Linear team key, such as ENG, to the repository its jobs
	// work in, for issues whose project is not in Projects. An issue that
	// matches neither is ignored, with a comment saying why.
	Teams map[string]string `yaml:"teams"`
	// MaxJobsPerHour caps the jobs Linear can start in any hour.
	MaxJobsPerHour int `yaml:"max_jobs_per_hour"`
	// PollInterval is how often Machinist asks Linear for new assignments,
	// such as 60s. Poll holds it parsed.
	PollInterval string        `yaml:"poll_interval"`
	Poll         time.Duration `yaml:"-"`
	// ClientID and ClientSecret are the app's client credentials. Machinist
	// gets and renews its own access token with them. They are read from the
	// environment, and take the place of Token when either is set.
	ClientID     string `yaml:"-"`
	ClientSecret string `yaml:"-"`
	// Token is a fixed access token. It is read from the environment.
	Token string `yaml:"-"`
}

// UsesClientCredentials reports whether Machinist gets its own Linear token.
func (l Linear) UsesClientCredentials() bool { return l.ClientID != "" }

// Enabled reports whether Machinist takes work from Linear.
func (l Linear) Enabled() bool { return len(l.Projects) > 0 || len(l.Teams) > 0 }

// CheckCredentials reports whether Linear, when it is enabled, has what it
// needs to sign in: both client credentials, or a fixed token. Only the control
// plane talks to Linear, so only it calls this.
func (l Linear) CheckCredentials() error {
	switch {
	case !l.Enabled():
		return nil
	case l.ClientID != "" && l.ClientSecret != "":
		return nil
	case l.ClientID != "" || l.ClientSecret != "":
		return fmt.Errorf("linear: set both %s and %s to the Machinist app's client credentials", LinearClientIDEnv, LinearClientSecretEnv)
	case l.Token != "":
		return nil
	default:
		return fmt.Errorf("linear: set %s and %s to the Machinist app's client credentials, or %s to an access token", LinearClientIDEnv, LinearClientSecretEnv, LinearTokenEnv)
	}
}

// The variables that hold the app's Linear credentials.
const (
	LinearClientIDEnv     = "LINEAR_CLIENT_ID"
	LinearClientSecretEnv = "LINEAR_CLIENT_SECRET"
	LinearTokenEnv        = "LINEAR_API_TOKEN"
)

const (
	defaultLinearJobsPerHour = 20
	defaultLinearPoll        = time.Minute
	minLinearPoll            = 10 * time.Second
)

// Notify posts a JSON message to a webhook when a job needs a person: when its
// pull request waits for a person or the job fails.
type Notify struct {
	Webhook string `yaml:"webhook"`
}

type Config struct {
	Server Server `yaml:"server"`
	GitHub GitHub `yaml:"github"`
	Linear Linear `yaml:"linear"`
	Notify Notify `yaml:"notify"`
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
	if err := applyGitHubDefaults(machinistConfig.GitHub, machinistConfig.Server.configDir); err != nil {
		return Config{}, err
	}
	machinistConfig.Linear, err = applyLinearDefaults(machinistConfig.Linear, os.Getenv)
	if err != nil {
		return Config{}, err
	}
	machinistConfig.Notify, err = applyNotifyDefaults(machinistConfig.Notify)
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
	server.PublicURL = strings.TrimSpace(server.PublicURL)
	if server.PublicURL != "" {
		parsed, err := url.Parse(server.PublicURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return Server{}, errors.New("public_url must be an http or https URL")
		}
		server.PublicURL = strings.TrimRight(server.PublicURL, "/")
	}
	if server.DefaultPreset == "" {
		server.DefaultPreset = defaultPreset
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

func applyLinearDefaults(linear Linear, getenv func(string) string) (Linear, error) {
	if !linear.Enabled() {
		if linear.AppUserID != "" || linear.Preset != "" || len(linear.AllowedUsers) > 0 {
			return Linear{}, errors.New("linear: set projects or teams to take work from Linear, or remove the linear section")
		}
		return linear, nil
	}
	// The credentials are read here and checked by CheckCredentials when the
	// control plane starts. Commands that only open the database, such as
	// token create, run in shells that do not have them.
	linear.ClientID = strings.TrimSpace(getenv(LinearClientIDEnv))
	linear.ClientSecret = strings.TrimSpace(getenv(LinearClientSecretEnv))
	if linear.ClientID == "" && linear.ClientSecret == "" {
		linear.Token = strings.TrimSpace(getenv(LinearTokenEnv))
	}
	linear.Poll = defaultLinearPoll
	if linear.PollInterval != "" {
		poll, err := time.ParseDuration(linear.PollInterval)
		if err != nil || poll < minLinearPoll {
			return Linear{}, fmt.Errorf("linear: poll_interval must be a duration of at least %s, such as 60s", minLinearPoll)
		}
		linear.Poll = poll
	}
	linear.AppUserID = strings.TrimSpace(linear.AppUserID)
	if linear.AppUserID == "" {
		return Linear{}, errors.New("linear: set app_user_id to the Machinist app's Linear user, so only issues assigned to it start jobs")
	}
	allowed := linear.AllowedUsers[:0:0]
	for _, user := range linear.AllowedUsers {
		if user = strings.TrimSpace(user); user != "" {
			allowed = append(allowed, user)
		}
	}
	if len(allowed) == 0 {
		return Linear{}, errors.New("linear: list the Linear user IDs allowed to assign work in allowed_users")
	}
	linear.AllowedUsers = allowed
	for project, repository := range linear.Projects {
		if strings.TrimSpace(project) == "" || strings.TrimSpace(repository) == "" {
			return Linear{}, errors.New("linear: each project needs a name or ID and a repository")
		}
	}
	for team, repository := range linear.Teams {
		if strings.TrimSpace(team) == "" || strings.TrimSpace(repository) == "" {
			return Linear{}, errors.New("linear: each team needs a key and a repository")
		}
	}
	if linear.MaxJobsPerHour < 0 {
		return Linear{}, errors.New("linear: max_jobs_per_hour must be positive")
	}
	if linear.MaxJobsPerHour == 0 {
		linear.MaxJobsPerHour = defaultLinearJobsPerHour
	}
	return linear, nil
}

func applyNotifyDefaults(notify Notify) (Notify, error) {
	notify.Webhook = strings.TrimSpace(notify.Webhook)
	if notify.Webhook == "" {
		return notify, nil
	}
	parsed, err := url.Parse(notify.Webhook)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return Notify{}, errors.New("notify.webhook must be an http or https URL")
	}
	return notify, nil
}

func applyGitHubDefaults(github GitHub, base string) error {
	if github.StewardRetries != nil {
		if *github.StewardRetries < 0 || *github.StewardRetries > MaxStewardRetries {
			return fmt.Errorf("github.steward_retries must be between 0 and %d", MaxStewardRetries)
		}
	}
	return applyAppDefaults(github.App, base)
}

func applyAppDefaults(app *GitHubApp, base string) error {
	if app == nil {
		return nil
	}
	if app.ID <= 0 {
		return errors.New("github.app.id is required: the App ID from the app's settings page")
	}
	if app.PrivateKeyFile == "" {
		return errors.New("github.app.private_key_file is required: the path to the app's private key")
	}
	path, err := resolveConfigPath(app.PrivateKeyFile, base)
	if err != nil {
		return err
	}
	app.PrivateKeyFile = path
	return nil
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
