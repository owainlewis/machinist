package controlplane

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
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

func TestNewRejectsAStepThatCouldOutliveAnAppToken(t *testing.T) {
	preset := func(timeout time.Duration, steps ...time.Duration) *factory.Factory {
		loaded := factory.Preset{Name: "default", Timeout: timeout}
		for index, step := range steps {
			loaded.Steps = append(loaded.Steps, runner.Step{Name: fmt.Sprintf("step%d", index+1), Run: "true", TimeoutMillis: step.Milliseconds()})
		}
		return &factory.Factory{Presets: map[string]factory.Preset{"default": loaded}}
	}
	for _, test := range []struct {
		name       string
		runTimeout string
		loaded     *factory.Factory
		want       string
	}{
		{"a step with no timeout gets the whole run_timeout", "2h", testFactory(), `preset "default" step "work" may run 2h0m0s (run_timeout)`},
		{"a step with no timeout gets the whole preset timeout", "", preset(90*time.Minute, 0), `preset "default" step "step1" may run 1h30m0s (the preset's timeout)`},
		{"a step timeout longer than a token", "", preset(3*time.Hour, 30*time.Minute, 2*time.Hour), `preset "default" step "step2" may run 2h0m0s (its own timeout)`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(appConfig(t, test.runTimeout), nil, test.loaded, "v", io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "GitHub App tokens last 1h0m0s") {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

// A local runner takes a new token before each step, so a run may last longer
// than one token when no single step can.
func TestNewAllowsALongRunWhoseEveryStepFitsInAToken(t *testing.T) {
	long := &factory.Factory{Presets: map[string]factory.Preset{"default": {Name: "default", Timeout: 3 * time.Hour, Steps: []runner.Step{
		{Name: "work", Run: "true", TimeoutMillis: time.Hour.Milliseconds()},
		{Name: "review", Run: "true", TimeoutMillis: 45 * time.Minute.Milliseconds()},
	}}}}
	if _, err := New(appConfig(t, "4h"), nil, long, "v", io.Discard); err != nil {
		t.Fatal(err)
	}
	// A step timeout past the end of the run is cut to the run.
	short := &factory.Factory{Presets: map[string]factory.Preset{"default": {Name: "default", Timeout: 50 * time.Minute, Steps: []runner.Step{{Name: "work", Run: "true", TimeoutMillis: (2 * time.Hour).Milliseconds()}}}}}
	if _, err := New(appConfig(t, ""), nil, short, "v", io.Discard); err != nil {
		t.Fatal(err)
	}
}
