package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/githubauth"
	"github.com/owainlewis/machinist/internal/harness"
	"github.com/owainlewis/machinist/internal/protocol"
)

const (
	maxControlFile = 64 << 10
	defaultSummary = "Every step passed."
)

var (
	varName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	// FilePath is the form of a factory file a run carries.
	FilePath = regexp.MustCompile(`^(agents|prompts|scripts)/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

const defaultPath = "/usr/local/bin:/usr/bin:/bin"

// WorkspaceOptions describes the private directory and credentials of one run.
type WorkspaceOptions struct {
	Request     RunRequest
	Root        string
	GitHubToken string
	// Environment is the base environment: the allowlist and the runner's env.
	Environment []string
	// Home is private to the run unless the runner shares the user's login.
	Home string
}

// Workspace is an empty directory, the control directory beside it and the
// complete environment of the steps. The control directory holds prompt.md and
// the factory's agents/, prompts/ and scripts/, and a step can leave result.md there. scripts/ is first on PATH, so a step runs a script by name. It is outside the
// workspace, so a clone into the workspace never commits it.
type Workspace struct {
	Directory   string
	Control     string
	Environment []string
}

// PrepareWorkspace makes an empty workspace and writes the prompt beside it.
// Nothing is cloned: checking out a repository is a step like any other.
func PrepareWorkspace(options WorkspaceOptions) (Workspace, error) {
	request := options.Request
	if err := os.MkdirAll(options.Home, 0o700); err != nil {
		return Workspace{}, err
	}
	var paths [2]string
	for index, name := range []string{"workspace", "control"} {
		directory := filepath.Join(options.Root, name)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return Workspace{}, err
		}
		canonical, err := filepath.EvalSymlinks(directory)
		if err != nil {
			return Workspace{}, err
		}
		paths[index] = canonical
	}
	workspace := Workspace{Directory: paths[0], Control: paths[1]}
	if err := os.WriteFile(filepath.Join(workspace.Control, "prompt.md"), []byte(request.Prompt), 0o600); err != nil {
		return Workspace{}, err
	}
	if err := writeFiles(workspace.Control, request.Files); err != nil {
		return Workspace{}, err
	}
	environment, err := stepEnvironment(options, workspace.Control)
	if err != nil {
		return Workspace{}, err
	}
	workspace.Environment = environment
	return workspace, nil
}

// writeFiles writes the factory's agents, prompts and scripts. Scripts are executable.
func writeFiles(control string, files map[string]string) error {
	for path, body := range files {
		if !FilePath.MatchString(path) || strings.Contains(path, "..") {
			return fmt.Errorf("invalid factory file %q", path)
		}
		mode := os.FileMode(0o600)
		if strings.HasPrefix(path, "scripts/") {
			mode = 0o700
		}
		target := filepath.Join(control, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(body), mode); err != nil {
			return err
		}
	}
	return nil
}

// withScriptsOnPath puts the factory's scripts first on PATH, and the running
// machinist's own directory last, so a step can call machinist agent-log even
// when machinist is not installed on PATH. When a key is listed twice, a
// process sees the last value.
func withScriptsOnPath(environment []string, control string) []string {
	path := defaultPath
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok && value != "" {
			path = value
		}
	}
	path = filepath.Join(control, "scripts") + string(os.PathListSeparator) + path
	if executable, err := os.Executable(); err == nil {
		path += string(os.PathListSeparator) + filepath.Dir(executable)
	}
	return append(environment, "PATH="+path)
}

func stepEnvironment(options WorkspaceOptions, control string) ([]string, error) {
	request := options.Request
	environment := append([]string(nil), options.Environment...)
	environment = append(environment,
		"HOME="+options.Home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME="+request.GitName, "GIT_COMMITTER_NAME="+request.GitName,
		"GIT_AUTHOR_EMAIL="+request.GitEmail, "GIT_COMMITTER_EMAIL="+request.GitEmail,
		"MACHINIST_JOB_ID="+request.TicketID,
		"MACHINIST_PRESET="+request.Preset,
		"MACHINIST_BRANCH="+request.Branch,
		"MACHINIST_DIR="+control,
	)
	environment = withScriptsOnPath(environment, control)
	for name, value := range request.Vars {
		if !varName.MatchString(name) {
			return nil, fmt.Errorf("invalid var name %q", name)
		}
		environment = append(environment, "MACHINIST_"+strings.ToUpper(name)+"="+value)
	}
	if request.Repository != "" {
		github, err := githubauth.GitHubEnvironment(options.GitHubToken, request.Repository)
		if err != nil {
			return nil, err
		}
		environment = append(environment, github...)
	}
	return environment, nil
}

// RunSteps runs the steps of a request in order in a prepared workspace. Each
// step is a new process, and only the workspace carries over. The run ends at
// the first step that exits non-zero or times out. No step can wait for a
// person. The run timeout covers all steps together.
func RunSteps(ctx context.Context, request RunRequest, workspace Workspace, dataDirectory string) RunResult {
	if !ValidRunID(request.RunID) {
		return RunResult{State: StateFailed, Error: fmt.Sprintf("invalid run ID %q", request.RunID)}
	}
	if len(request.Steps) == 0 {
		return RunResult{State: StateFailed, Error: "the preset has no steps"}
	}
	runDirectory := filepath.Join(dataDirectory, "runs", request.RunID)
	if err := createDurableDirectory(runDirectory); err != nil {
		return RunResult{State: StateFailed, Error: fmt.Sprintf("create run directory: %v", err)}
	}
	// Every step appends to one event log, so the sequence numbers stay
	// continuous for the whole run.
	log, err := newEventLog(filepath.Join(runDirectory, "events.jsonl"))
	if err != nil {
		return RunResult{State: StateFailed, Error: err.Error()}
	}

	output := RunResult{State: StateSucceeded}
	results := make([]protocol.StageResult, len(request.Steps))
	for index, step := range request.Steps {
		results[index] = protocol.StageResult{Name: step.Name, Kind: "step", State: protocol.StageSkipped}
	}
	deadline := time.Now().Add(time.Duration(request.TimeoutMillis) * time.Millisecond)
	for index, step := range request.Steps {
		workspace.Environment = freshToken(ctx, request, workspace.Environment)
		stderr := &lastLine{}
		result, err := runStep(ctx, request, step, workspace, dataDirectory, log, time.Until(deadline), stderr)
		if result.State == "" {
			result.State = StateFailed
		}
		entry := &results[index]
		entry.State = string(result.State)
		entry.DurationMillis = result.DurationMillis
		stepUsage(workspace.Control, entry)
		if result.State != StateSucceeded {
			output.State = result.State
			if err != nil {
				entry.Error = err.Error()
			}
			// A failed step's own last word on stderr is usually why it failed.
			if said := stderr.String(); said != "" && result.State == StateFailed {
				entry.Error = strings.TrimPrefix(entry.Error+": "+said, ": ")
			}
			output.Error = fmt.Sprintf("step %s: %s", step.Name, entry.Error)
			break
		}
	}
	if output.State == StateSucceeded && output.Report == nil {
		output.Report, err = finalReport(workspace.Control)
		if err != nil {
			output.State, output.Error = StateFailed, err.Error()
		}
	}
	if err := log.close(); err != nil && output.Error == "" {
		output.Error = err.Error()
	}
	output.Stages = results
	return output
}

// stepUsage reads what the step's agent reported, which machinist agent
// leaves in the control directory: its tokens and cost, and the agent,
// harness and model that ran. It removes the file so the next step starts
// without it. A missing or unreadable file reports none.
func stepUsage(control string, entry *protocol.StageResult) {
	body, err := readControlFile(control, harness.UsageFile)
	_ = os.Remove(filepath.Join(control, harness.UsageFile))
	if err != nil || body == "" {
		return
	}
	var usage harness.Usage
	if json.Unmarshal([]byte(body), &usage) != nil {
		return
	}
	entry.Agents = usage.Agents
	if usage.Tokens < 0 || usage.Cost < 0 || (usage.Tokens == 0 && usage.Cost == 0) {
		return
	}
	entry.Tokens, entry.CostUnknown = &usage.Tokens, usage.CostUnknown
	// A harness that reports no cost leaves it unknown, not free.
	if usage.Cost > 0 || !usage.CostUnknown {
		entry.Cost = &usage.Cost
	}
}

// finalReport is the run's result: result.md, or a plain summary.
func finalReport(directory string) (*protocol.Report, error) {
	summary, err := readControlFile(directory, "result.md")
	if err != nil {
		return nil, err
	}
	if summary == "" {
		summary = defaultSummary
	}
	return &protocol.Report{Outcome: protocol.OutcomeNoChange, Summary: summary}, nil
}

// readControlFile reads a file a step left for Machinist. A missing file is empty.
func readControlFile(directory, name string) (string, error) {
	path := filepath.Join(directory, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", name)
	}
	if info.Size() > maxControlFile {
		return "", fmt.Errorf("%s is larger than %d bytes", name, maxControlFile)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

// runStep starts one step with sh -c and waits for it. The step gets the
// prompt on stdin too, so a command such as claude -p can read it there.
// freshToken swaps a new GitHub token into the step environment, when the
// run's token came from the control plane and it can make another. If it
// cannot, the step keeps the token it had, which may still be valid.
func freshToken(ctx context.Context, request RunRequest, environment []string) []string {
	if request.GitHubToken == "" || request.RefreshToken == nil {
		return environment
	}
	token, err := request.RefreshToken(ctx)
	if err != nil || token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return environment
	}
	fresh := make([]string, len(environment))
	for index, entry := range environment {
		switch {
		case strings.HasPrefix(entry, "GH_TOKEN="):
			entry = "GH_TOKEN=" + token
		case strings.HasPrefix(entry, "GITHUB_TOKEN="):
			entry = "GITHUB_TOKEN=" + token
		}
		fresh[index] = entry
	}
	return fresh
}

func runStep(ctx context.Context, request RunRequest, step Step, workspace Workspace, dataDirectory string, log *eventLog, remaining time.Duration, stderr io.Writer) (Result, error) {
	if remaining <= 0 {
		return Result{State: StateTimedOut}, errors.New("the run timed out before this step started")
	}
	timeout := remaining
	if step.TimeoutMillis > 0 {
		timeout = min(timeout, time.Duration(step.TimeoutMillis)*time.Millisecond)
	}
	idle := DefaultIdleTimeout
	if step.IdleTimeoutMillis > 0 {
		idle = time.Duration(step.IdleTimeoutMillis) * time.Millisecond
	}
	if idle >= timeout {
		idle = 0
	}
	if err := log.append("step.started", "", "name="+step.Name); err != nil {
		return Result{}, err
	}
	return Execute(ctx, Options{
		Environment:   workspace.Environment,
		Prompt:        request.Prompt,
		RunID:         request.RunID,
		Command:       Command{Name: step.Name, Args: []string{"sh", "-e", "-c", step.Run}, Timeout: timeout, IdleTimeout: idle},
		Directory:     workspace.Directory,
		DataDirectory: dataDirectory,
		Stdout:        io.Discard,
		Stderr:        stderr,
		log:           log,
	})
}

const (
	// lastLineKept is how much of a step's stderr is kept to find its last line.
	lastLineKept = 4096
	// lastLineShown caps the last line a failed step's error carries.
	lastLineShown = 300
)

// lastLine keeps the end of what a step writes, so a failed step's error can
// carry the last line it wrote.
type lastLine struct {
	mu   sync.Mutex
	tail []byte
}

func (l *lastLine) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tail = append(l.tail, p...)
	if extra := len(l.tail) - lastLineKept; extra > 0 {
		l.tail = append(l.tail[:0], l.tail[extra:]...)
	}
	return len(p), nil
}

// String is the last line that is not blank, or "" when there is none. A long
// line keeps its end, where a message usually says what went wrong.
func (l *lastLine) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	text := strings.TrimRight(string(l.tail), " \t\r\n")
	if index := strings.LastIndexByte(text, '\n'); index >= 0 {
		text = text[index+1:]
	}
	text = strings.TrimSpace(text)
	if len(text) > lastLineShown {
		text = "…" + strings.ToValidUTF8(text[len(text)-lastLineShown:], "")
	}
	return text
}
