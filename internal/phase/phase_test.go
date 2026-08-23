package phase

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writePhase(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(Dir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const buildPhase = `---
name: build
description: Implement one issue and open a pull request
runtime: claude
model: opus
timeout: 90m
---
Implement {{ issue.identifier }}: {{ run.title }}

{{ run.criteria }}
`

func TestLoadReadsFrontmatterAndFreezesHash(t *testing.T) {
	root := t.TempDir()
	writePhase(t, root, "build", buildPhase)

	loaded, err := Load(root, "build")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Description != "Implement one issue and open a pull request" {
		t.Fatalf("description = %q", loaded.Description)
	}
	if loaded.Runtime != "claude" || loaded.Model != "opus" {
		t.Fatalf("runtime = %q model = %q", loaded.Runtime, loaded.Model)
	}
	if loaded.Timeout != 90*time.Minute {
		t.Fatalf("timeout = %s", loaded.Timeout)
	}
	if loaded.ReadOnly {
		t.Fatal("phase defaulted to read-only")
	}
	if len(loaded.Hash) != 64 || len(loaded.ShortHash()) != 7 {
		t.Fatalf("hash = %q", loaded.Hash)
	}

	// The hash must follow the file, so an edited prompt is a different version.
	writePhase(t, root, "build", strings.Replace(buildPhase, "Implement {{", "Please implement {{", 1))
	edited, err := Load(root, "build")
	if err != nil {
		t.Fatal(err)
	}
	if edited.Hash == loaded.Hash {
		t.Fatal("editing the body did not change the hash")
	}
}

func TestLoadDefaultsAndPermissions(t *testing.T) {
	root := t.TempDir()
	writePhase(t, root, "audit", `---
description: Find real defects and file one issue for each
permissions: read-only
max_findings: 5
---
Audit {{ run.repo }} and file at most {{ phase.max_findings }} issues.
`)
	loaded, err := Load(root, "audit")
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.ReadOnly {
		t.Fatal("permissions: read-only was not applied")
	}
	if loaded.Timeout != DefaultTimeout {
		t.Fatalf("timeout = %s, want the default", loaded.Timeout)
	}
	if loaded.Vars["max_findings"] != "5" {
		t.Fatalf("uninterpreted frontmatter was dropped: %v", loaded.Vars)
	}
}

func TestLoadRejectsBrokenFiles(t *testing.T) {
	for _, testCase := range []struct{ name, content, want string }{
		{"no frontmatter", "Just a prompt\n", "must start with a ---"},
		{"unclosed", "---\ndescription: x\nstill open\n", "not closed"},
		{"no description", "---\nruntime: claude\n---\nBody\n", "description is required"},
		{"empty body", "---\ndescription: x\n---\n\n", "body is empty"},
		{"bad timeout", "---\ndescription: x\ntimeout: soon\n---\nBody\n", "not a duration"},
		{"bad permissions", "---\ndescription: x\npermissions: some\n---\nBody\n", "read-only or write"},
		{"indented", "---\ndescription: x\n  nested: y\n---\nBody\n", "only flat key: value"},
		{"duplicate key", "---\ndescription: x\ndescription: y\n---\nBody\n", "more than once"},
		{"name mismatch", "---\nname: other\ndescription: x\n---\nBody\n", "does not match the file name"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			writePhase(t, root, "build", testCase.content)
			_, err := Load(root, "build")
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}
}

func TestLoadRejectsUnsafeNames(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"", "../escape", "build/nested", "build.md"} {
		if _, err := Load(root, name); err == nil {
			t.Fatalf("name %q was accepted", name)
		}
	}
}

func TestLoadAllSortsAndFailsOnOneBrokenFile(t *testing.T) {
	root := t.TempDir()
	writePhase(t, root, "verify", "---\ndescription: Check the change\n---\nCheck it.\n")
	writePhase(t, root, "build", buildPhase)

	phases, err := LoadAll(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) != 2 || phases[0].Name != "build" || phases[1].Name != "verify" {
		t.Fatalf("phases = %v", phases)
	}

	writePhase(t, root, "broken", "no frontmatter\n")
	if _, err := LoadAll(root); err == nil {
		t.Fatal("one broken phase did not fail the load")
	}
}

func TestRenderSubstitutesEveryNamespace(t *testing.T) {
	root := t.TempDir()
	writePhase(t, root, "build", buildPhase)
	loaded, err := Load(root, "build")
	if err != nil {
		t.Fatal(err)
	}

	rendered, err := loaded.Render(Context{
		Run:   map[string]string{"title": "Add rate limiting", "criteria": "429 after 100 req/min"},
		Issue: map[string]string{"identifier": "acme/api#412"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"acme/api#412", "Add rate limiting", "429 after 100 req/min"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered prompt is missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "{{") {
		t.Fatalf("rendered prompt still holds a reference:\n%s", rendered)
	}
}

func TestRenderIsStrict(t *testing.T) {
	root := t.TempDir()

	t.Run("unknown key names what is available", func(t *testing.T) {
		writePhase(t, root, "build", "---\ndescription: x\n---\nUse {{ run.missing }}.\n")
		loaded, err := Load(root, "build")
		if err != nil {
			t.Fatal(err)
		}
		_, err = loaded.Render(Context{Run: map[string]string{"title": "t"}})
		if err == nil || !strings.Contains(err.Error(), "run.missing") {
			t.Fatalf("error = %v", err)
		}
		if !strings.Contains(err.Error(), "title") {
			t.Fatalf("error did not list the available keys: %v", err)
		}
	})

	t.Run("issue on a repository run", func(t *testing.T) {
		writePhase(t, root, "audit", "---\ndescription: x\n---\nAudit {{ issue.title }}.\n")
		loaded, err := Load(root, "audit")
		if err != nil {
			t.Fatal(err)
		}
		_, err = loaded.Render(Context{Run: map[string]string{"repo": "api"}})
		if err == nil || !strings.Contains(err.Error(), "targets a repository") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("unknown namespace", func(t *testing.T) {
		writePhase(t, root, "build", "---\ndescription: x\n---\nUse {{ task.title }}.\n")
		loaded, err := Load(root, "build")
		if err != nil {
			t.Fatal(err)
		}
		_, err = loaded.Render(Context{Run: map[string]string{}})
		if err == nil || !strings.Contains(err.Error(), "unknown namespace") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("unclosed reference", func(t *testing.T) {
		writePhase(t, root, "build", "---\ndescription: x\n---\nUse {{ run.title.\n")
		loaded, err := Load(root, "build")
		if err != nil {
			t.Fatal(err)
		}
		_, err = loaded.Render(Context{Run: map[string]string{"title": "t"}})
		if err == nil || !strings.Contains(err.Error(), "unclosed") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestRenderDefaultsPhaseNamespaceToFrontmatter(t *testing.T) {
	root := t.TempDir()
	writePhase(t, root, "audit", "---\ndescription: x\nmax_findings: 5\n---\nFile at most {{ phase.max_findings }}.\n")
	loaded, err := Load(root, "audit")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := loaded.Render(Context{Run: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "File at most 5.") {
		t.Fatalf("rendered = %q", rendered)
	}
}

func TestRenderRejectsAnOversizedPrompt(t *testing.T) {
	root := t.TempDir()
	writePhase(t, root, "build", "---\ndescription: x\n---\n{{ run.body }}\n")
	loaded, err := Load(root, "build")
	if err != nil {
		t.Fatal(err)
	}
	_, err = loaded.Render(Context{Run: map[string]string{"body": strings.Repeat("a", MaxPromptBytes+1)}})
	if err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("error = %v", err)
	}
}
