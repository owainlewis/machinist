package factory

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestUnifiedDiff(t *testing.T) {
	if got := UnifiedDiff("a", "b", "same\n", "same\n"); got != "" {
		t.Fatalf("identical texts produced diff: %q", got)
	}
	diff := UnifiedDiff("installed", "shipped", "line1\nold\nline3\n", "line1\nnew\nline3\n")
	for _, want := range []string{"--- installed\n", "+++ shipped\n", "@@ -1,3 +1,3 @@\n", " line1\n", "-old\n", "+new\n", " line3\n"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff missing %q:\n%s", want, diff)
		}
	}

	// Multiple separated hunks.
	oldText := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n"
	newText := "1\nTWO\n3\n4\n5\n6\n7\n8\n9\n10\nELEVEN\n12\n"
	multi := UnifiedDiff("a", "b", oldText, newText)
	if count := strings.Count(multi, "@@ "); count != 2 {
		t.Fatalf("expected 2 hunks, got %d:\n%s", count, multi)
	}

	// Trailing newline change only.
	nlDiff := UnifiedDiff("a", "b", "hello", "hello\n")
	if !strings.Contains(nlDiff, `\ No newline at end of file`) {
		t.Fatalf("expected no-newline marker:\n%s", nlDiff)
	}

	// Empty old text to non-empty new text.
	added := UnifiedDiff("a", "b", "", "one\ntwo\n")
	if !strings.Contains(added, "+one\n+two\n") {
		t.Fatalf("expected added lines:\n%s", added)
	}
}

func TestCheckClassifiesUneditedEditedCustomizedAndMissing(t *testing.T) {
	shippedV1 := fstest.MapFS{
		"presets/default.yaml": {Data: []byte("v1 default\n")},
		"agents/publish.md":    {Data: []byte("v1 publish\n")},
		"agents/review.md":     {Data: []byte("v1 review\n")},
		"scripts/publish.sh":   {Data: []byte("#!/bin/sh\necho v1\n")},
	}
	shippedV2 := fstest.MapFS{
		"presets/default.yaml": {Data: []byte("v2 default\n")},
		"agents/publish.md":    {Data: []byte("v2 publish\n")},
		"agents/review.md":     {Data: []byte("v1 review\n")}, // unchanged in v2
		"scripts/publish.sh":   {Data: []byte("#!/bin/sh\necho v2\n")},
		"scripts/new.sh":       {Data: []byte("#!/bin/sh\necho new\n")},
	}

	dir := writeFiles(t, map[string]string{
		"presets/default.yaml": "v2 default\n",          // already matches v2 -> current
		"agents/publish.md":    "team custom publish\n", // edited on top of v1, v2 changed -> edited
		"agents/review.md":     "team custom review\n",  // edited on top of v1, v2 unchanged -> customized
		"scripts/publish.sh":   "#!/bin/sh\necho v1\n",  // matches v1 manifest, v2 changed -> outdated
		"presets/custom.yaml":  "team extra preset\n",   // not a shipped file -> ignored
		// scripts/new.sh is missing -> missing
	})
	if err := WriteManifest(dir, Manifest{Files: map[string]string{
		"presets/default.yaml": HashBytes(shippedV1["presets/default.yaml"].Data),
		"agents/publish.md":    HashBytes(shippedV1["agents/publish.md"].Data),
		"agents/review.md":     HashBytes(shippedV1["agents/review.md"].Data),
		"scripts/publish.sh":   HashBytes(shippedV1["scripts/publish.sh"].Data),
	}}); err != nil {
		t.Fatal(err)
	}

	updates, err := CheckAgainst(dir, shippedV2)
	if err != nil {
		t.Fatal(err)
	}
	want := []FileUpdate{
		{Path: "agents/publish.md", Status: FileEdited},
		{Path: "scripts/new.sh", Status: FileMissing},
		{Path: "scripts/publish.sh", Status: FileOutdated},
	}
	if !slices.Equal(updates, want) {
		t.Fatalf("updates = %+v, want %+v", updates, want)
	}
}

func TestCheckWithoutManifestTreatsDifferingFilesAsEditedAndMissingAsMissing(t *testing.T) {
	shipped := fstest.MapFS{
		"agents/publish.md":  {Data: []byte("shipped publish\n")},
		"scripts/publish.sh": {Data: []byte("#!/bin/sh\necho shipped\n")},
		"scripts/missing.sh": {Data: []byte("#!/bin/sh\necho missing\n")},
	}
	dir := writeFiles(t, map[string]string{
		"agents/publish.md":  "shipped publish\n",
		"scripts/publish.sh": "#!/bin/sh\necho old\n",
	})

	updates, err := CheckAgainst(dir, shipped)
	if err != nil {
		t.Fatal(err)
	}
	want := []FileUpdate{
		{Path: "scripts/missing.sh", Status: FileMissing},
		{Path: "scripts/publish.sh", Status: FileEdited},
	}
	if !slices.Equal(updates, want) {
		t.Fatalf("updates = %+v, want %+v", updates, want)
	}
}

func TestUpgradeUpdatesUneditedCreatesMissingAndSkipsEditedUntilForce(t *testing.T) {
	shippedV1 := fstest.MapFS{
		"presets/default.yaml": {Data: []byte("v1 default\n")},
		"agents/publish.md":    {Data: []byte("line 1\nv1 publish\nline 3\n")},
		"agents/review.md":     {Data: []byte("v1 review\n")},
		"scripts/publish.sh":   {Data: []byte("#!/bin/sh\necho v1\n")},
		"scripts/removed.sh":   {Data: []byte("#!/bin/sh\necho removed\n")},
	}
	shippedV2 := fstest.MapFS{
		"presets/default.yaml": {Data: []byte("v2 default\n")},
		"agents/publish.md":    {Data: []byte("line 1\nv2 publish\nline 3\n")},
		"agents/review.md":     {Data: []byte("v1 review\n")},
		"scripts/publish.sh":   {Data: []byte("#!/bin/sh\necho v2\n")},
		"scripts/new.sh":       {Data: []byte("#!/bin/sh\necho new\n")},
	}

	dir := writeFiles(t, map[string]string{
		"presets/default.yaml": "v1 default\n",                   // unedited outdated
		"agents/publish.md":    "line 1\nteam publish\nline 3\n", // edited outdated
		"agents/review.md":     "team review\n",                  // customized on unchanged shipped file
		"scripts/publish.sh":   "#!/bin/sh\necho v1\n",           // unedited outdated script
		"presets/extra.yaml":   "keep me\n",                      // extra team file
	})
	if err := WriteManifest(dir, Manifest{Files: map[string]string{
		"presets/default.yaml": HashBytes(shippedV1["presets/default.yaml"].Data),
		"agents/publish.md":    HashBytes(shippedV1["agents/publish.md"].Data),
		"agents/review.md":     HashBytes(shippedV1["agents/review.md"].Data),
		"scripts/publish.sh":   HashBytes(shippedV1["scripts/publish.sh"].Data),
		"scripts/removed.sh":   HashBytes(shippedV1["scripts/removed.sh"].Data),
	}}); err != nil {
		t.Fatal(err)
	}

	// Safe upgrade (force == false).
	result, err := UpgradeAgainst(dir, shippedV2, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ManifestUpdated {
		t.Fatal("expected manifest to be updated")
	}
	if len(result.Entries) != 4 {
		t.Fatalf("entries = %+v", result.Entries)
	}
	byPath := map[string]UpgradeEntry{}
	for _, entry := range result.Entries {
		byPath[entry.Path] = entry
	}
	if e := byPath["presets/default.yaml"]; e.Action != ActionUpdated || e.Status != FileOutdated {
		t.Fatalf("presets/default.yaml = %+v", e)
	}
	if e := byPath["scripts/publish.sh"]; e.Action != ActionUpdated || e.Status != FileOutdated {
		t.Fatalf("scripts/publish.sh = %+v", e)
	}
	if e := byPath["scripts/new.sh"]; e.Action != ActionCreated || e.Status != FileMissing {
		t.Fatalf("scripts/new.sh = %+v", e)
	}
	if e := byPath["agents/publish.md"]; e.Action != ActionSkipped || e.Status != FileEdited || !strings.Contains(e.Diff, "-team publish\n") || !strings.Contains(e.Diff, "+v2 publish\n") {
		t.Fatalf("agents/publish.md = %+v", e)
	}

	// Verify file contents and permissions on disk.
	assertContent := func(rel, want string, wantPerm os.FileMode) {
		t.Helper()
		full := filepath.Join(dir, filepath.FromSlash(rel))
		got, err := os.ReadFile(full)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", rel, got, want)
		}
		info, err := os.Stat(full)
		if err != nil {
			t.Fatal(err)
		}
		if wantPerm != 0 && info.Mode().Perm() != wantPerm {
			t.Fatalf("%s perm = %o, want %o", rel, info.Mode().Perm(), wantPerm)
		}
	}
	assertContent("presets/default.yaml", "v2 default\n", 0o600)
	assertContent("scripts/publish.sh", "#!/bin/sh\necho v2\n", 0o700)
	assertContent("scripts/new.sh", "#!/bin/sh\necho new\n", 0o700)
	assertContent("agents/publish.md", "line 1\nteam publish\nline 3\n", 0)
	assertContent("agents/review.md", "team review\n", 0)
	assertContent("presets/extra.yaml", "keep me\n", 0)

	// Manifest pruned removed.sh and kept v1 hash for skipped agents/publish.md.
	manifest, exists, err := LoadManifest(dir)
	if err != nil || !exists {
		t.Fatalf("manifest exists=%v err=%v", exists, err)
	}
	if _, ok := manifest.Files["scripts/removed.sh"]; ok {
		t.Fatal("stale manifest key scripts/removed.sh was not pruned")
	}
	if manifest.Files["agents/publish.md"] != HashBytes(shippedV1["agents/publish.md"].Data) {
		t.Fatalf("skipped file manifest hash changed: %q", manifest.Files["agents/publish.md"])
	}

	// Forced upgrade overwrites edited outdated file, still preserves customized review.md.
	forced, err := UpgradeAgainst(dir, shippedV2, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(forced.Entries) != 1 || forced.Entries[0].Path != "agents/publish.md" || forced.Entries[0].Action != ActionUpdated {
		t.Fatalf("forced entries = %+v", forced.Entries)
	}
	assertContent("agents/publish.md", "line 1\nv2 publish\nline 3\n", 0o600)
	assertContent("agents/review.md", "team review\n", 0)

	// Factory is now completely up to date.
	remaining, err := CheckAgainst(dir, shippedV2)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remaining = %+v, err = %v", remaining, err)
	}
}

func TestInitManifestPreservesExistingEntries(t *testing.T) {
	shipped := fstest.MapFS{
		"a.txt": {Data: []byte("shipped-a\n")},
		"b.txt": {Data: []byte("shipped-b\n")},
	}
	dir := writeFiles(t, map[string]string{
		"a.txt": "shipped-a\n",
		"b.txt": "edited-b\n",
	})
	written, err := InitManifestAgainst(dir, shipped)
	if err != nil || !written {
		t.Fatalf("written=%v err=%v", written, err)
	}
	manifest, _, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Files["a.txt"] != HashBytes([]byte("shipped-a\n")) {
		t.Fatalf("a.txt hash = %q", manifest.Files["a.txt"])
	}
	if _, ok := manifest.Files["b.txt"]; ok {
		t.Fatal("InitManifest should not record a file that does not match shipped")
	}

	// Re-running InitManifest keeps an older recorded hash intact.
	manifest.Files["a.txt"] = "sha256:older"
	if err := WriteManifest(dir, manifest); err != nil {
		t.Fatal(err)
	}
	written, err = InitManifestAgainst(dir, shipped)
	if err != nil || written {
		t.Fatalf("second init written=%v err=%v", written, err)
	}
	manifest, _, err = LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Files["a.txt"] != "sha256:older" {
		t.Fatalf("InitManifest overwrote existing hash: %q", manifest.Files["a.txt"])
	}
}

func TestLoadManifestRejectsInvalidAndSymlinks(t *testing.T) {
	dir := writeFiles(t, validFactory())
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte("unknown_key: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("expected manifest error, got %v", err)
	}

	symDir := writeFiles(t, validFactory())
	target := filepath.Join(t.TempDir(), "other.yaml")
	if err := os.WriteFile(target, []byte("files: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(symDir, ManifestName)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadManifest(symDir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestWarningLine(t *testing.T) {
	if got := WarningLine(nil); got != "" {
		t.Fatalf("got = %q", got)
	}
	one := WarningLine([]FileUpdate{{Path: "scripts/publish.sh", Status: FileOutdated}})
	if one != "factory file is out of date (scripts/publish.sh); run machinist factory upgrade" {
		t.Fatalf("one = %q", one)
	}
	many := WarningLine([]FileUpdate{
		{Path: "agents/publish.md", Status: FileEdited},
		{Path: "scripts/publish.sh", Status: FileOutdated},
		{Path: "scripts/revise.sh", Status: FileMissing},
	})
	want := "factory files are out of date (agents/publish.md (edited), scripts/publish.sh, scripts/revise.sh (missing)); run machinist factory upgrade"
	if many != want {
		t.Fatalf("many = %q, want %q", many, want)
	}
}
