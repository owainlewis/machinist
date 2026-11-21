package harness

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Agent is one agent file, factory/agents/<name>.md: YAML front matter that
// picks the harness and model, then the agent's prompt.
//
//	---
//	harness: codex
//	model: gpt-5-codex
//	---
//	Review the change on this branch...
type Agent struct {
	Name    string
	Harness string
	// Model is the model to ask for. Empty means the harness's default.
	Model string
	// Program replaces the harness's program, such as a full path. The
	// arguments stay the harness's own. Its key in the file is command.
	Program string
	Prompt  string
}

const fence = "---\n"

// ParseAgent reads an agent file.
func ParseAgent(name, body string) (Agent, error) {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	if !strings.HasPrefix(body, fence) {
		return Agent{}, errors.New("must start with --- YAML front matter that names its harness")
	}
	front, prompt, found := strings.Cut(strings.TrimPrefix(body, fence), "\n"+fence)
	if !found {
		front, found = strings.CutSuffix(strings.TrimPrefix(body, fence), "\n---")
		if !found {
			return Agent{}, errors.New("front matter is not closed with ---")
		}
		prompt = ""
	}
	var settings struct {
		Harness string `yaml:"harness"`
		Model   string `yaml:"model"`
		Command string `yaml:"command"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader([]byte(front)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&settings); err != nil && !errors.Is(err, io.EOF) {
		return Agent{}, fmt.Errorf("front matter: %w", err)
	}
	if _, ok := Find(settings.Harness); !ok {
		return Agent{}, fmt.Errorf("harness must be one of %s, not %q", strings.Join(Names(), ", "), settings.Harness)
	}
	if strings.TrimSpace(prompt) == "" {
		return Agent{}, errors.New("the prompt after the front matter is empty")
	}
	return Agent{Name: name, Harness: settings.Harness, Model: settings.Model, Program: settings.Command, Prompt: strings.TrimSpace(prompt)}, nil
}

// Command builds the agent's argv and stdin for one turn.
func (a Agent) Command(system, prompt string) ([]string, string) {
	found, _ := Find(a.Harness)
	args, stdin := found.Command(Request{System: system, Prompt: prompt, Model: a.Model})
	if a.Program != "" {
		args[0] = a.Program
	}
	return args, stdin
}

// Format is the agent's harness's formatter.
func (a Agent) Format() Formatter {
	found, _ := Find(a.Harness)
	return found.Format
}
