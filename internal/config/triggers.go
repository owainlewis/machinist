package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	triggercron "github.com/owainlewis/machinist/internal/triggers"
)

const (
	minTriggerEvery  = time.Minute
	maxGitHubEvery   = 24 * time.Hour
	maxIntervalEvery = 720 * time.Hour
)

var (
	triggerNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	repositoryPattern  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?/[A-Za-z0-9._-]{1,100}$`)
)

// LoadTriggers loads and resolves every managed trigger without applying
// machine-local server defaults. An empty trigger configuration is valid.
func LoadTriggers(path string) ([]ResolvedTrigger, error) {
	definition, err := loadConfigFile(path)
	if err != nil {
		return nil, err
	}
	return definition.ResolveTriggers()
}

func validateTriggerKeys(raw map[string]any) error {
	value, ok := raw["triggers"]
	if !ok {
		return nil
	}
	families, ok := value.(map[string]any)
	if !ok {
		return errors.New("triggers must be a table")
	}
	allowed := map[string]map[string]bool{
		"github":   {"every": true, "label": true, "agent": true, "pipeline": true, "model": true},
		"interval": {"every": true, "repository": true, "agent": true, "pipeline": true, "model": true, "prompt": true},
		"cron":     {"schedule": true, "timezone": true, "repository": true, "agent": true, "pipeline": true, "model": true, "prompt": true},
	}
	for _, family := range sortedMapKeys(families) {
		definitions, ok := families[family].(map[string]any)
		if !ok {
			return fmt.Errorf("trigger family %q must be a table", family)
		}
		fields, known := allowed[family]
		if !known && len(definitions) == 0 {
			return fmt.Errorf("unknown trigger family %q", family)
		}
		for _, name := range sortedMapKeys(definitions) {
			identity := family + "/" + name
			if !known {
				return fmt.Errorf("trigger %q uses unknown family %q", identity, family)
			}
			definition, ok := definitions[name].(map[string]any)
			if !ok {
				return fmt.Errorf("trigger %q must be a table", identity)
			}
			for _, field := range sortedMapKeys(definition) {
				if !fields[field] {
					return fmt.Errorf("trigger %q has unknown field %q", identity, field)
				}
			}
		}
	}
	return nil
}

// ResolveTriggers validates repositories, schedules, selections, models, and
// prompts before any trigger loop is started.
func (c Config) ResolveTriggers() ([]ResolvedTrigger, error) {
	repositories, err := resolveGitHubRepositories(c.GitHub.Repositories)
	if err != nil {
		return nil, err
	}
	result := make([]ResolvedTrigger, 0, len(c.Triggers.GitHub)+len(c.Triggers.Interval)+len(c.Triggers.Cron))
	seenLabels := make(map[string]string, len(c.Triggers.GitHub))

	for _, name := range sortedMapKeys(c.Triggers.GitHub) {
		identity, err := triggerIdentity("github", name)
		if err != nil {
			return nil, err
		}
		definition := c.Triggers.GitHub[name]
		every, err := triggerDuration(identity, "every", definition.Every, minTriggerEvery, maxGitHubEvery)
		if err != nil {
			return nil, err
		}
		label, err := triggerLabel(identity, definition.Label)
		if err != nil {
			return nil, err
		}
		canonicalLabel := strings.ToLower(label)
		if previous, ok := seenLabels[canonicalLabel]; ok {
			return nil, fmt.Errorf("trigger %q uses the same case-insensitive label as trigger %q", identity, previous)
		}
		seenLabels[canonicalLabel] = identity
		if len(repositories) == 0 {
			return nil, fmt.Errorf("trigger %q requires at least one github.repositories entry", identity)
		}
		kind, selection, agents, err := c.resolveTriggerSelection(identity, definition.TriggerSelection, "")
		if err != nil {
			return nil, err
		}
		resolved := ResolvedTrigger{
			Identity: identity, Family: "github", Name: name,
			GitHubRepositories: cloneStrings(repositories), Every: every, Label: label,
			SelectionKind: kind, SelectionName: selection, Model: strings.TrimSpace(definition.Model), Agents: agents,
		}
		resolved.Signature, err = triggerSignature(resolved)
		if err != nil {
			return nil, fmt.Errorf("trigger %q signature: %w", identity, err)
		}
		result = append(result, resolved)
	}

	for _, name := range sortedMapKeys(c.Triggers.Interval) {
		identity, err := triggerIdentity("interval", name)
		if err != nil {
			return nil, err
		}
		definition := c.Triggers.Interval[name]
		every, err := triggerDuration(identity, "every", definition.Every, minTriggerEvery, maxIntervalEvery)
		if err != nil {
			return nil, err
		}
		repository, slug, err := resolveTriggerRepository(identity, definition.Repository, repositories)
		if err != nil {
			return nil, err
		}
		prompt, err := triggerPrompt(identity, definition.Prompt)
		if err != nil {
			return nil, err
		}
		kind, selection, agents, err := c.resolveTriggerSelection(identity, definition.TriggerSelection, prompt)
		if err != nil {
			return nil, err
		}
		resolved := ResolvedTrigger{
			Identity: identity, Family: "interval", Name: name, Repository: repository, GitHubRepository: slug,
			Every: every, SelectionKind: kind, SelectionName: selection, Model: strings.TrimSpace(definition.Model), Prompt: prompt, Agents: agents,
		}
		resolved.Signature, err = triggerSignature(resolved)
		if err != nil {
			return nil, fmt.Errorf("trigger %q signature: %w", identity, err)
		}
		result = append(result, resolved)
	}

	for _, name := range sortedMapKeys(c.Triggers.Cron) {
		identity, err := triggerIdentity("cron", name)
		if err != nil {
			return nil, err
		}
		definition := c.Triggers.Cron[name]
		repository, slug, err := resolveTriggerRepository(identity, definition.Repository, repositories)
		if err != nil {
			return nil, err
		}
		prompt, err := triggerPrompt(identity, definition.Prompt)
		if err != nil {
			return nil, err
		}
		parsed, err := triggercron.ParseCron(definition.Schedule, definition.Timezone)
		if err != nil {
			return nil, fmt.Errorf("trigger %q schedule: %w", identity, err)
		}
		kind, selection, agents, err := c.resolveTriggerSelection(identity, definition.TriggerSelection, prompt)
		if err != nil {
			return nil, err
		}
		resolved := ResolvedTrigger{
			Identity: identity, Family: "cron", Name: name, Repository: repository, GitHubRepository: slug,
			Schedule: parsed.Expression(), Timezone: parsed.Timezone(), SelectionKind: kind, SelectionName: selection,
			Model: strings.TrimSpace(definition.Model), Prompt: prompt, Agents: agents, cron: parsed,
		}
		resolved.Signature, err = triggerSignature(resolved)
		if err != nil {
			return nil, fmt.Errorf("trigger %q signature: %w", identity, err)
		}
		result = append(result, resolved)
	}
	return result, nil
}

func resolveGitHubRepositories(input map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(input))
	seen := make(map[string]string, len(input))
	for _, name := range sortedMapKeys(input) {
		if !triggerNamePattern.MatchString(name) {
			return nil, fmt.Errorf("github repository name %q is invalid", name)
		}
		slug := input[name]
		if slug != strings.TrimSpace(slug) || !repositoryPattern.MatchString(slug) {
			return nil, fmt.Errorf("github repository %q must be a safe OWNER/REPO slug", name)
		}
		canonical := strings.ToLower(slug)
		if previous, ok := seen[canonical]; ok {
			return nil, fmt.Errorf("github repositories %q and %q have the same case-insensitive slug %q", previous, name, slug)
		}
		seen[canonical] = name
		result[name] = slug
	}
	return result, nil
}

func triggerIdentity(family, name string) (string, error) {
	identity := family + "/" + name
	if !triggerNamePattern.MatchString(name) {
		return "", fmt.Errorf("trigger %q has an invalid name", identity)
	}
	return identity, nil
}

func triggerDuration(identity, field, input string, min, max time.Duration) (time.Duration, error) {
	if strings.TrimSpace(input) == "" {
		return 0, fmt.Errorf("trigger %q %s is required", identity, field)
	}
	value, err := time.ParseDuration(input)
	if err != nil {
		return 0, fmt.Errorf("trigger %q %s: %w", identity, field, err)
	}
	if value < min || value > max {
		return 0, fmt.Errorf("trigger %q %s must be between %s and %s", identity, field, min, max)
	}
	return value, nil
}

func triggerLabel(identity, input string) (string, error) {
	label := strings.TrimSpace(input)
	if label == "" || label != input || len(label) > 50 || strings.ContainsAny(label, "\x00\r\n") {
		return "", fmt.Errorf("trigger %q label must be a non-empty GitHub label of at most 50 bytes on one line", identity)
	}
	return label, nil
}

func triggerPrompt(identity, input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", fmt.Errorf("trigger %q prompt is required", identity)
	}
	if len(input) > maxInputPromptBytes {
		return "", fmt.Errorf("trigger %q prompt exceeds %d bytes", identity, maxInputPromptBytes)
	}
	return input, nil
}

func resolveTriggerRepository(identity, input string, repositories map[string]string) (string, string, error) {
	repository := strings.TrimSpace(input)
	if repository == "" || repository != input {
		return "", "", fmt.Errorf("trigger %q repository is required", identity)
	}
	slug, ok := repositories[repository]
	if !ok {
		return "", "", fmt.Errorf("trigger %q references unknown github repository %q", identity, repository)
	}
	return repository, slug, nil
}

func (c Config) resolveTriggerSelection(identity string, selection TriggerSelection, prompt string) (string, string, []ResolvedAgent, error) {
	agentName := strings.TrimSpace(selection.Agent)
	pipelineName := strings.TrimSpace(selection.Pipeline)
	if (agentName == "") == (pipelineName == "") {
		return "", "", nil, fmt.Errorf("trigger %q must select exactly one agent or pipeline", identity)
	}
	if agentName != selection.Agent || pipelineName != selection.Pipeline {
		return "", "", nil, fmt.Errorf("trigger %q selection names must not have surrounding whitespace", identity)
	}
	model := strings.TrimSpace(selection.Model)
	if model != selection.Model || len(model) > 128 || strings.ContainsAny(model, "\x00\r\n") {
		return "", "", nil, fmt.Errorf("trigger %q model is invalid", identity)
	}

	kind, name := "agent", agentName
	var agents []ResolvedAgent
	if agentName != "" {
		agent, ok := c.Agents[agentName]
		if !ok {
			return "", "", nil, fmt.Errorf("trigger %q references undefined agent %q", identity, agentName)
		}
		resolved, err := resolveAgent(c.path, agentName, agent)
		if err != nil {
			return "", "", nil, fmt.Errorf("trigger %q: %w", identity, err)
		}
		agents = []ResolvedAgent{resolved}
	} else {
		kind, name = "pipeline", pipelineName
		pipeline, ok := c.Pipelines[pipelineName]
		if !ok {
			return "", "", nil, fmt.Errorf("trigger %q references undefined pipeline %q", identity, pipelineName)
		}
		if len(pipeline.Agents) == 0 {
			return "", "", nil, fmt.Errorf("trigger %q pipeline %q must define at least one agent", identity, pipelineName)
		}
		for index, member := range pipeline.Agents {
			if strings.TrimSpace(member) == "" {
				return "", "", nil, fmt.Errorf("trigger %q pipeline %q agent %d is empty", identity, pipelineName, index+1)
			}
			agent, ok := c.Agents[member]
			if !ok {
				return "", "", nil, fmt.Errorf("trigger %q pipeline %q references undefined agent %q", identity, pipelineName, member)
			}
			resolved, err := resolveAgent(c.path, member, agent)
			if err != nil {
				return "", "", nil, fmt.Errorf("trigger %q: %w", identity, err)
			}
			agents = append(agents, resolved)
		}
	}
	for _, agent := range agents {
		if agent.Name == "shepherd" {
			return "", "", nil, fmt.Errorf("trigger %q cannot select Shepherd directly or through a pipeline", identity)
		}
	}
	if err := ValidateModelSelection(agents, model); err != nil {
		return "", "", nil, fmt.Errorf("trigger %q model: %w", identity, err)
	}
	if prompt != "" {
		for index := range agents {
			rendered, err := RenderPrompt(agents[index], prompt)
			if err != nil {
				return "", "", nil, fmt.Errorf("trigger %q: %w", identity, err)
			}
			rendered.Model = model
			agents[index] = rendered
		}
	} else {
		for index := range agents {
			agents[index].Model = model
		}
	}
	return kind, name, agents, nil
}

// FirstDue returns the first scheduled occurrence after startup. GitHub intake
// polls immediately; interval triggers wait one interval; cron uses its calendar.
func (t ResolvedTrigger) FirstDue(startup time.Time) time.Time {
	if t.Family == "github" {
		return startup
	}
	return t.NextDue(startup)
}

// NextDue returns the next occurrence strictly after the supplied occurrence.
func (t ResolvedTrigger) NextDue(after time.Time) time.Time {
	switch t.Family {
	case "github", "interval":
		return after.Add(t.Every)
	case "cron":
		if t.cron != nil {
			return t.cron.Next(after)
		}
	}
	return time.Time{}
}

func triggerSignature(trigger ResolvedTrigger) (string, error) {
	type repositoryPair struct{ Name, Slug string }
	repositories := make([]repositoryPair, 0, len(trigger.GitHubRepositories))
	for _, name := range sortedMapKeys(trigger.GitHubRepositories) {
		repositories = append(repositories, repositoryPair{name, trigger.GitHubRepositories[name]})
	}
	agentHashes := make([]string, len(trigger.Agents))
	for index, agent := range trigger.Agents {
		agentHashes[index] = agent.Name + ":" + agent.Hash
	}
	body, err := json.Marshal(struct {
		Identity, Family, Repository, GitHubRepository, Schedule, Timezone, Label string
		Every                                                                     int64
		SelectionKind, SelectionName, Model, Prompt                               string
		Repositories                                                              []repositoryPair
		Agents                                                                    []string
	}{
		Identity: trigger.Identity, Family: trigger.Family, Repository: trigger.Repository,
		GitHubRepository: trigger.GitHubRepository, Schedule: trigger.Schedule, Timezone: trigger.Timezone,
		Label: trigger.Label, Every: int64(trigger.Every), SelectionKind: trigger.SelectionKind,
		SelectionName: trigger.SelectionName, Model: trigger.Model, Prompt: trigger.Prompt,
		Repositories: repositories, Agents: agentHashes,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func cloneStrings(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
