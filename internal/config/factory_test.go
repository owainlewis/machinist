package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func factoryFixture(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("Original instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	return Config{path: filepath.Join(dir, "config.toml"), Factory: FactoryConfig{Enabled: true, Foreman: "foreman", DefaultPipeline: "delivery", Projects: map[string]FactoryProject{"project": {Path: "repo"}}, Agents: map[string]FactoryAgent{"foreman": {PromptFile: "prompt.md"}, "worker": {PromptFile: "prompt.md"}}, Pipelines: map[string]FactoryPipeline{"delivery": {Steps: []FactoryStep{{ID: "plan", Stage: "design", Type: "agent", Agent: "worker"}, {ID: "design", Stage: "design", Type: "approval", Subject: "design"}, {ID: "build", Stage: "build", Type: "agent", Agent: "worker"}, {ID: "checks", Stage: "build", Type: "script", Command: []string{"go", "test", "./..."}}, {ID: "review", Stage: "review", Type: "agent", Agent: "worker"}, {ID: "code", Stage: "review", Type: "approval", Subject: "code"}}}}}}
}
func TestFactorySnapshot(t *testing.T) {
	c := factoryFixture(t)
	r, err := c.ResolveFactory()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(c.path), "prompt.md"), []byte("Changed instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	p := c.Factory.Pipelines["delivery"]
	p.Steps[3].Command[0] = "changed"
	if r.Agents["worker"].Prompt != "Original instructions" || r.Pipelines["delivery"].Steps[3].Command[0] != "go" {
		t.Fatal("snapshot changed after configuration edit")
	}
	fresh, err := c.ResolveFactory()
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Agents["worker"].Prompt != "Changed instructions" {
		t.Fatal("new resolution did not read prompt")
	}
	if !filepath.IsAbs(r.Projects["project"].Path) {
		t.Fatal("project path not resolved")
	}
}
func TestFactoryValidation(t *testing.T) {
	cases := []struct {
		name, want string
		mutate     func(*FactoryConfig)
	}{
		{"runtime", "unsupported", func(f *FactoryConfig) { a := f.Agents["worker"]; a.Runtime = "unknown"; f.Agents["worker"] = a }},
		{"timeout", "positive duration", func(f *FactoryConfig) { a := f.Agents["worker"]; a.Timeout = "0s"; f.Agents["worker"] = a }},
		{"foreman", "undefined", func(f *FactoryConfig) { f.Foreman = "missing" }},
		{"prompt", "prompt", func(f *FactoryConfig) { a := f.Agents["worker"]; a.PromptFile = "missing"; f.Agents["worker"] = a }},
		{"pipeline", "undefined", func(f *FactoryConfig) { f.DefaultPipeline = "missing" }},
		{"design gate", "design approval", func(f *FactoryConfig) {
			p := f.Pipelines["delivery"]
			p.Steps = append(p.Steps[:1], p.Steps[2:]...)
			f.Pipelines["delivery"] = p
		}},
		{"code gate", "final code approval", func(f *FactoryConfig) {
			p := f.Pipelines["delivery"]
			p.Steps = p.Steps[:len(p.Steps)-1]
			f.Pipelines["delivery"] = p
		}},
		{"agent ref", "undefined", func(f *FactoryConfig) {
			p := f.Pipelines["delivery"]
			p.Steps[0].Agent = "missing"
			f.Pipelines["delivery"] = p
		}},
		{"checks", "checks before review", func(f *FactoryConfig) {
			p := f.Pipelines["delivery"]
			p.Steps = append(p.Steps[:3], p.Steps[4:]...)
			f.Pipelines["delivery"] = p
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := factoryFixture(t)
			tc.mutate(&c.Factory)
			_, err := c.ResolveFactory()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}
func TestFactoryDisabled(t *testing.T) {
	r, err := (Config{}).ResolveFactory()
	if err != nil || r.Enabled {
		t.Fatalf("disabled config: %v %#v", err, r)
	}
}

func TestFactoryHosts(t *testing.T) {
	c := factoryFixture(t)
	c.Factory.Hosts = map[string]FactoryHost{"remote": {SSH: "worker@example.org", ACPCommand: []string{"claude-agent-acp"}}}
	p := c.Factory.Projects["project"]
	p.Host = "remote"
	p.Path = "/srv/project"
	c.Factory.Projects["project"] = p
	r, err := c.ResolveFactory()
	if err != nil {
		t.Fatal(err)
	}
	if r.Projects["project"].Path != "/srv/project" || r.Projects["project"].Host != "remote" {
		t.Fatal("remote placement changed")
	}
	h := c.Factory.Hosts["remote"]
	h.ACPCommand[0] = "changed"
	if r.Hosts["remote"].ACPCommand[0] != "claude-agent-acp" {
		t.Fatal("host command snapshot changed")
	}
	for _, target := range []string{"-oProxyCommand=bad", "host;command", "host\nother", "host$(command)"} {
		h.SSH = target
		c.Factory.Hosts["remote"] = h
		if _, err := c.ResolveFactory(); err == nil {
			t.Fatalf("accepted invalid target %q", target)
		}
	}
}
func TestLoadFactoryExample(t *testing.T) {
	r, err := LoadFactory("../../examples/factory.toml")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Agents) != 4 || len(r.Pipelines["delivery"].Steps) != 6 {
		t.Fatalf("unexpected example %#v", r)
	}
}

func TestFactoryRemoteTools(t *testing.T) {
	c := factoryFixture(t)
	c.Factory.Hosts = map[string]FactoryHost{"remote": {SSH: "worker@example.org", ACPCommand: []string{"claude-agent-acp"}}}
	r, err := c.ResolveFactory()
	if err != nil {
		t.Fatal(err)
	}
	if r.Hosts["remote"].ToolsPort != 7332 || r.Hosts["remote"].MachinistCommand != "machinist" {
		t.Fatal("remote tools defaults missing")
	}
	for _, port := range []int{-1, 80, 65536} {
		h := c.Factory.Hosts["remote"]
		h.ToolsPort = port
		c.Factory.Hosts["remote"] = h
		if _, err := c.ResolveFactory(); err == nil {
			t.Fatalf("accepted port %d", port)
		}
	}
	h := c.Factory.Hosts["remote"]
	h.ToolsPort = 7332
	h.ACPCommand = nil
	c.Factory.Hosts["remote"] = h
	if _, err := c.ResolveFactory(); err == nil {
		t.Fatal("accepted remote without ACP command")
	}
	h.ACPCommand = []string{"claude-agent-acp"}
	h.MachinistCommand = "bad\x00command"
	c.Factory.Hosts["remote"] = h
	if _, err := c.ResolveFactory(); err == nil {
		t.Fatal("accepted null command")
	}
}

func TestFactoryRequiresApprovalAfterFinalDesignProducer(t *testing.T) {
	c := factoryFixture(t)
	p := c.Factory.Pipelines["delivery"]
	second := FactoryStep{ID: "second-plan", Stage: "design", Type: "agent", Agent: "worker"}
	p.Steps = append(p.Steps[:2], append([]FactoryStep{second}, p.Steps[2:]...)...)
	c.Factory.Pipelines["delivery"] = p
	if _, err := c.ResolveFactory(); err == nil || !strings.Contains(err.Error(), "design approval before build") {
		t.Fatalf("accepted design changed after approval: %v", err)
	}
	approval := FactoryStep{ID: "second-approval", Stage: "design", Type: "approval", Subject: "design"}
	p.Steps = append(p.Steps[:3], append([]FactoryStep{approval}, p.Steps[3:]...)...)
	c.Factory.Pipelines["delivery"] = p
	if _, err := c.ResolveFactory(); err != nil {
		t.Fatalf("reapproved final design rejected: %v", err)
	}
}

func TestFactoryRequiresChecksAfterLastBuilder(t *testing.T) {
	for _, checked := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchecked revision", true: "checked revision"}[checked], func(t *testing.T) {
			c := factoryFixture(t)
			pipeline := c.Factory.Pipelines["delivery"]
			second := pipeline.Steps[2]
			second.ID = "second-build"
			steps := append([]FactoryStep(nil), pipeline.Steps[:4]...)
			steps = append(steps, second)
			if checked {
				check := pipeline.Steps[3]
				check.ID = "final-checks"
				steps = append(steps, check)
			}
			pipeline.Steps = append(steps, pipeline.Steps[4:]...)
			c.Factory.Pipelines["delivery"] = pipeline
			_, err := c.ResolveFactory()
			if checked && err != nil {
				t.Fatal(err)
			}
			if !checked && (err == nil || !strings.Contains(err.Error(), "checks before review")) {
				t.Fatalf("unchecked final builder accepted: %v", err)
			}
		})
	}
}

func TestFactoryAllowsBrowserOnboardingWithoutProjects(t *testing.T) {
	c := factoryFixture(t)
	c.Factory.Projects = nil
	r, err := c.ResolveFactory()
	if err != nil {
		t.Fatal(err)
	}
	if !r.Enabled || len(r.Projects) != 0 || r.Hosts["local"].Name == "" {
		t.Fatal("empty factory cannot onboard projects")
	}
}

func TestFactoryProjectIDsAreSafeURLSegments(t *testing.T) {
	for _, id := range []string{"", "owner/repo", "project?view=board", "project#board", "a b", "a%2Fb", ".", "..", "café", "a\\b"} {
		t.Run(id, func(t *testing.T) {
			c := factoryFixture(t)
			p := c.Factory.Projects["project"]
			c.Factory.Projects = map[string]FactoryProject{id: p}
			_, err := c.ResolveFactory()
			if err == nil || !strings.Contains(err.Error(), "project ID") {
				t.Fatalf("unsafe project ID accepted: %q err=%v", id, err)
			}
		})
	}
	for _, id := range []string{"project", "my-project", "project_2", "project.v2", ".hidden", "-project", "_project"} {
		t.Run(id, func(t *testing.T) {
			c := factoryFixture(t)
			p := c.Factory.Projects["project"]
			p.Name = "Readable project name"
			c.Factory.Projects = map[string]FactoryProject{id: p}
			resolved, err := c.ResolveFactory()
			if err != nil || resolved.Projects[id].Name != p.Name {
				t.Fatalf("valid project ID rejected: %q err=%v", id, err)
			}
		})
	}
}

func TestFactoryGitHubRepositoryUsesSafeNonemptyComponents(t *testing.T) {
	for _, value := range []string{"owner/", "/repo", "owner/repo?x", "owner/repo#x", "owner/repo/extra", "./repo", "owner/..", "owner/repo%2Fextra", "owner name/repo", "owner/repo\\name"} {
		t.Run(value, func(t *testing.T) {
			c := factoryFixture(t)
			p := c.Factory.Projects["project"]
			p.GitHub = value
			c.Factory.Projects["project"] = p
			_, err := c.ResolveFactory()
			if err == nil || !strings.Contains(err.Error(), "github must be owner/repository") {
				t.Fatalf("unsafe GitHub repository accepted: %q err=%v", value, err)
			}
		})
	}
	for _, value := range []string{"", "owner/repo", "owner-name/repo_name.v2"} {
		t.Run(value, func(t *testing.T) {
			c := factoryFixture(t)
			p := c.Factory.Projects["project"]
			p.GitHub = value
			c.Factory.Projects["project"] = p
			if _, err := c.ResolveFactory(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFactoryRemotePathsUsePOSIXRules(t *testing.T) {
	for _, value := range []string{"/srv/project", `/srv/repo\literal/project`} {
		t.Run(value, func(t *testing.T) {
			c := factoryFixture(t)
			c.Factory.Hosts = map[string]FactoryHost{"vm": {SSH: "vm", ACPCommand: []string{"claude-agent-acp"}}}
			p := c.Factory.Projects["project"]
			p.Host = "vm"
			p.Path = value
			c.Factory.Projects["project"] = p
			resolved, err := c.ResolveFactory()
			if err != nil || resolved.Projects["project"].Path != value {
				t.Fatalf("POSIX remote path rejected or changed: %q err=%v", value, err)
			}
		})
	}
	for _, value := range []string{"relative/repo", "C:/srv/project", `C:\srv\project`} {
		t.Run(value, func(t *testing.T) {
			c := factoryFixture(t)
			c.Factory.Hosts = map[string]FactoryHost{"vm": {SSH: "vm", ACPCommand: []string{"claude-agent-acp"}}}
			p := c.Factory.Projects["project"]
			p.Host = "vm"
			p.Path = value
			c.Factory.Projects["project"] = p
			if _, err := c.ResolveFactory(); err == nil {
				t.Fatalf("non-POSIX remote path accepted: %q", value)
			}
		})
	}
}

func TestFactoryProjectSourceValidation(t *testing.T) {
	for _, tc := range []struct {
		source, url string
		valid       bool
	}{
		{"", "", true}, {"folder", "", true}, {"folder", "legacy unused value", true},
		{"unexpected", "", false}, {"Git", "https://github.com/owner/repo.git", false},
		{"git", "", false}, {"git", "-repository", false}, {"git", "https://token@github.com/owner/repo.git", false},
		{"git", "ssh://git:secret@github.com/owner/repo.git", false}, {"git", "git@-oProxyCommand=command:repo", false},
		{"git", "https://github.com/owner/repo.git?token=secret", false}, {"git", "file:///tmp/repo", false},
		{"git", "https://github.com/owner/repo.git", true}, {"git", "ssh://git@github.com/owner/repo.git", true},
		{"git", "git@github.com:owner/repo.git", true},
	} {
		t.Run(tc.source+tc.url, func(t *testing.T) {
			c := factoryFixture(t)
			p := c.Factory.Projects["project"]
			p.Source = tc.source
			p.GitURL = tc.url
			c.Factory.Projects["project"] = p
			resolved, e := c.ResolveFactory()
			if (e == nil) != tc.valid {
				t.Fatalf("source %q URL %q validation mismatch: %v", tc.source, tc.url, e)
			}
			if tc.valid && resolved.Projects["project"].Source != tc.source {
				t.Fatal("source unexpectedly changed")
			}
		})
	}
}
