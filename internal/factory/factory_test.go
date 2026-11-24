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

const defaultPreset = `description: >
  Clone the repository
  and run the tests.
timeout: 30m

steps:
  - name: checkout
    run: gh repo clone "$MACHINIST_REPO" .
    timeout: 5m

  - name: test
    idle_timeout: 20m
    run: |
      go test ./...
`

const nightly = "---\nschedule: \"0 6 * * 1\"\ntimezone: UTC\nrepo: owner/repo\npreset: default\n---\nCheck the repo.\n"

func validFactory() map[string]string {
	return map[string]string{
		"presets/default.yaml":   defaultPreset,
		"automations/nightly.md": nightly,
		"prompts/plan.md":        "Write a plan.\n",
		"scripts/agent.sh":       "#!/bin/sh\nclaude\n",
	}
}

func TestLoadReadsPresetsAndAutomations(t *testing.T) {
	factory, err := Load(writeFiles(t, validFactory()))
	if err != nil {
		t.Fatal(err)
	}
	preset, ok := factory.Preset("default")
	if !ok || preset.Name != "default" || preset.Description != "Clone the repository and run the tests." || preset.Timeout != 30*time.Minute || len(preset.Steps) != 2 {
		t.Fatalf("preset = %+v", preset)
	}
	if preset.Steps[0] != (runner.Step{Name: "checkout", Run: `gh repo clone "$MACHINIST_REPO" .`, TimeoutMillis: (5 * time.Minute).Milliseconds()}) || preset.Steps[1] != (runner.Step{Name: "test", Run: "go test ./...\n", IdleTimeoutMillis: (20 * time.Minute).Milliseconds()}) {
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
	if len(factory.Files) != 2 || factory.Files["prompts/plan.md"] != "Write a plan.\n" || factory.Files["scripts/agent.sh"] != "#!/bin/sh\nclaude\n" {
		t.Fatalf("files = %+v", factory.Files)
	}
}

func TestAgentStepRunsTheAgent(t *testing.T) {
	files := validFactory()
	files["agents/review.md"] = "---\nharness: codex\nmodel: gpt-5\n---\nReview the branch.\n"
	files["presets/default.yaml"] = "description: Review.\nsteps:\n  - name: review\n    agent: review\n"
	factory, err := Load(writeFiles(t, files))
	if err != nil {
		t.Fatal(err)
	}
	if step := factory.Presets["default"].Steps[0]; step.Run != "machinist agent review" {
		t.Fatalf("step = %+v", step)
	}
	if agent := factory.Agents["review"]; agent.Harness != "codex" || agent.Model != "gpt-5" || agent.Prompt != "Review the branch." {
		t.Fatalf("agent = %+v", agent)
	}
	if factory.Files["agents/review.md"] != files["agents/review.md"] {
		t.Fatalf("files = %v", factory.Files)
	}
}

func TestPresetTimeoutIsOptional(t *testing.T) {
	factory, err := Load(writeFiles(t, map[string]string{"presets/quick.yaml": "description: Quick.\nsteps:\n  - name: only\n    run: \"true\"\n"}))
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
	steps := func(body string) string { return "steps:\n" + body }
	automation := func(front string) string { return "---\n" + front + "---\nGo.\n" }
	for _, test := range []struct {
		name, want string
		change     func(map[string]string)
	}{
		{"no presets", "no presets", func(files map[string]string) { delete(files, "presets/default.yaml") }},
		{"no steps", "at least one step", func(files map[string]string) { files["presets/default.yaml"] = "timeout: 5m\n" }},
		{"bad step name", "invalid name", func(files map[string]string) {
			files["presets/default.yaml"] = steps("  - name: Build It\n    run: \"true\"\n")
		}},
		{"step twice", "listed twice", func(files map[string]string) {
			files["presets/default.yaml"] = steps("  - name: build\n    run: \"true\"\n  - name: build\n    run: \"true\"\n")
		}},
		{"empty run", "give it agent or run", func(files map[string]string) {
			files["presets/default.yaml"] = steps("  - name: build\n    run: \" \"\n")
		}},
		{"bad timeout", "positive duration", func(files map[string]string) {
			files["presets/default.yaml"] = "timeout: soon\n" + steps("  - name: b\n    run: \"true\"\n")
		}},
		{"agent and run", "agent or run, not both", func(files map[string]string) {
			files["agents/work.md"] = "---\nharness: claude\n---\nWork.\n"
			files["presets/default.yaml"] = steps("  - name: b\n    agent: work\n    run: \"true\"\n")
		}},
		{"missing agent", `agent "work" is not in agents/`, func(files map[string]string) {
			files["presets/default.yaml"] = steps("  - name: b\n    agent: work\n")
		}},
		{"unknown harness", "harness must be one of agy, claude, codex, pi", func(files map[string]string) {
			files["agents/work.md"] = "---\nharness: gemini\n---\nWork.\n"
		}},
		{"no description", "add a description", func(files map[string]string) {
			files["presets/default.yaml"] = "steps:\n  - name: b\n    run: \"true\"\n"
		}},
		{"bad step timeout", "step b: timeout", func(files map[string]string) {
			files["presets/default.yaml"] = steps("  - name: b\n    run: \"true\"\n    timeout: -1m\n")
		}},
		{"unknown key", "unknown key needs", func(files map[string]string) {
			files["presets/default.yaml"] = steps("  - name: b\n    run: \"true\"\n    needs: a\n")
		}},
		{"old TOML preset", "is TOML, and Machinist now reads YAML", func(files map[string]string) {
			files["presets/old.toml"] = "[[steps]]\nname = \"b\"\nrun = \"true\"\n"
		}},
		{"no front matter", "front matter", func(files map[string]string) { files["automations/nightly.md"] = "Just a prompt\n" }},
		{"TOML front matter", "Machinist now reads YAML between --- lines", func(files map[string]string) {
			files["automations/nightly.md"] = "+++\nrepo = \"x\"\n+++\nGo.\n"
		}},
		{"unclosed front matter", "not closed", func(files map[string]string) { files["automations/nightly.md"] = "---\nrepo: x\n" }},
		{"bad cron", "", func(files map[string]string) {
			files["automations/nightly.md"] = automation("schedule: nope\ntimezone: UTC\nrepo: o/r\npreset: default\n")
		}},
		{"no repo", "repo and preset", func(files map[string]string) {
			files["automations/nightly.md"] = automation("schedule: \"0 6 * * 1\"\ntimezone: UTC\npreset: default\n")
		}},
		{"old agent key", "unknown key agent", func(files map[string]string) {
			files["automations/nightly.md"] = automation("schedule: \"0 6 * * 1\"\ntimezone: UTC\nrepo: o/r\nagent: default\n")
		}},
		{"empty automation prompt", "prompt is empty", func(files map[string]string) {
			files["automations/nightly.md"] = "---\nschedule: \"0 6 * * 1\"\ntimezone: UTC\nrepo: o/r\npreset: default\n---\n\n"
		}},
		{"bad script name", "invalid file name", func(files map[string]string) { files["scripts/.hidden"] = "x" }},
		{"script directory", "regular file", func(files map[string]string) { files["scripts/nested/run.sh"] = "x" }},
		{"too many file bytes", "add up to less than", func(files map[string]string) {
			for _, name := range []string{"a", "b", "c", "d", "e"} {
				files["prompts/"+name+".md"] = strings.Repeat("x", 1<<20-1)
			}
		}},
		{"unknown extends base", `extends unknown preset "missing"`, func(files map[string]string) {
			files["presets/neon.yaml"] = "description: Neon.\nextends: missing\n"
		}},
		{"self extends cycle", "extends cycle: loop -> loop", func(files map[string]string) {
			files["presets/loop.yaml"] = "description: Loop.\nextends: loop\n"
		}},
		{"two-preset extends cycle", "extends cycle: a -> b -> a", func(files map[string]string) {
			files["presets/a.yaml"] = "description: A.\nextends: b\n"
			files["presets/b.yaml"] = "description: B.\nextends: a\n"
		}},
		{"three-preset extends cycle", "extends cycle: a -> b -> c -> a", func(files map[string]string) {
			files["presets/a.yaml"] = "description: A.\nextends: b\n"
			files["presets/b.yaml"] = "description: B.\nextends: c\n"
			files["presets/c.yaml"] = "description: C.\nextends: a\n"
		}},
		{"invalid preset harness", "harness must be one of agy, claude, codex, pi", func(files map[string]string) {
			files["presets/default.yaml"] = "description: Bad.\nharness: gemini\n" + steps("  - name: b\n    run: \"true\"\n")
		}},
		{"invalid step harness", "step b: harness must be one of agy, claude, codex, pi", func(files map[string]string) {
			files["presets/default.yaml"] = "description: Bad.\n" + steps("  - name: b\n    run: \"true\"\n    harness: gemini\n")
		}},
		{"extends step override not in base", `step typo: not in base preset "default"`, func(files map[string]string) {
			files["presets/neon.yaml"] = "description: Neon.\nextends: default\nsteps:\n  - name: typo\n    harness: agy\n"
		}},
		{"extends step empty run", "step checkout: give it agent or run", func(files map[string]string) {
			files["presets/neon.yaml"] = "description: Neon.\nextends: default\nsteps:\n  - name: checkout\n    run: \" \"\n"
		}},
		{"extends step agent and run", "step checkout: give it agent or run, not both", func(files map[string]string) {
			files["agents/work.md"] = "---\nharness: claude\n---\nWork.\n"
			files["presets/neon.yaml"] = "description: Neon.\nextends: default\nsteps:\n  - name: checkout\n    agent: work\n    run: \"true\"\n"
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
	path := filepath.Join(root, "presets", "default.yaml")
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

// The default preset merges only when github.auto_merge is on, so its
// description must not promise a merge.
func TestShippedDefaultPresetSaysMergingNeedsAutoMerge(t *testing.T) {
	factory, err := Load(filepath.Join("..", "..", "examples", "factory"))
	if err != nil {
		t.Fatal(err)
	}
	preset, ok := factory.Preset("default")
	if !ok {
		t.Fatal("no default preset")
	}
	if !strings.Contains(preset.Description, "when github.auto_merge is on") {
		t.Fatalf("description = %q", preset.Description)
	}
}

func TestPresetExtendsAndOverrides(t *testing.T) {
	files := map[string]string{
		"agents/implement.md": "---\nharness: claude\n---\nImplement.\n",
		"agents/review.md":    "---\nharness: claude\nmodel: sonnet\n---\nReview.\n",
		"agents/publish.md":   "---\nharness: claude\n---\nPublish.\n",
		"scripts/review.sh":   "#!/bin/sh\nmachinist agent review\n",
		"scripts/publish.sh":  "#!/bin/sh\nmachinist agent publish\n",
		"presets/default.yaml": `description: Default pipeline.
timeout: 60m
steps:
  - name: checkout
    timeout: 5m
    run: checkout.sh
  - name: implement
    agent: implement
  - name: review
    timeout: 15m
    run: review.sh
  - name: publish
    timeout: 10m
    run: publish.sh
`,
		// Alphabetically before "default" to verify load-order independence.
		"presets/aaa-neon.yaml": `description: Default pipeline on Antigravity (Gemini 4 Neon).
extends: default
harness: agy
model: gemini-4-neon-high
`,
		// Multi-level extends with cross-model review step override and a new step.
		"presets/cross-model.yaml": `extends: aaa-neon
steps:
  - name: review
    harness: claude
    model: opus
  - name: publish
    harness: codex
  - name: verify
    run: go test ./...
`,
		// Base with preset-level override and a pinned step, extended by a child
		// with a different preset-level override.
		"presets/base-pinned.yaml": `description: Base pinned.
harness: claude
model: sonnet
steps:
  - name: implement
    agent: implement
  - name: review
    run: review.sh
    harness: codex
    model: gpt-5
`,
		"presets/child-pinned.yaml": `description: Child overrides unpinned steps.
extends: base-pinned
harness: agy
model: gemini-4-neon-high
`,
	}

	loaded, err := Load(writeFiles(t, files))
	if err != nil {
		t.Fatal(err)
	}

	neon, ok := loaded.Preset("aaa-neon")
	if !ok {
		t.Fatal("missing aaa-neon preset")
	}
	if neon.Description != "Default pipeline on Antigravity (Gemini 4 Neon)." || neon.Timeout != 60*time.Minute || neon.Harness != "agy" || neon.Model != "gemini-4-neon-high" {
		t.Fatalf("neon = %+v", neon)
	}
	if len(neon.Steps) != 4 {
		t.Fatalf("neon steps = %+v", neon.Steps)
	}
	for _, step := range neon.Steps {
		if step.Harness != "agy" || step.Model != "gemini-4-neon-high" {
			t.Fatalf("neon step %s = %+v", step.Name, step)
		}
	}
	if neon.Steps[0].TimeoutMillis != (5*time.Minute).Milliseconds() || neon.Steps[1].Run != "machinist agent implement" || neon.Steps[2].TimeoutMillis != (15*time.Minute).Milliseconds() {
		t.Fatalf("neon steps = %+v", neon.Steps)
	}

	cross, ok := loaded.Preset("cross-model")
	if !ok {
		t.Fatal("missing cross-model preset")
	}
	// Omitting description inherits the parent's description.
	if cross.Description != neon.Description || cross.Timeout != 60*time.Minute || len(cross.Steps) != 5 {
		t.Fatalf("cross = %+v", cross)
	}
	if s := cross.Steps[1]; s.Name != "implement" || s.Harness != "agy" || s.Model != "gemini-4-neon-high" {
		t.Fatalf("implement step = %+v", s)
	}
	if s := cross.Steps[2]; s.Name != "review" || s.Run != "review.sh" || s.TimeoutMillis != (15*time.Minute).Milliseconds() || s.Harness != "claude" || s.Model != "opus" {
		t.Fatalf("review step = %+v", s)
	}
	// Switching harness on a step without naming a model drops the preset's model.
	if s := cross.Steps[3]; s.Name != "publish" || s.Run != "publish.sh" || s.Harness != "codex" || s.Model != "" {
		t.Fatalf("publish step = %+v", s)
	}
	if s := cross.Steps[4]; s.Name != "verify" || s.Run != "go test ./..." || s.Harness != "agy" || s.Model != "gemini-4-neon-high" {
		t.Fatalf("verify step = %+v", s)
	}

	childPinned, ok := loaded.Preset("child-pinned")
	if !ok {
		t.Fatal("missing child-pinned preset")
	}
	if s := childPinned.Steps[0]; s.Name != "implement" || s.Harness != "agy" || s.Model != "gemini-4-neon-high" {
		t.Fatalf("child-pinned implement = %+v", s)
	}
	if s := childPinned.Steps[1]; s.Name != "review" || s.Harness != "codex" || s.Model != "gpt-5" {
		t.Fatalf("child-pinned review should keep base explicit step override: %+v", s)
	}
}
