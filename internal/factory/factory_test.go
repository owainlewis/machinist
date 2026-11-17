package factory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/runner"
)

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const defaultPreset = `timeout = "30m"

[[steps]]
name = "checkout"
run = "gh repo clone \"$MACHINIST_REPO\" ."
timeout = "5m"

[[steps]]
name = "test"
run = '''
go test ./...
'''
`

func validFactory() map[string]string {
	return map[string]string{
		"presets/default.toml":   defaultPreset,
		"automations/nightly.md": "+++\nschedule = \"0 6 * * 1\"\ntimezone = \"UTC\"\nrepo = \"owner/repo\"\npreset = \"default\"\n+++\nCheck the repo.\n",
	}
}

func TestLoadReadsPresetsAndAutomations(t *testing.T) {
	factory, err := Load(writeFiles(t, validFactory()))
	if err != nil {
		t.Fatal(err)
	}
	preset, ok := factory.Preset("default")
	if !ok || preset.Name != "default" || preset.Timeout != 30*time.Minute || len(preset.Steps) != 2 {
		t.Fatalf("preset = %+v", preset)
	}
	if preset.Steps[0] != (runner.Step{Name: "checkout", Run: `gh repo clone "$MACHINIST_REPO" .`, TimeoutMillis: (5 * time.Minute).Milliseconds()}) || preset.Steps[1] != (runner.Step{Name: "test", Run: "go test ./...\n"}) {
		t.Fatalf("steps = %+v", preset.Steps)
	}
	if _, ok := factory.Preset("missing"); ok {
		t.Fatal("found a preset that is not in the factory")
	}
	automation := factory.Automations[0]
	if automation.Name != "nightly" || automation.Repo != "owner/repo" || automation.Preset != "default" || automation.Prompt != "Check the repo." || automation.Cron == nil {
		t.Fatalf("automation = %+v", automation)
	}
	if err := factory.Validate("default", []string{"owner/repo"}); err != nil {
		t.Fatal(err)
	}
}

func TestPresetTimeoutIsOptional(t *testing.T) {
	factory, err := Load(writeFiles(t, map[string]string{"presets/quick.toml": "[[steps]]\nname = \"only\"\nrun = \"true\"\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if preset := factory.Presets["quick"]; preset.Timeout != 0 || preset.Steps[0].TimeoutMillis != 0 {
		t.Fatalf("preset = %+v", preset)
	}
}

func TestLoadAllowsNoAutomations(t *testing.T) {
	files := validFactory()
	delete(files, "automations/nightly.md")
	factory, err := Load(writeFiles(t, files))
	if err != nil || len(factory.Automations) != 0 {
		t.Fatalf("factory = %+v, err = %v", factory, err)
	}
}

func TestLoadRejectsInvalidFactories(t *testing.T) {
	step := func(body string) string { return "[[steps]]\n" + body + "\n" }
	for _, test := range []struct {
		name, want string
		change     func(map[string]string)
	}{
		{"no presets", "no presets", func(files map[string]string) { delete(files, "presets/default.toml") }},
		{"no steps", "at least one step", func(files map[string]string) { files["presets/default.toml"] = "timeout = \"5m\"\n" }},
		{"bad step name", "invalid name", func(files map[string]string) {
			files["presets/default.toml"] = step("name = \"Build It\"\nrun = \"true\"")
		}},
		{"step twice", "listed twice", func(files map[string]string) {
			files["presets/default.toml"] = step("name = \"build\"\nrun = \"true\"") + step("name = \"build\"\nrun = \"true\"")
		}},
		{"empty run", "run is empty", func(files map[string]string) { files["presets/default.toml"] = step("name = \"build\"\nrun = \" \"") }},
		{"bad timeout", "positive duration", func(files map[string]string) {
			files["presets/default.toml"] = "timeout = \"soon\"\n" + step("name = \"b\"\nrun = \"true\"")
		}},
		{"bad step timeout", "step b: timeout", func(files map[string]string) {
			files["presets/default.toml"] = step("name = \"b\"\nrun = \"true\"\ntimeout = \"-1m\"")
		}},
		{"unknown key", "unknown key", func(files map[string]string) {
			files["presets/default.toml"] = step("name = \"b\"\nrun = \"true\"\nneeds = \"a\"")
		}},
		{"no front matter", "front matter", func(files map[string]string) { files["automations/nightly.md"] = "Just a prompt\n" }},
		{"unclosed front matter", "not closed", func(files map[string]string) { files["automations/nightly.md"] = "+++\nrepo = \"x\"\n" }},
		{"bad cron", "", func(files map[string]string) {
			files["automations/nightly.md"] = "+++\nschedule = \"nope\"\ntimezone = \"UTC\"\nrepo = \"o/r\"\npreset = \"default\"\n+++\nGo.\n"
		}},
		{"no repo", "repo and preset", func(files map[string]string) {
			files["automations/nightly.md"] = "+++\nschedule = \"0 6 * * 1\"\ntimezone = \"UTC\"\npreset = \"default\"\n+++\nGo.\n"
		}},
		{"old agent key", "unknown key agent", func(files map[string]string) {
			files["automations/nightly.md"] = "+++\nschedule = \"0 6 * * 1\"\ntimezone = \"UTC\"\nrepo = \"o/r\"\nagent = \"default\"\n+++\nGo.\n"
		}},
		{"empty automation prompt", "prompt is empty", func(files map[string]string) {
			files["automations/nightly.md"] = "+++\nschedule = \"0 6 * * 1\"\ntimezone = \"UTC\"\nrepo = \"o/r\"\npreset = \"default\"\n+++\n\n"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := validFactory()
			test.change(files)
			if _, err := Load(writeFiles(t, files)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadRejectsSymlinkedPreset(t *testing.T) {
	root := writeFiles(t, validFactory())
	path := filepath.Join(root, "presets", "default.toml")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateChecksConfigurationReferences(t *testing.T) {
	factory, err := Load(writeFiles(t, validFactory()))
	if err != nil {
		t.Fatal(err)
	}
	if err := factory.Validate("missing", []string{"owner/repo"}); err == nil || !strings.Contains(err.Error(), "default_preset") {
		t.Fatalf("error = %v", err)
	}
	if err := factory.Validate("default", []string{"owner/other"}); err == nil || !strings.Contains(err.Error(), "github.repositories") {
		t.Fatalf("error = %v", err)
	}
	factory.Automations[0].Preset = "ghost"
	if err := factory.Validate("default", []string{"owner/repo"}); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error = %v", err)
	}
}
