package examples

import (
	"strings"
	"testing"
)

func TestShepherdPromptKeepsAutomaticMergeAuthorityOptIn(t *testing.T) {
	prompt := shepherdPrompt(t)
	for name, required := range map[string][]string{
		"label bootstrap":         {"ensure the repository defines", "create only the label", "label definition", "change an existing label"},
		"unlabelled pull request": {"without that label is inventory-only", "Never add the permission label", "never attach the label to a pull request"},
		"label removal":           {"If the label was removed", "do not mutate it"},
		"head change":             {"head SHA equals the expected head SHA", "failed safeguard is a state change"},
		"audit comments":          {"Immediately before an audit comment", "Audit comments may", "document a labelled draft blocker", "classification `blocked`", "classification `merged`", "classification `deferred`", "Unlabelled pull"},
		"checks and findings":     {"required checks to be present", "every current review thread or automated finding"},
		"base update":             {"expected-head base update", "fresh independent review of the new head"},
		"repair separation":       {"separate repair subagent", "fresh read-only reviewer"},
		"dependabot":              {"Dependabot patch and minor", "major updates require a person"},
		"complete action budget":  {"Count every GitHub", "repository label creation", "comment creation or editing", "thread resolution", "Once the limit is reached, stop"},
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
		"topological stack":       {"topological order", "Build branch-stack relationships", "supplying head repository", "different repositories", "retarget the dependent"},
		"oldest independent":      {"oldest creation time", "oldest-first"},
		"blocked versus eligible": {"must never stop another eligible pull request", "blocks only that pull request"},
		"serial refresh":          {"Process one pull request at a time", "rebuild the full queue"},
		"action limit":            {"Once the limit is reached", "later scheduled run", "rediscover the queue", "durable queue state"},
		"restart":                 {"Existing merged state is terminal", "restart.", "pending-retarget", "parent merge used the final action", "max_actions=1", "separate runs"},
		"deferred stack retarget": {"Before merging a pull request that supplies", "persist a pending stack transition", "process a current `pending-retarget`", "parent is no longer open", "no active pending", "Never infer that an", "obsolete parent branch"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, text := range required {
				if !strings.Contains(prompt, text) {
					t.Fatalf("Shepherd prompt is missing %q", text)
				}
			}
		})
	}
	if strings.Contains(prompt, "does not consume an action") {
		t.Fatal("Shepherd prompt exempts a GitHub mutation from max_actions")
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
