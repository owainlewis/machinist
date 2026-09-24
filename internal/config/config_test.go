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
	names := []string{"audit", "run", "task-to-pr"}
	if len(definitions.Commands) != len(names) {
		t.Fatalf("example commands = %#v, want %v", definitions.Commands, names)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			command, err := LoadCommand(definition, name)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(command.Prompt, promptParameter) {
				t.Fatalf("command prompt does not contain %s", promptParameter)
			}
		})
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

func TestLoadConfigRejectsRemovedShepherdSchedules(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.toml")
	writeTestFile(t, filepath.Join(directory, "shepherd.md"), "{{machinist.prompt}}\n")
	writeTestFile(t, path, "[commands.shepherd]\nexecutor=\"test\"\nprompt_file=\"shepherd.md\"\n[shepherd.api]\nrepository=\"api\"\nevery=\"15m\"\nmax_actions=1\n")
	_, err := LoadDefinitions(path)
	if err == nil || !strings.Contains(err.Error(), "shepherd schedules were removed") {
		t.Fatalf("error = %v, want removed shepherd schedule guidance", err)
	}
}

func TestDirectCommandNamesLeavesOutWorkflowOnlyCommands(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "plan.md"), "Plan {{task.spec}} into {{task.output_dir}}\n")
	writeTestFile(t, filepath.Join(directory, "audit.md"), "Audit: {{machinist.prompt}}\n")
	path := filepath.Join(directory, "config.toml")
	writeTestFile(t, path, `[commands.run]
executor = "codex"

[commands.audit]
executor = "codex"
prompt_file = "audit.md"

[commands.plan]
executor = "codex"
prompt_file = "plan.md"
`)
	definition, err := LoadDefinitions(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(definition.DirectCommandNames(), ","); got != "audit,run" {
		t.Fatalf("direct commands = %s, want audit,run", got)
	}
}
