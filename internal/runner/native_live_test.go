package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/owainlewis/machinist/internal/config"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Explicit opt-in: these smoke tests spend real provider credits on key credentials.
func TestNativeLiveRepositoryAgents(t *testing.T) {
	if os.Getenv("MACHINIST_NATIVE_LIVE") != "1" {
		t.Skip("key-credential smoke test is opt-in")
	}
	for _, adapter := range []string{"claude-stream", "codex-app-server"} {
		t.Run(adapter, func(t *testing.T) {
			root := t.TempDir()
			repo := filepath.Join(root, "repo")
			_ = os.Mkdir(repo, 0700)
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = repo
				if b, e := cmd.CombinedOutput(); e != nil {
					t.Fatalf("git: %v %s", e, b)
				}
			}
			git("init", "-q")
			git("config", "user.email", "fixture@example.invalid")
			git("config", "user.name", "Fixture")
			write := func(path, text string) {
				t.Helper()
				path = filepath.Join(repo, path)
				_ = os.MkdirAll(filepath.Dir(path), 0700)
				if e := os.WriteFile(path, []byte(text), 0600); e != nil {
					t.Fatal(e)
				}
			}
			write("fixture.txt", "ORANGE-17\n")
			write(".claude/agents/fixture_reader.md", "---\nname: fixture_reader\ndescription: Read the fixture marker for verification\ntools: Read\n---\nRead the file requested by the primary, defaulting to fixture.txt. If missing, return READ_FAILED. Otherwise return its marker and FILE_AGENT_29. No edits, commands, network, or descendants.\n")
			write(".codex/agents/fixture_reader.toml", "name = \"fixture_reader\"\ndescription = \"Read the fixture marker for verification\"\ndeveloper_instructions = \"Read the file requested by the primary using a read-only shell command, defaulting to fixture.txt. If missing, return READ_FAILED. Otherwise return its marker and FILE_AGENT_29. No edits, network, or descendants.\"\n")
			git("add", ".")
			git("commit", "-qm", "fixture")
			repo, _ = filepath.EvalSymlinks(repo)
			prompt := "Use exactly one native fixture_reader subagent to read fixture.txt. Wait for it. Do not read the fixture yourself. Do not edit files, use network, or spawn other agents. Finish with outcome no_change and put its marker and its configured verification suffix in the summary."
			if os.Getenv("MACHINIST_NATIVE_LIVE_EXTENDED") == "1" {
				prompt = "Use exactly two native fixture_reader subagents in parallel. Ask one to read fixture.txt and the other to read absent.txt. Wait for both. Recover from the missing file. Do not read files yourself, edit, use network or spawn further agents. Finish with outcome no_change and put the successful marker, the configured verification suffix, and READ_FAILED from the failed read in the summary."
			}
			home := filepath.Join(root, "home")
			codexHome := filepath.Join(home, ".codex")
			if err := os.MkdirAll(codexHome, 0700); err != nil {
				t.Fatal(err)
			}
			environment := []string{"HOME=" + home, "CODEX_HOME=" + codexHome, "PATH=" + os.Getenv("PATH")}
			command := config.ResolvedCommand{Name: "probe", Executor: "claude", Adapter: adapter, Timeout: 2 * time.Minute}
			if adapter == "claude-stream" {
				key := os.Getenv("ANTHROPIC_API_KEY")
				if key == "" {
					t.Skip("ANTHROPIC_API_KEY is required")
				}
				environment = append(environment, "ANTHROPIC_API_KEY="+key)
				command.Model = os.Getenv("MACHINIST_CLAUDE_LIVE_MODEL")
				command.Command = []string{"claude", "--print", "--verbose", "--output-format", "stream-json", "--include-partial-messages", "--dangerously-skip-permissions"}
			} else {
				key := os.Getenv("OPENAI_API_KEY")
				if key == "" {
					t.Skip("OPENAI_API_KEY is required")
				}
				login := exec.Command("codex", "login", "--with-api-key")
				login.Env = environment
				login.Stdin = strings.NewReader(key)
				if b, e := login.CombinedOutput(); e != nil {
					t.Fatalf("codex login: %v %s", e, b)
				}
				trust := fmt.Sprintf("[projects.%q]\ntrust_level = \"trusted\"\n", repo)
				if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(trust), 0600); err != nil {
					t.Fatal(err)
				}
				command.Executor = "codex"
				command.Model = os.Getenv("MACHINIST_CODEX_LIVE_MODEL")
				command.Command = []string{"codex", "app-server", "--stdio", "-c", "features.multi_agent=true", "-c", `sandbox_mode="danger-full-access"`}
			}
			result, e := Execute(context.Background(), Options{Environment: environment, Prompt: prompt, Command: command, Repository: repo, DataDirectory: filepath.Join(root, "data"), Stdout: io.Discard, Stderr: io.Discard})
			if e != nil {
				t.Fatalf("adapter failed: %v (log %s)", e, result.EventsPath)
			}
			body, _ := os.ReadFile(result.EventsPath)
			_ = os.WriteFile(filepath.Join(os.TempDir(), "machinist-live-"+adapter+".jsonl"), body, 0600)
			if result.Report == nil || !strings.Contains(result.FinalMessage, "ORANGE-17") || !strings.Contains(result.FinalMessage, "FILE_AGENT_29") {
				t.Fatalf("repo agent definition missing: %s", result.FinalMessage)
			}
			if os.Getenv("MACHINIST_NATIVE_LIVE_EXTENDED") == "1" {
				if !strings.Contains(result.FinalMessage, "READ_FAILED") {
					t.Fatalf("missing failure recovery: %s", result.FinalMessage)
				}
				ids := map[string]bool{}
				active := map[string]bool{}
				peak := 0
				firstActivity := int64(0)
				completedSequence := int64(0)
				for _, line := range strings.Split(string(body), "\n") {
					var e Event
					if json.Unmarshal([]byte(line), &e) == nil && e.Type == "run.completed" {
						completedSequence = e.Sequence
					}
					if json.Unmarshal([]byte(line), &e) == nil && e.Type == "agent.activity" {
						var a Activity
						_ = json.Unmarshal([]byte(e.Message), &a)
						ids[a.NativeID] = true
						if firstActivity == 0 {
							firstActivity = e.Sequence
						}
						if a.Status == "running" {
							active[a.NativeID] = true
						} else {
							delete(active, a.NativeID)
						}
						if len(active) > peak {
							peak = len(active)
						}
					}
				}
				if peak < 2 || firstActivity == 0 || completedSequence <= firstActivity {
					t.Fatalf("live parallel activity not observed before completion: peak=%d first=%d completed=%d", peak, firstActivity, completedSequence)
				}
				if len(ids) < 2 {
					t.Fatalf("missing repeated parallel child identities: %v", ids)
				}
			}
			body, _ = os.ReadFile(result.EventsPath)
			if !strings.Contains(string(body), "agent.activity") {
				t.Fatal("no live activity")
			}
			t.Logf("%s: repo child discovered, primary marker verified, activity spooled", adapter)
		})
	}
}
