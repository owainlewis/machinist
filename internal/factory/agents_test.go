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
		for _, agent := range factory.StepAgents(runner.Step{Run: step}) {
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
	if got := names("machinist agent implement # then review.sh"); !slices.Equal(got, []string{"implement"}) {
		t.Fatalf("a script named only in a comment is not read: agents = %v", got)
	}
	preset := Preset{Steps: []runner.Step{{Name: "review", Run: "review.sh"}, {Name: "later", Run: "# machinist agent codex"}}}
	if got := factory.Needs(preset); !slices.Equal(got, []string{"agy", "claude"}) {
		t.Fatalf("needs = %v", got)
	}
}

func TestStepAgentsAndNeedsRespectOverrides(t *testing.T) {
	loaded := &Factory{
		Files: map[string]string{
			"scripts/review.sh": "#!/bin/sh\nmachinist agent review\n",
			"scripts/revise.sh": "#!/bin/sh\nmachinist agent revise\nmachinist agent review\n",
		},
		Agents: map[string]harness.Agent{
			"implement": {Name: "implement", Harness: "claude", Model: "sonnet"},
			"review":    {Name: "review", Harness: "claude", Model: "sonnet"},
			"revise":    {Name: "revise", Harness: "claude"},
			"custom":    {Name: "custom", Harness: "codex", Program: "/opt/codex"},
		},
	}

	neon := Preset{
		Harness: "agy",
		Model:   "gemini-4-neon-high",
		Steps: []runner.Step{
			{Name: "implement", Run: "machinist agent implement"},
			{Name: "review", Run: "review.sh"},
			{Name: "revise", Run: "revise.sh"},
			{Name: "custom", Run: "machinist agent custom"},
		},
	}
	if got := loaded.Needs(neon); !slices.Equal(got, []string{"agy"}) {
		t.Fatalf("neon needs = %v, want [agy]", got)
	}
	reviewAgents := loaded.StepAgents(neon.EffectiveStep(neon.Steps[1]))
	if len(reviewAgents) != 1 || reviewAgents[0].Name != "review" || reviewAgents[0].Harness != "agy" || reviewAgents[0].Model != "gemini-4-neon-high" {
		t.Fatalf("review agents = %+v", reviewAgents)
	}
	reviseAgents := loaded.StepAgents(neon.EffectiveStep(neon.Steps[2]))
	if len(reviseAgents) != 2 || reviseAgents[0].Harness != "agy" || reviseAgents[0].Model != "gemini-4-neon-high" || reviseAgents[1].Harness != "agy" || reviseAgents[1].Model != "gemini-4-neon-high" {
		t.Fatalf("revise agents = %+v", reviseAgents)
	}

	// Step override beats preset override, which beats agent file front matter.
	cross := Preset{
		Harness: "agy",
		Model:   "gemini-4-neon-high",
		Steps: []runner.Step{
			{Name: "implement", Run: "machinist agent implement"},
			{Name: "review", Run: "review.sh", Harness: "claude", Model: "opus"},
			{Name: "default-claude", Run: "review.sh", Harness: "claude"},
			{Name: "default-codex", Run: "review.sh", Harness: "codex"},
		},
	}
	if got := loaded.Needs(cross); !slices.Equal(got, []string{"agy", "claude", "codex"}) {
		t.Fatalf("cross needs = %v", got)
	}
	if got := loaded.StepAgents(cross.EffectiveStep(cross.Steps[1])); len(got) != 1 || got[0].Harness != "claude" || got[0].Model != "opus" {
		t.Fatalf("step review = %+v", got)
	}
	// Step overrides harness back to claude without model -> keeps review.md's claude model (sonnet), not preset's neon model.
	if got := loaded.StepAgents(cross.EffectiveStep(cross.Steps[2])); len(got) != 1 || got[0].Harness != "claude" || got[0].Model != "sonnet" {
		t.Fatalf("step default-claude = %+v", got)
	}
	// Step overrides harness to codex without model -> clears both preset's neon model and review.md's claude model.
	if got := loaded.StepAgents(cross.EffectiveStep(cross.Steps[3])); len(got) != 1 || got[0].Harness != "codex" || got[0].Model != "" {
		t.Fatalf("step default-codex = %+v", got)
	}
}

func TestStepScriptsReturnsReferencedScriptsInOrderWithoutCommentsOrDuplicates(t *testing.T) {
	loaded := &Factory{
		Files: map[string]string{
			"scripts/checkout.sh": "#!/bin/sh\ngit clone repo\n",
			"scripts/review.sh":   "#!/bin/sh\nmachinist agent review\n",
			"scripts/unused.sh":   "#!/bin/sh\necho unused\n",
		},
	}
	got := loaded.StepScripts(runner.Step{Run: "./checkout.sh && review.sh && checkout.sh # unused.sh"})
	want := []StepScript{
		{Path: "scripts/checkout.sh", Content: "#!/bin/sh\ngit clone repo\n"},
		{Path: "scripts/review.sh", Content: "#!/bin/sh\nmachinist agent review\n"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("scripts = %+v, want %+v", got, want)
	}
	if none := loaded.StepScripts(runner.Step{Run: "machinist merge"}); len(none) != 0 {
		t.Fatalf("none = %+v", none)
	}
}
