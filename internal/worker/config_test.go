package worker

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestLoadConfigDefaultsMaxConcurrentToTen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.toml")
	if err := os.WriteFile(path, []byte("name = \"pool\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxConcurrent != 10 {
		t.Fatalf("default max_concurrent = %d; want 10", config.MaxConcurrent)
	}
	if config.Runtime != protocol.RuntimeCodex || len(config.Runtimes) != 1 ||
		config.Runtimes[0] != protocol.RuntimeCodex {
		t.Fatalf("legacy runtime defaults = %q %#v", config.Runtime, config.Runtimes)
	}
}

func TestLoadConfigAcceptsSeveralCodingAgentRuntimes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.toml")
	body := "name = \"local\"\nruntime = \"codex\"\nruntimes = [\"pi\", \"codex\", \"claude-code\"]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{protocol.RuntimePi, protocol.RuntimeCodex, protocol.RuntimeClaudeCode}
	if config.Runtime != protocol.RuntimeCodex || !reflect.DeepEqual(config.Runtimes, want) {
		t.Fatalf("multi-runtime config = %q %#v, want codex %#v", config.Runtime, config.Runtimes, want)
	}
}

func TestLoadConfigDefaultsPrimaryToFirstConfiguredRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.toml")
	body := "name = \"local\"\nruntimes = [\"pi\", \"codex\", \"claude-code\"]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{protocol.RuntimePi, protocol.RuntimeCodex, protocol.RuntimeClaudeCode}
	if config.Runtime != protocol.RuntimePi || !reflect.DeepEqual(config.Runtimes, want) {
		t.Fatalf("upgraded multi-runtime config = %q %#v, want pi %#v", config.Runtime, config.Runtimes, want)
	}
}

func TestLoadConfigAcceptsRuntimesWithoutRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.toml")
	if err := os.WriteFile(path, []byte("name = \"w\"\nruntimes = [\"pi\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{protocol.RuntimePi}
	if config.Runtime != protocol.RuntimePi || !reflect.DeepEqual(config.Runtimes, want) {
		t.Fatalf("runtimes-only config = %q %#v, want pi %#v", config.Runtime, config.Runtimes, want)
	}
}

func TestLoadConfigDefaultsBothRuntimeFieldsToCodex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.toml")
	if err := os.WriteFile(path, []byte("name = \"w\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{protocol.RuntimeCodex}
	if config.Runtime != protocol.RuntimeCodex || !reflect.DeepEqual(config.Runtimes, want) {
		t.Fatalf("empty runtime config = %q %#v, want codex %#v", config.Runtime, config.Runtimes, want)
	}
}

func TestLoadConfigAcceptsRemoteWorkerSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote.toml")
	body := "server = \"https://factory.example.com:7443\"\nname = \"build-vm\"\n" +
		"enrollment_token = \"factory_enroll_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"\n" +
		"ca_certificate = \"tls/ca.crt\"\n[labels]\nregion = \"eu-west\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.CACertificate != filepath.Join(filepath.Dir(path), "tls", "ca.crt") ||
		config.Labels["region"] != "eu-west" {
		t.Fatalf("remote config = %#v", config)
	}
}

func TestLoadConfigRejectsInvalidServerPorts(t *testing.T) {
	for _, server := range []string{
		"http://127.0.0.1:0",
		"http://127.0.0.1:00",
		"http://127.0.0.1:01",
		"https://factory.example.com:65536",
	} {
		t.Run(server, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "worker.toml")
			body := "server = \"" + server + "\"\nname = \"pool\"\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(path); err == nil ||
				!strings.Contains(err.Error(), "port must be a canonical integer between 1 and 65535") {
				t.Fatalf("LoadConfig server %q error = %v", server, err)
			}
		})
	}
}

func TestLoadConfigAcceptsValidServerPorts(t *testing.T) {
	for _, server := range []string{
		"http://127.0.0.1:1",
		"http://[::1]:65535",
		"https://factory.example.com:7443",
	} {
		t.Run(server, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "worker.toml")
			body := "server = \"" + server + "\"\nname = \"pool\"\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(path); err != nil {
				t.Fatalf("LoadConfig server %q: %v", server, err)
			}
		})
	}
}

func TestWorkerCapacityUsesSharedRange(t *testing.T) {
	for _, capacity := range []int{protocol.MinWorkerCapacity, protocol.MaxWorkerCapacity} {
		config := Config{
			Server: "http://127.0.0.1:7337", Name: "pool", Runtime: protocol.RuntimeCodex,
			MaxConcurrent: capacity, DataDirectory: t.TempDir(),
		}
		if err := validateConfig(config); err != nil {
			t.Fatalf("max_concurrent %d rejected: %v", capacity, err)
		}
	}

	for _, capacity := range []int{protocol.MinWorkerCapacity - 1, protocol.MaxWorkerCapacity + 1} {
		config := Config{
			Server: "http://127.0.0.1:7337", Name: "pool", Runtime: protocol.RuntimeCodex,
			MaxConcurrent: capacity, DataDirectory: t.TempDir(),
		}
		err := validateConfig(config)
		if err == nil || !strings.Contains(err.Error(), "between 1 and 100") {
			t.Fatalf("max_concurrent %d error = %v", capacity, err)
		}
	}
}

func TestLoadConfigRejectsExplicitZeroCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.toml")
	if err := os.WriteFile(path, []byte("name = \"pool\"\nmax_concurrent = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "between 1 and 100") {
		t.Fatalf("explicit zero max_concurrent error = %v", err)
	}
}

func TestNewDefaultsPrimaryToFirstConfiguredRuntime(t *testing.T) {
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager, err := New(Config{
		Server: defaultServer, Name: "runtimes-only", Runtimes: []string{protocol.RuntimePi}, MaxConcurrent: 1,
		DataDirectory: filepath.Join(t.TempDir(), "worker"),
	}, testOptions(codexPath), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if manager.config.Runtime != protocol.RuntimePi {
		t.Fatalf("primary runtime = %q, want pi", manager.config.Runtime)
	}
}
