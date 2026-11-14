package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/owainlewis/machinist/internal/config"
)

// Record inputs without copying or translating native definitions. Discovery
// and trust remain the harness's responsibility in the actual task worktree.
func nativeProvenance(directory, repository string, command config.ResolvedCommand, prompt string) error {
	type file struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}
	files := []file{}
	for _, pattern := range []string{".claude/agents/*.md", ".codex/agents/*.toml", ".claude/settings.json", ".codex/config.toml", "AGENTS.md", "CLAUDE.md"} {
		paths, err := filepath.Glob(filepath.Join(repository, pattern))
		if err != nil {
			return err
		}
		for _, path := range paths {
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() > 1<<20 {
				return fmt.Errorf("native configuration must be a regular file under 1 MiB: %s", path)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(body)
			relative, _ := filepath.Rel(repository, path)
			files = append(files, file{relative, hex.EncodeToString(sum[:])})
		}
	}
	trustPaths, err := nativeTrustPaths(context.Background(), repository)
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(map[string]any{"version": 1, "adapter": command.Adapter, "command": command.Command, "model": command.Model, "prompt": prompt, "definition": command.Definition, "command_hash": command.Hash, "repository_files": files, "trust_paths": trustPaths, "trust": "requires saved CLI trust for project configuration"}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "native-input.json"), body, 0600)
}

func nativeTrustPaths(ctx context.Context, repository string) ([]string, error) {
	command := exec.CommandContext(ctx, "git", "-C", repository, "rev-parse", "--path-format=absolute", "--git-common-dir")
	command.Env = SanitizedEnvironment(os.Environ())
	body, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("resolve native repository trust: %w", err)
	}
	common, err := filepath.EvalSymlinks(strings.TrimSpace(string(body)))
	if err != nil {
		return nil, err
	}
	paths := []string{repository}
	if filepath.Base(common) == ".git" {
		root := filepath.Dir(common)
		if root != repository {
			paths = append(paths, root)
		}
	}
	return paths, nil
}
