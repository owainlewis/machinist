package factory

import (
	"slices"
	"testing"

	"github.com/owainlewis/machinist/internal/harness"
	"github.com/owainlewis/machinist/internal/runner"
)

func TestNeedsListsTheHarnessesAPresetsAgentsRun(t *testing.T) {
	factory := &Factory{
		Files: map[string]string{"scripts/review.sh": "rm -f review.json\nmachinist agent review\nmachinist verdict\n"},
		Agents: map[string]harness.Agent{
			"implement": {Name: "implement", Harness: "claude"},
			"review":    {Name: "review", Harness: "agy"},
			"custom":    {Name: "custom", Harness: "codex", Program: "/opt/codex"},
		},
	}
	preset := Preset{Steps: []runner.Step{
		{Name: "checkout", Run: "gh repo clone"},
		{Name: "implement", Run: "machinist agent implement"},
		{Name: "review", Run: "review.sh"},
		{Name: "custom", Run: "machinist agent custom"},
		{Name: "again", Run: "machinist agent implement"},
	}}
	if got := factory.Needs(preset); !slices.Equal(got, []string{"agy", "claude"}) {
		t.Fatalf("needs = %v", got)
	}
	if got := factory.Needs(Preset{Steps: []runner.Step{{Name: "test", Run: "go test ./..."}}}); len(got) != 0 {
		t.Fatalf("needs = %v", got)
	}
}

func TestStepAgentsIgnoresShellComments(t *testing.T) {
	factory := &Factory{
		Files: map[string]string{"scripts/review.sh": "#!/bin/sh\n# machinist agent codex, once it can review\n  # machinist agent codex\nmachinist agent review # not machinist agent codex\necho 'machinist agent implement # quoted'\n"},
		Agents: map[string]harness.Agent{
			"implement": {Name: "implement", Harness: "claude"},
			"review":    {Name: "review", Harness: "agy"},
			"codex":     {Name: "codex", Harness: "codex"},
		},
	}
	names := func(step string) []string {
		var names []string
		for _, agent := range factory.StepAgents(step) {
			names = append(names, agent.Name)
		}
		return names
	}
	if got := names("review.sh"); !slices.Equal(got, []string{"review", "implement"}) {
		t.Fatalf("agents = %v", got)
	}
	if got := names("machinist agent implement\n# machinist agent codex"); !slices.Equal(got, []string{"implement"}) {
		t.Fatalf("agents = %v", got)
	}
	if got := names("echo issue#machinist agent codex\necho \\ #machinist agent implement"); !slices.Equal(got, []string{"codex", "implement"}) {
		t.Fatalf("a # inside a word, or after an escaped space, is not a comment: agents = %v", got)
	}
	preset := Preset{Steps: []runner.Step{{Name: "review", Run: "review.sh"}, {Name: "later", Run: "# machinist agent codex"}}}
	if got := factory.Needs(preset); !slices.Equal(got, []string{"agy", "claude"}) {
		t.Fatalf("needs = %v", got)
	}
}
