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
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/triggers"
)

const maxFileBytes = 1 << 20

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// Preset is a straight list of steps. Every job of a preset runs the same
// steps, in order, in one empty workspace.
type Preset struct {
	Name string
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
}

// Load reads and validates a factory directory.
func Load(directory string) (*Factory, error) {
	factory := &Factory{Presets: map[string]Preset{}}
	if err := eachFile(filepath.Join(directory, "presets"), "*.toml", func(name, body string) error {
		preset, err := parsePreset(name, body)
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

func parsePreset(name, body string) (Preset, error) {
	var settings struct {
		Timeout string `toml:"timeout"`
		Steps   []struct {
			Name    string `toml:"name"`
			Run     string `toml:"run"`
			Timeout string `toml:"timeout"`
		} `toml:"steps"`
	}
	if err := config.DecodeStrict(name, []byte(body), &settings); err != nil {
		return Preset{}, err
	}
	timeout, err := parseDuration("timeout", settings.Timeout)
	if err != nil {
		return Preset{}, err
	}
	preset := Preset{Name: name, Timeout: timeout}
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
		if strings.TrimSpace(step.Run) == "" {
			return Preset{}, fmt.Errorf("step %s: run is empty", step.Name)
		}
		stepTimeout, err := parseDuration("step "+step.Name+": timeout", step.Timeout)
		if err != nil {
			return Preset{}, err
		}
		preset.Steps = append(preset.Steps, runner.Step{Name: step.Name, Run: step.Run, TimeoutMillis: stepTimeout.Milliseconds()})
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
		Preset   string `toml:"preset"`
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
