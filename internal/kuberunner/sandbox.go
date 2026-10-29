// Package kuberunner runs one native coding-agent attempt per Kubernetes Job.
// The connector retains GitHub and cluster authority outside the sandbox.
package kuberunner

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
)

const MaxBundle = 100 << 20
const MaxLog = 10 << 20

// The emptyDir mount itself is root-owned. Clone into a child created by the
// non-root init container so Git's ownership check also works for the agent.
const SourceDirectory = "/source/repository"

type Request struct {
	RunID          string                   `json:"run_id"`
	JobID          string                   `json:"job_id"`
	Branch         string                   `json:"branch"`
	Base           string                   `json:"base"`
	ReadOnly       bool                     `json:"read_only"`
	Command        config.ResolvedCommand   `json:"command"`
	Factory        *protocol.FactoryContext `json:"factory,omitempty"`
	Task           *protocol.Task           `json:"task,omitempty"`
	Revision       *protocol.Revision       `json:"revision,omitempty"`
	ReviewEvidence string                   `json:"review_evidence,omitempty"`
	Agent          string                   `json:"agent,omitempty"`
}

// ClaudeTokenFile is the Secret key holding a `claude setup-token` OAuth token.
const ClaudeTokenFile = "oauth-token"

// providerHost is the model endpoint the init container proves reachable.
func providerHost(agent string) string {
	if agent == "claude" {
		return "api.anthropic.com"
	}
	return "chatgpt.com"
}

type Output struct {
	Result runner.Result `json:"result"`
	Error  string        `json:"error,omitempty"`
	Head   string        `json:"head"`
	Dirty  bool          `json:"dirty"`
}

// Receive accepts only the two fixed input files, never arbitrary tar paths.
func Receive(input io.Reader, directory string) error {
	reader := tar.NewReader(io.LimitReader(input, MaxBundle+(2<<20)))
	seen := map[string]bool{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		limit := int64(MaxBundle)
		if header.Name == "request.json" {
			limit = 1 << 20
		}
		if (header.Name != "request.json" && header.Name != "input.bundle") || header.Typeflag != tar.TypeReg || seen[header.Name] || header.Size > limit || header.Size < 0 {
			return fmt.Errorf("invalid sandbox input archive")
		}
		seen[header.Name] = true
		file, err := os.OpenFile(filepath.Join(directory, header.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(file, reader, header.Size)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if len(seen) != 2 {
		return fmt.Errorf("sandbox input is incomplete")
	}
	return os.WriteFile(filepath.Join(directory, "ready"), []byte("ready"), 0600)
}

func readRequest(directory string) (Request, error) {
	var request Request
	body, err := os.ReadFile(filepath.Join(directory, "request.json"))
	if err != nil {
		return request, err
	}
	err = json.Unmarshal(body, &request)
	return request, err
}

func waitFile(ctx context.Context, path string) error {
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func git(ctx context.Context, directory string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)...)
	command.Dir = directory
	body, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("sandbox git: %w: %.2048s", err, body)
	}
	return body, nil
}

func Prepare(ctx context.Context, exchange, source string) error {
	if err := waitFile(ctx, filepath.Join(exchange, "ready")); err != nil {
		return err
	}
	r, err := readRequest(exchange)
	if err != nil {
		return err
	}
	if err = ProbeNetwork(ctx, providerHost(r.Agent)); err != nil {
		return err
	}
	if _, err = git(ctx, exchange, "clone", "--no-checkout", filepath.Join(exchange, "input.bundle"), source); err != nil {
		return err
	}
	if _, err = git(ctx, source, "checkout", "-B", r.Branch, r.Base); err != nil {
		return err
	}
	for _, args := range [][]string{{"remote", "remove", "origin"}, {"config", "user.name", "Machinist"}, {"config", "user.email", "machinist@localhost"}} {
		if _, err = git(ctx, source, args...); err != nil {
			return err
		}
	}
	return nil
}

// Sandbox runs inside the unprivileged container. Authentication is native
// subscription auth: a Codex managed account or a Claude setup-token. There is
// deliberately no API-key fallback.
func Sandbox(ctx context.Context, exchange, source, home, credentials string) error {
	r, err := readRequest(exchange)
	if err != nil {
		return err
	}
	if r.Agent == "claude" {
		err = prepareClaude(&r, home, credentials)
	} else {
		err = prepareCodex(&r, home, credentials)
	}
	if err != nil {
		return err
	}
	return execute(ctx, r, exchange, source)
}

// prepareClaude uses the long-lived token from `claude setup-token`. It does
// not refresh, so nothing is written back to the cluster Secret.
func prepareClaude(r *Request, home, credentials string) error {
	token, err := os.ReadFile(filepath.Join(credentials, ClaudeTokenFile))
	if err != nil {
		return fmt.Errorf("Claude account credential missing")
	}
	if err = ValidateClaudeToken(token); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(home, ".claude"), 0700); err != nil {
		return err
	}
	if err = os.Setenv("HOME", home); err != nil {
		return err
	}
	if err = os.Setenv("CLAUDE_CODE_OAUTH_TOKEN", strings.TrimSpace(string(token))); err != nil {
		return err
	}
	r.Command.Command = []string{"claude", "--print", "--verbose", "--output-format", "stream-json", "--dangerously-skip-permissions"}
	r.Command.Executor = "claude"
	if r.Command.Model != "" {
		r.Command.Command = append(r.Command.Command, "--model", r.Command.Model)
	}
	return nil
}

func prepareCodex(r *Request, home, credentials string) error {
	cache := filepath.Join(home, ".codex")
	if err := os.MkdirAll(cache, 0700); err != nil {
		return err
	}
	auth, err := os.ReadFile(filepath.Join(credentials, "auth.json"))
	if err != nil {
		return fmt.Errorf("Codex account credential missing")
	}
	if err = ValidateAuth(auth); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(cache, "auth.json"), auth, 0600); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(cache, "config.toml"), []byte("cli_auth_credentials_store = \"file\"\nforced_login_method = \"chatgpt\"\n"), 0600); err != nil {
		return err
	}
	if err = os.Setenv("CODEX_HOME", cache); err != nil {
		return err
	}
	if err = os.Setenv("HOME", home); err != nil {
		return err
	}
	r.Command.Command = []string{"codex", "exec", "--json", "--ephemeral", "--dangerously-bypass-approvals-and-sandbox", "-"}
	r.Command.Executor = "codex"
	if r.Command.Model != "" {
		r.Command.Command = append(r.Command.Command[:len(r.Command.Command)-1], "--model", r.Command.Model, "-")
	}
	return nil
}

func execute(ctx context.Context, r Request, exchange, source string) error {
	stdout, err := os.OpenFile(filepath.Join(exchange, "stdout.log"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(exchange, "stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer stderr.Close()
	runContext, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		if waitFile(runContext, filepath.Join(exchange, "stop")) == nil {
			stop()
		}
	}()
	result, runErr := runner.Execute(runContext, runner.Options{Command: r.Command, Factory: r.Factory, Task: r.Task, Revision: r.Revision, ReviewEvidence: r.ReviewEvidence, Workflow: r.Factory != nil, RunID: r.RunID, JobID: r.JobID, Repository: source, DataDirectory: filepath.Join(exchange, "runtime"), Stdout: &limitedWriter{writer: stdout, remaining: MaxLog}, Stderr: &limitedWriter{writer: stderr, remaining: MaxLog}})
	out := Output{Result: result}
	if runErr != nil {
		out.Error = runErr.Error()
	}
	head, headErr := git(ctx, source, "rev-parse", "HEAD")
	if headErr != nil {
		return headErr
	}
	out.Head = strings.TrimSpace(string(head))
	status, statusErr := git(ctx, source, "status", "--porcelain")
	if statusErr != nil {
		return statusErr
	}
	out.Dirty = len(status) > 0
	if out.Head != r.Base && !r.ReadOnly && !out.Dirty {
		if _, err = git(ctx, source, "bundle", "create", filepath.Join(exchange, "output.bundle"), "HEAD", "^"+r.Base); err != nil {
			return err
		}
	}
	body, _ := json.Marshal(out)
	if err = os.WriteFile(filepath.Join(exchange, "result.json"), body, 0600); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(exchange, "done"), []byte("done"), 0600); err != nil {
		return err
	}
	// Keep the pod alive until the connector has durably retained the refreshed
	// auth cache and result. Deadline/cancel still stops this Job.
	return waitFile(ctx, filepath.Join(exchange, "ack"))
}

func ValidateAuth(body []byte) error {
	var auth struct {
		Mode   string `json:"auth_mode"`
		Tokens struct {
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
		} `json:"tokens"`
	}
	if len(body) > 64<<10 || json.Unmarshal(body, &auth) != nil || auth.Mode != "chatgpt" || auth.Tokens.Access == "" || auth.Tokens.Refresh == "" {
		return fmt.Errorf("a dedicated Codex ChatGPT login with refresh credentials is required")
	}
	return nil
}

// ValidateClaudeToken accepts one bounded token line without whitespace.
func ValidateClaudeToken(body []byte) error {
	token := strings.TrimSpace(string(body))
	if token == "" || len(token) > 4<<10 || strings.ContainsAny(token, " \t\r\n") {
		return fmt.Errorf("a Claude token from `claude setup-token` is required")
	}
	return nil
}

type limitedWriter struct {
	writer    io.Writer
	remaining int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, fmt.Errorf("sandbox output limit exceeded")
	}
	n, e := w.writer.Write(p)
	w.remaining -= n
	return n, e
}
