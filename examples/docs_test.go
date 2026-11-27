package examples_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/factory"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("expected go.mod at %s: %v", root, err)
	}
	return root
}

func TestExampleConfigsAndFactoryLoad(t *testing.T) {
	root := repoRoot(t)

	// Control plane example configs.
	for _, tc := range []struct {
		rel   string
		check func(t *testing.T, cfg config.Config)
	}{
		{
			rel: filepath.Join("examples", "config.yaml"),
			check: func(t *testing.T, cfg config.Config) {
				if cfg.Server.DefaultPreset != "default" {
					t.Fatalf("DefaultPreset = %q, want default", cfg.Server.DefaultPreset)
				}
				if !cfg.GitHub.AutoMerges() {
					t.Fatalf("AutoMerges = false, want true")
				}
				if cfg.GitHub.StewardRetryLimit() != config.DefaultStewardRetries {
					t.Fatalf("StewardRetryLimit = %d, want %d", cfg.GitHub.StewardRetryLimit(), config.DefaultStewardRetries)
				}
			},
		},
		{
			rel: filepath.Join("deploy", "cloudrun", "config.example.yaml"),
			check: func(t *testing.T, cfg config.Config) {
				if !cfg.Auth.GitHub.Enabled {
					t.Fatalf("Auth.GitHub.Enabled = false, want true")
				}
				if cfg.Server.TrustedProxyHops != 1 {
					t.Fatalf("TrustedProxyHops = %d, want 1", cfg.Server.TrustedProxyHops)
				}
				if cfg.GitHub.App == nil || cfg.GitHub.App.ID <= 0 {
					t.Fatalf("GitHub.App = %+v, want non-nil with positive ID", cfg.GitHub.App)
				}
			},
		},
	} {
		t.Run(tc.rel, func(t *testing.T) {
			cfg, err := config.LoadConfig(filepath.Join(root, tc.rel))
			if err != nil {
				t.Fatalf("LoadConfig(%s): %v", tc.rel, err)
			}
			tc.check(t, cfg)
		})
	}

	// Runner example config.
	t.Run("examples/runner.yaml", func(t *testing.T) {
		runnerCfg, err := config.LoadRunner(filepath.Join(root, "examples", "runner.yaml"))
		if err != nil {
			t.Fatalf("LoadRunner(examples/runner.yaml): %v", err)
		}
		if runnerCfg.Kind() != "local" {
			t.Fatalf("Kind = %q, want local", runnerCfg.Kind())
		}
		if runnerCfg.Slots <= 0 {
			t.Fatalf("Slots = %d, want > 0", runnerCfg.Slots)
		}
	})

	// Starter factory presets, agents, prompts, and scripts.
	t.Run("examples/factory", func(t *testing.T) {
		loaded, err := factory.Load(filepath.Join(root, "examples", "factory"))
		if err != nil {
			t.Fatalf("factory.Load(examples/factory): %v", err)
		}
		preset, ok := loaded.Presets["default"]
		if !ok {
			t.Fatalf("default preset missing from examples/factory")
		}
		wantSteps := []string{"checkout", "implement", "review", "revise", "publish", "merge-gate", "merge"}
		gotSteps := make([]string, 0, len(preset.Steps))
		for _, step := range preset.Steps {
			gotSteps = append(gotSteps, step.Name)
		}
		if !slices.Equal(gotSteps, wantSteps) {
			t.Fatalf("default preset steps = %v, want %v", gotSteps, wantSteps)
		}
		for _, agent := range []string{"implement", "review", "revise", "publish", "merge-gate"} {
			if _, ok := loaded.Agents[agent]; !ok {
				t.Fatalf("agent %q missing from examples/factory/agents", agent)
			}
		}
	})

	// Starter merge policy templates.
	t.Run("examples/merge-policies", func(t *testing.T) {
		matches, err := filepath.Glob(filepath.Join(root, "examples", "merge-policies", "*.md"))
		if err != nil {
			t.Fatalf("glob merge policies: %v", err)
		}
		if len(matches) == 0 {
			t.Fatalf("no merge policy templates found in examples/merge-policies")
		}
		for _, path := range matches {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			if strings.TrimSpace(string(body)) == "" {
				t.Fatalf("%s is empty", path)
			}
		}
	})
}

var (
	markdownLinkPattern = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)
	htmlAttrPattern     = regexp.MustCompile(`(?:href|src|srcset)="([^"]+)"`)
	htmlAnchorPattern   = regexp.MustCompile(`<a\s+[^>]*id=["']([^"']+)["']`)
	headingPattern      = regexp.MustCompile(`^#{1,6}\s+(.*)$`)
	htmlTagPattern      = regexp.MustCompile(`<[^>]+>`)
)

func TestAllDocLinksResolve(t *testing.T) {
	root := repoRoot(t)

	var mdFiles []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "dist", ".machinist":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			mdFiles = append(mdFiles, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk markdown files: %v", err)
	}
	if len(mdFiles) == 0 {
		t.Fatal("found no markdown files")
	}

	anchorsByFile := make(map[string]map[string]bool, len(mdFiles))
	contentsByFile := make(map[string]string, len(mdFiles))
	for _, path := range mdFiles {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		cleaned := stripFencedCodeBlocks(string(raw))
		contentsByFile[path] = cleaned
		anchorsByFile[path] = collectAnchors(cleaned)
	}

	for _, path := range mdFiles {
		relPath, _ := filepath.Rel(root, path)
		cleaned := contentsByFile[path]

		var refs []string
		for _, match := range markdownLinkPattern.FindAllStringSubmatch(cleaned, -1) {
			refs = append(refs, strings.TrimSpace(match[1]))
		}
		for _, match := range htmlAttrPattern.FindAllStringSubmatch(cleaned, -1) {
			refs = append(refs, strings.TrimSpace(match[1]))
		}

		for _, ref := range refs {
			// Strip optional Markdown link title: [text](path "title").
			if idx := strings.IndexAny(ref, " \t"); idx >= 0 {
				ref = ref[:idx]
			}
			if ref == "" || strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "mailto:") {
				continue
			}

			filePart, fragment, hasFragment := strings.Cut(ref, "#")
			targetPath := path
			if filePart != "" {
				targetPath = filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(filePart)))
				if _, err := os.Stat(targetPath); err != nil {
					t.Errorf("%s: broken link %q (resolved %s): %v", relPath, ref, targetPath, err)
					continue
				}
			}

			if hasFragment && fragment != "" {
				if anchors, ok := anchorsByFile[targetPath]; ok && !anchors[fragment] {
					t.Errorf("%s: broken anchor %q in %s", relPath, ref, targetPath)
				}
			}
		}
	}
}

func stripFencedCodeBlocks(body string) string {
	var out strings.Builder
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if !inFence {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func collectAnchors(cleaned string) map[string]bool {
	anchors := map[string]bool{}
	counts := map[string]int{}
	for _, line := range strings.Split(cleaned, "\n") {
		if m := headingPattern.FindStringSubmatch(line); m != nil {
			base := githubSlug(m[1])
			if base != "" {
				if n := counts[base]; n == 0 {
					anchors[base] = true
				} else {
					anchors[base+"-"+itoa(n)] = true
				}
				counts[base]++
			}
		}
		for _, m := range htmlAnchorPattern.FindAllStringSubmatch(line, -1) {
			anchors[m[1]] = true
		}
	}
	return anchors
}

func githubSlug(heading string) string {
	s := htmlTagPattern.ReplaceAllString(strings.TrimSpace(heading), "")
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte('-')
		}
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
