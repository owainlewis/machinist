package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	machinistexamples "github.com/owainlewis/machinist/examples"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/fakegh"
)

func TestInitWritesFactoryManifestAndFactoryIsCurrent(t *testing.T) {
	directory := inTempDirectory(t)
	var stdout, stderr bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &stdout, &stderr, "test"); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	factoryDir := filepath.Join(directory, "factory")
	info, err := os.Stat(filepath.Join(factoryDir, factory.ManifestName))
	if err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode = %o, want 600", info.Mode().Perm())
	}
	updates, err := factory.Check(factoryDir)
	if err != nil || len(updates) != 0 {
		t.Fatalf("updates = %+v, err = %v", updates, err)
	}
}

func TestStartWarnsAboutOutdatedFactoryFilesAndNamesUpgradeCommand(t *testing.T) {
	fakegh.Install(t, "machinist-bot")
	directory := inTempDirectory(t)
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("init exit code = %d", exitCode)
	}
	writeConfigFile(t, directory, "config.yaml", "server:\n  listen: 127.0.0.1:0\n  database: .machinist/tickets.db\n  factory: factory\n\ngithub:\n  handle: machinist-bot\n")

	// Freshly initialized factory emits no factory warning.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	freshStderr := &cancelOnListen{cancel: cancel}
	if exitCode := Execute(ctx, []string{"start"}, strings.NewReader(""), &bytes.Buffer{}, freshStderr, "test"); exitCode != 0 {
		t.Fatalf("fresh start exit = %d, stderr = %q", exitCode, freshStderr.body.String())
	}
	if strings.Contains(freshStderr.body.String(), "factory file") {
		t.Fatalf("unexpected factory warning on fresh install: %q", freshStderr.body.String())
	}

	// Simulate an older shipped version: unedited publish.sh, edited publish.md, missing revise.sh.
	factoryDir := filepath.Join(directory, "factory")
	manifest, _, err := factory.LoadManifest(factoryDir)
	if err != nil {
		t.Fatal(err)
	}
	oldScript := []byte("#!/bin/sh\necho old publish\n")
	if err := os.WriteFile(filepath.Join(factoryDir, "scripts", "publish.sh"), oldScript, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest.Files["scripts/publish.sh"] = factory.HashBytes(oldScript)

	if err := os.WriteFile(filepath.Join(factoryDir, "agents", "publish.md"), []byte("---\nharness: claude\n---\nTeam custom publish.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest.Files["agents/publish.md"] = factory.HashBytes([]byte("---\nharness: claude\n---\nOlder shipped publish.\n"))

	if err := os.Remove(filepath.Join(factoryDir, "scripts", "revise.sh")); err != nil {
		t.Fatal(err)
	}
	if err := factory.WriteManifest(factoryDir, manifest); err != nil {
		t.Fatal(err)
	}

	ctx2, cancel2 := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel2()
	staleStderr := &cancelOnListen{cancel: cancel2}
	if exitCode := Execute(ctx2, []string{"start"}, strings.NewReader(""), &bytes.Buffer{}, staleStderr, "test"); exitCode != 0 {
		t.Fatalf("stale start exit = %d, stderr = %q", exitCode, staleStderr.body.String())
	}
	output := staleStderr.body.String()
	for _, want := range []string{
		"machinist: warning:",
		"agents/publish.md (edited)",
		"scripts/publish.sh",
		"scripts/revise.sh (missing)",
		"machinist factory upgrade",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("start stderr missing %q:\n%s", want, output)
		}
	}
}

func TestFactoryUpgradeCLIHandlesUneditedEditedMissingAndForce(t *testing.T) {
	directory := inTempDirectory(t)
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("init exit code = %d", exitCode)
	}

	factoryDir := filepath.Join(directory, "factory")
	manifest, _, err := factory.LoadManifest(factoryDir)
	if err != nil {
		t.Fatal(err)
	}
	// 1. Unedited older shipped file.
	oldScript := []byte("#!/bin/sh\necho old publish\n")
	if err := os.WriteFile(filepath.Join(factoryDir, "scripts", "publish.sh"), oldScript, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest.Files["scripts/publish.sh"] = factory.HashBytes(oldScript)

	// 2. Edited file based on an older shipped version.
	editedPublish := "---\nharness: claude\n---\nTeam custom publish prompt.\n"
	if err := os.WriteFile(filepath.Join(factoryDir, "agents", "publish.md"), []byte(editedPublish), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest.Files["agents/publish.md"] = factory.HashBytes([]byte("---\nharness: claude\n---\nOlder shipped publish.\n"))

	// 3. Customized file based on the current shipped version (must stay untouched even with --force).
	customizedReview := "---\nharness: codex\n---\nTeam custom review prompt.\n"
	if err := os.WriteFile(filepath.Join(factoryDir, "agents", "review.md"), []byte(customizedReview), 0o600); err != nil {
		t.Fatal(err)
	}

	// 4. Missing shipped file.
	if err := os.Remove(filepath.Join(factoryDir, "scripts", "revise.sh")); err != nil {
		t.Fatal(err)
	}
	if err := factory.WriteManifest(factoryDir, manifest); err != nil {
		t.Fatal(err)
	}

	// Run upgrade without --force.
	var stdout, stderr bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"factory", "upgrade"}, strings.NewReader(""), &stdout, &stderr, "test"); exitCode != 0 {
		t.Fatalf("upgrade exit = %d, stderr = %q", exitCode, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"updated scripts/publish.sh",
		"created scripts/revise.sh",
		"skipped agents/publish.md",
		"--- agents/publish.md (installed)",
		"+++ agents/publish.md (shipped)",
		"-Team custom publish prompt.",
		"--force",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("upgrade output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "agents/review.md") {
		t.Fatalf("upgrade should not touch customized current file:\n%s", out)
	}

	// Verify unedited and missing files match shipped, and edited file was NOT overwritten.
	wantPublishScript, _ := machinistexamples.Files.ReadFile("factory/scripts/publish.sh")
	wantReviseScript, _ := machinistexamples.Files.ReadFile("factory/scripts/revise.sh")
	wantPublishAgent, _ := machinistexamples.Files.ReadFile("factory/agents/publish.md")

	if got, _ := os.ReadFile(filepath.Join(factoryDir, "scripts", "publish.sh")); !bytes.Equal(got, wantPublishScript) {
		t.Fatalf("scripts/publish.sh was not updated")
	}
	if info, err := os.Stat(filepath.Join(factoryDir, "scripts", "publish.sh")); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("scripts/publish.sh mode = %v, err = %v", info, err)
	}
	if got, _ := os.ReadFile(filepath.Join(factoryDir, "scripts", "revise.sh")); !bytes.Equal(got, wantReviseScript) {
		t.Fatalf("scripts/revise.sh was not created")
	}
	if info, err := os.Stat(filepath.Join(factoryDir, "scripts", "revise.sh")); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("scripts/revise.sh mode = %v, err = %v", info, err)
	}
	if got, _ := os.ReadFile(filepath.Join(factoryDir, "agents", "publish.md")); string(got) != editedPublish {
		t.Fatalf("agents/publish.md was overwritten without --force: %q", got)
	}

	// Run upgrade with --force.
	stdout.Reset()
	stderr.Reset()
	if exitCode := Execute(t.Context(), []string{"factory", "upgrade", "--force"}, strings.NewReader(""), &stdout, &stderr, "test"); exitCode != 0 {
		t.Fatalf("forced upgrade exit = %d, stderr = %q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "updated agents/publish.md") {
		t.Fatalf("forced upgrade output = %q", stdout.String())
	}
	if got, _ := os.ReadFile(filepath.Join(factoryDir, "agents", "publish.md")); !bytes.Equal(got, wantPublishAgent) {
		t.Fatalf("agents/publish.md was not overwritten with --force")
	}
	if got, _ := os.ReadFile(filepath.Join(factoryDir, "agents", "review.md")); string(got) != customizedReview {
		t.Fatalf("customized agents/review.md was overwritten by --force: %q", got)
	}

	// Third run reports up to date.
	stdout.Reset()
	stderr.Reset()
	if exitCode := Execute(t.Context(), []string{"factory", "upgrade"}, strings.NewReader(""), &stdout, &stderr, "test"); exitCode != 0 {
		t.Fatalf("third upgrade exit = %d, stderr = %q", exitCode, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "factory is up to date" {
		t.Fatalf("third upgrade output = %q", stdout.String())
	}
}
