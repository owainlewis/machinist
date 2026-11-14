package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func workspaceGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func newRemote(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "origin.git")
	workspaceGit(t, root, "init", "--quiet", "--bare", "--initial-branch=main", remote)
	seed := filepath.Join(root, "seed")
	workspaceGit(t, root, "init", "--quiet", "--initial-branch=main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, seed, "add", ".")
	workspaceGit(t, seed, "commit", "--quiet", "-m", "init")
	workspaceGit(t, seed, "push", "--quiet", remote, "main")
	return remote
}

func workspaceOptions(t *testing.T, remote, harness string) WorkspaceOptions {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	return WorkspaceOptions{
		Request:     RunRequest{Repository: "owner/repo", Branch: "machinist/t_0123456789abcdef", Harness: harness, GitName: "machinist-bot", GitEmail: "machinist-bot@users.noreply.github.com"},
		Root:        root,
		CloneURL:    remote,
		GitHubToken: "fixture-token",
		Environment: []string{"PATH=" + os.Getenv("PATH")},
		Home:        home,
		CodexHome:   filepath.Join(home, ".codex"),
	}
}

func TestPrepareWorkspaceStartsNewBranchWithIdentityAndHiddenAgents(t *testing.T) {
	options := workspaceOptions(t, newRemote(t), HarnessClaude)
	options.Request.AgentFiles = []AgentFile{{Path: ".claude/agents/machinist-review.md", Body: "review"}, {Path: ".codex/agents/machinist-review.toml", Body: "name = \"x\""}}
	workspace, err := PrepareWorkspace(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	repository := workspace.Repository
	if branch := workspaceGit(t, repository, "branch", "--show-current"); branch != options.Request.Branch {
		t.Fatalf("branch = %q", branch)
	}
	if name := workspaceGit(t, repository, "config", "user.name"); name != "machinist-bot" {
		t.Fatalf("name = %q", name)
	}
	if body, err := os.ReadFile(filepath.Join(repository, ".claude", "agents", "machinist-review.md")); err != nil || string(body) != "review" {
		t.Fatalf("agent file = %q, %v", body, err)
	}
	if status := workspaceGit(t, repository, "status", "--porcelain"); status != "" {
		t.Fatalf("agent files are visible to Git: %q", status)
	}
	joined := strings.Join(workspace.Environment, "\n")
	for _, want := range []string{"HOME=" + options.Home, "CODEX_HOME=" + options.CodexHome, "GH_TOKEN=fixture-token", "GIT_CONFIG_GLOBAL=/dev/null"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %s", want, joined)
		}
	}
	if _, err := os.Stat(filepath.Join(options.CodexHome, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("Claude run wrote Codex configuration: %v", err)
	}
}

func TestPrepareWorkspaceResumesPushedBranch(t *testing.T) {
	remote := newRemote(t)
	options := workspaceOptions(t, remote, HarnessClaude)
	seed := t.TempDir()
	workspaceGit(t, seed, "clone", "--quiet", remote, "work")
	work := filepath.Join(seed, "work")
	workspaceGit(t, work, "checkout", "--quiet", "-b", options.Request.Branch)
	workspaceGit(t, work, "commit", "--quiet", "--allow-empty", "-m", "earlier work")
	workspaceGit(t, work, "push", "--quiet", "origin", options.Request.Branch)

	workspace, err := PrepareWorkspace(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if subject := workspaceGit(t, workspace.Repository, "log", "-1", "--format=%s"); subject != "earlier work" {
		t.Fatalf("head = %q", subject)
	}
}

func TestPrepareWorkspaceRejectsUnsafeAgentFiles(t *testing.T) {
	for _, path := range []string{"../escape.md", ".claude/agents/../../escape.md", "src/main.go", "/etc/passwd", ".claude/settings.json"} {
		options := workspaceOptions(t, newRemote(t), HarnessClaude)
		options.Request.AgentFiles = []AgentFile{{Path: path, Body: "x"}}
		if _, err := PrepareWorkspace(t.Context(), options); err == nil || !strings.Contains(err.Error(), "invalid agent file") {
			t.Fatalf("%s: error = %v", path, err)
		}
	}
}

func TestPrepareWorkspaceTrustsCloneAndLogsInToCodex(t *testing.T) {
	bin := t.TempDir()
	received := filepath.Join(bin, "received")
	script := "#!/bin/sh\ntest \"$1 $2\" = 'login --with-api-key' || exit 9\ncat > " + received + "\necho \"$CODEX_HOME\" >> " + received + "\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	options := workspaceOptions(t, newRemote(t), HarnessCodex)
	options.CodexAPIKey = "sk-fixture"
	options.Environment = []string{"PATH=" + os.Getenv("PATH")}
	workspace, err := PrepareWorkspace(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(options.CodexHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`cli_auth_credentials_store = "file"`, `[projects."` + workspace.Repository + `"]`, `trust_level = "trusted"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	login, err := os.ReadFile(received)
	if err != nil || string(login) != "sk-fixture"+options.CodexHome+"\n" {
		t.Fatalf("login input = %q, %v", login, err)
	}
	if strings.Contains(strings.Join(workspace.Environment, "\n"), "sk-fixture") {
		t.Fatal("key entered the agent environment")
	}
}

func TestPrepareWorkspaceLoginAppendsToExistingCodexConfig(t *testing.T) {
	options := workspaceOptions(t, newRemote(t), HarnessCodex)
	options.Login = true
	if err := os.MkdirAll(options.CodexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(options.CodexHome, "config.toml")
	if err := os.WriteFile(path, []byte("model = \"mine\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := PrepareWorkspace(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(body), "model = \"mine\"\n") || !strings.Contains(string(body), workspace.Repository) || strings.Contains(string(body), "cli_auth_credentials_store") {
		t.Fatalf("config = %s", body)
	}
}
