// Package factory loads a team's agents, jobs, reviewers and automations from files.
package factory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/triggers"
)

const maxFileBytes = 1 << 20

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

type Agent struct {
	Name     string
	Harness  string
	Model    string
	Playbook string
	Files    []runner.AgentFile
}

// Stage is one step of a job: an agent of the factory, or a shell script.
type Stage struct {
	Name   string `toml:"name"`
	Agent  string `toml:"agent"`
	Script string `toml:"script"`
}

// Job is a list of stages that run in order in one workspace. Every agent
// stage of a job uses Harness.
type Job struct {
	Name    string
	Harness string
	Stages  []Stage
}

type Reviewer struct {
	Name   string
	Prompt string
}

type Automation struct {
	Name   string
	Repo   string
	Agent  string
	Prompt string
	Cron   *triggers.Cron
}

type Factory struct {
	Agents      map[string]Agent
	Jobs        map[string]Job
	Reviewers   []Reviewer
	Automations []Automation
}

// Load reads and validates a factory directory.
func Load(directory string) (*Factory, error) {
	factory := &Factory{Agents: map[string]Agent{}, Jobs: map[string]Job{}}
	agents, err := subdirectories(filepath.Join(directory, "agents"))
	if err != nil {
		return nil, fmt.Errorf("read agents: %w", err)
	}
	for _, name := range agents {
		agent, err := loadAgent(filepath.Join(directory, "agents", name), name)
		if err != nil {
			return nil, fmt.Errorf("agent %s: %w", name, err)
		}
		factory.Agents[name] = agent
	}
	if len(factory.Agents) == 0 {
		return nil, fmt.Errorf("factory %s has no agents", directory)
	}
	if err := eachFile(filepath.Join(directory, "jobs"), "*.toml", func(name, body string) error {
		job, err := factory.parseJob(name, body)
		if err != nil {
			return err
		}
		factory.Jobs[name] = job
		return nil
	}); err != nil {
		return nil, fmt.Errorf("job: %w", err)
	}
	if err := eachFile(filepath.Join(directory, "reviewers"), "*.md", func(name, body string) error {
		if strings.TrimSpace(body) == "" {
			return errors.New("prompt is empty")
		}
		factory.Reviewers = append(factory.Reviewers, Reviewer{Name: name, Prompt: body})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("reviewer: %w", err)
	}
	if err := eachFile(filepath.Join(directory, "automations"), "*.md", func(name, body string) error {
		automation, err := parseAutomation(name, body)
		if err != nil {
			return err
		}
		factory.Automations = append(factory.Automations, automation)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("automation: %w", err)
	}
	return factory, nil
}

// Job finds what a ticket runs by name. An agent is also a job with one stage.
func (f *Factory) Job(name string) (Job, bool) {
	if job, ok := f.Jobs[name]; ok {
		return job, true
	}
	agent, ok := f.Agents[name]
	if !ok {
		return Job{}, false
	}
	return Job{Name: name, Harness: agent.Harness, Stages: []Stage{{Name: name, Agent: name}}}, true
}

// Validate checks what depends on the control plane's own configuration.
func (f *Factory) Validate(defaultAgent string, repositories []string) error {
	if _, ok := f.Job(defaultAgent); !ok {
		return fmt.Errorf("default_agent %q is not in the factory", defaultAgent)
	}
	for _, automation := range f.Automations {
		if _, ok := f.Job(automation.Agent); !ok {
			return fmt.Errorf("automation %s: agent %q is not in the factory", automation.Name, automation.Agent)
		}
		if !slices.Contains(repositories, automation.Repo) {
			return fmt.Errorf("automation %s: repository %q is not in github.repositories", automation.Name, automation.Repo)
		}
	}
	return nil
}

func subdirectories(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

func readFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return "", fmt.Errorf("%s must be a regular file under 1 MiB", path)
	}
	body, err := os.ReadFile(path)
	return string(body), err
}

// eachFile visits matching files in name order. A missing directory has none.
func eachFile(directory, pattern string, visit func(name, body string) error) error {
	paths, err := filepath.Glob(filepath.Join(directory, pattern))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if !validName.MatchString(name) {
			return fmt.Errorf("invalid file name %q", filepath.Base(path))
		}
		body, err := readFile(path)
		if err != nil {
			return err
		}
		if err := visit(name, body); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func loadAgent(directory, name string) (Agent, error) {
	if !validName.MatchString(name) {
		return Agent{}, errors.New("invalid agent name")
	}
	path := filepath.Join(directory, "agent.toml")
	body, err := readFile(path)
	if err != nil {
		return Agent{}, err
	}
	var settings struct {
		Harness string `toml:"harness"`
		Model   string `toml:"model"`
	}
	if err := config.DecodeStrict(path, []byte(body), &settings); err != nil {
		return Agent{}, err
	}
	if settings.Harness != runner.HarnessClaude && settings.Harness != runner.HarnessCodex {
		return Agent{}, fmt.Errorf("harness must be claude or codex, not %q", settings.Harness)
	}
	playbook, err := readFile(filepath.Join(directory, "playbook.md"))
	if err != nil {
		return Agent{}, err
	}
	if strings.TrimSpace(playbook) == "" {
		return Agent{}, errors.New("playbook.md is empty")
	}
	agent := Agent{Name: name, Harness: settings.Harness, Model: settings.Model, Playbook: playbook}
	for _, source := range []struct{ relative, pattern string }{{".claude/agents", "*.md"}, {".codex/agents", "*.toml"}} {
		paths, err := filepath.Glob(filepath.Join(directory, filepath.FromSlash(source.relative), source.pattern))
		if err != nil {
			return Agent{}, err
		}
		sort.Strings(paths)
		for _, path := range paths {
			body, err := readFile(path)
			if err != nil {
				return Agent{}, err
			}
			agent.Files = append(agent.Files, runner.AgentFile{Path: source.relative + "/" + filepath.Base(path), Body: body})
		}
	}
	return agent, nil
}

// parseJob reads a job file. Its agent stages must name agents of the factory
// that all use one harness, because a run has one harness and one credential.
func (f *Factory) parseJob(name, body string) (Job, error) {
	if _, ok := f.Agents[name]; ok {
		return Job{}, errors.New("an agent already has this name")
	}
	var settings struct {
		Stages []Stage `toml:"stages"`
	}
	if err := config.DecodeStrict(name, []byte(body), &settings); err != nil {
		return Job{}, err
	}
	job := Job{Name: name, Stages: settings.Stages}
	names := map[string]bool{}
	for index, stage := range job.Stages {
		if !validName.MatchString(stage.Name) {
			return Job{}, fmt.Errorf("stage %d: invalid name %q", index+1, stage.Name)
		}
		if names[stage.Name] {
			return Job{}, fmt.Errorf("stage %s is listed twice", stage.Name)
		}
		names[stage.Name] = true
		if (stage.Agent == "") == (strings.TrimSpace(stage.Script) == "") {
			return Job{}, fmt.Errorf("stage %s: set agent or script, and not both", stage.Name)
		}
		if stage.Agent == "" {
			continue
		}
		agent, ok := f.Agents[stage.Agent]
		if !ok {
			return Job{}, fmt.Errorf("stage %s: agent %q is not in the factory", stage.Name, stage.Agent)
		}
		if job.Harness != "" && agent.Harness != job.Harness {
			return Job{}, fmt.Errorf("stage %s: agent %q uses %s, but an earlier stage uses %s", stage.Name, stage.Agent, agent.Harness, job.Harness)
		}
		job.Harness = agent.Harness
	}
	if job.Harness == "" {
		return Job{}, errors.New("a job needs at least one agent stage")
	}
	return job, nil
}

const frontMatterFence = "+++\n"

func parseAutomation(name, body string) (Automation, error) {
	if !strings.HasPrefix(body, frontMatterFence) {
		return Automation{}, errors.New("must start with +++ TOML front matter")
	}
	front, prompt, found := strings.Cut(strings.TrimPrefix(body, frontMatterFence), "\n"+frontMatterFence)
	if !found {
		return Automation{}, errors.New("front matter is not closed with +++")
	}
	var settings struct {
		Schedule string `toml:"schedule"`
		Timezone string `toml:"timezone"`
		Repo     string `toml:"repo"`
		Agent    string `toml:"agent"`
	}
	if err := config.DecodeStrict(name, []byte(front), &settings); err != nil {
		return Automation{}, err
	}
	if settings.Repo == "" || settings.Agent == "" {
		return Automation{}, errors.New("repo and agent are required")
	}
	if strings.TrimSpace(prompt) == "" {
		return Automation{}, errors.New("prompt is empty")
	}
	cron, err := triggers.ParseCron(settings.Schedule, settings.Timezone)
	if err != nil {
		return Automation{}, err
	}
	return Automation{Name: name, Repo: settings.Repo, Agent: settings.Agent, Prompt: strings.TrimSpace(prompt), Cron: cron}, nil
}
