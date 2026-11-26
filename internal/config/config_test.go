package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigAppliesDefaults(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	writeTestFile(t, path, "")
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	server := loaded.Server
	if server.Listen != "127.0.0.1:7331" || server.DefaultPreset != "default" || server.RunDeadline() != time.Hour {
		t.Fatalf("server = %+v", server)
	}
	if server.Database != filepath.Join(directory, ".machinist", "tickets.db") || server.Factory != filepath.Join(directory, "factory") {
		t.Fatalf("paths = %q, %q", server.Database, server.Factory)
	}
	if loaded.Path() != path {
		t.Fatalf("path = %q", loaded.Path())
	}
	if !loaded.GitHub.AutoMerges() {
		t.Fatal("auto-merge is off by default")
	}
	if loaded.GitHub.StewardRetryLimit() != DefaultStewardRetries {
		t.Fatalf("steward_retries default = %d, want %d", loaded.GitHub.StewardRetryLimit(), DefaultStewardRetries)
	}
}

func TestLoadConfigReadsTheCurrentDirectoryByDefault(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	writeTestFile(t, filepath.Join(directory, "config.yaml"), "github:\n  handle: bot\n")
	loaded, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Path() != filepath.Join(directory, "config.yaml") || loaded.GitHub.Handle != "bot" {
		t.Fatalf("loaded %q with handle %q", loaded.Path(), loaded.GitHub.Handle)
	}
}

func TestLoadConfigReadsEveryKeyAndResolvesPaths(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	writeTestFile(t, path, `server:
  listen: 10.0.0.5:7331
  public_url: https://machinist.example.com/
  database: data/tickets.db
  factory: factory
  default_preset: maintainer
  run_timeout: 15m
  tls_cert: tls.crt
  tls_key: tls.key

github:
  handle: machinist-bot
  repositories: [owner/one, owner/two]
  auto_merge: false
  steward_retries: 3

notify:
  webhook: https://hooks.slack.com/services/T000/B000/secret
`)
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	server := loaded.Server
	if server.Listen != "10.0.0.5:7331" || server.PublicURL != "https://machinist.example.com" || server.DefaultPreset != "maintainer" || server.RunDeadline() != 15*time.Minute {
		t.Fatalf("server = %+v", server)
	}
	for got, want := range map[string]string{server.Database: "data/tickets.db", server.Factory: "factory", server.TLSCert: "tls.crt", server.TLSKey: "tls.key"} {
		if got != filepath.Join(directory, filepath.FromSlash(want)) {
			t.Fatalf("path = %q, want it under %q", got, directory)
		}
	}
	if loaded.GitHub.Handle != "machinist-bot" || strings.Join(loaded.GitHub.Repositories, ",") != "owner/one,owner/two" || loaded.GitHub.AutoMerges() || loaded.GitHub.StewardRetryLimit() != 3 {
		t.Fatalf("github = %+v", loaded.GitHub)
	}
	if loaded.Notify.Webhook != "https://hooks.slack.com/services/T000/B000/secret" {
		t.Fatalf("notify = %+v", loaded.Notify)
	}
}

func TestLoadConfigNamesOldKeys(t *testing.T) {
	for _, test := range []struct{ body, key string }{
		{"server:\n  state: factory\n", "state on line 2"},
		{"server:\n  worker_token_file: worker.token\n", "worker_token_file"},
		{"server:\n  max_concurrent_jobs: 2\n", "max_concurrent_jobs"},
		{"commands:\n  plan:\n    executor: codex\n", "commands on line 1"},
		{"workflows:\n  factory: {}\n", "workflows"},
		{"storage:\n  backend: local\n", "storage"},
		{"server:\n  max_turns_without_human: 5\n", "max_turns_without_human"},
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		writeTestFile(t, path, test.body)
		if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "unknown key "+test.key) {
			t.Fatalf("%q: error = %v", test.body, err)
		}
	}
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	for _, test := range []struct{ body, want string }{
		{"server:\n  run_timeout: soon\n", "run_timeout"},
		{"server:\n  run_timeout: -1m\n", "run_timeout"},
		{"server:\n  tls_cert: tls.crt\n", "tls_cert and tls_key"},
		{"server:\n  public_url: machinist.example.com\n", "public_url"},
		{"server:\n  public_url: ftp://machinist.example.com\n", "public_url"},
		{"notify:\n  webhook: hooks.slack.com/services/a/b/c\n", "notify.webhook"},
		{"notify:\n  webhook: ftp://hooks.example.com/hook\n", "notify.webhook"},
		{"notify:\n  email: ops@example.com\n", "unknown key email"},
		{"github:\n  steward_retries: -1\n", "github.steward_retries"},
		{"github:\n  steward_retries: 6\n", "github.steward_retries"},
		{"server: [", "parse config"},
		{"server:\n  run_timeout: [1]\n", "parse config"},
		{"github:\n  handle: one\n---\ngithub:\n  handle: two\n", "only one YAML document"},
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		writeTestFile(t, path, test.body)
		if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%q: error = %v", test.body, err)
		}
	}
}

func TestLoadConfigReadsTheGitHubApp(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	writeTestFile(t, path, "github:\n  handle: bot\n  app:\n    id: 123\n    private_key_file: keys/app.pem\n")
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if app := loaded.GitHub.App; app == nil || app.ID != 123 || app.PrivateKeyFile != filepath.Join(directory, "keys", "app.pem") {
		t.Fatalf("app = %+v", loaded.GitHub.App)
	}
	for _, test := range []struct{ body, want string }{
		{"github:\n  app:\n    private_key_file: app.pem\n", "github.app.id"},
		{"github:\n  app:\n    id: 1\n", "github.app.private_key_file"},
		{"github:\n  app:\n    id: 1\n    private_key: secret\n", "unknown key private_key"},
	} {
		writeTestFile(t, path, test.body)
		if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%q: error = %v", test.body, err)
		}
	}
}

func TestExampleConfigurationLoads(t *testing.T) {
	loaded, err := LoadConfig(filepath.Join("..", "..", "examples", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Server.DefaultPreset != "default" || loaded.GitHub.Handle == "" {
		t.Fatalf("example = %+v", loaded)
	}
}

func TestLoadConfigExplainsAnOldTOMLFile(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	writeTestFile(t, filepath.Join(directory, "config.toml"), "[github]\nhandle = \"bot\"\n")
	for _, path := range []string{"", filepath.Join(directory, "config.toml")} {
		if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "is TOML, and Machinist now reads YAML. Rewrite it as "+filepath.Join(directory, "config.yaml")) {
			t.Fatalf("%q: error = %v", path, err)
		}
	}
}

func TestLinearIsOffUntilTeamsAreSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, path, "github:\n  handle: machinist-bot\n")
	loaded, err := LoadConfig(path)
	if err != nil || loaded.Linear.Enabled() {
		t.Fatalf("linear = %+v, err = %v", loaded.Linear, err)
	}
	writeTestFile(t, path, "linear:\n  app_user_id: app-user\n")
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "set projects or teams") {
		t.Fatalf("error = %v", err)
	}
}

func TestLinearProjectsTurnLinearOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(LinearTokenEnv, "lin_oauth_token")
	writeTestFile(t, path, "linear:\n  app_user_id: app-user\n  allowed_users: [owain]\n  projects:\n    Machinist: owner/machinist\n    Airtime: owner/airtime\n")
	loaded, err := LoadConfig(path)
	if err != nil || !loaded.Linear.Enabled() || loaded.Linear.Projects["Airtime"] != "owner/airtime" || len(loaded.Linear.Teams) != 0 {
		t.Fatalf("linear = %+v, err = %v", loaded.Linear, err)
	}
	writeTestFile(t, path, "linear:\n  app_user_id: app-user\n  allowed_users: [owain]\n  projects:\n    Airtime: \"\"\n")
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "each project needs a name or ID and a repository") {
		t.Fatalf("error = %v", err)
	}
}

func TestLinearNeedsATokenAndATrigger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, path, "linear:\n  app_user_id: app-user\n  allowed_users: [owain]\n  teams:\n    ENG: owner/repo\n")
	t.Setenv(LinearTokenEnv, "")
	t.Setenv(LinearClientIDEnv, "")
	t.Setenv(LinearClientSecretEnv, "")
	// Only the control plane signs in to Linear, so loading the file does not
	// need the credentials; CheckCredentials asks for them.
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("no token: error = %v", err)
	}
	if err := loaded.Linear.CheckCredentials(); err == nil || !strings.Contains(err.Error(), LinearTokenEnv) || !strings.Contains(err.Error(), LinearClientIDEnv) {
		t.Fatalf("no token: error = %v", err)
	}
	t.Setenv(LinearTokenEnv, " lin_oauth_token ")
	loaded, err = LoadConfig(path)
	if err == nil {
		err = loaded.Linear.CheckCredentials()
	}
	if err != nil || loaded.Linear.Token != "lin_oauth_token" || loaded.Linear.MaxJobsPerHour != 20 || loaded.Linear.Poll != time.Minute {
		t.Fatalf("linear = %+v, err = %v", loaded.Linear, err)
	}
	writeTestFile(t, path, "linear:\n  app_user_id: app-user\n  allowed_users: [owain]\n  poll_interval: 2m\n  teams:\n    ENG: owner/repo\n")
	if loaded, err := LoadConfig(path); err != nil || loaded.Linear.Poll != 2*time.Minute {
		t.Fatalf("poll = %v, err = %v", loaded.Linear.Poll, err)
	}
	for _, interval := range []string{"1s", "soon"} {
		writeTestFile(t, path, "linear:\n  app_user_id: app-user\n  allowed_users: [owain]\n  poll_interval: "+interval+"\n  teams:\n    ENG: owner/repo\n")
		if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "poll_interval") {
			t.Fatalf("poll_interval %s: error = %v", interval, err)
		}
	}
	writeTestFile(t, path, "linear:\n  app_user_id: app-user\n  teams:\n    ENG: owner/repo\n")
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "allowed_users") {
		t.Fatalf("no allowed users: error = %v", err)
	}
	writeTestFile(t, path, "linear:\n  allowed_users: [owain]\n  teams:\n    ENG: owner/repo\n")
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "app_user_id") {
		t.Fatalf("no trigger: error = %v", err)
	}
	writeTestFile(t, path, "linear:\n  token: in-the-file\n  teams:\n    ENG: owner/repo\n")
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "unknown key token") {
		t.Fatalf("token in the file: error = %v", err)
	}
}

func TestLinearClientCredentialsComeFromTheEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, path, "linear:\n  app_user_id: app-user\n  allowed_users: [owain]\n  teams:\n    ENG: owner/repo\n")
	t.Setenv(LinearTokenEnv, "")
	t.Setenv(LinearClientIDEnv, " client-id ")
	t.Setenv(LinearClientSecretEnv, "")
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Linear.CheckCredentials(); err == nil || !strings.Contains(err.Error(), "set both") {
		t.Fatalf("no secret: error = %v", err)
	}
	// A secret without an ID is as incomplete, even with a fixed token set.
	t.Setenv(LinearClientIDEnv, "")
	t.Setenv(LinearClientSecretEnv, "client-secret")
	t.Setenv(LinearTokenEnv, "lin_oauth_token")
	if loaded, err = LoadConfig(path); err == nil {
		err = loaded.Linear.CheckCredentials()
	}
	if err == nil || !strings.Contains(err.Error(), "set both") {
		t.Fatalf("no ID: error = %v", err)
	}
	t.Setenv(LinearTokenEnv, "")
	t.Setenv(LinearClientIDEnv, " client-id ")
	t.Setenv(LinearClientSecretEnv, " client-secret ")
	loaded, err = LoadConfig(path)
	if err == nil {
		err = loaded.Linear.CheckCredentials()
	}
	if err != nil || loaded.Linear.ClientID != "client-id" || loaded.Linear.ClientSecret != "client-secret" || !loaded.Linear.UsesClientCredentials() {
		t.Fatalf("linear = %+v, err = %v", loaded.Linear, err)
	}
	// The client credentials renew themselves, so they win over a fixed token.
	t.Setenv(LinearTokenEnv, "lin_oauth_token")
	loaded, err = LoadConfig(path)
	if err != nil || !loaded.Linear.UsesClientCredentials() || loaded.Linear.Token != "" {
		t.Fatalf("both set: linear = %+v, err = %v", loaded.Linear, err)
	}
	writeTestFile(t, path, "linear:\n  client_secret: in-the-file\n  teams:\n    ENG: owner/repo\n")
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "unknown key client_secret") {
		t.Fatalf("secret in the file: error = %v", err)
	}
}

func TestLoadConfigStewardRetriesBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"disabled", "github:\n  steward_retries: 0\n", 0},
		{"default omitted", "github:\n  handle: bot\n", DefaultStewardRetries},
		{"max allowed", "github:\n  steward_retries: 5\n", MaxStewardRetries},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			writeTestFile(t, path, tc.body)
			loaded, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig failed: %v", err)
			}
			if got := loaded.GitHub.StewardRetryLimit(); got != tc.want {
				t.Fatalf("StewardRetryLimit() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestPostgresURLAndRedaction(t *testing.T) {
	for _, dsn := range []string{
		"postgres://user:secret@db.example.com:5432/machinist?sslmode=require",
		"POSTGRESQL://user:secret@db.example.com/machinist",
		"postgres://user:secret@/machinist?host=/cloudsql/proj:us-central1:inst",
	} {
		if !IsPostgresURL(dsn) {
			t.Fatalf("IsPostgresURL(%q) = false, want true", dsn)
		}
		redacted := RedactDatabaseURL(dsn)
		if strings.Contains(redacted, "secret") {
			t.Fatalf("RedactDatabaseURL(%q) leaked password: %q", dsn, redacted)
		}
	}
	if IsPostgresURL(".machinist/tickets.db") || IsPostgresURL("/var/lib/machinist/tickets.db") {
		t.Fatal("IsPostgresURL matched sqlite file path")
	}
	if got := RedactDatabaseURL(".machinist/tickets.db"); got != ".machinist/tickets.db" {
		t.Fatalf("RedactDatabaseURL sqlite path = %q", got)
	}
	// Malformed URL with unescaped special characters in password must still redact cleanly.
	malformed := "postgres://user:p@ss%ZZ#word@db.example.com:5432/machinist?password=leak"
	redactedMalformed := RedactDatabaseURL(malformed)
	if strings.Contains(redactedMalformed, "p@ss") || strings.Contains(redactedMalformed, "word") || strings.Contains(redactedMalformed, "leak") {
		t.Fatalf("RedactDatabaseURL malformed leaked secret: %q", redactedMalformed)
	}
	// Query-param password redaction, including malformed query encodings.
	queryPass := "postgres://user@db.example.com/machinist?password=topsecret&sslmode=require"
	if got := RedactDatabaseURL(queryPass); strings.Contains(got, "topsecret") {
		t.Fatalf("RedactDatabaseURL query password leaked: %q", got)
	}
	malformedQuery := "postgres://user@db.example.com/machinist?password=secret%ZZ"
	if got := RedactDatabaseURL(malformedQuery); strings.Contains(got, "secret") {
		t.Fatalf("RedactDatabaseURL malformed query leaked secret: %q", got)
	}
}

func TestDatabaseURLEnvOverridesConfigAndSkipsPathResolution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeTestFile(t, path, "server:\n  database: .machinist/tickets.db\ngithub:\n  handle: bot\n")

	wantDSN := "postgres://machinist:pass@/machinist?host=/cloudsql/proj:region:inst"
	t.Setenv(DatabaseURLEnv, wantDSN)

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if loaded.Server.Database != wantDSN {
		t.Fatalf("Server.Database = %q, want %q", loaded.Server.Database, wantDSN)
	}
}

func TestPortFallbackSafetyAndNotifyWebhookEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// 1. Local dev (no K_SERVICE, no public_url): PORT is ignored to avoid accidental 0.0.0.0 exposure.
	writeTestFile(t, path, "github:\n  handle: bot\n")
	t.Setenv("PORT", "8080")
	t.Setenv("K_SERVICE", "")
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("local PORT LoadConfig: %v", err)
	}
	if loaded.Server.Listen != defaultListen {
		t.Fatalf("local listen = %q, want %q", loaded.Server.Listen, defaultListen)
	}

	// 2. Cloud Run (K_SERVICE set, listen omitted): PORT binds :8080.
	t.Setenv("K_SERVICE", "machinist")
	loaded, err = LoadConfig(path)
	if err != nil {
		t.Fatalf("Cloud Run PORT LoadConfig: %v", err)
	}
	if loaded.Server.Listen != ":8080" {
		t.Fatalf("Cloud Run listen = %q, want :8080", loaded.Server.Listen)
	}

	// 3. Explicit server.listen always wins over PORT.
	writeTestFile(t, path, "server:\n  listen: 127.0.0.1:9000\ngithub:\n  handle: bot\n")
	loaded, err = LoadConfig(path)
	if err != nil {
		t.Fatalf("explicit listen LoadConfig: %v", err)
	}
	if loaded.Server.Listen != "127.0.0.1:9000" {
		t.Fatalf("explicit listen = %q, want 127.0.0.1:9000", loaded.Server.Listen)
	}

	// 4. public_url set with omitted listen honors PORT.
	t.Setenv("K_SERVICE", "")
	writeTestFile(t, path, "server:\n  public_url: https://machinist.example.com\ngithub:\n  handle: bot\n")
	loaded, err = LoadConfig(path)
	if err != nil {
		t.Fatalf("public_url PORT LoadConfig: %v", err)
	}
	if loaded.Server.Listen != ":8080" {
		t.Fatalf("public_url listen = %q, want :8080", loaded.Server.Listen)
	}

	// 5. Invalid PORT fails closed when gated on.
	for _, bad := range []string{"0", "70000", "abc"} {
		t.Setenv("PORT", bad)
		if _, err := LoadConfig(path); err == nil {
			t.Fatalf("expected error for invalid PORT=%q", bad)
		}
	}
	t.Setenv("PORT", "8080")

	// 6. NOTIFY_WEBHOOK_URL overrides notify.webhook and validates URL.
	t.Setenv(NotifyWebhookEnv, "https://hooks.example.com/secret-token")
	loaded, err = LoadConfig(path)
	if err != nil {
		t.Fatalf("NOTIFY_WEBHOOK_URL LoadConfig: %v", err)
	}
	if loaded.Notify.Webhook != "https://hooks.example.com/secret-token" {
		t.Fatalf("Notify.Webhook = %q", loaded.Notify.Webhook)
	}
	t.Setenv(NotifyWebhookEnv, "ftp://bad.example.com/hook")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error for invalid NOTIFY_WEBHOOK_URL scheme")
	}
}
