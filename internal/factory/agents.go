package factory

import (
	"regexp"
	"strings"

	"github.com/owainlewis/machinist/internal/protocol"
)

var agentCall = regexp.MustCompile(`machinist\s+agent\s+([A-Za-z0-9][A-Za-z0-9_-]*)`)

// StepAgents names the agents a step runs, in order, so a job can show them
// before it starts. It reads the step's command and any script from scripts/
// that the command names for "machinist agent <name>". A step that runs no
// agent, or one Machinist cannot see into, has none.
func (f *Factory) StepAgents(step string) []protocol.Agent {
	text := step
	for _, word := range strings.Fields(step) {
		if script, ok := f.Files["scripts/"+strings.TrimPrefix(word, "./")]; ok {
			text += "\n" + script
		}
	}
	agents := []protocol.Agent{}
	for _, match := range agentCall.FindAllStringSubmatch(text, -1) {
		if agent, ok := f.Agents[match[1]]; ok {
			agents = append(agents, protocol.Agent{Name: agent.Name, Harness: agent.Harness, Model: agent.Model})
		}
	}
	return agents
}
