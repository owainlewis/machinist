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
