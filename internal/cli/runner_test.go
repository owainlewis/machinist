package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/fakegh"
)

func writeConfigFile(t *testing.T, directory, name, body string) string {
	t.Helper()
	path := filepath.Join(directory, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeStartConfig(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	writeConfigFile(t, directory, "factory/presets/default.yaml", "description: Test.\nsteps:\n  - name: work\n    run: \"true\"\n")
	return writeConfigFile(t, directory, "config.yaml", "server:\n  listen: 127.0.0.1:0\n  database: tickets.db\n  factory: factory\n\ngithub:\n  handle: machinist-bot\n")
}

type cancelOnListen struct {
	mu     sync.Mutex
	body   bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelOnListen) Write(body []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.body.Write(body)
	if strings.Contains(w.body.String(), "listening on") {
		w.cancel()
	}
	return len(body), nil
}

func TestStartReportsListeningAfterBindAndStopsOnCancellation(t *testing.T) {
	fakegh.Install(t, "machinist-bot")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	stderr := &cancelOnListen{cancel: cancel}
	exitCode := Execute(ctx, []string{"start", "--config=" + writeStartConfig(t)}, strings.NewReader(""), &bytes.Buffer{}, stderr, "test")
	listening := regexp.MustCompile(`(?m)^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z machinist: control plane listening on 127\.0\.0\.1:\d+$`)
	if exitCode != 0 || !listening.MatchString(stderr.body.String()) {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.body.String())
	}
}

func TestStartRejectsInvalidFactory(t *testing.T) {
	directory := t.TempDir()
	path := writeConfigFile(t, directory, "config.yaml", "server:\n  factory: missing\n  database: tickets.db\n\ngithub:\n  handle: bot\n")
	var stderr bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"start", "--config=" + path}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode != 2 || !strings.Contains(stderr.String(), "load factory") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

const runnerConfigBody = "control_plane: http://127.0.0.1:1\ntoken_file: runner.token\nlogin: true\n"

func TestKubernetesRunnerValidatesItsConfiguration(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), "runner.yaml", "control_plane: http://127.0.0.1:1\ntoken_file: runner.token\nenv: [ANTHROPIC_API_KEY]\n\nkubernetes:\n  secret: s\n")
	var stderr bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"runner", "start", "--config=" + path}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode != 2 || !strings.Contains(stderr.String(), "explicit context") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func installFakeDockerCLI(t *testing.T, runtimes string) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
case "$1" in
  info)
    printf '%s' '` + runtimes + `'
    exit 0
    ;;
  run)
    if echo "$*" | grep -q -- "--entrypoint sh"; then
      printf 'claude\n'
      exit 0
    fi
    # Find exchange mount and write a succeeded result.json for runner check.
    exchange=""
    prev=""
    for arg in "$@"; do
      if [ "$prev" = "-v" ]; then
        case "$arg" in
          *:/exchange:rw) exchange="${arg%:/exchange:rw}" ;;
        esac
      fi
      prev="$arg"
    done
    if [ -n "$exchange" ]; then
      printf '{"State":"succeeded"}' > "$exchange/result.json"
    fi
    exit 0
    ;;
  rm)
    exit 0
    ;;
esac
exit 1
`
	dockerPath := filepath.Join(bin, "docker")
	if err := os.WriteFile(dockerPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestDockerRunnerStartAndCheckValidateRuntimeAndDefaults(t *testing.T) {
	t.Setenv("GH_TOKEN", "fixture")
	t.Setenv("ANTHROPIC_API_KEY", "fixture-key")

	t.Run("missing runsc fails on start and check", func(t *testing.T) {
		installFakeDockerCLI(t, `{"runc":{"path":"runc"}}`)
		dir := t.TempDir()
		writeConfigFile(t, dir, "runner.token", "token\n")
		path := writeConfigFile(t, dir, "runner.yaml", "control_plane: http://127.0.0.1:1\ntoken_file: runner.token\ndocker:\n  image: ghcr.io/owainlewis/machinist-runner:latest\n  runtime: runsc\n  env: [ANTHROPIC_API_KEY]\n")
		for _, args := range [][]string{
			{"runner", "start", "--config=" + path},
			{"runner", "check", "--repo=owner/repo", "--config=" + path},
		} {
			var stderr bytes.Buffer
			if exitCode := Execute(t.Context(), args, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode != 2 || !strings.Contains(stderr.String(), `"runsc" is not installed`) {
				t.Fatalf("args=%v exit=%d stderr=%q", args, exitCode, stderr.String())
			}
		}
	})

	t.Run("vanilla docker starts and checks with linux runc note", func(t *testing.T) {
		installFakeDockerCLI(t, `{"runc":{"path":"runc"}}`)
		dir := t.TempDir()
		writeConfigFile(t, dir, "runner.token", "token\n")
		path := writeConfigFile(t, dir, "runner.yaml", "control_plane: http://127.0.0.1:1\ntoken_file: runner.token\ndocker:\n  image: ghcr.io/owainlewis/machinist-runner:latest\n  env: [ANTHROPIC_API_KEY]\n")

		var stdout, stderr bytes.Buffer
		if exitCode := Execute(t.Context(), []string{"runner", "check", "--repo=owner/repo", "--config=" + path}, strings.NewReader(""), &stdout, &stderr, "test"); exitCode != 0 {
			t.Fatalf("check exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "ok: a step reached owner/repo, with 1 variables from env") {
			t.Fatalf("check stdout=%q", stdout.String())
		}
		if runtime.GOOS == "linux" && (!strings.Contains(stderr.String(), "runc") || !strings.Contains(stderr.String(), "runsc")) {
			t.Fatalf("expected Linux runc note in stderr, got %q", stderr.String())
		}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		startErr := &cancelOnConnect{cancel: cancel}
		if exitCode := Execute(ctx, []string{"runner", "start", "--config=" + path}, strings.NewReader(""), &bytes.Buffer{}, startErr, "test"); exitCode != 0 {
			t.Fatalf("start exit=%d stderr=%q", exitCode, startErr.String())
		}
	})

	t.Run("installed runsc succeeds on check", func(t *testing.T) {
		installFakeDockerCLI(t, `{"runc":{"path":"runc"},"runsc":{"path":"/usr/bin/runsc"}}`)
		dir := t.TempDir()
		writeConfigFile(t, dir, "runner.token", "token\n")
		path := writeConfigFile(t, dir, "runner.yaml", "control_plane: http://127.0.0.1:1\ntoken_file: runner.token\ndocker:\n  image: ghcr.io/owainlewis/machinist-runner:latest\n  runtime: runsc\n  env: [ANTHROPIC_API_KEY]\n")

		var stdout, stderr bytes.Buffer
		if exitCode := Execute(t.Context(), []string{"runner", "check", "--repo=owner/repo", "--config=" + path}, strings.NewReader(""), &stdout, &stderr, "test"); exitCode != 0 {
			t.Fatalf("check exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
		}
		if strings.Contains(stderr.String(), "default runtime (runc)") {
			t.Fatalf("unexpected runc note when runsc is configured: %q", stderr.String())
		}
	})
}

func TestRunnerStartReadsTokenFileAndStopsOnCancellation(t *testing.T) {
	t.Setenv("GH_TOKEN", "fixture")
	directory := t.TempDir()
	path := writeConfigFile(t, directory, "runner.yaml", runnerConfigBody+"\n")
	var stderr bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"runner", "start", "--config=" + path}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode != 2 || !strings.Contains(stderr.String(), "runner.token") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	writeConfigFile(t, directory, "runner.token", "token\n")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	stderr.Reset()
	if exitCode := Execute(ctx, []string{"runner", "start", "--config=" + path}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestRunnerCheckRequiresRepository(t *testing.T) {
	var stderr bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"runner", "check"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode != 2 || !strings.Contains(stderr.String(), "repo") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

// cancelOnConnect lets runner start finish without contacting a control plane.
type cancelOnConnect struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelOnConnect) Write(body []byte) (int, error) {
	n, err := w.Buffer.Write(body)
	if strings.Contains(w.String(), "runner connecting to") {
		w.cancel()
	}
	return n, err
}

func TestRunnerTokenScopeWarning(t *testing.T) {
	const noWorkflowWarning = "machinist: warning: the GitHub token has no workflow scope, so jobs cannot push changes to .github/workflows/; run gh auth refresh -s workflow\n"
	for _, command := range []string{"check", "start"} {
		for _, test := range []struct{ name, scopes, want string }{
			{"wide", "repo, workflow, delete_repo, admin:public_key", "machinist: warning: the GitHub token has extra scopes: delete_repo, admin:public_key; use a token limited to the repositories the bot works on\n"},
			{"narrow", "repo, workflow", ""},
			{"public", "public_repo, workflow", ""},
			{"no-workflow", "repo", noWorkflowWarning},
			{"wide-no-workflow", "repo, delete_repo", "machinist: warning: the GitHub token has extra scopes: delete_repo; use a token limited to the repositories the bot works on\n" + noWorkflowWarning},
			// GitHub App and fine-grained tokens report no scopes.
			{"fine-grained", "", ""},
			{"lookup-failure", "", ""},
		} {
			t.Run(command+"/"+test.name, func(t *testing.T) {
				github := fakegh.Install(t, "bot")
				if test.scopes != "" {
					github.SetScopes(test.scopes)
				}
				token := "ghp_secretNeverPrintThis"
				t.Setenv("GH_TOKEN", token)
				if test.name == "lookup-failure" {
					github.Fail("bad credentials: " + token)
				}
				directory := t.TempDir()
				path := writeConfigFile(t, directory, "runner.yaml", runnerConfigBody)
				writeConfigFile(t, directory, "runner.token", "runner-token\n")
				// Fail git locally so check cannot reach GitHub.
				git := writeConfigFile(t, directory, "bin/git", "#!/bin/sh\nexit 1\n")
				if err := os.Chmod(git, 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", filepath.Dir(git)+string(os.PathListSeparator)+os.Getenv("PATH"))
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				stderr := &cancelOnConnect{cancel: cancel}
				var stdout bytes.Buffer
				args := []string{"runner", command, "--config=" + path}
				if command == "check" {
					args = append(args, "--repo=owner/repo")
				}
				exitCode := Execute(ctx, args, strings.NewReader(""), &stdout, stderr, "test")
				if command == "start" && exitCode != 0 {
					t.Fatalf("exit = %d, stderr = %s", exitCode, stderr.String())
				}
				var warnings string
				for _, line := range strings.Split(stderr.String(), "\n") {
					if strings.Contains(line, "warning:") {
						warnings += line + "\n"
					}
				}
				if warnings != test.want {
					t.Fatalf("warnings = %q, want %q", warnings, test.want)
				}
				if strings.Contains(stderr.String()+stdout.String(), token) {
					t.Fatal("token printed")
				}
			})
		}
	}
}
