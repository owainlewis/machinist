package examples

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPRFeedbackLoopAddressesOneRoundThenStopsOnApproval runs the Python example
// against a local bare remote, a fake gh that reports one review thread and then an
// approval, and a fake agent that commits a file each time it is asked.
func TestPRFeedbackLoopAddressesOneRoundThenStopsOnApproval(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	directory := t.TempDir()
	remote := filepath.Join(directory, "remote.git")
	repository := filepath.Join(directory, "repository")
	bin := filepath.Join(directory, "bin")
	state := filepath.Join(directory, "gh-calls")
	for _, args := range [][]string{
		{"init", "--quiet", "--bare", "--initial-branch=main", remote},
		{"init", "--quiet", "--initial-branch=main", repository},
		{"-C", repository, "config", "user.email", "test@example.com"},
		{"-C", repository, "config", "user.name", "Test"},
		{"-C", repository, "commit", "--quiet", "--allow-empty", "-m", "initial"},
		{"-C", repository, "remote", "add", "origin", remote},
		{"-C", repository, "push", "--quiet", "-u", "origin", "main"},
	} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	// The fake gh answers by subcommand. GraphQL answers change with each call so the
	// first round carries a fresh review thread and the second an approval.
	gh := `#!/bin/sh
case "$1 $2" in
"repo view") printf '{"nameWithOwner":"owner/repo","defaultBranchRef":{"name":"main"}}\n' ;;
"api user") printf '{"login":"bot"}\n' ;;
"pr create") printf 'https://github.com/owner/repo/pull/7\n' ;;
"pr checks") printf '[{"name":"tests","bucket":"pass","link":""}]\n' ;;
"api graphql")
  count=0; [ ! -f "$GH_STATE" ] || count=$(cat "$GH_STATE"); count=$((count + 1)); printf '%s' "$count" > "$GH_STATE"
  if [ "$count" -eq 1 ]; then
    printf '%s\n' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"isResolved":false,"isOutdated":false,"path":"main.go","line":3,"comments":{"nodes":[{"author":{"login":"reviewer"},"body":"Handle the empty case","createdAt":"2999-01-01T00:00:00Z","url":"https://github.com/owner/repo/pull/7#r1"}]}}]},"reviews":{"nodes":[]}}}}}'
  else
    printf '%s\n' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]},"reviews":{"nodes":[{"author":{"login":"reviewer"},"state":"APPROVED","submittedAt":"2999-01-01T00:00:00Z"}]}}}}}'
  fi ;;
*) printf 'unexpected gh %s\n' "$*" >&2; exit 9 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o700); err != nil {
		t.Fatal(err)
	}
	agent := `#!/bin/sh
prompt=$(cat)
printf '%s\n' "$prompt" >> "$AGENT_LOG"
printf 'change\n' >> main.go
git add main.go && git commit --quiet -m "agent change"
`
	agentPath := filepath.Join(bin, "agent")
	if err := os.WriteFile(agentPath, []byte(agent), 0o700); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join("workflows", "pr-feedback-loop", "pr_feedback_loop.py"))
	if err != nil {
		t.Fatal(err)
	}
	agentLog := filepath.Join(directory, "agent.log")
	command := exec.Command(python, script)
	command.Dir = repository
	command.Stdin = bytes.NewBufferString("Add a --json flag")
	command.Env = append(os.Environ(),
		"PATH="+bin+":"+os.Getenv("PATH"),
		"GH_STATE="+state,
		"AGENT_LOG="+agentLog,
		"MACHINIST_AGENT_COMMAND="+agentPath,
		"MACHINIST_BASE_BRANCH=main",
		"MACHINIST_MAX_ROUNDS=2",
		"MACHINIST_FEEDBACK_WAIT=0",
		"MACHINIST_POLL_INTERVAL=0",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("feedback loop: %v: %s", err, output)
	}
	text := string(output)
	for _, want := range []string{"opened https://github.com/owner/repo/pull/7", "address feedback: round 1/2", "approved with green checks"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
	prompts, err := os.ReadFile(agentLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(prompts), "Add a --json flag") != 2 || !strings.Contains(string(prompts), "Handle the empty case") {
		t.Fatalf("agent prompts = %s", prompts)
	}
	pushed, err := exec.Command("git", "-C", remote, "log", "--oneline", "--all").Output()
	if err != nil || strings.Count(string(pushed), "agent change") != 2 {
		t.Fatalf("remote log = %s, %v", pushed, err)
	}
}
