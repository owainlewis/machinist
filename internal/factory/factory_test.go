package factory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func validFactory() map[string]string {
	return map[string]string{
		"agents/default/agent.toml":                     "harness = \"claude\"\nmodel = \"sonnet\"\n",
		"agents/default/playbook.md":                    "Do good work.\n",
		"agents/default/.claude/agents/machinist-x.md":  "claude agent",
		"agents/default/.codex/agents/machinist-x.toml": "name = \"x\"",
		"reviewers/review.md":                           "Review the PR.\n",
		"automations/nightly.md":                        "+++\nschedule = \"0 6 * * 1\"\ntimezone = \"UTC\"\nrepo = \"owner/repo\"\nagent = \"default\"\n+++\nCheck the repo.\n",
	}
}

func TestLoadReadsAgentsReviewersAndAutomations(t *testing.T) {
	factory, err := Load(writeFiles(t, validFactory()))
	if err != nil {
		t.Fatal(err)
	}
	agent := factory.Agents["default"]
	if agent.Harness != "claude" || agent.Model != "sonnet" || agent.Playbook != "Do good work.\n" {
		t.Fatalf("agent = %+v", agent)
	}
	if len(agent.Files) != 2 || agent.Files[0].Path != ".claude/agents/machinist-x.md" || agent.Files[1].Path != ".codex/agents/machinist-x.toml" || agent.Files[0].Body != "claude agent" {
		t.Fatalf("files = %+v", agent.Files)
	}
	if len(factory.Reviewers) != 1 || factory.Reviewers[0].Name != "review" {
		t.Fatalf("reviewers = %+v", factory.Reviewers)
	}
	automation := factory.Automations[0]
	if automation.Name != "nightly" || automation.Repo != "owner/repo" || automation.Prompt != "Check the repo." || automation.Cron == nil {
		t.Fatalf("automation = %+v", automation)
	}
	if err := factory.Validate("default", []string{"owner/repo"}); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAllowsNoReviewersOrAutomations(t *testing.T) {
	files := validFactory()
	delete(files, "reviewers/review.md")
	delete(files, "automations/nightly.md")
	factory, err := Load(writeFiles(t, files))
	if err != nil || len(factory.Reviewers) != 0 || len(factory.Automations) != 0 {
		t.Fatalf("factory = %+v, err = %v", factory, err)
	}
}

func TestLoadRejectsInvalidFactories(t *testing.T) {
	for _, test := range []struct {
		name, want string
		change     func(map[string]string)
	}{
		{"no agents", "", func(files map[string]string) {
			for name := range files {
				if strings.HasPrefix(name, "agents/") {
					delete(files, name)
				}
			}
		}},
		{"unknown harness", "harness must be", func(files map[string]string) { files["agents/default/agent.toml"] = "harness = \"gemini\"\n" }},
		{"old key", "unknown key command", func(files map[string]string) {
			files["agents/default/agent.toml"] = "harness = \"claude\"\ncommand = [\"x\"]\n"
		}},
		{"missing playbook", "playbook.md", func(files map[string]string) { delete(files, "agents/default/playbook.md") }},
		{"empty playbook", "playbook.md is empty", func(files map[string]string) { files["agents/default/playbook.md"] = " \n" }},
		{"empty reviewer", "prompt is empty", func(files map[string]string) { files["reviewers/review.md"] = "\n" }},
		{"no front matter", "front matter", func(files map[string]string) { files["automations/nightly.md"] = "Just a prompt\n" }},
		{"unclosed front matter", "not closed", func(files map[string]string) { files["automations/nightly.md"] = "+++\nrepo = \"x\"\n" }},
		{"bad cron", "", func(files map[string]string) {
			files["automations/nightly.md"] = "+++\nschedule = \"nope\"\ntimezone = \"UTC\"\nrepo = \"o/r\"\nagent = \"default\"\n+++\nGo.\n"
		}},
		{"no repo", "repo and agent", func(files map[string]string) {
			files["automations/nightly.md"] = "+++\nschedule = \"0 6 * * 1\"\ntimezone = \"UTC\"\nagent = \"default\"\n+++\nGo.\n"
		}},
		{"empty automation prompt", "prompt is empty", func(files map[string]string) {
			files["automations/nightly.md"] = "+++\nschedule = \"0 6 * * 1\"\ntimezone = \"UTC\"\nrepo = \"o/r\"\nagent = \"default\"\n+++\n\n"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := validFactory()
			test.change(files)
			root := writeFiles(t, files)
			if err := os.MkdirAll(filepath.Join(root, "agents"), 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(root); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadRejectsSymlinkedPlaybook(t *testing.T) {
	root := writeFiles(t, validFactory())
	path := filepath.Join(root, "agents", "default", "playbook.md")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateChecksConfigurationReferences(t *testing.T) {
	factory, err := Load(writeFiles(t, validFactory()))
	if err != nil {
		t.Fatal(err)
	}
	if err := factory.Validate("missing", []string{"owner/repo"}); err == nil || !strings.Contains(err.Error(), "default_agent") {
		t.Fatalf("error = %v", err)
	}
	if err := factory.Validate("default", []string{"owner/other"}); err == nil || !strings.Contains(err.Error(), "github.repositories") {
		t.Fatalf("error = %v", err)
	}
	factory.Automations[0].Agent = "ghost"
	if err := factory.Validate("default", []string{"owner/repo"}); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error = %v", err)
	}
}

// jobFactory is validFactory with a second agent and a job of three stages.
func jobFactory() map[string]string {
	files := validFactory()
	files["agents/reviewer/agent.toml"] = "harness = \"claude\"\nmodel = \"opus\"\n"
	files["agents/reviewer/playbook.md"] = "Review the branch.\n"
	files["jobs/ship.toml"] = `[[stages]]
name = "build"
agent = "default"

[[stages]]
name = "test"
script = "go test ./..."

[[stages]]
name = "review"
agent = "reviewer"
`
	return files
}

func TestLoadReadsJobs(t *testing.T) {
	factory, err := Load(writeFiles(t, jobFactory()))
	if err != nil {
		t.Fatal(err)
	}
	job, ok := factory.Job("ship")
	if !ok || job.Harness != "claude" || len(job.Stages) != 3 {
		t.Fatalf("job = %+v", job)
	}
	if job.Stages[0] != (Stage{Name: "build", Agent: "default"}) || job.Stages[1] != (Stage{Name: "test", Script: "go test ./..."}) || job.Stages[2].Agent != "reviewer" {
		t.Fatalf("stages = %+v", job.Stages)
	}
	if err := factory.Validate("ship", []string{"owner/repo"}); err != nil {
		t.Fatal(err)
	}
}

func TestAnAgentIsAJobWithOneStage(t *testing.T) {
	factory, err := Load(writeFiles(t, validFactory()))
	if err != nil {
		t.Fatal(err)
	}
	job, ok := factory.Job("default")
	if !ok || job.Harness != "claude" || len(job.Stages) != 1 || job.Stages[0] != (Stage{Name: "default", Agent: "default"}) {
		t.Fatalf("job = %+v", job)
	}
	if _, ok := factory.Job("missing"); ok {
		t.Fatal("found a job that is not in the factory")
	}
}

func TestLoadRejectsInvalidJobs(t *testing.T) {
	stage := func(body string) string { return "[[stages]]\n" + body + "\n" }
	for _, test := range []struct {
		name, want, job string
	}{
		{"no stages", "at least one agent stage", ""},
		{"only scripts", "at least one agent stage", stage(`name = "test"` + "\n" + `script = "true"`)},
		{"agent and script", "agent or script", stage(`name = "build"` + "\n" + `agent = "default"` + "\n" + `script = "true"`)},
		{"neither", "agent or script", stage(`name = "build"`)},
		{"bad stage name", "invalid name", stage(`name = "Build It"` + "\n" + `agent = "default"`)},
		{"stage twice", "listed twice", stage(`name = "build"`+"\n"+`agent = "default"`) + stage(`name = "build"`+"\n"+`script = "true"`)},
		{"unknown agent", `agent "ghost" is not in the factory`, stage(`name = "build"` + "\n" + `agent = "ghost"`)},
		{"two harnesses", "an earlier stage uses claude", stage(`name = "build"`+"\n"+`agent = "default"`) + stage(`name = "review"`+"\n"+`agent = "other"`)},
		{"unknown key", "unknown key", stage(`name = "build"` + "\n" + `agent = "default"` + "\n" + `needs = "test"`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := validFactory()
			files["agents/other/agent.toml"] = "harness = \"codex\"\n"
			files["agents/other/playbook.md"] = "Review.\n"
			files["jobs/ship.toml"] = test.job
			if _, err := Load(writeFiles(t, files)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
	files := validFactory()
	files["jobs/default.toml"] = stage(`name = "build"` + "\n" + `agent = "default"`)
	if _, err := Load(writeFiles(t, files)); err == nil || !strings.Contains(err.Error(), "an agent already has this name") {
		t.Fatalf("error = %v", err)
	}
}
