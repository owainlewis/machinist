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
		"exact audit state":       {"exactly `head`, `base`, `state`, and `classification`", "uppercase `OPEN` or `MERGED`", "Never accept a prefix", "exactly matching Shepherd"},
		"checks and findings":     {"required checks to be present", "every current review thread or automated finding"},
		"exact comparison review": {"exact head and", "base SHA comparison", "comparison-specific", "comparison values still match"},
		"trusted review author":   {"authenticated GitHub actor's canonical login", "viewerDidAuthor", "authenticated actor must not be the pull request author", "any other account is untrusted"},
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
		"deferred stack retarget": {"Before merging a pull request that supplies", "persist a pending stack transition", "process a current `pending-retarget`", "parent is no longer open", "base SHA", "comment provenance", "includesCreatedEdit", "never edit or delete it", "pending comment id", "Never infer that an", "obsolete parent branch"},
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
	body, err := Files.ReadFile("prompts/foreman.md")
	if err != nil {
		t.Fatal(err)
	}
	prompt := string(body)
	if strings.Count(strings.ToLower(prompt), "never merge") < 2 {
		t.Fatal("Foreman prompt no longer has both never-merge boundaries")
	}
}

func TestForemanPromptBatchesTerminalAutomatedFindings(t *testing.T) {
	body, err := Files.ReadFile("prompts/foreman.md")
	if err != nil {
		t.Fatal(err)
	}
	prompt := string(body)
	for name, required := range map[string][]string{
		"terminal collection": {"Do not reserve or dispatch repair until every applicable expected check and automated reviewer", "Once all are terminal, collect failed checks, reviews, threads, and bot comments."},
		"deduplication":       {"Deduplicate equivalent confirmed", "defects across sources", "one disposition per equivalent group."},
		"batching":            {"current-head code defect in one Shared repair-loop handoff.", "complete batched findings."},
		"immediate safety":    {"destructive-action concerns stop immediately;", "do not wait or batch them."},
	} {
		t.Run(name, func(t *testing.T) {
			for _, text := range required {
				if !strings.Contains(prompt, text) {
					t.Fatalf("Foreman prompt is missing %q", text)
				}
			}
		})
	}
	if terminal, repair := strings.Index(prompt, "Once all are terminal"), strings.Index(prompt, "Reserve the next positive repair count"); terminal < 0 || repair < 0 || terminal > repair {
		t.Fatalf("Foreman prompt must collect terminal automated findings before reserving a repair: terminal=%d repair=%d", terminal, repair)
	}
}

func shepherdPrompt(t *testing.T) string {
	t.Helper()
	body, err := Files.ReadFile("prompts/shepherd.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
