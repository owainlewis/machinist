package config

import (
	"fmt"
	"github.com/owainlewis/machinist/internal/artifacts"
	"github.com/owainlewis/machinist/internal/protocol"
	"regexp"
	"strings"
)

// Workflow deliberately describes a sequence, not a general dependency graph.
type Workflow struct {
	Steps []any `toml:"steps"`
}
type WorkflowStep struct {
	SharedOutputs   bool              `json:"shared_outputs,omitempty"`
	ID              string            `json:"id,omitempty"`
	RequiredOutputs []string          `json:"required_outputs,omitempty"`
	Inputs          map[string]string `json:"inputs,omitempty"`
	Command         ResolvedCommand   `json:"command"`
	Approval        bool              `json:"approval"`
}

func (c Config) WorkflowNames() []string { return sortedMapKeys(c.Workflows) }
func (c Config) ResolveWorkflow(name, prompt, model string) ([]WorkflowStep, error) {
	return c.resolveWorkflow(name, prompt, model, false)
}
func (c Config) ResolveTaskWorkflow(name, model string) ([]WorkflowStep, error) {
	return c.resolveWorkflow(name, "", model, true)
}
func (c Config) resolveWorkflow(name, prompt, model string, snapshot bool) ([]WorkflowStep, error) {
	workflow, ok := c.Workflows[name]
	if !ok {
		return nil, fmt.Errorf("unknown workflow %q", name)
	}
	if len(workflow.Steps) == 0 || len(workflow.Steps) > 32 {
		return nil, fmt.Errorf("workflow %q must have 1 to 32 steps", name)
	}
	steps := make([]WorkflowStep, 0, len(workflow.Steps))
	seen := map[string]bool{}
	for i, raw := range workflow.Steps {
		step := WorkflowStep{}
		var commandName string
		approval := false
		switch value := raw.(type) {
		case string:
			commandName = value
		case map[string]any:
			for key := range value {
				if key != "command" && key != "approval" && key != "id" && key != "required_outputs" && key != "inputs" {
					return nil, fmt.Errorf("workflow %q step %d: unknown field %q", name, i+1, key)
				}
			}
			commandName, _ = value["command"].(string)
			if rawID, ok := value["id"]; ok {
				var valid bool
				step.ID, valid = rawID.(string)
				if !valid || !identifier.MatchString(step.ID) {
					return nil, fmt.Errorf("invalid step id")
				}
			}
			if rawOutputs, ok := value["required_outputs"]; ok {
				values, ok := rawOutputs.([]any)
				if !ok {
					return nil, fmt.Errorf("required_outputs must be an array")
				}
				for _, raw := range values {
					path, ok := raw.(string)
					if !ok || !artifacts.ValidPath(path) {
						return nil, fmt.Errorf("invalid required output path")
					}
					step.RequiredOutputs = append(step.RequiredOutputs, path)
				}
			}
			if rawInputs, ok := value["inputs"]; ok {
				values, ok := rawInputs.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("inputs must be a table")
				}
				step.Inputs = map[string]string{}
				for alias, raw := range values {
					ref, ok := raw.(string)
					from, path, found := strings.Cut(ref, "/")
					if !identifier.MatchString(alias) || !ok || !found || !seen[from] || !artifacts.ValidPath(path) {
						return nil, fmt.Errorf("input %q must reference an earlier step ID and artifact path", alias)
					}
					step.Inputs[alias] = ref
				}
			}
			if setting, exists := value["approval"]; exists {
				if setting != "before" {
					return nil, fmt.Errorf("workflow %q step %d: approval must be before", name, i+1)
				}
				approval = true
			}
		default:
			return nil, fmt.Errorf("workflow %q step %d must be a command name or inline table", name, i+1)
		}
		if strings.TrimSpace(commandName) == "" {
			return nil, fmt.Errorf("workflow %q step %d: command is required", name, i+1)
		}
		command, err := c.ResolveCommand(commandName)
		if err != nil {
			return nil, err
		}
		if !snapshot {
			if len(step.Inputs) > 0 || len(step.RequiredOutputs) > 0 || strings.Contains(command.Prompt, "{{task.") || strings.Contains(command.Prompt, "{{inputs.") || strings.Contains(command.Prompt, "{{stage.") {
				return nil, fmt.Errorf("workflow %q uses task artifacts; submit spec or source_url instead of prompt", name)
			}
			command, err = RenderPrompt(command, prompt)
			if err != nil {
				return nil, err
			}
		}
		command.Model = model
		if step.ID == "" {
			step.ID = commandName
		}
		if seen[step.ID] {
			return nil, fmt.Errorf("duplicate step ID %q; set an explicit id", step.ID)
		}
		seen[step.ID] = true
		step.SharedOutputs = strings.Contains(command.Prompt, "{{task.output_dir}}")
		step.Command = command
		step.Approval = approval
		if snapshot {
			inputPaths := map[string]string{}
			for alias := range step.Inputs {
				inputPaths[alias] = "input"
			}
			if _, err := RenderTaskTemplate(command.Prompt, protocol.Task{Spec: "validation"}, "outputs", inputPaths); err != nil {
				return nil, err
			}
		}
		steps = append(steps, step)
	}
	return steps, nil
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var taskParameter = regexp.MustCompile(`\{\{(?:task|stage|inputs|machinist)\.[^{}]+\}\}`)

// Replace only tokens in the original template. User text is never re-templated.
func RenderTaskTemplate(template string, task protocol.Task, outputDir string, inputs map[string]string) (string, error) {
	for _, prefix := range []string{"{{task.", "{{stage.", "{{inputs.", "{{machinist."} {
		rest := template
		for {
			index := strings.Index(rest, prefix)
			if index < 0 {
				break
			}
			rest = rest[index:]
			token := taskParameter.FindStringIndex(rest)
			if token == nil || token[0] != 0 {
				return "", fmt.Errorf("malformed template field %s", prefix)
			}
			rest = rest[token[1]:]
		}
	}
	values := map[string]string{"task.title": task.Title, "task.source_url": task.SourceURL, "task.spec": task.Spec, "machinist.prompt": task.Brief(), "stage.output_dir": outputDir, "task.output_dir": outputDir}
	for alias, path := range inputs {
		values["inputs."+alias] = path
	}
	var resultErr error
	rendered := taskParameter.ReplaceAllStringFunc(template, func(token string) string {
		key := token[2 : len(token)-2]
		value, ok := values[key]
		if !ok {
			resultErr = fmt.Errorf("unknown task template field %s", key)
		}
		return value
	})
	if len(rendered) > maxRenderedPromptBytes {
		return "", fmt.Errorf("rendered prompt too large")
	}
	return rendered, resultErr
}
