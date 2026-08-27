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
	writeTestFile(t, path, `[agents.foreman]
executor = "codex"
prompt_file = "foreman.md"

[agents.audit]
executor = "codex"
prompt_file = "audit.md"

[pipelines.review]
agents = ["audit", "foreman"]

[github.repositories]
machinist = "owainlewis/machinist"

[triggers.github.issue-intake]
every = "5m"
label = "machinist:requested"
agent = "foreman"

[triggers.interval.repository-audit]
every = "6h"
repository = "machinist"
pipeline = "review"
model = "fast"
prompt = "Audit this repository for provable bugs."

[triggers.cron.nightly-audit]
schedule = "0 2 * * *"
timezone = "UTC"
repository = "machinist"
agent = "audit"
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
	if !strings.Contains(github.Agents[0].Prompt, promptParameter) {
		t.Fatalf("github agent was prematurely rendered: %#v", github.Agents[0])
	}
	interval := resolved[1]
	if interval.Identity != "interval/repository-audit" || interval.Repository != "machinist" || interval.GitHubRepository != "owainlewis/machinist" || interval.SelectionKind != "pipeline" || len(interval.Agents) != 2 {
		t.Fatalf("interval trigger = %#v", interval)
	}
	for _, agent := range interval.Agents {
		if !strings.Contains(agent.Prompt, interval.Prompt) || agent.Model != "fast" {
			t.Fatalf("interval agent = %#v", agent)
		}
	}
	cron := resolved[2]
	if cron.Identity != "cron/nightly-audit" || cron.Schedule != "0 2 * * *" || cron.Timezone != "UTC" || !strings.Contains(cron.Agents[0].Prompt, cron.Prompt) {
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
		"conflicting selection": {
			body: `[triggers.github.intake]
every="5m"
label="requested"
agent="audit"
pipeline="review"
`,
			want: "exactly one",
		},
		"unknown fixed repository": {
			body: `[triggers.interval.audit]
every="1h"
repository="missing"
agent="audit"
prompt="audit"
`,
			want: `trigger "interval/audit"`,
		},
		"short github interval": {
			body: `[triggers.github.intake]
every="59s"
label="requested"
agent="audit"
`,
			want: "between 1m0s and 24h0m0s",
		},
		"long fixed interval": {
			body: `[triggers.interval.audit]
every="721h"
repository="machinist"
agent="audit"
prompt="audit"
`,
			want: "720h0m0s",
		},
		"empty prompt": {
			body: `[triggers.interval.audit]
every="1h"
repository="machinist"
agent="audit"
prompt="  "
`,
			want: "prompt is required",
		},
		"invalid cron": {
			body: `[triggers.cron.audit]
schedule="0 0 0 * * *"
timezone="UTC"
repository="machinist"
agent="audit"
prompt="audit"
`,
			want: "exactly five fields",
		},
		"invalid timezone": {
			body: `[triggers.cron.audit]
schedule="0 0 * * *"
timezone="Nowhere/Invalid"
repository="machinist"
agent="audit"
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
			writeTestFile(t, path, `[agents.audit]
executor="codex"
prompt_file="audit.md"
[pipelines.review]
agents=["audit"]
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
		"unsafe":      "../owner/repo",
		"credentials": "https://token@example.com/owner/repo",
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

func TestLoadTriggersRejectsShepherdDirectlyAndThroughPipeline(t *testing.T) {
	for _, selection := range []string{"agent=\"shepherd\"", "pipeline=\"with-shepherd\""} {
		t.Run(selection, func(t *testing.T) {
			directory := t.TempDir()
			writeTestFile(t, filepath.Join(directory, "shepherd.md"), "{{machinist.prompt}}\n")
			path := filepath.Join(directory, "config.toml")
			writeTestFile(t, path, `[agents.shepherd]
executor="codex"
prompt_file="shepherd.md"
[pipelines.with-shepherd]
agents=["shepherd"]
[github.repositories]
machinist="owainlewis/machinist"
[triggers.interval.audit]
every="1h"
repository="machinist"
prompt="audit"
`+selection+"\n")
			_, err := LoadTriggers(path)
			if err == nil || !strings.Contains(err.Error(), "cannot select Shepherd") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestTriggerSignatureChangesWithExecutionAndSchedule(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "audit.md"), "{{machinist.prompt}}\n")
	path := filepath.Join(directory, "config.toml")
	body := `[agents.audit]
executor="codex"
prompt_file="audit.md"
[github.repositories]
machinist="owainlewis/machinist"
[triggers.interval.audit]
every="1h"
repository="machinist"
agent="audit"
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
