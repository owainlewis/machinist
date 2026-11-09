package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	machinistexamples "github.com/owainlewis/machinist/examples"
	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/tickets"
)

func TestInitInstallsDefaultConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &stdout, &stderr, "test")
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	directory := filepath.Join(home, ".machinist")
	installed := regularFiles(t, directory)
	for _, name := range []string{
		"config.toml", "runner.toml", "runner.token",
		"factory/agents/default/agent.toml", "factory/agents/default/playbook.md",
		"factory/agents/default/.claude/agents/machinist-reviewer.md",
		"factory/agents/default/.codex/agents/machinist-reviewer.toml",
		"factory/agents/maintainer/playbook.md", "factory/reviewers/review.md",
		"factory/automation-templates/review-open-prs.md",
	} {
		if !slices.Contains(installed, name) {
			t.Fatalf("%s is missing from %q", name, installed)
		}
	}
	assertStarterFactoryAndTokenWork(t, directory)
	want, err := machinistexamples.Files.ReadFile("config.toml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "config.toml")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("installed config.toml does not match its default")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode for %s = %o, want 600", path, info.Mode().Perm())
	}
	for _, path := range []string{directory, filepath.Join(directory, "server")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("mode for %s = %o, want 700", path, info.Mode().Perm())
		}
	}
	if _, err := config.LoadConfig(path); err != nil {
		t.Fatalf("load installed configuration: %v", err)
	}
	if !strings.Contains(stdout.String(), "created config.toml") || !strings.Contains(stdout.String(), "Set github.handle and repositories in config.toml") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestInitLeavesLegacyFactoryDirectoryUntouched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacyDirectory := filepath.Join(home, ".factory")
	if err := os.Mkdir(legacyDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyDirectory, "config.toml")
	legacyBody := []byte("incompatible legacy configuration\n")
	if err := os.WriteFile(legacyPath, legacyBody, 0o600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	got, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy configuration: %v", err)
	}
	if !bytes.Equal(got, legacyBody) {
		t.Fatalf("legacy configuration changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".machinist", "config.toml")); err != nil {
		t.Fatalf("Machinist configuration was not created: %v", err)
	}
}

func TestInitKeepsExistingFilesAndRestoresMissingDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("first init exit code = %d", exitCode)
	}
	path := filepath.Join(home, ".machinist", "config.toml")
	if err := os.WriteFile(path, []byte("custom config\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &stdout, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("second init exit code = %d", exitCode)
	}
	if got, _ := os.ReadFile(path); string(got) != "custom config\n" {
		t.Fatalf("init changed the existing file: %q", got)
	}
	if !strings.Contains(stdout.String(), "kept config.toml") {
		t.Fatalf("stdout = %q", stdout.String())
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &stdout, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("third init exit code = %d", exitCode)
	}
	if !strings.Contains(stdout.String(), "created config.toml") {
		t.Fatalf("missing default not restored: %q", stdout.String())
	}
}

func assertStarterFactoryAndTokenWork(t *testing.T, directory string) {
	t.Helper()
	loaded, err := factory.Load(filepath.Join(directory, "factory"))
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Validate("default", nil); err != nil {
		t.Fatal(err)
	}
	if len(loaded.Agents["default"].Files) != 6 || len(loaded.Reviewers) != 1 || len(loaded.Automations) != 0 {
		t.Fatalf("factory = %+v", loaded)
	}
	machinistConfig, err := config.LoadConfig(filepath.Join(directory, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := tickets.Open(machinistConfig.Server.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token, err := os.ReadFile(filepath.Join(directory, "runner.token"))
	if err != nil {
		t.Fatal(err)
	}
	if name, err := store.TokenName(t.Context(), strings.TrimSpace(string(token))); err != nil || name != "local" {
		t.Fatalf("name = %q, err = %v", name, err)
	}
	info, err := os.Stat(filepath.Join(directory, "runner.token"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("info = %v, err = %v", info, err)
	}
	if _, err := config.LoadRunner(filepath.Join(directory, "runner.toml")); err != nil {
		t.Fatal(err)
	}
}

func TestInitReplacesALostRunnerToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for range 2 {
		if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
			t.Fatalf("exit code = %d", exitCode)
		}
		if err := os.Remove(filepath.Join(home, ".machinist", "runner.token")); err != nil {
			t.Fatal(err)
		}
	}
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("exit code = %d", exitCode)
	}
	assertStarterFactoryAndTokenWork(t, filepath.Join(home, ".machinist"))
}

func regularFiles(t *testing.T, root string) []string {
	t.Helper()
	files := []string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(relative))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func TestInitRejectsExistingNonFile(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "directory", setup: func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", setup: func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(target, []byte("custom\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "broken symlink", setup: func(t *testing.T, path string) {
			if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			directory := filepath.Join(home, ".machinist")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			test.setup(t, filepath.Join(directory, "config.toml"))

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &stdout, &stderr, "test")
			if exitCode != 2 || !strings.Contains(stderr.String(), "config.toml already exists and is not a regular file") {
				t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String(), "configuration is ready") {
				t.Fatalf("init reported success for an unusable configuration: %q", stdout.String())
			}
		})
	}
}

func TestInitRejectsSymlinkedSetupDirectories(t *testing.T) {
	for _, name := range []string{".machinist", ".machinist/server"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			path := filepath.Join(home, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			external := t.TempDir()
			if err := os.Symlink(external, path); err != nil {
				t.Fatal(err)
			}

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &stdout, &stderr, "test")
			if exitCode != 2 || !strings.Contains(stderr.String(), "already exists and is not a directory") {
				t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
			}
			entries, err := os.ReadDir(external)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("init wrote outside its setup directory: %#v", entries)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	var stdout bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"version"}, strings.NewReader(""), &stdout, &bytes.Buffer{}, "v1.2.3"); exitCode != 0 {
		t.Fatalf("exit code = %d", exitCode)
	}
	if stdout.String() != "v1.2.3\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRemovedCommandsAreUnknown(t *testing.T) {
	for _, name := range []string{"run", "submit", "worker"} {
		var stderr bytes.Buffer
		if exitCode := Execute(t.Context(), []string{name}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode != 2 || !strings.Contains(stderr.String(), "unknown command") {
			t.Fatalf("%s: exit code = %d, stderr = %q", name, exitCode, stderr.String())
		}
	}
}

func TestKubernetesCommandReadsRunnerConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.toml")
	if err := os.WriteFile(path, []byte("[executors.codex]\ncommand = [\"codex\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"kubernetes", "install", "--config=" + path}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 2 || !strings.Contains(stderr.String(), "unknown key executors") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}
