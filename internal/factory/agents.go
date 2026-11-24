package factory

import (
	"regexp"
	"sort"
	"strings"

	"github.com/owainlewis/machinist/internal/harness"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
)

// Needs lists the harnesses a preset's agents run, sorted, so the control
// plane sends its jobs only to a runner that has them. An agent that names its
// own program is left out: a runner cannot tell whether it has that program.
func (f *Factory) Needs(preset Preset) []string {
	seen := map[string]bool{}
	for _, step := range preset.Steps {
		for _, agent := range f.stepResolvedAgents(preset.EffectiveStep(step)) {
			if agent.Program == "" {
				seen[agent.Harness] = true
			}
		}
	}
	needs := make([]string, 0, len(seen))
	for name := range seen {
		needs = append(needs, name)
	}
	sort.Strings(needs)
	return needs
}

var agentCall = regexp.MustCompile(`machinist\s+agent\s+([A-Za-z0-9][A-Za-z0-9_-]*)`)

// StepAgents names the agents a step runs, in order, with any harness or model
// override applied, so a job can show them before it starts. It reads the
// step's command and any script from scripts/ that the command names for
// "machinist agent <name>", leaving out shell comments. A step that runs no
// agent, or one Machinist cannot see into, has none.
func (f *Factory) StepAgents(step runner.Step) []protocol.Agent {
	resolved := f.stepResolvedAgents(step)
	agents := make([]protocol.Agent, 0, len(resolved))
	for _, agent := range resolved {
		agents = append(agents, protocol.Agent{Name: agent.Name, Harness: agent.Harness, Model: agent.Model})
	}
	return agents
}

func (f *Factory) stepResolvedAgents(step runner.Step) []harness.Agent {
	text := withoutComments(step.Run)
	for _, word := range strings.Fields(text) {
		if script, ok := f.Files["scripts/"+strings.TrimPrefix(word, "./")]; ok {
			text += "\n" + withoutComments(script)
		}
	}
	agents := []harness.Agent{}
	for _, match := range agentCall.FindAllStringSubmatch(text, -1) {
		if agent, ok := f.Agents[match[1]]; ok {
			if overridden, err := agent.Override(step.Harness, step.Model); err == nil {
				agent = overridden
			}
			agents = append(agents, agent)
		}
	}
	return agents
}

// withoutComments drops shell comments from a script: a # that starts a word,
// outside quotes, to the end of its line.
func withoutComments(script string) string {
	var out strings.Builder
	var quote rune
	escaped, comment, wordStart := false, false, true
	for _, char := range script {
		separator := false
		switch {
		case comment:
			if char != '\n' {
				continue
			}
			comment, separator = false, true
		case escaped:
			escaped = false
		case quote != 0:
			if char == quote {
				quote = 0
			} else if char == '\\' && quote == '"' {
				escaped = true
			}
		case char == '\\':
			escaped = true
		case char == '\'' || char == '"':
			quote = char
		case char == '#' && wordStart:
			comment = true
			continue
		default:
			separator = strings.ContainsRune(" \t\n;&|()", char)
		}
		wordStart = separator
		out.WriteRune(char)
	}
	return out.String()
}
