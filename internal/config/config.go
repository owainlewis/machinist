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
	"strconv"
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
	// MaxTrustedProxyHops caps server.trusted_proxy_hops.
	MaxTrustedProxyHops = 10

	// DatabaseURLEnv overrides server.database with a PostgreSQL connection URL
	// (or SQLite file path) from the environment.
	DatabaseURLEnv = "DATABASE_URL"
	// NotifyWebhookEnv overrides notify.webhook from the environment.
	NotifyWebhookEnv = "NOTIFY_WEBHOOK_URL"
)

// IsPostgresURL reports whether dsn is a PostgreSQL connection URL
// (postgres:// or postgresql://), including empty-host Cloud SQL Unix socket URLs.
func IsPostgresURL(dsn string) bool {
	lower := strings.ToLower(strings.TrimSpace(dsn))
	return strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://")
}

// RedactDatabaseURL removes credentials from a PostgreSQL connection URL so it
// can be safely logged or included in error messages. Non-URL paths are
// returned unchanged. Malformed PostgreSQL URLs still have credentials stripped.
func RedactDatabaseURL(dsn string) string {
	trimmed := strings.TrimSpace(dsn)
	if !IsPostgresURL(trimmed) {
		return dsn
	}
	if parsed, err := url.Parse(trimmed); err == nil {
		if parsed.User != nil {
			if _, hasPass := parsed.User.Password(); hasPass {
				parsed.User = url.UserPassword(parsed.User.Username(), "REDACTED")
			} else if parsed.User.Username() != "" {
				parsed.User = url.User(parsed.User.Username())
			}
		}
		if parsed.RawQuery != "" {
			q, qErr := url.ParseQuery(parsed.RawQuery)
			if qErr != nil {
				parsed.RawQuery = "redacted=true"
			} else if q.Has("password") {
				q.Set("password", "REDACTED")
				parsed.RawQuery = q.Encode()
			}
		}
		return parsed.String()
	}
	// String-level fallback when url.Parse fails (e.g. unescaped password characters).
	schemeIdx := strings.Index(trimmed, "://")
	if schemeIdx < 0 {
		return "postgres://<redacted>"
	}
	prefix := trimmed[:schemeIdx+3]
	rest := trimmed[schemeIdx+3:]
	// Strip query password=... if present.
	if qIdx := strings.IndexByte(rest, '?'); qIdx >= 0 {
		rest = rest[:qIdx] + "?<redacted>"
	}
	if atIdx := strings.LastIndexByte(rest, '@'); atIdx >= 0 {
		return prefix + "REDACTED@" + rest[atIdx+1:]
	}
	return prefix + "<redacted>"
}

type Server struct {
	Listen           string `yaml:"listen"`
	PublicURL        string `yaml:"public_url"`
	TrustedProxyHops int    `yaml:"trusted_proxy_hops"`
	Database         string `yaml:"database"`
	Factory          string `yaml:"factory"`
	DefaultPreset    string `yaml:"default_preset"`
	RunTimeout       string `yaml:"run_timeout"`
	TLSCert          string `yaml:"tls_cert"`
	TLSKey           string `yaml:"tls_key"`
	runTimeout       time.Duration
	configDir        string
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
	// LinearClientIDEnv and siblings hold the app's Linear credentials.
	LinearClientIDEnv     = "LINEAR_CLIENT_ID"
	LinearClientSecretEnv = "LINEAR_CLIENT_SECRET"
	LinearTokenEnv        = "LINEAR_API_TOKEN"
	// GitHubTokenEnv holds a fallback GitHub token when github.app is not set.
	GitHubTokenEnv = "GH_TOKEN"
)

const (
	defaultLinearJobsPerHour       = 20
	defaultLinearPoll              = time.Minute
	minLinearPoll                  = 10 * time.Second
	defaultGitHubIssuesLabel       = "machinist"
	defaultGitHubIssuesPollSeconds = 60
	minGitHubIssuesPollSeconds     = 10
	defaultGitHubIssuesJobsPerHour = 10
)

var githubRepoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)

// GitHubIssues starts jobs from open GitHub issues carrying a trigger label,
// by polling the GitHub REST API on an interval. It is off until allowed_users
// is configured.
type GitHubIssues struct {
	// Label is the GitHub label whose application queues a job. Defaults to "machinist".
	Label string `yaml:"label"`
	// InProgressLabel is an optional label added when a job starts and removed
	// when the job returns, fails, or finishes.
	InProgressLabel string `yaml:"in_progress_label"`
	// AllowedUsers are the GitHub logins whose label applications start jobs
	// and whose comments trigger follow-up runs. Required when enabled.
	AllowedUsers []string `yaml:"allowed_users"`
	// Repositories is an optional subset of github.repositories to watch.
	// When omitted, all repositories in github.repositories are watched.
	Repositories []string `yaml:"repositories"`
	// Preset is the preset GitHub Issues jobs use. Defaults to server.default_preset.
	Preset string `yaml:"preset"`
	// PollSeconds is how often Machinist polls GitHub Issues, in seconds. Defaults to 60.
	PollSeconds int `yaml:"poll_seconds"`
	// MaxJobsPerHour caps the jobs GitHub Issues can start in any hour. Defaults to 10.
	MaxJobsPerHour int `yaml:"max_jobs_per_hour"`
	// Token is read from GH_TOKEN (or GITHUB_TOKEN) in the environment when github.app is not set.
	Token string `yaml:"-"`
}

// Enabled reports whether Machinist takes work from GitHub Issues.
func (g GitHubIssues) Enabled() bool { return len(g.AllowedUsers) > 0 }

// EffectiveLabel returns the configured trigger label, or the default ("machinist").
func (g GitHubIssues) EffectiveLabel() string {
	if g.Label == "" {
		return defaultGitHubIssuesLabel
	}
	return g.Label
}

// PollInterval returns how often Machinist polls GitHub Issues.
func (g GitHubIssues) PollInterval() time.Duration {
	if g.PollSeconds <= 0 {
		return defaultGitHubIssuesPollSeconds * time.Second
	}
	return time.Duration(g.PollSeconds) * time.Second
}

// EffectiveMaxJobsPerHour returns the configured hourly job cap, or the default (10).
func (g GitHubIssues) EffectiveMaxJobsPerHour() int {
	if g.MaxJobsPerHour <= 0 {
		return defaultGitHubIssuesJobsPerHour
	}
	return g.MaxJobsPerHour
}

// CheckCredentials reports whether GitHub Issues, when enabled, has a GitHub App
// or environment token to authenticate against the GitHub REST API.
func (g GitHubIssues) CheckCredentials(hasApp bool) error {
	if !g.Enabled() || hasApp {
		return nil
	}
	if g.Token != "" {
		return nil
	}
	return fmt.Errorf("github_issues: configure github.app or set %s in the environment", GitHubTokenEnv)
}

// Environment variables for GitHub OAuth sign-in and one-time admin bootstrap.
const (
	GitHubOAuthClientIDEnv     = "GITHUB_OAUTH_CLIENT_ID"
	GitHubOAuthClientSecretEnv = "GITHUB_OAUTH_CLIENT_SECRET"
	BootstrapAdminEnv          = "MACHINIST_BOOTSTRAP_ADMIN"
)

// AuthGitHub configures GitHub OAuth for human browser sign-in.
type AuthGitHub struct {
	Enabled      bool   `yaml:"enabled"`
	ClientID     string `yaml:"-"`
	ClientSecret string `yaml:"-"`
}

// Auth configures human sign-in, public deployment guardrails, and initial admin bootstrap.
type Auth struct {
	// DisableTokenSignin disables quickstart token sign-in (POST /api/v1/session).
	// Defaults to true when auth.github.enabled is true, and false otherwise.
	DisableTokenSignin *bool `yaml:"disable_token_signin"`
	// AllowTokenOnlyPublic explicitly permits token-only auth on a non-loopback public_url.
	AllowTokenOnlyPublic bool `yaml:"allow_token_only_public"`
	// BootstrapAdmin names the one-time initial admin in "github:<login>" format.
	// MACHINIST_BOOTSTRAP_ADMIN overrides the YAML value when set.
	BootstrapAdmin string     `yaml:"bootstrap_admin"`
	GitHub         AuthGitHub `yaml:"github"`
}

// TokenSigninEnabled reports whether POST /api/v1/session accepts operator tokens.
func (a Auth) TokenSigninEnabled() bool {
	if a.DisableTokenSignin != nil {
		return !*a.DisableTokenSignin
	}
	return !a.GitHub.Enabled
}

// CheckCredentials verifies that required OAuth secrets are present in the
// environment when GitHub OAuth is enabled. Only the control plane calls this.
func (a Auth) CheckCredentials() error {
	if !a.GitHub.Enabled {
		return nil
	}
	if a.GitHub.ClientID == "" || a.GitHub.ClientSecret == "" {
		return fmt.Errorf("auth.github: set both %s and %s when auth.github.enabled is true", GitHubOAuthClientIDEnv, GitHubOAuthClientSecretEnv)
	}
	return nil
}

// Notify posts a JSON message to a webhook when a job needs a person: when its
// pull request waits for a person or the job fails.
type Notify struct {
	Webhook string `yaml:"webhook"`
}

type Config struct {
	Server       Server       `yaml:"server"`
	Auth         Auth         `yaml:"auth"`
	GitHub       GitHub       `yaml:"github"`
	GitHubIssues GitHubIssues `yaml:"github_issues"`
	Linear       Linear       `yaml:"linear"`
	Notify       Notify       `yaml:"notify"`
	path         string
}

func (c Config) Path() string { return c.path }

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
	machinistConfig.Server, err = applyServerDefaults(machinistConfig.Server, os.Getenv)
	if err != nil {
		return Config{}, err
	}
	machinistConfig.Auth, err = applyAuthDefaults(machinistConfig.Auth, os.Getenv)
	if err != nil {
		return Config{}, err
	}
	if err := applyGitHubDefaults(machinistConfig.GitHub, machinistConfig.Server.configDir); err != nil {
		return Config{}, err
	}
	machinistConfig.GitHubIssues, err = applyGitHubIssuesDefaults(machinistConfig.GitHubIssues, machinistConfig.GitHub, os.Getenv)
	if err != nil {
		return Config{}, err
	}
	machinistConfig.Linear, err = applyLinearDefaults(machinistConfig.Linear, os.Getenv)
	if err != nil {
		return Config{}, err
	}
	machinistConfig.Notify, err = applyNotifyDefaults(machinistConfig.Notify, os.Getenv)
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

func applyServerDefaults(server Server, getenv func(string) string) (Server, error) {
	server.PublicURL = strings.TrimSpace(server.PublicURL)
	if server.PublicURL != "" {
		parsed, err := url.Parse(server.PublicURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return Server{}, errors.New("public_url must be an http or https URL")
		}
		server.PublicURL = strings.TrimRight(server.PublicURL, "/")
	}
	server.Listen = strings.TrimSpace(server.Listen)
	if server.Listen == "" {
		var port string
		var cloudRun bool
		if getenv != nil {
			port = strings.TrimSpace(getenv("PORT"))
			cloudRun = strings.TrimSpace(getenv("K_SERVICE")) != ""
		}
		if port != "" && (cloudRun || server.PublicURL != "") {
			p, err := strconv.Atoi(port)
			if err != nil || p < 1 || p > 65535 {
				return Server{}, fmt.Errorf("PORT must be an integer between 1 and 65535 (got %q)", port)
			}
			server.Listen = ":" + port
		} else {
			server.Listen = defaultListen
		}
	}
	if server.TrustedProxyHops < 0 || server.TrustedProxyHops > MaxTrustedProxyHops {
		return Server{}, fmt.Errorf("server.trusted_proxy_hops must be between 0 and %d", MaxTrustedProxyHops)
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
	if getenv != nil {
		if envDB := strings.TrimSpace(getenv(DatabaseURLEnv)); envDB != "" {
			server.Database = envDB
		}
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
		if path == &server.Database && IsPostgresURL(*path) {
			continue
		}
		*path, err = resolveConfigPath(*path, server.configDir)
		if err != nil {
			return Server{}, err
		}
	}
	return server, nil
}

func applyAuthDefaults(auth Auth, getenv func(string) string) (Auth, error) {
	// Read OAuth credentials from env here, checked by Auth.CheckCredentials
	// when the control plane starts (so CLI commands work without secrets set).
	auth.GitHub.ClientID = strings.TrimSpace(getenv(GitHubOAuthClientIDEnv))
	auth.GitHub.ClientSecret = strings.TrimSpace(getenv(GitHubOAuthClientSecretEnv))

	if envBootstrap := strings.TrimSpace(getenv(BootstrapAdminEnv)); envBootstrap != "" {
		auth.BootstrapAdmin = envBootstrap
	} else {
		auth.BootstrapAdmin = strings.TrimSpace(auth.BootstrapAdmin)
	}
	if auth.BootstrapAdmin != "" {
		lower := strings.ToLower(auth.BootstrapAdmin)
		login, ok := strings.CutPrefix(lower, "github:")
		login = strings.TrimSpace(login)
		if !ok || login == "" || strings.ContainsAny(login, " \t\r\n/") {
			return Auth{}, errors.New(`auth.bootstrap_admin must be in "github:<login>" format`)
		}
		auth.BootstrapAdmin = "github:" + login
	}
	return auth, nil
}

func applyGitHubIssuesDefaults(ghIssues GitHubIssues, github GitHub, getenv func(string) string) (GitHubIssues, error) {
	configured := ghIssues.Label != "" ||
		ghIssues.InProgressLabel != "" ||
		ghIssues.AllowedUsers != nil ||
		ghIssues.Repositories != nil ||
		ghIssues.Preset != "" ||
		ghIssues.PollSeconds != 0 ||
		ghIssues.MaxJobsPerHour != 0
	if !configured {
		return GitHubIssues{}, nil
	}

	allowed := make([]string, 0, len(ghIssues.AllowedUsers))
	for _, user := range ghIssues.AllowedUsers {
		user = strings.TrimSpace(user)
		if user == "" {
			continue
		}
		if strings.ContainsAny(user, " \t\r\n/") {
			return GitHubIssues{}, fmt.Errorf("github_issues: invalid GitHub login %q in allowed_users", user)
		}
		allowed = append(allowed, user)
	}
	if len(allowed) == 0 {
		return GitHubIssues{}, errors.New("github_issues: list the GitHub logins allowed to label issues in allowed_users")
	}
	ghIssues.AllowedUsers = allowed

	ghIssues.Label = strings.TrimSpace(ghIssues.Label)
	if ghIssues.Label == "" {
		ghIssues.Label = defaultGitHubIssuesLabel
	}
	ghIssues.InProgressLabel = strings.TrimSpace(ghIssues.InProgressLabel)
	if ghIssues.InProgressLabel != "" && strings.EqualFold(ghIssues.InProgressLabel, ghIssues.Label) {
		return GitHubIssues{}, errors.New("github_issues: in_progress_label must differ from label")
	}
	ghIssues.Preset = strings.TrimSpace(ghIssues.Preset)

	if ghIssues.PollSeconds < 0 {
		return GitHubIssues{}, fmt.Errorf("github_issues: poll_seconds must be at least %d", minGitHubIssuesPollSeconds)
	}
	if ghIssues.PollSeconds == 0 {
		ghIssues.PollSeconds = defaultGitHubIssuesPollSeconds
	} else if ghIssues.PollSeconds < minGitHubIssuesPollSeconds {
		return GitHubIssues{}, fmt.Errorf("github_issues: poll_seconds must be at least %d", minGitHubIssuesPollSeconds)
	}

	if ghIssues.MaxJobsPerHour < 0 {
		return GitHubIssues{}, errors.New("github_issues: max_jobs_per_hour must be positive")
	}
	if ghIssues.MaxJobsPerHour == 0 {
		ghIssues.MaxJobsPerHour = defaultGitHubIssuesJobsPerHour
	}

	if len(ghIssues.Repositories) > 0 {
		repos := make([]string, 0, len(ghIssues.Repositories))
		for _, repo := range ghIssues.Repositories {
			repo = strings.TrimSpace(repo)
			if repo == "" || !githubRepoPattern.MatchString(repo) || strings.Contains(repo, "..") {
				return GitHubIssues{}, fmt.Errorf("github_issues.repositories: invalid repository %q", repo)
			}
			canonical := ""
			for _, listed := range github.Repositories {
				if strings.EqualFold(listed, repo) {
					canonical = listed
					break
				}
			}
			if canonical == "" {
				return GitHubIssues{}, fmt.Errorf("github_issues.repositories: %s must also be listed in github.repositories", repo)
			}
			dup := false
			for _, seen := range repos {
				if strings.EqualFold(seen, canonical) {
					dup = true
					break
				}
			}
			if !dup {
				repos = append(repos, canonical)
			}
		}
		ghIssues.Repositories = repos
	} else {
		if len(github.Repositories) == 0 {
			return GitHubIssues{}, errors.New("github_issues: set at least one repository in github.repositories")
		}
		ghIssues.Repositories = nil
	}

	if getenv != nil {
		ghIssues.Token = strings.TrimSpace(getenv(GitHubTokenEnv))
		if ghIssues.Token == "" {
			ghIssues.Token = strings.TrimSpace(getenv("GITHUB_TOKEN"))
		}
	}
	return ghIssues, nil
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

func applyNotifyDefaults(notify Notify, getenv func(string) string) (Notify, error) {
	if getenv != nil {
		if envWebhook := strings.TrimSpace(getenv(NotifyWebhookEnv)); envWebhook != "" {
			notify.Webhook = envWebhook
		}
	}
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
