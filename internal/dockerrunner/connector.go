package dockerrunner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/githubapp"
	"github.com/owainlewis/machinist/internal/harness"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
)

const (
	defaultSetupAllowance = 2 * time.Minute
	defaultCancelGrace    = 5 * time.Second
	cleanupTimeout        = 30 * time.Second
	runtimeCheckTimeout   = 30 * time.Second
	probeTimeout          = 30 * time.Second
	defaultTailInterval   = 100 * time.Millisecond
	maxResultBytes        = 1<<20 + 3*protocol.MaxPatchBytes
	maxEventLineBytes     = 1 << 20
	maxDiagnosticBytes    = 4096
	defaultNonRootUID     = 1000
	defaultNonRootGID     = 1000
)

// RunDirs holds the isolated per-run host directories mounted into a container.
type RunDirs struct {
	Workspace string
	Control   string
	Home      string
	Tmp       string
	Exchange  string
}

type containerProcess interface {
	Wait() error
	Kill() error
	Stderr() string
}

type execProcess struct {
	cmd    *exec.Cmd
	stderr *cappedBuffer
}

func (p *execProcess) Wait() error { return p.cmd.Wait() }

func (p *execProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

func (p *execProcess) Stderr() string { return strings.TrimSpace(p.stderr.String()) }

// Connector runs each job in its own hardened, ephemeral Docker container.
type Connector struct {
	Config config.Docker
	// Env names the host environment variables forwarded to each container.
	Env []string

	// Test hooks. Production code leaves these nil.
	runCmd         func(ctx context.Context, env []string, limit int, args ...string) ([]byte, error)
	startContainer func(env []string, args ...string) (containerProcess, error)
	getenv         func(string) string
	hostEnv        func() []string
	goos           string
	user           func() (string, int, int)
	tailInterval   time.Duration
	setupAllowance time.Duration
	cancelGrace    time.Duration
}

func (c Connector) getEnv(name string) string {
	if c.getenv != nil {
		return c.getenv(name)
	}
	return os.Getenv(name)
}

func (c Connector) baseEnv() []string {
	if c.hostEnv != nil {
		return c.hostEnv()
	}
	return os.Environ()
}

func (c Connector) osName() string {
	if c.goos != "" {
		return c.goos
	}
	return runtime.GOOS
}

func (c Connector) containerUser() (string, int, int) {
	if c.user != nil {
		return c.user()
	}
	return defaultContainerUser()
}

func defaultContainerUser() (string, int, int) {
	uid, gid := os.Getuid(), os.Getgid()
	if uid <= 0 {
		return fmt.Sprintf("%d:%d", defaultNonRootUID, defaultNonRootGID), defaultNonRootUID, defaultNonRootGID
	}
	return fmt.Sprintf("%d:%d", uid, gid), uid, gid
}

func (c Connector) command(ctx context.Context, env []string, limit int, args ...string) ([]byte, error) {
	if c.runCmd != nil {
		return c.runCmd(ctx, env, limit, args...)
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	if env != nil {
		cmd.Env = env
	}
	var out bytes.Buffer
	cmd.Stdout = &limitedWriter{writer: &out, remaining: limit}
	var stderr cappedBuffer
	stderr.limit = maxDiagnosticBytes
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("docker command interrupted: %w", ctx.Err())
		}
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return nil, fmt.Errorf("%w: %s", err, detail)
		}
		return nil, err
	}
	return out.Bytes(), nil
}

func (c Connector) startRun(env []string, args ...string) (containerProcess, error) {
	if c.startContainer != nil {
		return c.startContainer(env, args...)
	}
	cmd := exec.Command("docker", args...)
	cmd.Env = env
	stderr := &cappedBuffer{limit: maxDiagnosticBytes}
	cmd.Stdout = io.Discard
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProcess{cmd: cmd, stderr: stderr}, nil
}

// CheckRuntime verifies that Docker is reachable and, when docker.runtime is
// configured, that Docker reports that runtime installed. When runtime is unset
// on Linux, it logs a note that vanilla runc is in use and mentions runsc.
func (c Connector) CheckRuntime(ctx context.Context, log io.Writer) error {
	cfg := c.Config.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, runtimeCheckTimeout)
	defer cancel()
	out, err := c.command(checkCtx, nil, 1<<20, "info", "--format", "{{json .Runtimes}}")
	if err != nil {
		return fmt.Errorf("docker is not ready: %w (ensure the Docker daemon is running and accessible)", err)
	}
	var runtimes map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(out), &runtimes); err != nil {
		return fmt.Errorf("parse docker runtimes from docker info: %w", err)
	}
	if cfg.Runtime != "" {
		if _, ok := runtimes[cfg.Runtime]; !ok {
			available := make([]string, 0, len(runtimes))
			for name := range runtimes {
				available = append(available, name)
			}
			slices.Sort(available)
			availText := "none"
			if len(available) > 0 {
				availText = strings.Join(available, ", ")
			}
			if cfg.Runtime == "runsc" {
				return fmt.Errorf("docker runtime %q is not installed (available runtimes: %s); install gVisor (runsc) and register it in /etc/docker/daemon.json", cfg.Runtime, availText)
			}
			return fmt.Errorf("docker runtime %q is not installed (available runtimes: %s)", cfg.Runtime, availText)
		}
		return nil
	}
	if c.osName() == "linux" && log != nil {
		fmt.Fprintln(log, "machinist: note: Docker runner using default runtime (runc); set docker.runtime: runsc (gVisor) for stronger isolation on untrusted repositories")
	}
	return nil
}

// ProbeHarnesses inspects the configured Docker image in a locked-down
// ephemeral container to discover which supported coding harnesses are
// installed on PATH.
func (c Connector) ProbeHarnesses(ctx context.Context) ([]string, error) {
	cfg := c.Config.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	user, _, _ := c.containerUser()
	names := harness.Names()
	script := fmt.Sprintf(`for h in %s; do if command -v "$h" >/dev/null 2>&1; then printf '%%s\n' "$h"; fi; done`, strings.Join(names, " "))
	args := []string{
		"run",
		"--rm",
		"--user", user,
		"--read-only",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		"--pids-limit=64",
		"--memory=256m",
		"--network=none",
	}
	if cfg.Runtime != "" {
		args = append(args, "--runtime="+cfg.Runtime)
	}
	args = append(args, "--entrypoint", "sh", cfg.Image, "-c", script)
	out, err := c.command(probeCtx, nil, 64<<10, args...)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	found := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if _, ok := harness.Find(name); !ok {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		found = append(found, name)
	}
	slices.Sort(found)
	return found, nil
}

// ContainerName returns the deterministic Docker container name for a run ID.
func ContainerName(runID string) string {
	return "machinist-" + runID
}

// BuildRunArgs assembles the hardened docker run argument list and the process
// environment for the docker CLI. Secret values (GH_TOKEN, configured env vars,
// and proxy URLs with embedded credentials) are passed via process environment
// with bare "-e NAME" flags so they never appear in argv.
func (c Connector) BuildRunArgs(containerName string, dirs RunDirs, token, user string) ([]string, []string) {
	cfg := c.Config.WithDefaults()
	args := []string{
		"run",
		"--rm",
		"--name", containerName,
		"--label", "machinist.managed-by=machinist",
		"--label", "machinist.run-id=" + strings.TrimPrefix(containerName, "machinist-"),
		"--user", user,
		"--read-only",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		"--pids-limit=" + strconv.Itoa(cfg.PIDsLimit),
		"--memory=" + cfg.Memory,
		"--cpus=" + strconv.FormatFloat(cfg.CPUs, 'f', -1, 64),
	}
	if cfg.Runtime != "" {
		args = append(args, "--runtime="+cfg.Runtime)
	}
	if cfg.Network != "" {
		args = append(args, "--network="+cfg.Network)
	}
	args = append(args,
		"-v", dirs.Workspace+":"+WorkDirectory+"/workspace:rw",
		"-v", dirs.Control+":"+WorkDirectory+"/control:rw",
		"-v", dirs.Home+":"+HomeDirectory+":rw",
		"-v", dirs.Tmp+":/tmp:rw",
		"-v", dirs.Exchange+":"+ExchangeDirectory+":rw",
		"-w", WorkDirectory+"/workspace",
		"-e", "HOME="+HomeDirectory,
		"-e", "CODEX_HOME="+HomeDirectory+"/.codex",
		"-e", "TMPDIR=/tmp",
		"-e", "GIT_CONFIG_GLOBAL=/dev/null",
		"-e", "GIT_CONFIG_NOSYSTEM=1",
		"-e", "GIT_TERMINAL_PROMPT=0",
	)

	overrides := map[string]string{}
	if cfg.ProxyURL != "" {
		parsed, err := url.Parse(cfg.ProxyURL)
		if err == nil && parsed.User != nil {
			for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
				args = append(args, "-e", key)
				overrides[key] = cfg.ProxyURL
			}
		} else {
			args = append(args,
				"-e", "HTTP_PROXY="+cfg.ProxyURL,
				"-e", "HTTPS_PROXY="+cfg.ProxyURL,
				"-e", "http_proxy="+cfg.ProxyURL,
				"-e", "https_proxy="+cfg.ProxyURL,
			)
		}
		args = append(args,
			"-e", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
			"-e", "DISABLE_AUTOUPDATER=1",
		)
	}

	if token != "" {
		args = append(args, "-e", "GH_TOKEN")
		overrides["GH_TOKEN"] = token
	}
	seenEnv := map[string]struct{}{"GH_TOKEN": {}, "GITHUB_TOKEN": {}}
	for _, name := range c.Env {
		if _, dup := seenEnv[name]; dup {
			continue
		}
		seenEnv[name] = struct{}{}
		if value := c.getEnv(name); value != "" {
			args = append(args, "-e", name)
			overrides[name] = value
		}
	}

	args = append(args, "--entrypoint", "machinist", cfg.Image, "sandbox", "docker")
	return args, mergeEnv(c.baseEnv(), overrides)
}

func mergeEnv(base []string, overrides map[string]string) []string {
	env := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[name]; replaced {
			continue
		}
		env = append(env, entry)
	}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		env = append(env, k+"="+overrides[k])
	}
	return env
}

func stateFor(ctx context.Context) runner.State {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return runner.StateCancelled
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return runner.StateTimedOut
	}
	return runner.StateFailed
}

// Execute runs one request in an ephemeral, hardened Docker container, streams
// live events from the mounted exchange directory to dataDirectory/runs/<id>,
// and cleans up both the container and isolated per-run host directories.
func (c Connector) Execute(ctx context.Context, request runner.RunRequest, dataDirectory string) runner.RunResult {
	failure := func(err error) runner.RunResult {
		return runner.RunResult{State: stateFor(ctx), Error: err.Error()}
	}
	if !runner.ValidRunID(request.RunID) {
		return failure(fmt.Errorf("invalid run ID %q", request.RunID))
	}
	cfg := c.Config.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return failure(err)
	}
	timeout := time.Duration(request.TimeoutMillis) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Hour
	}
	// A container cannot ask the control plane for a new GitHub App token
	// between steps, so the token it starts with must last the whole run.
	if request.GitHubToken != "" && timeout > githubapp.TokenLifetime {
		return failure(fmt.Errorf("the run timeout of %s is longer than the one GitHub App token a Docker run keeps, which lasts %s; set the preset's timeout or run_timeout to %s or less", timeout, githubapp.TokenLifetime, githubapp.TokenLifetime))
	}
	token, err := runner.RunToken(request, c.getEnv("GH_TOKEN"))
	if err != nil {
		return failure(err)
	}
	if ctx.Err() != nil {
		return failure(ctx.Err())
	}

	root := filepath.Join(dataDirectory, "work", request.RunID)
	dirs, user, err := c.prepareRunDirs(root)
	if err != nil {
		return failure(err)
	}
	defer os.RemoveAll(root)

	exchangeRoot, err := os.OpenRoot(dirs.Exchange)
	if err != nil {
		return failure(fmt.Errorf("open exchange root: %w", err))
	}
	defer exchangeRoot.Close()

	// Strip GitHubToken before writing request.json so tokens never touch disk.
	storedRequest := request
	storedRequest.GitHubToken = ""
	requestBody, err := json.Marshal(storedRequest)
	if err != nil {
		return failure(fmt.Errorf("encode run request: %w", err))
	}
	if err := os.WriteFile(filepath.Join(dirs.Exchange, "request.json"), requestBody, 0o600); err != nil {
		return failure(fmt.Errorf("write run request: %w", err))
	}
	if os.Getuid() == 0 {
		_, uid, gid := c.containerUser()
		_ = os.Chown(filepath.Join(dirs.Exchange, "request.json"), uid, gid)
	}

	allowance := c.setupAllowance
	if allowance <= 0 {
		allowance = defaultSetupAllowance
	}
	cancelGrace := c.cancelGrace
	if cancelGrace <= 0 {
		cancelGrace = defaultCancelGrace
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout+allowance)
	defer cancel()

	containerName := ContainerName(request.RunID)
	args, cmdEnv := c.BuildRunArgs(containerName, dirs, token, user)

	relEvents := path.Join("runtime", "runs", request.RunID, "events.jsonl")
	dstEvents := filepath.Join(dataDirectory, "runs", request.RunID, "events.jsonl")
	stopTail := c.startEventTailer(exchangeRoot, relEvents, dstEvents)

	proc, err := c.startRun(cmdEnv, args...)
	if err != nil {
		stopTail()
		return failure(fmt.Errorf("start docker container: %w", err))
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- proc.Wait()
	}()

	var waitErr error
	var cancelled bool
	select {
	case waitErr = <-waitDone:
	case <-runCtx.Done():
		cancelled = true
		removed, rmErr := c.removeContainer(containerName)
		killed := false
		select {
		case <-waitDone:
		case <-time.After(cancelGrace):
			killed = true
			_ = proc.Kill()
			<-waitDone
		}
		if killed || (!removed && rmErr == nil) {
			if _, retryErr := c.removeContainer(containerName); retryErr != nil {
				rmErr = retryErr
			} else if killed {
				rmErr = nil
			}
		}
		stopTail()
		errText := runCtx.Err().Error()
		if errors.Is(runCtx.Err(), context.Canceled) {
			errText = "run cancelled"
		} else if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			errText = fmt.Sprintf("docker container exceeded timeout %s", timeout+allowance)
		}
		if rmErr != nil {
			errText = strings.TrimSpace(errText + " " + rmErr.Error())
		}
		return runner.RunResult{State: stateFor(runCtx), Error: errText}
	}

	stopTail()

	if !cancelled {
		result, readErr := readResultFile(exchangeRoot, "result.json")
		if readErr == nil {
			return result
		}
		// Container exited without producing result.json (e.g. OOM, image error,
		// or entrypoint failure). Ensure no container lingers and report cleanly.
		_, rmErr := c.removeContainer(containerName)
		var parts []string
		if waitErr != nil {
			parts = append(parts, fmt.Sprintf("docker run failed: %v", waitErr))
		} else {
			parts = append(parts, fmt.Sprintf("read sandbox result: %v", readErr))
		}
		if detail := proc.Stderr(); detail != "" {
			parts = append(parts, detail)
		}
		if rmErr != nil {
			parts = append(parts, rmErr.Error())
		}
		return runner.RunResult{State: stateFor(runCtx), Error: strings.Join(parts, ": ")}
	}
	return runner.RunResult{State: runner.StateFailed, Error: "docker run ended unexpectedly"}
}

func (c Connector) prepareRunDirs(root string) (RunDirs, string, error) {
	user, uid, gid := c.containerUser()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return RunDirs{}, "", fmt.Errorf("create run root: %w", err)
	}
	dirs := RunDirs{
		Workspace: filepath.Join(root, "workspace"),
		Control:   filepath.Join(root, "control"),
		Home:      filepath.Join(root, "home"),
		Tmp:       filepath.Join(root, "tmp"),
		Exchange:  filepath.Join(root, "exchange"),
	}
	for _, dir := range []string{dirs.Workspace, dirs.Control, dirs.Home, dirs.Tmp, dirs.Exchange} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return RunDirs{}, "", fmt.Errorf("create run directory %q: %w", dir, err)
		}
		if os.Getuid() == 0 {
			if err := os.Chown(dir, uid, gid); err != nil {
				return RunDirs{}, "", fmt.Errorf("chown run directory %q: %w", dir, err)
			}
		}
	}
	return dirs, user, nil
}

func (c Connector) removeContainer(name string) (bool, error) {
	cleanup, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_, err := c.command(cleanup, nil, 64<<10, "rm", "-f", name)
	if err == nil {
		return true, nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "no such container") {
		return false, nil
	}
	return false, fmt.Errorf("docker container %s may still be running; remove it with docker rm -f %s", name, name)
}

func openRegularFileInRoot(root *os.Root, relPath string) (*os.File, os.FileInfo, error) {
	file, err := root.OpenFile(relPath, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	fInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !fInfo.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, fmt.Errorf("%s is not a regular file", relPath)
	}
	// os.Root blocks symlink escapes outside the root, while still permitting
	// intra-root symlinks. Verify each path component with Lstat so symlinks
	// inside the container-writable exchange directory are rejected outright.
	parts := strings.Split(relPath, "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(prefix)
		if err != nil {
			_ = file.Close()
			return nil, nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			_ = file.Close()
			return nil, nil, fmt.Errorf("%s traverses a symbolic link", relPath)
		}
		if i < len(parts)-1 {
			if !info.IsDir() {
				_ = file.Close()
				return nil, nil, fmt.Errorf("%s parent is not a directory", relPath)
			}
		} else {
			if !info.Mode().IsRegular() || !os.SameFile(info, fInfo) {
				_ = file.Close()
				return nil, nil, fmt.Errorf("%s changed or is not a regular file", relPath)
			}
		}
	}
	return file, fInfo, nil
}

func readResultFile(exchangeRoot *os.Root, relPath string) (runner.RunResult, error) {
	file, _, err := openRegularFileInRoot(exchangeRoot, relPath)
	if err != nil {
		return runner.RunResult{}, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxResultBytes+1))
	if err != nil {
		return runner.RunResult{}, err
	}
	if len(body) > maxResultBytes {
		return runner.RunResult{}, errors.New("sandbox result exceeds size limit")
	}
	var result runner.RunResult
	if err := json.Unmarshal(body, &result); err != nil {
		return runner.RunResult{}, err
	}
	return result, nil
}

// startEventTailer tails complete JSONL lines from relSrc inside exchangeRoot
// and appends them to dst while the container runs. Calling the returned stop
// function stops the background ticker and performs one final drain before
// returning.
func (c Connector) startEventTailer(exchangeRoot *os.Root, relSrc, dst string) func() {
	interval := c.tailInterval
	if interval <= 0 {
		interval = defaultTailInterval
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	var offset, written int64
	drain := func() {
		forwardEvents(exchangeRoot, relSrc, dst, &offset, &written)
	}
	go func() {
		defer close(doneCh)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				drain()
				return
			case <-ticker.C:
				drain()
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stopCh)
			<-doneCh
		})
	}
}

func forwardEvents(exchangeRoot *os.Root, relSrc, dst string, offset, written *int64) {
	forwardEventsWithLimit(exchangeRoot, relSrc, dst, offset, written, runner.MaxEventLogBytes, maxEventLineBytes)
}

func forwardEventsWithLimit(exchangeRoot *os.Root, relSrc, dst string, offset, written *int64, maxBytes, maxLine int64) {
	if *written >= maxBytes {
		return
	}
	file, info, err := openRegularFileInRoot(exchangeRoot, relSrc)
	if err != nil {
		return
	}
	defer file.Close()
	if info.Size() <= *offset {
		return
	}
	if _, err := file.Seek(*offset, io.SeekStart); err != nil {
		return
	}
	remaining := maxBytes - *written
	limited := io.LimitReader(file, remaining+1)
	reader := bufio.NewReaderSize(limited, 64<<10)
	var batch bytes.Buffer
	nextOffset := *offset
	nextWritten := *written
	capped := false
	for {
		lineBudget := maxBytes - nextWritten
		if lineBudget <= 0 {
			capped = true
			break
		}
		if maxLine > 0 && lineBudget > maxLine {
			lineBudget = maxLine
		}
		line, complete, exceeded := readBoundedLine(reader, lineBudget)
		if exceeded {
			capped = true
			break
		}
		if !complete {
			// Incomplete trailing line or EOF: retry on the next tick.
			break
		}
		if !json.Valid(line) {
			nextOffset += int64(len(line))
			continue
		}
		if nextWritten+int64(len(line)) > maxBytes {
			capped = true
			break
		}
		batch.Write(line)
		nextOffset += int64(len(line))
		nextWritten += int64(len(line))
		if nextWritten >= maxBytes {
			capped = true
			break
		}
	}
	if batch.Len() == 0 {
		*offset = nextOffset
		if capped {
			*written = maxBytes
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	n, writeErr := out.Write(batch.Bytes())
	closeErr := out.Close()
	if writeErr == nil && closeErr == nil && n == batch.Len() {
		*offset = nextOffset
		if capped {
			*written = maxBytes
		} else {
			*written = nextWritten
		}
	}
}

func readBoundedLine(r *bufio.Reader, maxLen int64) (line []byte, complete bool, exceeded bool) {
	for {
		frag, err := r.ReadSlice('\n')
		if int64(len(line))+int64(len(frag)) > maxLen {
			return nil, false, true
		}
		line = append(line, frag...)
		switch {
		case err == nil:
			return line, true, false
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return line, false, false
		}
	}
}

type limitedWriter struct {
	writer    io.Writer
	remaining int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, errors.New("docker output limit exceeded")
	}
	n, err := w.writer.Write(p)
	w.remaining -= n
	return n, err
}

type cappedBuffer struct {
	mu    sync.Mutex
	limit int
	buf   bytes.Buffer
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() < b.limit {
		take := min(len(p), b.limit-b.buf.Len())
		b.buf.Write(p[:take])
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
