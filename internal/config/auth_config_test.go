package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuthAndProxyConfigDefaultsAndValidation(t *testing.T) {
	t.Setenv(GitHubOAuthClientIDEnv, "")
	t.Setenv(GitHubOAuthClientSecretEnv, "")
	t.Setenv(BootstrapAdminEnv, "")

	// Quickstart config: token sign-in enabled by default, trusted_proxy_hops=0.
	path := writeTempConfig(t, `
server:
  listen: 127.0.0.1:7331
github:
  handle: machinist-bot
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Auth.TokenSigninEnabled() || cfg.Auth.GitHub.Enabled || cfg.Server.TrustedProxyHops != 0 {
		t.Fatalf("quickstart cfg = %+v", cfg)
	}

	// GitHub OAuth enabled: LoadConfig succeeds without env vars (so CLI works),
	// TokenSigninEnabled defaults to false, and CheckCredentials catches missing env.
	path = writeTempConfig(t, `
server:
  listen: 0.0.0.0:8080
  public_url: https://machinist.example.com
  trusted_proxy_hops: 1
auth:
  bootstrap_admin: GitHub:OwainLewis
  github:
    enabled: true
github:
  handle: machinist-bot
`)
	cfg, err = LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig failed without OAuth env: %v", err)
	}
	if cfg.Auth.TokenSigninEnabled() {
		t.Fatal("TokenSigninEnabled should default to false when auth.github.enabled is true")
	}
	if cfg.Auth.BootstrapAdmin != "github:owainlewis" || cfg.Server.TrustedProxyHops != 1 {
		t.Fatalf("cfg = %+v", cfg)
	}
	if err := cfg.Auth.CheckCredentials(); err == nil || !strings.Contains(err.Error(), GitHubOAuthClientIDEnv) {
		t.Fatalf("CheckCredentials without env = %v", err)
	}

	// Env vars populate credentials and override bootstrap_admin.
	t.Setenv(GitHubOAuthClientIDEnv, "cid")
	t.Setenv(GitHubOAuthClientSecretEnv, "csec")
	t.Setenv(BootstrapAdminEnv, "github:AdminTwo")
	cfg, err = LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Auth.CheckCredentials(); err != nil {
		t.Fatalf("CheckCredentials with env = %v", err)
	}
	if cfg.Auth.BootstrapAdmin != "github:admintwo" || cfg.Auth.GitHub.ClientID != "cid" || cfg.Auth.GitHub.ClientSecret != "csec" {
		t.Fatalf("cfg.Auth = %+v", cfg.Auth)
	}

	// Explicit disable_token_signin: false overrides the GitHub OAuth default.
	path = writeTempConfig(t, `
auth:
  disable_token_signin: false
  github:
    enabled: true
`)
	cfg, err = LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Auth.TokenSigninEnabled() {
		t.Fatal("explicit disable_token_signin: false should enable token sign-in")
	}

	// Invalid trusted_proxy_hops and invalid bootstrap_admin are rejected.
	for _, bad := range []struct {
		name string
		yaml string
		want string
	}{
		{"negative hops", "server:\n  trusted_proxy_hops: -1\n", "trusted_proxy_hops"},
		{"excessive hops", "server:\n  trusted_proxy_hops: 11\n", "trusted_proxy_hops"},
		{"bad bootstrap format", "auth:\n  bootstrap_admin: owainlewis\n", "bootstrap_admin"},
		{"empty bootstrap login", "auth:\n  bootstrap_admin: \"github:\"\n", "bootstrap_admin"},
	} {
		t.Run(bad.name, func(t *testing.T) {
			t.Setenv(BootstrapAdminEnv, "")
			p := writeTempConfig(t, bad.yaml)
			if _, err := LoadConfig(p); err == nil || !strings.Contains(err.Error(), bad.want) {
				t.Fatalf("LoadConfig = %v, want error containing %q", err, bad.want)
			}
		})
	}
}
