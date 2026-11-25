package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/fakegh"
	"github.com/owainlewis/machinist/internal/tickets"
)

type listenNotifier struct {
	mu      sync.Mutex
	body    bytes.Buffer
	once    sync.Once
	ready   chan string
	pattern *regexp.Regexp
}

func newListenNotifier() *listenNotifier {
	return &listenNotifier{
		ready:   make(chan string, 1),
		pattern: regexp.MustCompile(`control plane listening on (\S+)`),
	}
}

func (n *listenNotifier) Write(p []byte) (int, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.body.Write(p)
	if match := n.pattern.FindStringSubmatch(n.body.String()); len(match) == 2 {
		n.once.Do(func() { n.ready <- match[1] })
	}
	return len(p), nil
}

func TestReloadCLIGoodBadAndRestartRequired(t *testing.T) {
	fakegh.Install(t, "machinist-bot")
	directory := inTempDirectory(t)

	writeConfigFile(t, directory, "factory/agents/implement.md", "---\nharness: claude\n---\nImplement v1.\n")
	writeConfigFile(t, directory, "factory/presets/default.yaml", "description: Default.\nsteps:\n  - name: work\n    agent: implement\n")
	writeConfigFile(t, directory, "config.yaml", `server:
  listen: 127.0.0.1:0
  database: .machinist/tickets.db
  factory: factory
  default_preset: default

github:
  handle: machinist-bot
  repositories:
    - owner/one
`)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	notifier := newListenNotifier()
	startDone := make(chan int, 1)
	go func() {
		startDone <- Execute(ctx, []string{"start"}, strings.NewReader(""), &bytes.Buffer{}, notifier, "test")
	}()

	var boundAddr string
	select {
	case boundAddr = <-notifier.ready:
	case code := <-startDone:
		t.Fatalf("start exited early with code %d: %s", code, notifier.body.String())
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for control plane to listen: %s", notifier.body.String())
	}

	// 1. Good reload with config and factory edits (no --url, no --token).
	writeConfigFile(t, directory, "config.yaml", `server:
  listen: 127.0.0.1:0
  database: .machinist/tickets.db
  factory: factory
  default_preset: default

github:
  handle: machinist-bot
  repositories:
    - owner/one
    - owner/two
`)
	writeConfigFile(t, directory, "factory/agents/implement.md", "---\nharness: codex\n---\nImplement v2.\n")
	writeConfigFile(t, directory, "factory/presets/quick.yaml", "description: Quick.\nsteps:\n  - name: work\n    run: \"true\"\n")

	var stdout, stderr bytes.Buffer
	if code := Execute(ctx, []string{"reload"}, strings.NewReader(""), &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("good reload failed: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"Reloaded:",
		"github.repositories: added owner/two",
		"agent updated: implement",
		"preset added: quick",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("reload stdout missing %q:\n%s", want, stdout.String())
		}
	}

	// Ephemeral reload token was cleaned up and does not appear in machinist token list.
	stdout.Reset()
	stderr.Reset()
	if code := Execute(ctx, []string{"token", "list"}, strings.NewReader(""), &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("token list failed: code=%d stderr=%q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "reload-") {
		t.Fatalf("ephemeral reload token leaked into token list:\n%s", stdout.String())
	}

	// 2. No-op reload prints "No changes."
	stdout.Reset()
	stderr.Reset()
	if code := Execute(ctx, []string{"reload"}, strings.NewReader(""), &stdout, &stderr, "test"); code != 0 || !strings.HasPrefix(stdout.String(), "No changes.\n") {
		t.Fatalf("no-op reload: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	// 3. Bad factory returns an error and leaves the control plane running with its previous state.
	writeConfigFile(t, directory, "factory/presets/quick.yaml", "description: Broken.\nsteps:\n  - name: bad\n    agent: missing-agent\n")
	stdout.Reset()
	stderr.Reset()
	if code := Execute(ctx, []string{"reload"}, strings.NewReader(""), &stdout, &stderr, "test"); code == 0 || !strings.Contains(stderr.String(), "missing-agent") {
		t.Fatalf("bad factory reload: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	// Restore valid quick.yaml.
	writeConfigFile(t, directory, "factory/presets/quick.yaml", "description: Quick.\nsteps:\n  - name: work\n    run: \"true\"\n")

	// 4. Settings that need a restart are named and not applied, even though server.listen and server.database changed in config.yaml.
	writeConfigFile(t, directory, "config.yaml", `server:
  listen: 127.0.0.1:9999
  database: .machinist/other.db
  factory: factory
  default_preset: default

github:
  handle: updated-bot
  repositories:
    - owner/one
    - owner/two
`)
	stdout.Reset()
	stderr.Reset()
	if code := Execute(ctx, []string{"reload"}, strings.NewReader(""), &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("restart-needed reload failed: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"Reloaded:",
		"github.handle: machinist-bot -> updated-bot",
		"Restart required to apply:",
		"server.listen",
		"server.database",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("restart-needed stdout missing %q:\n%s", want, stdout.String())
		}
	}

	// 5. Explicit --url and --token also work without touching local config.
	store, err := tickets.Open(filepath.Join(directory, ".machinist", "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	opToken, err := store.CreateToken(ctx, "ops", tickets.RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	stdout.Reset()
	stderr.Reset()
	if code := Execute(ctx, []string{"reload", "--url=http://" + boundAddr, "--token=" + opToken}, strings.NewReader(""), &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("remote reload failed: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "No reloadable changes applied.") || !strings.Contains(stdout.String(), "server.listen") {
		t.Fatalf("remote reload stdout = %q", stdout.String())
	}

	// Stop the control plane and confirm runtime state file is removed.
	cancel()
	if code := <-startDone; code != 0 {
		t.Fatalf("start exit code = %d, stderr = %s", code, notifier.body.String())
	}
	if _, err := os.Stat(filepath.Join(directory, ".machinist", controlPlaneStateFile)); !os.IsNotExist(err) {
		t.Fatalf("control-plane state file was not cleaned up: err = %v", err)
	}

	// Reload against stopped control plane returns a clear connection error.
	stdout.Reset()
	stderr.Reset()
	if code := Execute(t.Context(), []string{"reload", "--url=http://" + boundAddr, "--token=" + opToken}, strings.NewReader(""), &stdout, &stderr, "test"); code == 0 || !strings.Contains(stderr.String(), "could not reach control plane") {
		t.Fatalf("stopped reload: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
