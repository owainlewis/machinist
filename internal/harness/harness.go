// Package harness runs a coding agent, such as Claude Code, Codex, Pi or agy, inside
// a step. Each harness builds the agent's command and turns its machine
// output into lines a person can follow while a run is going: one line per
// message or action, and a summary at the end. Any line a harness does not
// know passes through unchanged.
package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/owainlewis/machinist/internal/protocol"
)

// ActionPrefix starts every line that names an action the agent took. The web
// app shows the latest one as what a running job is doing now.
const ActionPrefix = "▸ "

// maxLine bounds a line of agent output. A tool result can hold a whole file.
const maxLine = 16 << 20

// Usage is what a run reported spending. Tokens counts input, output and
// cache tokens together.
type Usage struct {
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
	// CostUnknown is true when an agent spent tokens but its harness reports
	// no cost, so Cost leaves part of the spend out.
	CostUnknown bool `json:"cost_unknown,omitempty"`
	// Model is the model the harness said it started with. It fills in the
	// model of an agent that left the choice to its harness.
	Model string `json:"-"`
	// Agents are the agents the step ran, in order.
	Agents []protocol.Agent `json:"agents,omitempty"`
}

// Request is one agent turn: the instructions, the task and the model.
type Request struct {
	// System is the agent's instructions: Machinist's rules and the agent
	// file's prompt.
	System string
	// Prompt is the job's prompt, which other people wrote.
	Prompt string
	// Model is the model to ask for. Empty means the harness's default.
	Model string
}

// Harness is one coding agent's command line.
type Harness interface {
	Name() string
	// Command is the agent's argv and what to send on its stdin.
	Command(Request) (args []string, stdin string)
	// Format copies the agent's output from r to w as readable lines, and
	// returns the usage it reported, if any.
	Format(r io.Reader, w io.Writer, cwd string) (*Usage, error)
}

var harnesses = map[string]Harness{"agy": Agy{}, "claude": Claude{}, "codex": Codex{}, "pi": Pi{}}

// Find returns the harness with a name.
func Find(name string) (Harness, bool) {
	found, ok := harnesses[name]
	return found, ok
}

// Installed lists the harnesses whose program lookPath finds, sorted. Each
// harness's program has the harness's name.
func Installed(lookPath func(string) (string, error)) []string {
	installed := []string{}
	for _, name := range Names() {
		if _, err := lookPath(name); err == nil {
			installed = append(installed, name)
		}
	}
	return installed
}

// Names lists the harnesses Machinist knows, sorted.
func Names() []string {
	names := make([]string, 0, len(harnesses))
	for name := range harnesses {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// eachLine calls handle for each line of r, and copies a line it does not
// handle to w. handle returns the lines to write and whether it knew the line.
func eachLine(r io.Reader, w io.Writer, handle func(line string) ([]string, bool)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), maxLine)
	for scanner.Scan() {
		line := scanner.Text()
		out, known := handle(line)
		if !known {
			out = []string{line}
		}
		for _, text := range out {
			if _, err := fmt.Fprintln(w, text); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func firstLine(text string, limit int) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	runes := []rune(line)
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return line
}

// relative shortens a path inside cwd.
func relative(cwd, file string) string {
	if rel, err := filepath.Rel(cwd, file); err == nil && cwd != "" && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return file
}

// UsageFile is where Run leaves the usage, in the run's control directory.
// The runner reads it after the step and adds it to the step's result.
const UsageFile = "usage.json"

// addUsage adds usage to what earlier agents in the same step left at path,
// so a step that runs two agents reports both.
func addUsage(path string, usage Usage) {
	earlier := readUsage(path)
	earlier.Tokens += usage.Tokens
	earlier.Cost += usage.Cost
	earlier.CostUnknown = earlier.CostUnknown || usage.CostUnknown
	if last := len(earlier.Agents) - 1; usage.Model != "" && last >= 0 && earlier.Agents[last].Model == "" {
		earlier.Agents[last].Model = usage.Model
	}
	writeUsage(path, earlier)
}

// RecordAgent adds the agent about to run to the step's usage file, before it
// starts, so a step shows what ran it even when the agent fails or reports no
// usage. A step that runs two agents lists both.
func RecordAgent(path string, agent protocol.Agent) {
	usage := readUsage(path)
	usage.Agents = append(usage.Agents, agent)
	writeUsage(path, usage)
}

// withModel notes the model a harness started with on its usage, making the
// usage when the agent stopped before reporting any.
func withModel(usage *Usage, model string) *Usage {
	if model == "" {
		return usage
	}
	if usage == nil {
		usage = &Usage{}
	}
	usage.Model = model
	return usage
}

func readUsage(path string) Usage {
	var usage Usage
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &usage)
	}
	return usage
}

func writeUsage(path string, usage Usage) {
	if data, err := json.Marshal(usage); err == nil {
		_ = os.WriteFile(path, data, 0o600)
	}
}

// Formatter turns an agent's output into readable lines.
type Formatter func(r io.Reader, w io.Writer, cwd string) (*Usage, error)

// Run starts command with stdin, formats its stdout onto stdout, and passes
// its stderr through. It adds the reported usage to usagePath, when the agent
// reported any and usagePath is set, and returns the command's exit code.
func Run(ctx context.Context, format Formatter, command []string, stdin io.Reader, stdout, stderr io.Writer, usagePath string) (int, error) {
	if len(command) == 0 {
		return 2, errors.New("name the agent command after --")
	}
	process := exec.CommandContext(ctx, command[0], command[1:]...)
	process.Stdin = stdin
	process.Stderr = stderr
	output, err := process.StdoutPipe()
	if err != nil {
		return 1, err
	}
	if err := process.Start(); err != nil {
		return 127, err
	}
	cwd, _ := os.Getwd()
	usage, formatErr := format(output, stdout, cwd)
	if formatErr != nil {
		_, _ = io.Copy(io.Discard, output)
	}
	waitErr := process.Wait()
	if usage != nil && usagePath != "" {
		addUsage(usagePath, *usage)
	}
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) {
		return exit.ExitCode(), nil
	}
	if waitErr != nil {
		return 1, waitErr
	}
	if formatErr != nil {
		return 1, formatErr
	}
	return 0, nil
}
