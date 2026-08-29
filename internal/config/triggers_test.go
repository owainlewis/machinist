package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadTriggersResolvesAllFamilies(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "foreman.md"), "Foreman: {{machinist.prompt}}\n")
	writeTestFile(t, filepath.Join(directory, "audit.md"), "Audit: {{machinist.prompt}}\n")
	path := filepath.Join(directory, "config.toml")
	writeTestFile(t, path, `[commands.foreman]
executor = "codex"
prompt_file = "foreman.md"

[commands.audit]
executor = "codex"
prompt_file = "audit.md"

[github.repositories]
machinist = "owainlewis/machinist"

[triggers.github.issue-intake]
every = "5m"
label = "machinist:requested"
command = "foreman"

[triggers.interval.repository-audit]
every = "6h"
repository = "machinist"
command = "audit"
model = "fast"
prompt = "Audit this repository for provable bugs."

[triggers.cron.nightly-audit]
schedule = "0 2 * * *"
timezone = "UTC"
repository = "machinist"
command = "audit"
prompt = "Audit this repository for provable bugs."
`)

	resolved, err := LoadTriggers(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 3 {
		t.Fatalf("triggers = %#v", resolved)
	}
	github := resolved[0]
	if github.Identity != "github/issue-intake" || github.Every != 5*time.Minute || github.Label != "machinist:requested" || github.GitHubRepositories["machinist"] != "owainlewis/machinist" {
		t.Fatalf("github trigger = %#v", github)
	}
	if !strings.Contains(github.Command.Prompt, promptParameter) {
		t.Fatalf("github command was prematurely rendered: %#v", github.Command)
	}
	interval := resolved[1]
	if interval.Identity != "interval/repository-audit" || interval.Repository != "machinist" || interval.GitHubRepository != "owainlewis/machinist" {
		t.Fatalf("interval trigger = %#v", interval)
	}
	if !strings.Contains(interval.Command.Prompt, interval.Prompt) || interval.Command.Model != "fast" {
		t.Fatalf("interval command = %#v", interval.Command)
	}
	cron := resolved[2]
	if cron.Identity != "cron/nightly-audit" || cron.Schedule != "0 2 * * *" || cron.Timezone != "UTC" || !strings.Contains(cron.Command.Prompt, cron.Prompt) {
		t.Fatalf("cron trigger = %#v", cron)
	}
	startup := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	if got := github.FirstDue(startup); !got.Equal(startup) {
		t.Fatalf("github first due = %s", got)
	}
	if got, want := interval.FirstDue(startup), startup.Add(6*time.Hour); !got.Equal(want) {
		t.Fatalf("interval first due = %s, want %s", got, want)
	}
	if got, want := cron.FirstDue(startup), time.Date(2026, time.August, 28, 2, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("cron first due = %s, want %s", got, want)
	}
	for _, trigger := range resolved {
		if len(trigger.Signature) != 64 {
			t.Fatalf("trigger %s signature = %q", trigger.Identity, trigger.Signature)
		}
	}
}

func TestLoadTriggersKeepsEmptyConfigurationCompatible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeTestFile(t, path, "")
	resolved, err := LoadTriggers(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 0 {
		t.Fatalf("triggers = %#v", resolved)
	}
}

func TestLoadTriggersRejectsUnknownTriggerField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeTestFile(t, path, "[triggers.interval.audit]\nunknown=true\n")
	_, err := LoadTriggers(path)
	if err == nil || !strings.Contains(err.Error(), `trigger "interval/audit" has unknown field "unknown"`) {
		t.Fatalf("error = %v", err)
	}

	writeTestFile(t, path, "[triggers.timer.audit]\nevery=\"1h\"\n")
	_, err = LoadTriggers(path)
	if err == nil || !strings.Contains(err.Error(), `trigger "timer/audit" uses unknown family "timer"`) {
		t.Fatalf("unknown family error = %v", err)
	}
}

func TestLoadTriggersRejectsGitHubSelectionThatCannotRenderMaximumIssuePrompt(t *testing.T) {
	for _, selection := range []string{"command=\"static\""} {
		t.Run(strings.Split(selection, "=")[0], func(t *testing.T) {
			directory := t.TempDir()
			writeTestFile(t, filepath.Join(directory, "static.md"), strings.Repeat(promptParameter, 3_000))
			writeTestFile(t, filepath.Join(directory, "dynamic.md"), "Complete {{machinist.prompt}}\n")
			path := filepath.Join(directory, "config.toml")
			writeTestFile(t, path, `[commands.static]
executor="codex"
prompt_file="static.md"
[commands.dynamic]
executor="codex"
prompt_file="dynamic.md"
[github.repositories]
machinist="owainlewis/machinist"
[triggers.github.intake]
every="5m"
label="machinist:requested"
`+selection+"\n")
			_, err := LoadTriggers(path)
			if err == nil || !strings.Contains(err.Error(), `trigger "github/intake": rendered command prompt exceeds`) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadTriggersRejectsInvalidConfigurationWithIdentity(t *testing.T) {
	tests := map[string]struct {
		body string
		want string
	}{
		"missing selection": {
			body: `[triggers.github.intake]
every="5m"
label="requested"
`,
			want: `trigger "github/intake"`,
		},
		"unknown fixed repository": {
			body: `[triggers.interval.audit]
every="1h"
repository="missing"
command="audit"
prompt="audit"
`,
			want: `trigger "interval/audit"`,
		},
		"short github interval": {
			body: `[triggers.github.intake]
every="59s"
label="requested"
command="audit"
`,
			want: "between 1m0s and 24h0m0s",
		},
		"comma github label": {
			body: `[triggers.github.intake]
every="5m"
label="requested,urgent"
command="audit"
`,
			want: "without commas",
		},
		"long fixed interval": {
			body: `[triggers.interval.audit]
every="721h"
repository="machinist"
command="audit"
prompt="audit"
`,
			want: "720h0m0s",
		},
		"empty prompt": {
			body: `[triggers.interval.audit]
every="1h"
repository="machinist"
command="audit"
prompt="  "
`,
			want: "prompt is required",
		},
		"invalid cron": {
			body: `[triggers.cron.audit]
schedule="0 0 0 * * *"
timezone="UTC"
repository="machinist"
command="audit"
prompt="audit"
`,
			want: "exactly five fields",
		},
		"impossible cron date": {
			body: `[triggers.cron.audit]
schedule="0 0 31 2 *"
timezone="UTC"
repository="machinist"
command="audit"
prompt="audit"
`,
			want: `trigger "cron/audit" schedule: cron schedule has no possible occurrence`,
		},
		"invalid timezone": {
			body: `[triggers.cron.audit]
schedule="0 0 * * *"
timezone="Nowhere/Invalid"
repository="machinist"
command="audit"
prompt="audit"
`,
			want: "timezone",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			writeTestFile(t, filepath.Join(directory, "audit.md"), "{{machinist.prompt}}\n")
			path := filepath.Join(directory, "config.toml")
			writeTestFile(t, path, `[commands.audit]
executor="codex"
prompt_file="audit.md"
[github.repositories]
machinist="owainlewis/machinist"
`+test.body)
			_, err := LoadTriggers(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadTriggersRejectsUnsafeOrDuplicateRepositorySlugs(t *testing.T) {
	tests := map[string]string{
		"unsafe":         "../owner/repo",
		"credentials":    "https://token@example.com/owner/repo",
		"hidden name":    "owner/.repo",
		"path-like name": "owner/repo..x",
	}
	for name, slug := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			writeTestFile(t, path, "[github.repositories]\nrepo = \""+slug+"\"\n")
			_, err := LoadTriggers(path)
			if err == nil || !strings.Contains(err.Error(), "safe OWNER/REPO") {
				t.Fatalf("error = %v", err)
			}
		})
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "config.toml")
	writeTestFile(t, path, `[github.repositories]
first="Owner/Repo"
second="owner/repo"
`)
	if _, err := LoadTriggers(path); err == nil || !strings.Contains(err.Error(), "same case-insensitive slug") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestLoadTriggersRejectsReservedQueuedLabelCaseInsensitively(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "foreman.md"), "{{machinist.prompt}}\n")
	path := filepath.Join(directory, "config.toml")
	writeTestFile(t, path, `[commands.foreman]
executor="codex"
prompt_file="foreman.md"
[github.repositories]
machinist="owainlewis/machinist"
[triggers.github.intake]
every="5m"
label="Machinist:Queued"
command="foreman"
`)

	_, err := LoadTriggers(path)
	if err == nil || !strings.Contains(err.Error(), `trigger "github/intake" label must differ from reserved label "machinist:queued"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestTriggerSignatureChangesWithExecutionAndSchedule(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "audit.md"), "{{machinist.prompt}}\n")
	path := filepath.Join(directory, "config.toml")
	body := `[commands.audit]
executor="codex"
prompt_file="audit.md"
[github.repositories]
machinist="owainlewis/machinist"
[triggers.interval.audit]
every="1h"
repository="machinist"
command="audit"
prompt="audit"
`
	writeTestFile(t, path, body)
	first, err := LoadTriggers(path)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, strings.Replace(body, `every="1h"`, `every="2h"`, 1))
	second, err := LoadTriggers(path)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Signature == second[0].Signature {
		t.Fatal("signature did not change with schedule")
	}
}
