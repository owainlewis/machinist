package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadWorkerResolvesRelativePathsFromConfig(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "worker.toml")
	writeTestFile(t, path, "data_directory = \"state\"\n")

	worker, err := LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	if worker.DataDirectory != filepath.Join(directory, "state") {
		t.Fatalf("data directory = %q", worker.DataDirectory)
	}
	definition, err := worker.ResolveMachinistConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if definition != filepath.Join(directory, "config.toml") {
		t.Fatalf("definition = %q", definition)
	}
}

func TestLoadWorkerDefaultsNameToHostname(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.toml")
	writeTestFile(t, path, "data_directory = \"state\"\n")

	worker, err := LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if worker.Name != hostname {
		t.Fatalf("worker name = %q, want hostname %q", worker.Name, hostname)
	}
}

func TestWorkerNameDefaultsReportHostnameFailure(t *testing.T) {
	want := errors.New("hostname unavailable")
	_, err := applyWorkerDefaultsWithHostname(Worker{DataDirectory: t.TempDir()}, func() (string, error) {
		return "", want
	})
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "find machine hostname") {
		t.Fatalf("error = %v", err)
	}
}

func TestWorkerExplicitNameDoesNotReadHostname(t *testing.T) {
	worker, err := applyWorkerDefaultsWithHostname(Worker{Name: " configured-worker ", DataDirectory: t.TempDir()}, func() (string, error) {
		t.Fatal("hostname lookup called for explicit worker name")
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if worker.Name != "configured-worker" {
		t.Fatalf("worker name = %q", worker.Name)
	}
}

func TestLoadWorkerRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.toml")
	writeTestFile(t, path, "mystery = true\n")

	_, err := LoadWorker(path)
	if err == nil || !strings.Contains(err.Error(), "strict mode") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestLoadManagedWorkerResolvesMachineConfiguration(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "token"), "secret\n")
	path := filepath.Join(directory, "worker.toml")
	writeTestFile(t, path, `name = "local"
data_directory = "state"

[control_plane]
url = "http://127.0.0.1:7331"
token_file = "token"

[executors.test]
command = ["agent", "run"]

[repositories.machinist]
path = "repository"
`)
	worker, err := LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	if repository, err := worker.ResolveRepository("machinist"); err != nil || repository != filepath.Join(directory, "repository") {
		t.Fatalf("repository = %q, %v", repository, err)
	}
	if token, err := worker.WorkerToken(); err != nil || token != "secret" {
		t.Fatalf("token = %q, %v", token, err)
	}
	resolved, err := worker.ResolveCommandModel(ResolvedCommand{Executor: "test"}, "")
	if err != nil || len(resolved.Command) != 2 || resolved.Command[1] != "run" {
		t.Fatalf("agent = %#v, %v", resolved, err)
	}
}

func TestLoadConfigCombinesServerAndCommands(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "token"), "secret")
	path := filepath.Join(directory, "config.toml")
	writeTestFile(t, filepath.Join(directory, "plan.md"), "Plan {{machinist.prompt}}.\n")
	writeTestFile(t, path, `[server]
database = "state/machinist.db"
worker_token_file = "token"
max_concurrent_jobs = 2

[commands.plan]
executor = "test"
prompt_file = "plan.md"

`)
	machinistConfig, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	server := machinistConfig.Server
	if server.Listen != "127.0.0.1:7331" || server.Database != filepath.Join(directory, "state", "machinist.db") || server.ConcurrentJobLimit() != 2 {
		t.Fatalf("server = %#v", server)
	}
	if machinistConfig.Path() != path || len(machinistConfig.Commands) != 1 {
		t.Fatalf("Machinist config = %#v, path = %q", machinistConfig, machinistConfig.Path())
	}
	if token, err := server.WorkerToken(); err != nil || token != "secret" {
		t.Fatalf("token = %q, %v", token, err)
	}
}

func TestLoadConfigRejectsRemovedConfigurationWithMigrationGuidance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeTestFile(t, path, "[pipelines.quality]\nagents=[\"review\"]\n")
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "replace each pipeline with a repository-owned orchestration script") {
		t.Fatalf("pipeline migration error = %v", err)
	}
	writeTestFile(t, path, "[agents.review]\nexecutor=\"codex\"\n")
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "agents were renamed to commands") {
		t.Fatalf("agent migration error = %v", err)
	}
}

func TestCommandWithoutPromptTemplatePassesInputThrough(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeTestFile(t, path, "[commands.script]\nexecutor=\"script\"\n")
	command, err := LoadCommand(path, "script")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderPrompt(command, "raw task")
	if err != nil || rendered.Prompt != "raw task" {
		t.Fatalf("rendered script command = %#v, %v", rendered, err)
	}
}

func TestLoadConfigValidatesOptionalConcurrentJobLimit(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  int
	}{
		{name: "omitted is unlimited", want: 0},
		{name: "positive limit", value: "max_concurrent_jobs = 1\n", want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			writeTestFile(t, filepath.Join(directory, "token"), "secret")
			path := filepath.Join(directory, "config.toml")
			writeTestFile(t, path, "[server]\nworker_token_file = \"token\"\n"+test.value)
			machinistConfig, err := LoadConfig(path)
			if err != nil || machinistConfig.Server.ConcurrentJobLimit() != test.want {
				t.Fatalf("limit = %d, error = %v", machinistConfig.Server.ConcurrentJobLimit(), err)
			}
		})
	}

	for _, value := range []string{"0", "-1"} {
		t.Run("reject "+value, func(t *testing.T) {
			directory := t.TempDir()
			writeTestFile(t, filepath.Join(directory, "token"), "secret")
			path := filepath.Join(directory, "config.toml")
			writeTestFile(t, path, "[server]\nworker_token_file = \"token\"\nmax_concurrent_jobs = "+value+"\n")
			if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "max_concurrent_jobs must be positive") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadShepherdSchedulesResolvesAgentAndLimits(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "shepherd.md"), "Policy:\n{{machinist.prompt}}\n")
	path := filepath.Join(directory, "config.toml")
	writeTestFile(t, path, `[commands.shepherd]
executor = "test"
prompt_file = "shepherd.md"
timeout = "2h"

[shepherd.api]
repository = "api"
every = "15m"
max_actions = 4
`)

	schedules, err := LoadShepherdSchedules(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 1 {
		t.Fatalf("schedules = %#v", schedules)
	}
	schedule := schedules[0]
	if schedule.Name != "api" || schedule.Repository != "api" || schedule.Every != 15*time.Minute || schedule.MaxActions != 4 {
		t.Fatalf("schedule = %#v", schedule)
	}
	if schedule.Command.Name != "shepherd" || schedule.Command.Timeout != 2*time.Hour || !strings.Contains(schedule.Command.Prompt, "max_actions=4") || !strings.Contains(schedule.Command.Prompt, "at most 4 mutating actions") {
		t.Fatalf("scheduled agent = %#v", schedule.Command)
	}
}

func TestLoadShepherdSchedulesRejectsUnsafeConfiguration(t *testing.T) {
	for name, test := range map[string]struct {
		body string
		want string
	}{
		"missing agent": {
			body: "[shepherd.api]\nrepository=\"api\"\nevery=\"15m\"\nmax_actions=1\n",
			want: "commands.shepherd",
		},
		"short interval": {
			body: "[commands.shepherd]\nexecutor=\"test\"\nprompt_file=\"shepherd.md\"\n[shepherd.api]\nrepository=\"api\"\nevery=\"30s\"\nmax_actions=1\n",
			want: "at least 1m",
		},
		"zero actions": {
			body: "[commands.shepherd]\nexecutor=\"test\"\nprompt_file=\"shepherd.md\"\n[shepherd.api]\nrepository=\"api\"\nevery=\"15m\"\nmax_actions=0\n",
			want: "must be positive",
		},
		"duplicate repository": {
			body: "[commands.shepherd]\nexecutor=\"test\"\nprompt_file=\"shepherd.md\"\n[shepherd.first]\nrepository=\"api\"\nevery=\"15m\"\nmax_actions=1\n[shepherd.second]\nrepository=\"api\"\nevery=\"30m\"\nmax_actions=2\n",
			want: "same repository",
		},
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			writeTestFile(t, filepath.Join(directory, "shepherd.md"), "{{machinist.prompt}}\n")
			path := filepath.Join(directory, "config.toml")
			writeTestFile(t, path, test.body)
			_, err := LoadShepherdSchedules(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadCommandResolvesPromptAndHashesDefinition(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "plan.md"), "Inspect the repository for {{machinist.prompt}}.\n")
	definition := filepath.Join(directory, "config.toml")
	writeTestFile(t, definition, `[commands.plan]
executor = "test"
prompt_file = "plan.md"
timeout = "45s"
`)

	agent, err := LoadCommand(definition, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Name != "plan" || agent.Prompt != "Inspect the repository for {{machinist.prompt}}.\n" {
		t.Fatalf("unexpected agent: %#v", agent)
	}
	if agent.Timeout != 45*time.Second {
		t.Fatalf("timeout = %s", agent.Timeout)
	}
	resolved, err := (Worker{Executors: map[string]Executor{"test": {Command: []string{"agent", "run"}}}}).ResolveCommandModel(agent, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Command) != 2 || resolved.Command[1] != "run" {
		t.Fatalf("command = %#v", resolved.Command)
	}
	if len(agent.Hash) != 64 {
		t.Fatalf("hash = %q", agent.Hash)
	}
}

func TestResolveCommandModelUsesAliasAndLeavesDefaultOptional(t *testing.T) {
	worker := Worker{Executors: map[string]Executor{"codex": {
		Command: []string{"codex", "exec", "--model=" + modelParameter, "-"},
		Models:  map[string]string{"luna": "gpt-5.6-luna"},
	}}}
	agent := ResolvedCommand{Executor: "codex"}

	resolved, err := worker.ResolveCommandModel(agent, "luna")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Model != "gpt-5.6-luna" || strings.Join(resolved.Command, " ") != "codex exec --model=gpt-5.6-luna -" {
		t.Fatalf("resolved = %#v", resolved)
	}
	defaulted, err := worker.ResolveCommandModel(agent, "")
	if err != nil {
		t.Fatal(err)
	}
	if defaulted.Model != "" || strings.Join(defaulted.Command, " ") != "codex exec -" {
		t.Fatalf("defaulted = %#v", defaulted)
	}
}

func TestResolveCommandModelRejectsUnsupportedSelection(t *testing.T) {
	for name, executor := range map[string]Executor{
		"missing placeholder": {Command: []string{"agent", "run"}},
		"unknown alias":       {Command: []string{"agent", "--model=" + modelParameter}, Models: map[string]string{"fast": "fast-v1"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := (Worker{Executors: map[string]Executor{"test": executor}}).ResolveCommandModel(ResolvedCommand{Executor: "test"}, "other")
			if err == nil {
				t.Fatal("expected model selection error")
			}
		})
	}
}

func TestWorkerModelCapabilitiesAndConfiguration(t *testing.T) {
	worker, err := applyWorkerDefaultsWithHostname(Worker{
		Name:          "test",
		DataDirectory: t.TempDir(),
		Executors: map[string]Executor{
			"aliased": {Command: []string{"agent", "--model=" + modelParameter}, Models: map[string]string{"slow": "v2", "fast": "v1"}},
			"raw":     {Command: []string{"agent", "--model=" + modelParameter}},
			"fixed":   {Command: []string{"agent"}},
		},
	}, func() (string, error) { return "unused", nil })
	if err != nil {
		t.Fatal(err)
	}
	capabilities := worker.ModelCapabilities()
	if strings.Join(capabilities["aliased"], ",") != "fast,slow" || capabilities["raw"] == nil {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if _, ok := capabilities["fixed"]; ok {
		t.Fatalf("fixed executor advertised model support: %#v", capabilities)
	}

	_, err = applyWorkerDefaultsWithHostname(Worker{
		Name:          "test",
		DataDirectory: t.TempDir(),
		Executors:     map[string]Executor{"invalid": {Command: []string{"agent"}, Models: map[string]string{"fast": "v1"}}},
	}, func() (string, error) { return "unused", nil })
	if err == nil || !strings.Contains(err.Error(), modelParameter) {
		t.Fatalf("invalid model config error = %v", err)
	}
}

func TestLoadWorkerRejectsCompoundModelPlaceholderArgument(t *testing.T) {
	_, err := applyWorkerDefaultsWithHostname(Worker{
		Name:          "test",
		DataDirectory: t.TempDir(),
		Executors:     map[string]Executor{"invalid": {Command: []string{"agent", "prefix-" + modelParameter}}},
	}, func() (string, error) { return "unused", nil })
	if err == nil || !strings.Contains(err.Error(), "complete optional") {
		t.Fatalf("compound placeholder error = %v", err)
	}
}

func TestLoadWorkerRejectsLegacyFactoryModelParameter(t *testing.T) {
	_, err := applyWorkerDefaultsWithHostname(Worker{
		Name:          "test",
		DataDirectory: t.TempDir(),
		Executors:     map[string]Executor{"invalid": {Command: []string{"agent", "--model={{factory.model}}"}}},
	}, func() (string, error) { return "unused", nil })
	if err == nil || !strings.Contains(err.Error(), "legacy Factory parameter namespace") {
		t.Fatalf("legacy model parameter error = %v", err)
	}
}

func TestRenderPromptReplacesEveryPromptParameterWithoutReevaluation(t *testing.T) {
	agent := ResolvedCommand{Prompt: "Before {{machinist.prompt}} between {{machinist.prompt}} after"}
	prompt := "fix {{machinist.prompt}} and $(touch never)"
	rendered, err := RenderPrompt(agent, prompt)
	if err != nil {
		t.Fatal(err)
	}
	want := "Before " + prompt + " between " + prompt + " after"
	if rendered.Prompt != want {
		t.Fatalf("prompt = %q, want %q", rendered.Prompt, want)
	}
}

func TestRenderPromptRejectsEmptyAndOversizedPrompts(t *testing.T) {
	agent := ResolvedCommand{Prompt: promptParameter}
	if _, err := RenderPrompt(agent, " \n\t"); err == nil || !strings.Contains(err.Error(), "prompt is required") {
		t.Fatalf("expected empty-prompt error, got %v", err)
	}
	if _, err := RenderPrompt(agent, strings.Repeat("x", maxInputPromptBytes+1)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected prompt-size error, got %v", err)
	}
}

func TestRenderPromptRejectsOversizedRenderedPromptBeforeReplacement(t *testing.T) {
	agent := ResolvedCommand{
		Name:   "plan",
		Prompt: strings.Repeat(promptParameter, maxPromptBytes/len(promptParameter)),
	}
	if _, err := RenderPrompt(agent, strings.Repeat("x", maxInputPromptBytes)); err == nil || !strings.Contains(err.Error(), "rendered command prompt exceeds") {
		t.Fatalf("expected rendered-size error, got %v", err)
	}
}

func TestLoadCommandRequiresPromptParameterAndRejectsUnsupportedMachinistParameter(t *testing.T) {
	for _, test := range []struct {
		name   string
		prompt string
		want   string
	}{
		{name: "missing prompt", prompt: "Plan this ticket.\n", want: "must include {{machinist.prompt}}"},
		{name: "legacy Factory namespace", prompt: "Plan {{machinist.prompt}} with {{factory.prompt}}.\n", want: "legacy Factory parameter namespace"},
		{name: "legacy task parameter", prompt: "Plan {{machinist.task}}.\n", want: "unsupported Machinist parameter"},
		{name: "unsupported parameter", prompt: "Plan {{machinist.prompt}} in {{machinist.repository}}.\n", want: "unsupported Machinist parameter"},
		{name: "empty parameter", prompt: "Plan {{machinist.prompt}} with {{machinist.}}.\n", want: "unsupported Machinist parameter"},
		{name: "unclosed parameter", prompt: "Plan {{machinist.prompt}} with {{machinist.repository.\n", want: "malformed Machinist parameter"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			writeTestFile(t, filepath.Join(directory, "plan.md"), test.prompt)
			definition := filepath.Join(directory, "config.toml")
			writeTestFile(t, definition, "[commands.plan]\nexecutor = \"test\"\nprompt_file = \"plan.md\"\n")
			if _, err := LoadCommand(definition, "plan"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}

func TestLoadCommandRejectsMissingAndInvalidDefinitions(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "plan.md"), "Plan.\n")
	definition := filepath.Join(directory, "config.toml")
	writeTestFile(t, definition, `[commands.plan]
prompt_file = "plan.md"
`)

	if _, err := LoadCommand(definition, "missing"); err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("expected missing-agent error, got %v", err)
	}
	if _, err := LoadCommand(definition, "plan"); err == nil || !strings.Contains(err.Error(), "must define executor") {
		t.Fatalf("expected executor error, got %v", err)
	}
}

func TestExampleCommandDefinitionsLoad(t *testing.T) {
	definition := filepath.Join("..", "..", "examples", "config.toml")
	definitions, err := LoadDefinitions(definition)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions.Commands) != 3 {
		t.Fatalf("example commands = %#v, want foreman, audit, and shepherd", definitions.Commands)
	}
	if _, ok := definitions.Commands["foreman"]; !ok {
		t.Fatal("example foreman agent is missing")
	}
	if _, ok := definitions.Commands["audit"]; !ok {
		t.Fatal("example audit agent is missing")
	}
	if _, ok := definitions.Commands["shepherd"]; !ok {
		t.Fatal("example shepherd agent is missing")
	}

	for _, name := range []string{"foreman", "audit", "shepherd"} {
		t.Run(name, func(t *testing.T) {
			agent, err := LoadCommand(definition, name)
			if err != nil {
				t.Fatal(err)
			}
			if agent.Name != name {
				t.Fatalf("agent name = %q, want %q", agent.Name, name)
			}
			if !strings.Contains(agent.Prompt, promptParameter) {
				t.Fatalf("agent prompt does not contain %s", promptParameter)
			}
		})
	}
	foreman, err := LoadCommand(definition, "foreman")
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{
		"Never plan the solution",
		"Perform this discovery at the start of every run",
		"**Existing implementation:**",
		"**CI failure:**",
		"**Review feedback:**",
		"**Open pull request:**",
		"**Completed planning:**",
		"**New issue:**",
		"a verified branch without an open pull request",
		"any dirty or incomplete work",
		"Positive numbers are repairs",
		"reset, reuse, or cap it on resume",
		"repair count without a maximum",
		"Existing work must reuse its branch, worktree, and pull request",
		"create a second pull request for the issue",
		"`machinist:ready-for-review` or a verified ready/completed state",
		"stale remote",
		"Repair or create its deterministic isolated worktree",
		"fast-forward a clean local head that is an ancestor",
		"Preserve dirty, ahead, or unpublished",
		"each recorded head is an ancestor of",
		"Never overwrite",
		"Create a missing local",
		"clean worktree and equality between the local branch head",
		"Every subagent prompt must require a concise Markdown handoff",
		"## Planning handoff",
		"## Build handoff",
		"## Review handoff",
		"## Repair handoff",
		"complete diff",
		"inspect the branch, HEAD, worktree",
		"return a valid handoff, whether it exits or remains active",
		"read-only reviewer",
		"Never inline the diff",
		"non-draft pull request linked",
		"For both paths, confirm the base, exact head, issue link",
		"recheck that it is open before pushing",
		"return to linked-pull-request resolution",
		"Use this one loop for local review, CI",
		"Resolve linked pull requests before",
		"Reuse exactly one open pull request and ignore historical closed or merged",
		"If multiple are open, or none is open and any is merged",
		"With none open and",
		"closed-unmerged candidates present",
		"multiple candidates or any",
		"selection, reopening, or verification failure",
		"For any existing or reopened",
		"open pull request without a usable worktree",
		"After every code change",
		"Approval applies only to the reviewed SHA",
		"push `<approved-sha>:refs/heads/<branch>`",
		"automated reviewers and review bots",
		"event, branch, path",
		"exactly match the",
		"missing expected results remain pending",
		"Exclude human",
		"Poll no more often than every 30 seconds",
		"at most 20 minutes",
		"set `machinist:blocked`",
		"resolve only threads whose feedback is fully addressed",
		"Compare each finding with",
		"`<!-- machinist:foreman-pr -->`",
		"persist its head, approval, checks",
		"If none remain, return to the originating stage",
		"Persist the count",
		"immediately after a code-changing commit and before Local review",
		"failure keeps the prior count",
		"If no",
		"pull request exists, continue to Create or reuse the pull request",
		"Never merge",
		"Keep the open-pull-request worktree",
		"Before any terminal stop or handoff",
	} {
		if !strings.Contains(foreman.Prompt, rule) {
			t.Fatalf("foreman prompt does not contain %q", rule)
		}
	}
	for _, forbidden := range []string{
		"open one draft pull request",
		"mark the pull request ready for human review",
		"branch, complete diff",
		"SUBAGENT role=<role>",
		"Attempts `1` and `2` are the two allowed repairs",
		"at most two total",
		"block if it would exceed two",
		"Attempts `1`, `2`, and `3` are the allowed repairs",
		"at most three total",
		"block if it would exceed three",
	} {
		if strings.Contains(foreman.Prompt, forbidden) {
			t.Fatalf("foreman prompt still contains %q", forbidden)
		}
	}
	for _, heading := range []string{
		"# Ordered state entry\n",
		"## Local review\n",
		"## Automation gate\n",
		"# Shared repair loop\n",
	} {
		if count := strings.Count(foreman.Prompt, heading); count != 1 {
			t.Fatalf("foreman prompt contains %q %d times, want once", heading, count)
		}
	}
	if existing, open := strings.Index(foreman.Prompt, "**Existing implementation:**"), strings.Index(foreman.Prompt, "**Open pull request:**"); existing < 0 || open < 0 || existing > open {
		t.Fatalf("foreman prompt must classify unpublished implementation before open pull request: existing=%d open=%d", existing, open)
	}
	if reopen, recover := strings.Index(foreman.Prompt, "closed-unmerged candidates present"), strings.Index(foreman.Prompt, "For any existing or reopened"); reopen < 0 || recover < 0 || reopen > recover {
		t.Fatalf("foreman prompt must reopen a unique safe pull request before worktree recovery: reopen=%d recover=%d", reopen, recover)
	}
	if words := len(strings.Fields(foreman.Prompt)); words > 2200 {
		t.Fatalf("foreman prompt has %d words, want no more than 2200", words)
	}

	audit, err := LoadCommand(definition, "audit")
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{
		"fresh general-purpose subagents",
		"For every candidate",
		"separate fresh general-purpose",
		"Do not combine candidates in one verification task",
		"verifier does not confirm as a correctness bug",
		"current open GitHub issues",
		"no more than three issues",
		"affected files and",
		"observed behavior, expected",
		"Never edit, create, delete, move, or format",
		"Never create or switch branches, commit, push, or open a pull request",
		"Never fix a bug, create a",
	} {
		if !strings.Contains(audit.Prompt, rule) {
			t.Fatalf("audit prompt does not contain %q", rule)
		}
	}
}

func TestWorkflowExampleDefinitionsLoad(t *testing.T) {
	tests := []struct {
		name     string
		commands []string
	}{
		{name: "issue-to-pr", commands: []string{"issue-to-pr"}},
		{name: "multi-review", commands: []string{"multi-review"}},
		{name: "code-audit", commands: []string{"code-audit"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := filepath.Join("..", "..", "examples", "workflows", test.name, "config.toml")
			for _, name := range test.commands {
				agent, err := LoadCommand(definition, name)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(agent.Prompt, promptParameter) {
					t.Fatalf("agent %q prompt does not contain %s", name, promptParameter)
				}
				if test.name == "issue-to-pr" {
					for _, rule := range []string{"continue without a fixed cap", "Repair confirmed code defects with the next repair number"} {
						if !strings.Contains(agent.Prompt, rule) {
							t.Fatalf("agent %q prompt does not contain %q", name, rule)
						}
					}
					for _, obsolete := range []string{"at most two repair rounds", "same two-round limit", "after both repair rounds", "at most three repair rounds", "same three-round limit", "after all three repair rounds"} {
						if strings.Contains(agent.Prompt, obsolete) {
							t.Fatalf("agent %q prompt still contains %q", name, obsolete)
						}
					}
				}
				if test.name == "multi-review" {
					continue
				}
				for _, section := range []string{"# Role", "# Input", "# Required result", "# Procedure", "# Boundaries"} {
					if !strings.Contains(agent.Prompt, section) {
						t.Fatalf("agent %q prompt does not contain %q", name, section)
					}
				}
			}
		})
	}
}

func TestShippedGuidanceDoesNotDescribeARepairCap(t *testing.T) {
	paths := []string{
		filepath.Join("..", "..", "skills", "machinist", "SKILL.md"),
		filepath.Join("..", "..", ".github", "site", "index.html"),
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, obsolete := range []string{"repair limit", "Limited repair attempts", "Repair loops have a fixed limit", "Bounded repair"} {
			if strings.Contains(string(body), obsolete) {
				t.Fatalf("%s still contains %q", path, obsolete)
			}
		}
	}
}

func TestShippedMachinistSkillDescribesGitHubIntake(t *testing.T) {
	path := filepath.Join("..", "..", "skills", "machinist", "SKILL.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	guidance := string(body)
	for _, required := range []string{"[triggers.github.<name>]", "machinist:requested", "machinist:queued", "intake labels"} {
		if !strings.Contains(guidance, required) {
			t.Fatalf("%s does not describe %q", path, required)
		}
	}
	if strings.Contains(guidance, "Label-based delegation is not implemented") {
		t.Fatalf("%s still rejects label-based delegation", path)
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
