package examples

import (
	"strings"
	"testing"
)

func TestShepherdPromptKeepsAutomaticMergeAuthorityOptIn(t *testing.T) {
	prompt := shepherdPrompt(t)
	for name, required := range map[string][]string{
		"label bootstrap":         {"ensure the repository defines", "create only the label definition", "Do not change an existing label"},
		"unlabelled pull request": {"without that label is inventory-only", "Never add the permission label", "never attach the label to a pull request"},
		"label removal":           {"If the label was removed", "do not mutate it"},
		"head change":             {"head SHA equals the expected head SHA", "failed safeguard is a state change"},
		"checks and findings":     {"required checks to be present", "every current review thread or automated finding"},
		"base update":             {"expected-head base update", "fresh independent review of the new head"},
		"repair separation":       {"separate repair subagent", "fresh read-only reviewer"},
		"dependabot":              {"Dependabot patch and minor", "major updates require a person"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, text := range required {
				if !strings.Contains(prompt, text) {
					t.Fatalf("Shepherd prompt is missing %q", text)
				}
			}
		})
	}
}

func TestShepherdPromptDefinesSerialRestartSafeQueue(t *testing.T) {
	prompt := shepherdPrompt(t)
	for name, required := range map[string][]string{
		"inventory":               {"inventory every open pull request", "Machinist, manual, and Dependabot"},
		"topological stack":       {"topological order", "Build branch-stack relationships", "retarget the dependent"},
		"oldest independent":      {"oldest creation time", "oldest-first order"},
		"blocked versus eligible": {"must never stop another eligible pull request", "blocks only that pull request"},
		"serial refresh":          {"Process one pull request at a time", "rebuild the full queue"},
		"action limit":            {"Once the limit is reached", "later scheduled run must rediscover"},
		"restart":                 {"Existing merged state is terminal", "restart."},
	} {
		t.Run(name, func(t *testing.T) {
			for _, text := range required {
				if !strings.Contains(prompt, text) {
					t.Fatalf("Shepherd prompt is missing %q", text)
				}
			}
		})
	}
}

func TestForemanStillCannotMerge(t *testing.T) {
	body, err := Files.ReadFile("agents/foreman.md")
	if err != nil {
		t.Fatal(err)
	}
	prompt := string(body)
	if strings.Count(strings.ToLower(prompt), "never merge") < 2 {
		t.Fatal("Foreman prompt no longer has both never-merge boundaries")
	}
}

func shepherdPrompt(t *testing.T) string {
	t.Helper()
	body, err := Files.ReadFile("agents/shepherd.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
