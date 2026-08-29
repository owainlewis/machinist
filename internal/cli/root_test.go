package cli

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	machinistexamples "github.com/owainlewis/machinist/examples"
	"github.com/owainlewis/machinist/internal/config"
)

func TestInitInstallsCompleteEditableDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &stdout, &stderr, "test")
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	directory := filepath.Join(home, ".machinist")
	wantFiles := []string{
		"config.toml",
		"prompts/audit.md",
		"prompts/foreman.md",
		"prompts/shepherd.md",
		"server/worker.token",
		"worker.toml",
	}
	if got := regularFiles(t, directory); strings.Join(got, "\n") != strings.Join(wantFiles, "\n") {
		t.Fatalf("installed files = %#v, want %#v", got, wantFiles)
	}
	for _, name := range initialFiles {
		want, err := machinistexamples.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(directory, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read installed %s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("installed %s does not match its default", name)
		}
	}
	tokenPath := filepath.Join(directory, "server", "worker.token")
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(token)))
	if err != nil || len(decoded) != 32 {
		t.Fatalf("worker token is not 32 random bytes: %q, %v", token, err)
	}
	for _, name := range wantFiles {
		path := filepath.Join(directory, filepath.FromSlash(name))
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode for %s = %o, want 600", path, info.Mode().Perm())
		}
	}
	for _, path := range []string{directory, filepath.Join(directory, "prompts"), filepath.Join(directory, "server")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("mode for %s = %o, want 700", path, info.Mode().Perm())
		}
	}

	definition := filepath.Join(directory, "config.toml")
	definitions, err := config.LoadDefinitions(definition)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions.Commands) != 3 {
		t.Fatalf("installed definitions = commands %#v", definitions.Commands)
	}
	for _, name := range []string{"foreman", "audit", "shepherd"} {
		if _, err := config.LoadCommand(definition, name); err != nil {
			t.Fatalf("load installed agent %s: %v", name, err)
		}
	}
	worker, err := config.LoadWorker(filepath.Join(directory, "worker.toml"))
	if err != nil {
		t.Fatalf("load installed worker: %v", err)
	}
	if got := strings.Join(worker.Executors["codex"].Command, " "); !strings.Contains(got, "codex exec --json") {
		t.Fatalf("installed Codex executor does not request structured output: %q", got)
	}
	if !strings.Contains(stdout.String(), "created prompts/audit.md") || !strings.Contains(stdout.String(), "Add repositories to worker.toml") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestInitLeavesLegacyFactoryDirectoryUntouched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacyDirectory := filepath.Join(home, ".factory")
	if err := os.Mkdir(legacyDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyDirectory, "config.toml")
	legacyBody := []byte("incompatible legacy configuration\n")
	if err := os.WriteFile(legacyPath, legacyBody, 0o600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	got, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy configuration: %v", err)
	}
	if !bytes.Equal(got, legacyBody) {
		t.Fatalf("legacy configuration changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".machinist", "config.toml")); err != nil {
		t.Fatalf("Machinist configuration was not created: %v", err)
	}
}

func TestInitKeepsExistingFilesAndRestoresMissingDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("first init exit code = %d", exitCode)
	}
	directory := filepath.Join(home, ".machinist")
	for name, body := range map[string]string{
		"config.toml":         "custom config\n",
		"worker.toml":         "custom worker\n",
		"prompts/foreman.md":  "custom foreman\n",
		"prompts/plan.md":     "old plan\n",
		"prompts/build.md":    "old build\n",
		"prompts/verify.md":   "old verify\n",
		"prompts/custom.md":   "custom agent\n",
		"server/worker.token": "custom token\n",
	} {
		if err := os.WriteFile(filepath.Join(directory, filepath.FromSlash(name)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	auditPath := filepath.Join(directory, "prompts", "audit.md")
	if err := os.Remove(auditPath); err != nil {
		t.Fatal(err)
	}
	preserved := regularFileContents(t, directory)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &stdout, &stderr, "test"); exitCode != 0 {
		t.Fatalf("second init exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	for name, want := range preserved {
		got, err := os.ReadFile(filepath.Join(directory, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read preserved %s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("init changed existing file %s", name)
		}
	}
	audit, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	wantAudit, err := machinistexamples.Files.ReadFile("prompts/audit.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(audit, wantAudit) {
		t.Fatal("init failed to restore the missing audit default")
	}
	if !strings.Contains(stdout.String(), "kept prompts/foreman.md") || !strings.Contains(stdout.String(), "created prompts/audit.md") || !strings.Contains(stdout.String(), "kept server/worker.token") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func regularFiles(t *testing.T, root string) []string {
	t.Helper()
	files := []string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(relative))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func regularFileContents(t *testing.T, root string) map[string][]byte {
	t.Helper()
	contents := make(map[string][]byte)
	for _, name := range regularFiles(t, root) {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		contents[name] = body
	}
	return contents
}

func TestInitRejectsExistingNonFile(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "directory", setup: func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", setup: func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(target, []byte("custom\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "broken symlink", setup: func(t *testing.T, path string) {
			if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			directory := filepath.Join(home, ".machinist")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			test.setup(t, filepath.Join(directory, "config.toml"))

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &stdout, &stderr, "test")
			if exitCode != 2 || !strings.Contains(stderr.String(), "config.toml already exists and is not a regular file") {
				t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String(), "configuration is ready") {
				t.Fatalf("init reported success for an unusable configuration: %q", stdout.String())
			}
		})
	}
}

func TestInitRejectsSymlinkedSetupDirectories(t *testing.T) {
	for _, name := range []string{".machinist", ".machinist/prompts", ".machinist/server"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			path := filepath.Join(home, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			external := t.TempDir()
			if err := os.Symlink(external, path); err != nil {
				t.Fatal(err)
			}

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &stdout, &stderr, "test")
			if exitCode != 2 || !strings.Contains(stderr.String(), "already exists and is not a directory") {
				t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String(), "configuration is ready") {
				t.Fatalf("init reported success with symlinked setup directory: %q", stdout.String())
			}
			entries, err := os.ReadDir(external)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("init wrote outside its setup directory: %#v", entries)
			}
		})
	}
}

func TestWorkerRunInjectsInputIntoNamedPrompt(t *testing.T) {
	repository := newCLIRepository(t)
	workerConfig := writeCLIConfig(t, "success")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Execute(t.Context(), []string{
		"worker", "run",
		"--command=plan",
		"--prompt=fix issue 123",
		"--repo=" + repository,
		"--config=" + workerConfig,
	}, strings.NewReader("ignored"), &stdout, &stderr, "test")
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	if stdout.String() != "configured plan prompt\n\nPrompt:\nfix issue 123\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "succeeded; events:") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunExecutesOneAgent(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"run",
		"--command=plan",
		"--prompt=issue 42",
		"--repo=" + newCLIRepository(t),
		"--config=" + writeCLIConfig(t, "success"),
	}, strings.NewReader(""), &stdout, &stderr, "test")
	if exitCode != 0 || stdout.String() != "configured plan prompt\n\nPrompt:\nissue 42\n" {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
	}
}

func TestRunRejectsScheduleOnlyShepherd(t *testing.T) {
	workerConfig := writeCLIConfig(t, "success")
	for _, selection := range [][]string{{"--command=shepherd"}} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		args := append([]string{"run"}, selection...)
		args = append(args,
			"--prompt=clear the queue",
			"--repo="+newCLIRepository(t),
			"--config="+workerConfig,
		)
		exitCode := Execute(t.Context(), args, strings.NewReader(""), &stdout, &stderr, "test")
		if exitCode != 2 || !strings.Contains(stderr.String(), "scheduled Shepherd cannot run directly") {
			t.Fatalf("selection = %v, exit code = %d, stdout = %q, stderr = %q", selection, exitCode, stdout.String(), stderr.String())
		}
		if stdout.Len() != 0 {
			t.Fatalf("selection = %v executed before rejection: %q", selection, stdout.String())
		}
	}
}

func TestRunAllowsDisposableShepherdWithoutSchedule(t *testing.T) {
	workerConfig := writeCLIConfig(t, "success")
	definitionPath := filepath.Join(filepath.Dir(workerConfig), "config.toml")
	body, err := os.ReadFile(definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.ReplaceAll(body, []byte("\n[shepherd.test]\nrepository = \"test\"\nevery = \"15m\"\nmax_actions = 1\n"), nil)
	if err := os.WriteFile(definitionPath, body, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"run",
		"--command=shepherd",
		"--prompt=exercise a disposable queue",
		"--repo=" + newCLIRepository(t),
		"--config=" + workerConfig,
	}, strings.NewReader(""), &stdout, &stderr, "test")
	if exitCode != 0 || !strings.Contains(stdout.String(), "configured shepherd prompt") {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
	}
}

func TestSubmitQueuesAgentWithConfiguredBearerToken(t *testing.T) {
	var gotAuthorization string
	var gotRequest submitJobRequest
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/catalog":
			writeTestJSON(response, map[string]any{
				"commands": []string{"plan"}, "repositories": []string{"machinist"},
			})
		case "/api/v1/jobs":
			gotAuthorization = request.Header.Get("Authorization")
			if err := json.NewDecoder(request.Body).Decode(&gotRequest); err != nil {
				http.Error(response, err.Error(), http.StatusBadRequest)
				return
			}
			writeTestJSON(response, map[string]string{"id": "job_admitted"})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	workerConfig := writeSubmitWorkerConfig(t, server.URL, "secret")

	var stdout, stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"submit",
		"--command=plan",
		"--model=luna",
		"--prompt=fix issue 13",
		"--repo=machinist",
		"--config=" + workerConfig,
	}, strings.NewReader(""), &stdout, &stderr, "test")
	if exitCode != 0 || stdout.String() != "job_admitted\n" {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
	}
	if gotAuthorization != "Bearer secret" {
		t.Fatalf("authorization = %q", gotAuthorization)
	}
	if gotRequest != (submitJobRequest{Prompt: "fix issue 13", Repository: "machinist", Command: "plan", Model: "luna"}) {
		t.Fatalf("submission = %#v", gotRequest)
	}
}

func TestSubmitUsesCatalogWhenStatusHistoryIsLarge(t *testing.T) {
	var catalogRequested bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/status":
			writeTestJSON(response, map[string]any{
				"jobs": []any{map[string]string{"prompt": strings.Repeat("history ", 1<<18)}},
			})
		case "/api/v1/catalog":
			catalogRequested = true
			writeTestJSON(response, map[string]any{
				"commands": []string{"plan"}, "repositories": []string{"machinist"},
			})
		case "/api/v1/jobs":
			writeTestJSON(response, map[string]string{"id": "job_large_history"})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	workerConfig := writeSubmitWorkerConfig(t, server.URL, "secret")

	var stdout, stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"submit",
		"--command=plan",
		"--prompt=work despite history",
		"--repo=machinist",
		"--config=" + workerConfig,
	}, strings.NewReader(""), &stdout, &stderr, "test")
	if exitCode != 0 || stdout.String() != "job_large_history\n" || !catalogRequested {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q, catalog requested = %t", exitCode, stdout.String(), stderr.String(), catalogRequested)
	}
}

func TestSubmitRejectsUnknownValuesBeforeCreatingJob(t *testing.T) {
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/catalog" {
			writeTestJSON(response, map[string]any{
				"commands": []string{"plan"}, "repositories": []string{"machinist"},
			})
			return
		}
		if request.URL.Path == "/api/v1/jobs" {
			postCount++
		}
		http.NotFound(response, request)
	}))
	defer server.Close()
	workerConfig := writeSubmitWorkerConfig(t, server.URL, "secret")

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "command", args: []string{"--command=missing", "--prompt=work", "--repo=machinist"}, want: `command "missing" is not defined`},
		{name: "repository", args: []string{"--command=plan", "--prompt=work", "--repo=missing"}, want: `repository "missing" is not defined in the control plane`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			args := append(test.args, "--config="+workerConfig)
			if exitCode := Execute(t.Context(), append([]string{"submit"}, args...), strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode == 0 || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
			}
		})
	}
	if postCount != 0 {
		t.Fatalf("invalid submissions sent %d POST requests", postCount)
	}
}

func TestRunRequiresExactlyOneSelection(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--prompt=issue 42"},
	} {
		var stderr bytes.Buffer
		if exitCode := Execute(t.Context(), args, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode != 2 {
			t.Fatalf("args = %#v, exit code = %d, stderr = %q", args, exitCode, stderr.String())
		}
	}
}

func TestWorkerRunSelectsDifferentAgentPrompts(t *testing.T) {
	repository := newCLIRepository(t)
	workerConfig := writeCLIConfig(t, "success")
	for _, test := range []struct {
		agent string
		want  string
	}{
		{agent: "plan", want: "configured plan prompt\n"},
		{agent: "review", want: "configured review prompt\n"},
	} {
		t.Run(test.agent, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := Execute(t.Context(), []string{
				"worker", "run",
				"--command=" + test.agent,
				"--prompt=check ticket",
				"--repo=" + repository,
				"--config=" + workerConfig,
			}, strings.NewReader(""), &stdout, &stderr, "test")
			if exitCode != 0 || stdout.String() != test.want+"\nPrompt:\ncheck ticket\n" {
				t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
			}
		})
	}
}

func TestWorkerRunRejectsPositionalPrompt(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"worker", "run", "old task", "--command=plan", "--prompt=new task"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 2 || !strings.Contains(stderr.String(), "unknown command") && !strings.Contains(stderr.String(), "accepts 0 arg") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestWorkerRunRequiresPrompt(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"worker", "run", "--command=plan"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 2 || !strings.Contains(stderr.String(), "required flag(s) \"prompt\"") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestWorkerRunRejectsLegacyTaskFlag(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"worker", "run", "--command=plan", "--task=issue 42"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 2 || !strings.Contains(stderr.String(), "unknown flag: --task") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestWorkerRunRejectsLegacyFactoryConfigFlag(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"worker", "run", "--command=plan", "--prompt=issue 42", "--factory-config=legacy.toml"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 2 || !strings.Contains(stderr.String(), "unknown flag: --factory-config") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestWorkerRunRejectsEmptyPrompt(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"worker", "run",
		"--command=plan",
		"--prompt=   ",
		"--repo=" + newCLIRepository(t),
		"--config=" + writeCLIConfig(t, "success"),
	}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 2 || !strings.Contains(stderr.String(), "prompt is required") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestWorkerRunDoesNotEvaluatePromptAsTemplateOrShell(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "not-run")
	prompt := "fix $(touch " + marker + ") and preserve {{machinist.prompt}}\nsecond line"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"worker", "run",
		"--command=plan",
		"--prompt=" + prompt,
		"--repo=" + newCLIRepository(t),
		"--config=" + writeCLIConfig(t, "success"),
	}, strings.NewReader(""), &stdout, &stderr, "test")
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	want := "configured plan prompt\n\nPrompt:\n" + prompt + "\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("prompt text was evaluated as a shell command: %v", err)
	}
}

func TestWorkerRunReturnsAgentExitStatus(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"worker", "run",
		"--command=plan",
		"--prompt=fail this task",
		"--repo=" + newCLIRepository(t),
		"--config=" + writeCLIConfig(t, "fail"),
	}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 9 || !strings.Contains(stderr.String(), "status 9") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestWorkerRunSelectsConfiguredModelAlias(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"worker", "run",
		"--command=plan",
		"--model=luna",
		"--prompt=test model selection",
		"--repo=" + newCLIRepository(t),
		"--config=" + writeCLIConfig(t, "model"),
	}, strings.NewReader(""), &stdout, &stderr, "test")
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if stdout.String() != "gpt-test-luna\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestWorkerRunReturnsRuntimeFailureStatus(t *testing.T) {
	workerConfig := writeCLIConfig(t, "success")
	blockedDataDirectory := filepath.Join(filepath.Dir(workerConfig), "blocked-data")
	if err := os.WriteFile(blockedDataDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	workerBody := "data_directory = " + strconv.Quote(blockedDataDirectory) + "\n" +
		"\n" +
		"[executors.default]\n" +
		"command = [\"/bin/sh\", \"-c\", \"cat\"]\n"
	if err := os.WriteFile(workerConfig, []byte(workerBody), 0o600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"worker", "run",
		"--command=plan",
		"--prompt=exercise runtime failure",
		"--repo=" + newCLIRepository(t),
		"--config=" + workerConfig,
	}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 1 || !strings.Contains(stderr.String(), "create run directory") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestVersion(t *testing.T) {
	var stdout bytes.Buffer
	exitCode := Execute(t.Context(), []string{"version"}, strings.NewReader(""), &stdout, &bytes.Buffer{}, "1.2.3")
	if exitCode != 0 || stdout.String() != "1.2.3\n" {
		t.Fatalf("exit code = %d, stdout = %q", exitCode, stdout.String())
	}
}

func TestValidateAcceptsCompleteConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("init exit code = %d", exitCode)
	}
	addCLIWorkerRepository(t, home)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"validate"}, strings.NewReader(""), &stdout, &stderr, "test")
	if exitCode != 0 || strings.TrimSpace(stdout.String()) != "configuration is valid" {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
	}
}

func TestValidateAcceptsExplicitControlPlaneConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("init exit code = %d", exitCode)
	}
	addCLIWorkerRepository(t, home)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	configPath := filepath.Join(home, ".machinist", "config.toml")
	exitCode := Execute(t.Context(), []string{"validate", "--config=" + configPath}, strings.NewReader(""), &stdout, &stderr, "test")
	if exitCode != 0 || strings.TrimSpace(stdout.String()) != "configuration is valid" {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
	}
}

func TestValidateAcceptsSeparateWorkerConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("init exit code = %d", exitCode)
	}
	addCLIWorkerRepository(t, home)
	configPath := filepath.Join(home, ".machinist", "config.toml")
	workerPath := filepath.Join(t.TempDir(), "custom-worker.toml")
	workerBody, err := os.ReadFile(filepath.Join(home, ".machinist", "worker.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workerPath, workerBody, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, ".machinist", "worker.toml")); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"validate", "--config=" + configPath, "--worker-config=" + workerPath}, strings.NewReader(""), &stdout, &stderr, "test")
	if exitCode != 0 || strings.TrimSpace(stdout.String()) != "configuration is valid" {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
	}
}

func TestValidateRejectsInvalidUnusedCommand(t *testing.T) {
	for name, command := range map[string]string{
		"prompt":   "[commands.unused]\nexecutor = \"codex\"\nprompt_file = \"prompts/missing.md\"\n",
		"timeout":  "[commands.unused]\nexecutor = \"codex\"\ntimeout = \"never\"\n",
		"executor": "[commands.unused]\nexecutor = \"missing\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
				t.Fatalf("init exit code = %d", exitCode)
			}
			addCLIWorkerRepository(t, home)
			configPath := filepath.Join(home, ".machinist", "config.toml")
			file, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString("\n" + command); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}

			var stderr bytes.Buffer
			exitCode := Execute(t.Context(), []string{"validate"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
			if exitCode != 2 || !strings.Contains(stderr.String(), "command \"unused\"") {
				t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
			}
		})
	}
}

func TestValidateRejectsTriggerModelUnsupportedByWorkerExecutor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("init exit code = %d", exitCode)
	}
	addCLIWorkerRepository(t, home)
	configPath := filepath.Join(home, ".machinist", "config.toml")
	file, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`
[github.repositories]
example = "owner/example"

[triggers.interval.unsupported-model]
every = "1h"
repository = "example"
command = "audit"
model = "missing"
prompt = "Audit the repository."
`); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"validate"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 2 || !strings.Contains(stderr.String(), `trigger "interval/unsupported-model"`) || !strings.Contains(stderr.String(), `model "missing" is not configured for executor "codex"`) {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestValidateRejectsManagedWorkloadRepositoryMissingFromWorker(t *testing.T) {
	for name, definition := range map[string]string{
		"shepherd schedule": `
[shepherd.unconfigured]
repository = "unconfigured"
every = "1h"
max_actions = 1
`,
		"interval trigger": `
[github.repositories]
unconfigured = "owner/repository"

[triggers.interval.unconfigured]
every = "1h"
repository = "unconfigured"
command = "audit"
prompt = "Audit the repository."
`,
		"cron trigger": `
[github.repositories]
unconfigured = "owner/repository"

[triggers.cron.unconfigured]
schedule = "0 * * * *"
timezone = "UTC"
repository = "unconfigured"
command = "audit"
prompt = "Audit the repository."
`,
		"github trigger": `
[github.repositories]
unconfigured = "owner/repository"

[triggers.github.unconfigured]
every = "1h"
label = "machinist:requested"
command = "audit"
`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
				t.Fatalf("init exit code = %d", exitCode)
			}
			addCLIWorkerRepository(t, home)
			configPath := filepath.Join(home, ".machinist", "config.toml")
			file, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString(definition); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}

			var stderr bytes.Buffer
			exitCode := Execute(t.Context(), []string{"validate"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
			if exitCode != 2 || !strings.Contains(stderr.String(), `repository "unconfigured" is not configured on this worker`) {
				t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
			}
		})
	}
}

func TestValidateRejectsInvalidControlPlaneConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("init exit code = %d", exitCode)
	}
	configPath := filepath.Join(home, ".machinist", "config.toml")
	if err := os.WriteFile(configPath, []byte("[server]\nworker_token_file = \"server/worker.token\"\nmax_concurrent_jobs = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"validate"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 2 || !strings.Contains(stderr.String(), "max_concurrent_jobs must be positive") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestValidateRejectsInvalidControlPlaneListenAddress(t *testing.T) {
	for _, listen := range []string{"not an address", "0.0.0.0:7331"} {
		t.Run(listen, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
				t.Fatalf("init exit code = %d", exitCode)
			}
			addCLIWorkerRepository(t, home)
			configPath := filepath.Join(home, ".machinist", "config.toml")
			body, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.Replace(body, []byte(`listen = "127.0.0.1:7331"`), []byte("listen = "+strconv.Quote(listen)), 1)
			if err := os.WriteFile(configPath, body, 0o600); err != nil {
				t.Fatal(err)
			}

			var stderr bytes.Buffer
			exitCode := Execute(t.Context(), []string{"validate"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
			if exitCode != 2 || !strings.Contains(stderr.String(), "listen address") {
				t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
			}
		})
	}
}

func TestValidateRejectsWorkerEndpointThatCannotReachLocalControlPlane(t *testing.T) {
	for name, endpoint := range map[string]string{
		"different port": "http://127.0.0.1:7332",
		"different host": "http://127.0.0.2:7331",
		"HTTPS endpoint": "https://127.0.0.1:7331",
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
				t.Fatalf("init exit code = %d", exitCode)
			}
			addCLIWorkerRepository(t, home)
			workerPath := filepath.Join(home, ".machinist", "worker.toml")
			body, err := os.ReadFile(workerPath)
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.Replace(body, []byte(`url = "http://127.0.0.1:7331"`), []byte("url = "+strconv.Quote(endpoint)), 1)
			if err := os.WriteFile(workerPath, body, 0o600); err != nil {
				t.Fatal(err)
			}

			var stderr bytes.Buffer
			exitCode := Execute(t.Context(), []string{"validate"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
			if exitCode != 2 || !strings.Contains(stderr.String(), "configured local control plane") {
				t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
			}
		})
	}
}

func TestValidateWorkerControlPlaneAcceptsEquivalentIPv6LoopbackSpelling(t *testing.T) {
	if err := validateWorkerControlPlane("[::1]:7331", "http://[0:0:0:0:0:0:0:1]:7331"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsMismatchedServerAndWorkerTokens(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("init exit code = %d", exitCode)
	}
	addCLIWorkerRepository(t, home)
	workerPath := filepath.Join(home, ".machinist", "worker.toml")
	workerBody, err := os.ReadFile(workerPath)
	if err != nil {
		t.Fatal(err)
	}
	workerBody = bytes.Replace(workerBody, []byte(`token_file = "~/.machinist/server/worker.token"`), []byte(`token_file = "worker.token"`), 1)
	if err := os.WriteFile(workerPath, workerBody, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".machinist", "worker.token"), []byte("different-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"validate"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 2 || !strings.Contains(stderr.String(), "authentication tokens do not match") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestValidateRejectsInvalidWorkerConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("init exit code = %d", exitCode)
	}

	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"validate"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 2 || !strings.Contains(stderr.String(), "requires at least one executor and repository") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestWorkerValidateRequiresRepository(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("init exit code = %d", exitCode)
	}

	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"worker", "validate"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 2 || !strings.Contains(stderr.String(), "requires at least one executor and repository") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestWorkerValidateAcceptsCompleteConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("init exit code = %d", exitCode)
	}
	addCLIWorkerRepository(t, home)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"worker", "validate"}, strings.NewReader(""), &stdout, &stderr, "test")
	if exitCode != 0 || strings.TrimSpace(stdout.String()) != "worker configuration is valid" {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
	}
}

func addCLIWorkerRepository(t *testing.T, home string) {
	t.Helper()
	workerPath := filepath.Join(home, ".machinist", "worker.toml")
	file, err := os.OpenFile(workerPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n[repositories.example]\npath = \"" + filepath.ToSlash(t.TempDir()) + "\"\n"); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("close worker configuration after write failure: %v", closeErr)
		}
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStartReportsListeningAfterBindAndStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	stderr := &cancelWriter{cancel: cancel}
	exitCode := Execute(ctx, []string{
		"start",
		"--config=" + writeStartConfig(t),
		"--listen=127.0.0.1:0",
	}, strings.NewReader(""), &bytes.Buffer{}, stderr, "test")
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	const prefix = "machinist: control plane listening on http://"
	if !strings.HasPrefix(stderr.String(), prefix) || !strings.HasSuffix(stderr.String(), "\n") || strings.Count(stderr.String(), "\n") != 1 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	address := strings.TrimSuffix(strings.TrimPrefix(stderr.String(), prefix), "\n")
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || port == "0" {
		t.Fatalf("reported address = %q, parse error = %v", address, err)
	}
	rebound, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("reported address %s was not released: %v", address, err)
	}
	rebound.Close()
}

func TestStartDoesNotReportListeningWhenAddressIsInUse(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"start",
		"--config=" + writeStartConfig(t),
		"--listen=" + occupied.Addr().String(),
	}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 2 || !strings.Contains(stderr.String(), "listen on "+occupied.Addr().String()) {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if strings.Contains(stderr.String(), "control plane listening") {
		t.Fatalf("start reported listening after bind failed: %q", stderr.String())
	}
}

type cancelWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelWriter) Write(body []byte) (int, error) {
	w.cancel()
	return w.Buffer.Write(body)
}

func writeStartConfig(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "worker.token"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "config.toml")
	body := "[server]\n" +
		"database = \"machinist.db\"\n" +
		"worker_token_file = \"worker.token\"\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func writeCLIConfig(t *testing.T, mode string) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "plan.md"), []byte("configured plan prompt\n\nPrompt:\n{{machinist.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "review.md"), []byte("configured review prompt\n\nPrompt:\n{{machinist.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "build.md"), []byte("configured build prompt\n\nPrompt:\n{{machinist.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "verify.md"), []byte("configured verify prompt\n\nPrompt:\n{{machinist.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "shepherd.md"), []byte("configured shepherd prompt\n\nPrompt:\n{{machinist.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definition := filepath.Join(directory, "config.toml")
	script := "cat"
	if mode == "fail" {
		script = "exit 9"
	}
	if mode == "model" {
		script = `printf '%s\n' "${0#--model=}"; cat >/dev/null`
	}
	buildScript := script
	if mode == "fail-build" {
		buildScript = "cat >/dev/null; exit 9"
	}
	definitionBody := "[commands.plan]\n" +
		"executor = \"default\"\n" +
		"prompt_file = \"plan.md\"\n" +
		"timeout = \"5s\"\n\n" +
		"[commands.review]\n" +
		"executor = \"default\"\n" +
		"prompt_file = \"review.md\"\n" +
		"timeout = \"5s\"\n\n" +
		"[commands.build]\n" +
		"executor = \"build\"\n" +
		"prompt_file = \"build.md\"\n" +
		"timeout = \"5s\"\n\n" +
		"[commands.verify]\n" +
		"executor = \"default\"\n" +
		"prompt_file = \"verify.md\"\n" +
		"timeout = \"5s\"\n\n" +
		"[commands.shepherd]\n" +
		"executor = \"default\"\n" +
		"prompt_file = \"shepherd.md\"\n" +
		"timeout = \"5s\"\n\n" +
		"[shepherd.test]\n" +
		"repository = \"test\"\n" +
		"every = \"15m\"\n" +
		"max_actions = 1\n"
	if err := os.WriteFile(definition, []byte(definitionBody), 0o600); err != nil {
		t.Fatal(err)
	}
	worker := filepath.Join(directory, "worker.toml")
	defaultCommand := "command = [\"/bin/sh\", \"-c\", " + strconv.Quote(script) + "]\n"
	if mode == "model" {
		defaultCommand = "command = [\"/bin/sh\", \"-c\", " + strconv.Quote(script) + ", \"--model={{machinist.model}}\"]\n" +
			"models = { luna = \"gpt-test-luna\" }\n"
	}
	workerBody := "data_directory = \"" + filepath.ToSlash(filepath.Join(directory, "data")) + "\"\n" +
		"\n" +
		"[executors.default]\n" +
		defaultCommand + "\n" +
		"[executors.build]\n" +
		"command = [\"/bin/sh\", \"-c\", " + strconv.Quote(buildScript) + "]\n"
	if err := os.WriteFile(worker, []byte(workerBody), 0o600); err != nil {
		t.Fatal(err)
	}
	return worker
}

func writeSubmitWorkerConfig(t *testing.T, endpoint, token string) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "token"), []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "worker.toml")
	body := "[control_plane]\nurl = " + strconv.Quote(endpoint) + "\ntoken_file = \"token\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeTestJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(value)
}

func newCLIRepository(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	command := exec.Command("git", "init", "--quiet", directory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	root, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	return root
}
