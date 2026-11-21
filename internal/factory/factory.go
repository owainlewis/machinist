// Package factory loads a team's presets and automations from files.
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
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/harness"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/triggers"
)

const maxFileBytes = 1 << 20

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// Preset is a straight list of steps. Every job of a preset runs the same
// steps, in order, in one empty workspace.
type Preset struct {
	Name string
	// Description says in a sentence what a job on this preset does. The New
	// job form shows it under the preset picker.
	Description string
	// Timeout covers all steps together. Zero means the server's run deadline.
	Timeout time.Duration
	Steps   []runner.Step
}

type Automation struct {
	Name   string
	Repo   string
	Preset string
	Prompt string
	Cron   *triggers.Cron
}

type Factory struct {
	Presets     map[string]Preset
	Automations []Automation
	// Agents are agents/*.md by name. A step with agent: <name> runs one.
	Agents map[string]harness.Agent
	// Files are agents/*.md, prompts/*.md and scripts/*, by path. Every run
	// carries them into its control directory, where steps read them.
	Files map[string]string
}

// Every run carries the factory's files, so they share one size limit.
const maxFactoryFiles = 4 << 20

// Load reads and validates a factory directory.
func Load(directory string) (*Factory, error) {
	factory := &Factory{Presets: map[string]Preset{}, Agents: map[string]harness.Agent{}}
	if err := eachFile(filepath.Join(directory, "agents"), "*.md", func(name, body string) error {
		agent, err := harness.ParseAgent(name, body)
		if err != nil {
			return err
		}
		factory.Agents[name] = agent
		return nil
	}); err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}
	if old, _ := filepath.Glob(filepath.Join(directory, "presets", "*.toml")); len(old) > 0 {
		return nil, fmt.Errorf("preset: %s is TOML, and Machinist now reads YAML. Rewrite it as %s", old[0], strings.TrimSuffix(old[0], ".toml")+".yaml")
	}
	if err := eachFile(filepath.Join(directory, "presets"), "*.yaml", func(name, body string) error {
		preset, err := parsePreset(name, body, factory.Agents)
		if err != nil {
			return err
		}
		factory.Presets[name] = preset
		return nil
	}); err != nil {
		return nil, fmt.Errorf("preset: %w", err)
	}
	if len(factory.Presets) == 0 {
		return nil, fmt.Errorf("factory %s has no presets", directory)
	}
	files, err := loadFiles(directory)
	if err != nil {
		return nil, err
	}
	factory.Files = files
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

// loadFiles reads the agents, prompts and scripts that steps use.
func loadFiles(directory string) (map[string]string, error) {
	files := map[string]string{}
	total := 0
	for _, kind := range []struct{ directory, pattern string }{{"agents", "*.md"}, {"prompts", "*.md"}, {"scripts", "*"}} {
		paths, err := filepath.Glob(filepath.Join(directory, kind.directory, kind.pattern))
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			relative := kind.directory + "/" + filepath.Base(path)
			if !runner.FilePath.MatchString(relative) {
				return nil, fmt.Errorf("%s: invalid file name", relative)
			}
			body, err := readFile(path)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", relative, err)
			}
			total += len(body)
			if total > maxFactoryFiles {
				return nil, fmt.Errorf("agents, prompts and scripts must add up to less than %d MiB", maxFactoryFiles>>20)
			}
			files[relative] = body
		}
	}
	return files, nil
}

// Preset finds a preset by name.
func (f *Factory) Preset(name string) (Preset, bool) {
	preset, ok := f.Presets[name]
	return preset, ok
}

func parseDuration(field, value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration such as 30m, not %q", field, value)
	}
	return duration, nil
}

func parsePreset(name, body string, agents map[string]harness.Agent) (Preset, error) {
	var settings struct {
		Description string `yaml:"description"`
		Timeout     string `yaml:"timeout"`
		Steps       []struct {
			Name        string `yaml:"name"`
			Run         string `yaml:"run"`
			Agent       string `yaml:"agent"`
			Timeout     string `yaml:"timeout"`
			IdleTimeout string `yaml:"idle_timeout"`
		} `yaml:"steps"`
	}
	if err := config.DecodeStrict(name, []byte(body), &settings); err != nil {
		return Preset{}, err
	}
	timeout, err := parseDuration("timeout", settings.Timeout)
	if err != nil {
		return Preset{}, err
	}
	preset := Preset{Name: name, Description: strings.Join(strings.Fields(settings.Description), " "), Timeout: timeout}
	if len(settings.Steps) == 0 {
		return Preset{}, errors.New("a preset needs at least one step")
	}
	names := map[string]bool{}
	for index, step := range settings.Steps {
		if !validName.MatchString(step.Name) {
			return Preset{}, fmt.Errorf("step %d: invalid name %q", index+1, step.Name)
		}
		if names[step.Name] {
			return Preset{}, fmt.Errorf("step %s is listed twice", step.Name)
		}
		names[step.Name] = true
		run := step.Run
		switch {
		case step.Agent != "" && strings.TrimSpace(step.Run) != "":
			return Preset{}, fmt.Errorf("step %s: give it agent or run, not both", step.Name)
		case step.Agent != "":
			if _, ok := agents[step.Agent]; !ok {
				return Preset{}, fmt.Errorf("step %s: agent %q is not in agents/", step.Name, step.Agent)
			}
			run = "machinist agent " + step.Agent
		case strings.TrimSpace(step.Run) == "":
			return Preset{}, fmt.Errorf("step %s: give it agent or run", step.Name)
		}
		stepTimeout, err := parseDuration("step "+step.Name+": timeout", step.Timeout)
		if err != nil {
			return Preset{}, err
		}
		idleTimeout, err := parseDuration("step "+step.Name+": idle_timeout", step.IdleTimeout)
		if err != nil {
			return Preset{}, err
		}
		preset.Steps = append(preset.Steps, runner.Step{Name: step.Name, Run: run, TimeoutMillis: stepTimeout.Milliseconds(), IdleTimeoutMillis: idleTimeout.Milliseconds()})
	}
	if preset.Description == "" {
		return Preset{}, errors.New("add a description: one sentence on what a job on this preset does")
	}
	return preset, nil
}

// Validate checks what depends on the control plane's own configuration.
func (f *Factory) Validate(defaultPreset string, repositories []string) error {
	if _, ok := f.Presets[defaultPreset]; !ok {
		return fmt.Errorf("default_preset %q is not in the factory", defaultPreset)
	}
	for _, automation := range f.Automations {
		if _, ok := f.Presets[automation.Preset]; !ok {
			return fmt.Errorf("automation %s: preset %q is not in the factory", automation.Name, automation.Preset)
		}
		if !slices.Contains(repositories, automation.Repo) {
			return fmt.Errorf("automation %s: repository %q is not in github.repositories", automation.Name, automation.Repo)
		}
	}
	return nil
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

const frontMatterFence = "---\n"

func parseAutomation(name, body string) (Automation, error) {
	if strings.HasPrefix(body, "+++\n") {
		return Automation{}, errors.New("front matter is TOML between +++ lines, and Machinist now reads YAML between --- lines")
	}
	if !strings.HasPrefix(body, frontMatterFence) {
		return Automation{}, errors.New("must start with --- YAML front matter")
	}
	front, prompt, found := strings.Cut(strings.TrimPrefix(body, frontMatterFence), "\n"+frontMatterFence)
	if !found {
		return Automation{}, errors.New("front matter is not closed with ---")
	}
	var settings struct {
		Schedule string `yaml:"schedule"`
		Timezone string `yaml:"timezone"`
		Repo     string `yaml:"repo"`
		Preset   string `yaml:"preset"`
	}
	if err := config.DecodeStrict(name, []byte(front), &settings); err != nil {
		return Automation{}, err
	}
	if settings.Repo == "" || settings.Preset == "" {
		return Automation{}, errors.New("repo and preset are required")
	}
	if strings.TrimSpace(prompt) == "" {
		return Automation{}, errors.New("prompt is empty")
	}
	cron, err := triggers.ParseCron(settings.Schedule, settings.Timezone)
	if err != nil {
		return Automation{}, err
	}
	return Automation{Name: name, Repo: settings.Repo, Preset: settings.Preset, Prompt: strings.TrimSpace(prompt), Cron: cron}, nil
}
