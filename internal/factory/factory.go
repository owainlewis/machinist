// Package factory loads a team's presets and automations from files.
package factory

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/harness"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/triggers"
)

const (
	maxFileBytes = 1 << 20
	// MaxInputBytes caps the UTF-8 byte length of a preset input default or value.
	MaxInputBytes = 1024
	// ReservedInputRepo is the built-in repo variable, which presets cannot redeclare.
	ReservedInputRepo = "repo"
)

var (
	validName      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	validInputName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// ValidName reports whether name is a valid preset, agent, step, or automation name.
func ValidName(name string) bool { return validName.MatchString(name) }

// ValidInputName reports whether name is a valid preset input name.
func ValidInputName(name string) bool { return validInputName.MatchString(name) }

// ValidateInputValue checks that an input default or runtime value is valid UTF-8,
// contains no NUL bytes, and is at most MaxInputBytes bytes long.
func ValidateInputValue(name, value string) error {
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("var %q must not contain NUL bytes", name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("var %q must be valid UTF-8", name)
	}
	if len(value) > MaxInputBytes {
		return fmt.Errorf("var %q must be at most %d bytes", name, MaxInputBytes)
	}
	return nil
}

// Input is one declared variable on a preset. A nil Default means the input is
// required; a non-nil Default (including empty string) makes it optional.
type Input struct {
	Description string  `json:"description,omitempty"`
	Default     *string `json:"default,omitempty"`
}

// Required reports whether this input must be supplied by the caller.
func (in Input) Required() bool { return in.Default == nil }

func cloneInput(in Input) Input {
	copied := Input{Description: in.Description}
	if in.Default != nil {
		value := *in.Default
		copied.Default = &value
	}
	return copied
}

func cloneInputs(inputs map[string]Input) map[string]Input {
	if len(inputs) == 0 {
		return nil
	}
	copied := make(map[string]Input, len(inputs))
	for name, in := range inputs {
		copied[name] = cloneInput(in)
	}
	return copied
}

// Preset is a straight list of steps. Every job of a preset runs the same
// steps, in order, in one empty workspace.
type Preset struct {
	Name string
	// Description says in a sentence what a job on this preset does. The New
	// job form shows it under the preset picker.
	Description string
	// Extends is the base preset this preset inherited from, if any.
	Extends string
	// Timeout covers all steps together. Zero means the server's run deadline.
	Timeout time.Duration
	// Harness and Model override every agent invoked in this preset unless a
	// step sets its own override.
	Harness string
	Model   string
	// RunsOn names the runner labels a host must advertise to claim jobs of
	// this preset. Empty matches any runner.
	RunsOn []string
	// Inputs are the named parameters this preset accepts, injected into steps
	// as MACHINIST_INPUT_<UPPER(NAME)>.
	Inputs map[string]Input
	Steps  []runner.Step
}

// RequiredInputs returns the sorted names of inputs that have no default.
func (p Preset) RequiredInputs() []string {
	var required []string
	for name, in := range p.Inputs {
		if in.Required() {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	return required
}

// ResolveVars validates provided preset input vars (excluding built-in repo)
// against p.Inputs, checks value limits, rejects unknown or missing required
// inputs, and applies defaults. It returns nil when the preset declares no inputs.
func (p Preset) ResolveVars(provided map[string]string) (map[string]string, error) {
	var unknown []string
	for name := range provided {
		if _, ok := p.Inputs[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown vars: %s", strings.Join(unknown, ", "))
	}
	for _, name := range slices.Sorted(maps.Keys(provided)) {
		if err := ValidateInputValue(name, provided[name]); err != nil {
			return nil, err
		}
	}
	if len(p.Inputs) == 0 {
		return nil, nil
	}
	var missing []string
	resolved := make(map[string]string, len(p.Inputs))
	for _, name := range slices.Sorted(maps.Keys(p.Inputs)) {
		spec := p.Inputs[name]
		value, ok := provided[name]
		if ok {
			if spec.Required() && value == "" {
				missing = append(missing, name)
				continue
			}
			resolved[name] = value
			continue
		}
		if spec.Required() {
			missing = append(missing, name)
			continue
		}
		resolved[name] = *spec.Default
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required vars: %s", strings.Join(missing, ", "))
	}
	return resolved, nil
}

// EffectiveStep returns step with preset-level harness and model overrides
// applied when the step does not override them itself.
func (p Preset) EffectiveStep(step runner.Step) runner.Step {
	step.Harness, step.Model = harness.MergeOverride(p.Harness, p.Model, step.Harness, step.Model)
	return step
}

// WithOverride returns a copy of p with run-level harness and model overrides
// applied on top of every step's effective override.
func (p Preset) WithOverride(harnessName, model string) Preset {
	if harnessName == "" && model == "" {
		return p
	}
	overridden := p
	// Every step's effective harness/model is materialized below; clear the
	// preset-level defaults so subsequent EffectiveStep calls do not reapply a
	// preset model onto a step whose model was cleared by a harness switch.
	overridden.Harness, overridden.Model = "", ""
	overridden.Steps = make([]runner.Step, len(p.Steps))
	for index, step := range p.Steps {
		effective := p.EffectiveStep(step)
		effective.Harness, effective.Model = harness.MergeOverride(effective.Harness, effective.Model, harnessName, model)
		overridden.Steps[index] = effective
	}
	return overridden
}

type Automation struct {
	Name    string
	Repo    string
	Preset  string
	Harness string
	Model   string
	RunsOn  []string
	Vars    map[string]string
	Prompt  string
	Cron    *triggers.Cron
}

type Factory struct {
	Presets     map[string]Preset
	Automations []Automation
	// Agents are agents/*.md by name. A step with agent: <name> runs one.
	Agents map[string]harness.Agent
	// Files are agents/*.md, prompts/*.md and scripts/*, by path. Every run
	// carries them into its control directory, where steps read them.
	Files map[string]string
	// Updates lists shipped factory files that are out of date, edited against
	// an older shipped version, or missing from the factory directory.
	Updates []FileUpdate
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
	rawPresets := map[string]rawPreset{}
	if err := eachFile(filepath.Join(directory, "presets"), "*.yaml", func(name, body string) error {
		preset, err := parsePreset(name, body, factory.Agents)
		if err != nil {
			return err
		}
		rawPresets[name] = preset
		return nil
	}); err != nil {
		return nil, fmt.Errorf("preset: %w", err)
	}
	if len(rawPresets) == 0 {
		return nil, fmt.Errorf("factory %s has no presets", directory)
	}
	presets, err := resolvePresets(rawPresets)
	if err != nil {
		return nil, fmt.Errorf("preset: %w", err)
	}
	factory.Presets = presets
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
	updates, err := Check(directory)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	factory.Updates = updates
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

type rawPreset struct {
	name        string
	extends     string
	description string
	timeout     time.Duration
	harness     string
	model       string
	runsOn      []string
	hasRunsOn   bool
	inputs      map[string]Input
	steps       []rawStep
}

type rawStep struct {
	name              string
	run               string
	hasCommand        bool
	timeoutMillis     int64
	idleTimeoutMillis int64
	harness           string
	model             string
}

// ValidateHarnessOverride checks that value is empty or a supported harness name.
func ValidateHarnessOverride(value string) error {
	if value == "" {
		return nil
	}
	if _, ok := harness.Find(value); !ok {
		return fmt.Errorf("harness must be one of %s, not %q", strings.Join(harness.Names(), ", "), value)
	}
	return nil
}

func validateHarnessOverride(value string) error { return ValidateHarnessOverride(value) }

func parsePreset(name, body string, agents map[string]harness.Agent) (rawPreset, error) {
	var settings struct {
		Description string    `yaml:"description"`
		Extends     string    `yaml:"extends"`
		Timeout     string    `yaml:"timeout"`
		Harness     string    `yaml:"harness"`
		Model       string    `yaml:"model"`
		RunsOn      *[]string `yaml:"runs_on"`
		Inputs      map[string]struct {
			Description string  `yaml:"description"`
			Default     *string `yaml:"default"`
		} `yaml:"inputs"`
		Steps []struct {
			Name        string `yaml:"name"`
			Run         string `yaml:"run"`
			Agent       string `yaml:"agent"`
			Timeout     string `yaml:"timeout"`
			IdleTimeout string `yaml:"idle_timeout"`
			Harness     string `yaml:"harness"`
			Model       string `yaml:"model"`
		} `yaml:"steps"`
	}
	if err := config.DecodeStrict(name, []byte(body), &settings); err != nil {
		return rawPreset{}, err
	}
	timeout, err := parseDuration("timeout", settings.Timeout)
	if err != nil {
		return rawPreset{}, err
	}
	if err := validateHarnessOverride(settings.Harness); err != nil {
		return rawPreset{}, err
	}
	var runsOn []string
	hasRunsOn := settings.RunsOn != nil
	if hasRunsOn {
		runsOn, err = config.NormalizeLabels(*settings.RunsOn)
		if err != nil {
			return rawPreset{}, fmt.Errorf("runs_on: %w", err)
		}
	}
	var inputs map[string]Input
	if len(settings.Inputs) > 0 {
		inputs = make(map[string]Input, len(settings.Inputs))
		for _, inputName := range slices.Sorted(maps.Keys(settings.Inputs)) {
			if inputName == ReservedInputRepo {
				return rawPreset{}, fmt.Errorf("input %q is reserved", inputName)
			}
			if !validInputName.MatchString(inputName) {
				return rawPreset{}, fmt.Errorf("input %q: invalid name (must match ^[a-z][a-z0-9_]*$)", inputName)
			}
			rawInput := settings.Inputs[inputName]
			in := Input{
				Description: strings.Join(strings.Fields(rawInput.Description), " "),
			}
			if rawInput.Default != nil {
				if err := ValidateInputValue(inputName, *rawInput.Default); err != nil {
					return rawPreset{}, fmt.Errorf("input %s default: %w", inputName, err)
				}
				val := *rawInput.Default
				in.Default = &val
			}
			inputs[inputName] = in
		}
	}
	preset := rawPreset{
		name:        name,
		extends:     settings.Extends,
		description: strings.Join(strings.Fields(settings.Description), " "),
		timeout:     timeout,
		harness:     settings.Harness,
		model:       strings.TrimSpace(settings.Model),
		runsOn:      runsOn,
		hasRunsOn:   hasRunsOn,
		inputs:      inputs,
	}
	if preset.extends == "" && len(settings.Steps) == 0 {
		return rawPreset{}, errors.New("a preset needs at least one step")
	}
	names := map[string]bool{}
	for index, step := range settings.Steps {
		if !validName.MatchString(step.Name) {
			return rawPreset{}, fmt.Errorf("step %d: invalid name %q", index+1, step.Name)
		}
		if names[step.Name] {
			return rawPreset{}, fmt.Errorf("step %s is listed twice", step.Name)
		}
		names[step.Name] = true
		if err := validateHarnessOverride(step.Harness); err != nil {
			return rawPreset{}, fmt.Errorf("step %s: %w", step.Name, err)
		}
		run := step.Run
		hasCommand := false
		switch {
		case step.Agent != "" && strings.TrimSpace(step.Run) != "":
			return rawPreset{}, fmt.Errorf("step %s: give it agent or run, not both", step.Name)
		case step.Agent != "":
			if _, ok := agents[step.Agent]; !ok {
				return rawPreset{}, fmt.Errorf("step %s: agent %q is not in agents/", step.Name, step.Agent)
			}
			run = "machinist agent " + step.Agent
			hasCommand = true
		case strings.TrimSpace(step.Run) != "":
			hasCommand = true
		case preset.extends == "" || step.Run != "":
			return rawPreset{}, fmt.Errorf("step %s: give it agent or run", step.Name)
		}
		stepTimeout, err := parseDuration("step "+step.Name+": timeout", step.Timeout)
		if err != nil {
			return rawPreset{}, err
		}
		idleTimeout, err := parseDuration("step "+step.Name+": idle_timeout", step.IdleTimeout)
		if err != nil {
			return rawPreset{}, err
		}
		preset.steps = append(preset.steps, rawStep{
			name:              step.Name,
			run:               run,
			hasCommand:        hasCommand,
			timeoutMillis:     stepTimeout.Milliseconds(),
			idleTimeoutMillis: idleTimeout.Milliseconds(),
			harness:           step.Harness,
			model:             strings.TrimSpace(step.Model),
		})
	}
	if preset.extends == "" && preset.description == "" {
		return rawPreset{}, errors.New("add a description: one sentence on what a job on this preset does")
	}
	return preset, nil
}

// resolvePresets expands extends chains and applies preset-level harness/model
// defaults to every step that does not set its own override.
func resolvePresets(raw map[string]rawPreset) (map[string]Preset, error) {
	resolvedRaw := map[string]rawPreset{}
	var resolve func(name string, chain []string) (rawPreset, error)
	resolve = func(name string, chain []string) (rawPreset, error) {
		if done, ok := resolvedRaw[name]; ok {
			return done, nil
		}
		if idx := slices.Index(chain, name); idx >= 0 {
			cycle := strings.Join(append(append([]string(nil), chain[idx:]...), name), " -> ")
			return rawPreset{}, fmt.Errorf("%s: extends cycle: %s", chain[idx], cycle)
		}
		current := raw[name]
		if current.extends == "" {
			resolvedRaw[name] = current
			return current, nil
		}
		if _, ok := raw[current.extends]; !ok {
			return rawPreset{}, fmt.Errorf("%s: extends unknown preset %q", name, current.extends)
		}
		base, err := resolve(current.extends, append(chain, name))
		if err != nil {
			return rawPreset{}, err
		}
		merged := rawPreset{
			name:        current.name,
			extends:     current.extends,
			description: base.description,
			timeout:     base.timeout,
			runsOn:      slices.Clone(base.runsOn),
			hasRunsOn:   base.hasRunsOn,
			inputs:      cloneInputs(base.inputs),
			steps:       append([]rawStep(nil), base.steps...),
		}
		if current.description != "" {
			merged.description = current.description
		}
		if current.timeout > 0 {
			merged.timeout = current.timeout
		}
		if current.hasRunsOn {
			merged.runsOn = slices.Clone(current.runsOn)
			merged.hasRunsOn = true
		}
		if len(current.inputs) > 0 {
			if merged.inputs == nil {
				merged.inputs = make(map[string]Input, len(current.inputs))
			}
			for inputName, override := range current.inputs {
				if existing, ok := merged.inputs[inputName]; ok {
					if override.Description != "" {
						existing.Description = override.Description
					}
					if override.Default != nil {
						val := *override.Default
						existing.Default = &val
					}
					merged.inputs[inputName] = existing
					continue
				}
				merged.inputs[inputName] = cloneInput(override)
			}
		}
		merged.harness, merged.model = harness.MergeOverride(base.harness, base.model, current.harness, current.model)
		// Switching the preset harness invalidates inherited model-only step
		// overrides so a model from the previous harness is not passed to the
		// new one.
		harnessChanged := current.harness != "" && current.harness != base.harness
		stepIndex := make(map[string]int, len(merged.steps))
		for i, step := range merged.steps {
			if harnessChanged && step.harness == "" {
				merged.steps[i].model = ""
			}
			stepIndex[step.name] = i
		}
		for _, override := range current.steps {
			if idx, ok := stepIndex[override.name]; ok {
				existing := merged.steps[idx]
				if override.hasCommand {
					existing.run = override.run
					existing.hasCommand = true
				}
				if override.timeoutMillis > 0 {
					existing.timeoutMillis = override.timeoutMillis
				}
				if override.idleTimeoutMillis > 0 {
					existing.idleTimeoutMillis = override.idleTimeoutMillis
				}
				existing.harness, existing.model = harness.MergeOverride(existing.harness, existing.model, override.harness, override.model)
				merged.steps[idx] = existing
				continue
			}
			if !override.hasCommand {
				return rawPreset{}, fmt.Errorf("%s: step %s: not in base preset %q; give it agent or run to add a new step", name, override.name, current.extends)
			}
			stepIndex[override.name] = len(merged.steps)
			merged.steps = append(merged.steps, override)
		}
		resolvedRaw[name] = merged
		return merged, nil
	}

	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)
	presets := make(map[string]Preset, len(raw))
	for _, name := range names {
		done, err := resolve(name, nil)
		if err != nil {
			return nil, err
		}
		steps := make([]runner.Step, len(done.steps))
		for i, step := range done.steps {
			effHarness, effModel := harness.MergeOverride(done.harness, done.model, step.harness, step.model)
			steps[i] = runner.Step{
				Name:              step.name,
				Run:               step.run,
				TimeoutMillis:     step.timeoutMillis,
				IdleTimeoutMillis: step.idleTimeoutMillis,
				Harness:           effHarness,
				Model:             effModel,
			}
		}
		presets[name] = Preset{
			Name:        done.name,
			Description: done.description,
			Extends:     done.extends,
			Timeout:     done.timeout,
			Harness:     done.harness,
			Model:       done.model,
			RunsOn:      slices.Clone(done.runsOn),
			Inputs:      cloneInputs(done.inputs),
			Steps:       steps,
		}
	}
	return presets, nil
}

// Validate checks what depends on the control plane's own configuration.
func (f *Factory) Validate(defaultPreset string, repositories []string) error {
	def, ok := f.Presets[defaultPreset]
	if !ok {
		return fmt.Errorf("default_preset %q is not in the factory", defaultPreset)
	}
	if required := def.RequiredInputs(); len(required) > 0 {
		return fmt.Errorf("default_preset %q declares required inputs (%s) and cannot be used as default_preset", defaultPreset, strings.Join(required, ", "))
	}
	for _, automation := range f.Automations {
		preset, ok := f.Presets[automation.Preset]
		if !ok {
			return fmt.Errorf("automation %s: preset %q is not in the factory", automation.Name, automation.Preset)
		}
		if !slices.Contains(repositories, automation.Repo) {
			return fmt.Errorf("automation %s: repository %q is not in github.repositories", automation.Name, automation.Repo)
		}
		if _, err := preset.ResolveVars(automation.Vars); err != nil {
			return fmt.Errorf("automation %s: %w", automation.Name, err)
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
		Schedule string            `yaml:"schedule"`
		Timezone string            `yaml:"timezone"`
		Repo     string            `yaml:"repo"`
		Preset   string            `yaml:"preset"`
		Harness  string            `yaml:"harness"`
		Model    string            `yaml:"model"`
		RunsOn   []string          `yaml:"runs_on"`
		Vars     map[string]string `yaml:"vars"`
	}
	if err := config.DecodeStrict(name, []byte(front), &settings); err != nil {
		return Automation{}, err
	}
	if settings.Repo == "" || settings.Preset == "" {
		return Automation{}, errors.New("repo and preset are required")
	}
	if err := ValidateHarnessOverride(strings.TrimSpace(settings.Harness)); err != nil {
		return Automation{}, err
	}
	runsOn, err := config.NormalizeLabels(settings.RunsOn)
	if err != nil {
		return Automation{}, fmt.Errorf("runs_on: %w", err)
	}
	if strings.TrimSpace(prompt) == "" {
		return Automation{}, errors.New("prompt is empty")
	}
	var cron *triggers.Cron
	schedule := strings.TrimSpace(settings.Schedule)
	timezone := strings.TrimSpace(settings.Timezone)
	if schedule != "" {
		var err error
		cron, err = triggers.ParseCron(schedule, timezone)
		if err != nil {
			return Automation{}, err
		}
	} else if timezone != "" {
		return Automation{}, errors.New("timezone requires schedule")
	}
	var vars map[string]string
	if len(settings.Vars) > 0 {
		vars = maps.Clone(settings.Vars)
	}
	return Automation{
		Name:    name,
		Repo:    settings.Repo,
		Preset:  settings.Preset,
		Harness: strings.TrimSpace(settings.Harness),
		Model:   strings.TrimSpace(settings.Model),
		RunsOn:  runsOn,
		Vars:    vars,
		Prompt:  strings.TrimSpace(prompt),
		Cron:    cron,
	}, nil
}
