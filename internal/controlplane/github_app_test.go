package controlplane

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/runner"
)

func writeAppKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func appConfig(t *testing.T, timeout string) config.Config {
	t.Helper()
	directory := t.TempDir()
	body := "github:\n  handle: machinist-bot\n  app:\n    id: 7\n    private_key_file: " + writeAppKey(t) + "\n"
	if timeout != "" {
		body += "server:\n  run_timeout: " + timeout + "\n"
	}
	path := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestNewLoadsTheGitHubApp(t *testing.T) {
	server, err := New(appConfig(t, ""), nil, testFactory(), "v", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if server.engine.GitHubToken == nil {
		t.Fatal("runs get no token from the app")
	}
}

func TestNewRejectsAGitHubAppItCannotUse(t *testing.T) {
	loaded := appConfig(t, "")
	if err := os.WriteFile(loaded.GitHub.App.PrivateKeyFile, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(loaded, nil, testFactory(), "v", io.Discard); err == nil || !strings.Contains(err.Error(), "private key") {
		t.Fatalf("error = %v", err)
	}
}

func TestNewRejectsRunsLongerThanAnAppToken(t *testing.T) {
	if _, err := New(appConfig(t, "2h"), nil, testFactory(), "v", io.Discard); err == nil || !strings.Contains(err.Error(), "run_timeout") {
		t.Fatalf("error = %v", err)
	}
	long := &factory.Factory{Presets: map[string]factory.Preset{"default": {Name: "default", Timeout: 90 * time.Minute, Steps: []runner.Step{{Name: "work", Run: "true"}}}}}
	if _, err := New(appConfig(t, ""), nil, long, "v", io.Discard); err == nil || !strings.Contains(err.Error(), `preset "default"`) {
		t.Fatalf("error = %v", err)
	}
}
