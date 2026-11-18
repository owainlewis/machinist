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

// inTempDirectory runs the test from an empty directory, with an empty home.
func inTempDirectory(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	directory := t.TempDir()
	t.Chdir(directory)
	return directory
}

func TestInitInstallsDefaultConfiguration(t *testing.T) {
	directory := inTempDirectory(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &stdout, &stderr, "test")
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	installed := regularFiles(t, directory)
	for _, name := range []string{
		"config.yaml", "runner.yaml", ".machinist/runner.token", ".gitignore",
		"factory/presets/default.yaml", "factory/presets/coding.yaml", "factory/presets/maintainer.yaml", "factory/presets/hello.yaml", "factory/presets/spec.yaml",
		"factory/prompts/rules.md", "factory/scripts/agent.sh",
		"factory/automation-templates/review-open-prs.md",
	} {
		if !slices.Contains(installed, name) {
			t.Fatalf("%s is missing from %q", name, installed)
		}
	}
	assertStarterFactoryAndTokenWork(t, directory)
	want, err := machinistexamples.Files.ReadFile("config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "config.yaml")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("installed config.yaml does not match its default")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode for %s = %o, want 600", path, info.Mode().Perm())
	}
	state := filepath.Join(directory, ".machinist")
	if info, err := os.Stat(state); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("state directory = %v, err = %v, want mode 700", info, err)
	}
	if ignore, err := os.ReadFile(filepath.Join(directory, ".gitignore")); err != nil || string(ignore) != "/.machinist/\n" {
		t.Fatalf(".gitignore = %q, err = %v", ignore, err)
	}
	if _, err := os.Stat(filepath.Join(state, "tickets.db")); err != nil {
		t.Fatalf("database is not in the state directory: %v", err)
	}
	if _, err := config.LoadConfig(path); err != nil {
		t.Fatalf("load installed configuration: %v", err)
	}
	if !strings.Contains(stdout.String(), "created config.yaml") || !strings.Contains(stdout.String(), "Set github.handle and repositories in config.yaml") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestInitLeavesLegacyFactoryDirectoryUntouched(t *testing.T) {
	directory := inTempDirectory(t)
	home := os.Getenv("HOME")
	legacyDirectory := filepath.Join(home, ".factory")
	if err := os.Mkdir(legacyDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyDirectory, "config.yaml")
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
	if _, err := os.Stat(filepath.Join(directory, "config.yaml")); err != nil {
		t.Fatalf("Machinist configuration was not created: %v", err)
	}
}

func TestInitKeepsExistingFilesAndRestoresMissingDefaults(t *testing.T) {
	directory := inTempDirectory(t)
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("first init exit code = %d", exitCode)
	}
	path := filepath.Join(directory, "config.yaml")
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
	if !strings.Contains(stdout.String(), "kept config.yaml") {
		t.Fatalf("stdout = %q", stdout.String())
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &stdout, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("third init exit code = %d", exitCode)
	}
	if !strings.Contains(stdout.String(), "created config.yaml") {
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
	if len(loaded.Presets["default"].Steps) != 3 || len(loaded.Presets) != 5 || len(loaded.Automations) != 0 {
		t.Fatalf("factory = %+v", loaded)
	}
	machinistConfig, err := config.LoadConfig(filepath.Join(directory, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := tickets.Open(machinistConfig.Server.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token, err := os.ReadFile(filepath.Join(directory, ".machinist", "runner.token"))
	if err != nil {
		t.Fatal(err)
	}
	if name, err := store.TokenName(t.Context(), strings.TrimSpace(string(token))); err != nil || name != "local" {
		t.Fatalf("name = %q, err = %v", name, err)
	}
	info, err := os.Stat(filepath.Join(directory, ".machinist", "runner.token"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("info = %v, err = %v", info, err)
	}
	if _, err := config.LoadRunner(filepath.Join(directory, "runner.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestInitReplacesALostRunnerToken(t *testing.T) {
	directory := inTempDirectory(t)
	for range 2 {
		if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
			t.Fatalf("exit code = %d", exitCode)
		}
		if err := os.Remove(filepath.Join(directory, ".machinist", "runner.token")); err != nil {
			t.Fatal(err)
		}
	}
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("exit code = %d", exitCode)
	}
	assertStarterFactoryAndTokenWork(t, directory)
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
			target := filepath.Join(t.TempDir(), "config.yaml")
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
			directory := inTempDirectory(t)
			test.setup(t, filepath.Join(directory, "config.yaml"))

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &stdout, &stderr, "test")
			if exitCode != 2 || !strings.Contains(stderr.String(), "config.yaml already exists and is not a regular file") {
				t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String(), "configuration is ready") {
				t.Fatalf("init reported success for an unusable configuration: %q", stdout.String())
			}
		})
	}
}

func TestInitRejectsSymlinkedSetupDirectories(t *testing.T) {
	for _, name := range []string{".machinist"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(inTempDirectory(t), name)
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

func TestInitWritesToTheNamedDirectory(t *testing.T) {
	working := inTempDirectory(t)
	var stdout bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"init", "deploy/machinist"}, strings.NewReader(""), &stdout, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("exit code = %d, stdout = %q", exitCode, stdout.String())
	}
	directory := filepath.Join(working, "deploy", "machinist")
	assertStarterFactoryAndTokenWork(t, directory)
	if _, err := os.Stat(filepath.Join(working, "config.yaml")); err == nil {
		t.Fatal("init wrote to the current directory instead of the named one")
	}
}

func TestInitAddsTheStateDirectoryToAnExistingGitignoreOnce(t *testing.T) {
	directory := inTempDirectory(t)
	path := filepath.Join(directory, ".gitignore")
	if err := os.WriteFile(path, []byte("node_modules"), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
			t.Fatalf("exit code = %d", exitCode)
		}
	}
	if got, _ := os.ReadFile(path); string(got) != "node_modules\n/.machinist/\n" {
		t.Fatalf(".gitignore = %q", got)
	}
}

func TestCommandsReadConfigurationFromTheCurrentDirectory(t *testing.T) {
	inTempDirectory(t)
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("init exit code = %d", exitCode)
	}
	var stdout, stderr bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"token", "create", "someone"}, strings.NewReader(""), &stdout, &stderr, "test"); exitCode != 0 {
		t.Fatalf("token create exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestCommandsExplainAMissingConfiguration(t *testing.T) {
	inTempDirectory(t)
	old := filepath.Join(os.Getenv("HOME"), ".machinist")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "config.toml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"token", "create", "someone"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode == 0 || !strings.Contains(stderr.String(), "no config.yaml in") || !strings.Contains(stderr.String(), "machinist init") || !strings.Contains(stderr.String(), "Older versions kept it in "+old) {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
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
	path := filepath.Join(t.TempDir(), "runner.yaml")
	if err := os.WriteFile(path, []byte("executors:\n  codex:\n    command: [codex]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"kubernetes", "install", "--config=" + path}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 2 || !strings.Contains(stderr.String(), "unknown key executors") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}
