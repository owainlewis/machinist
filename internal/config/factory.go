package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// FactoryConfig is operator-managed configuration, never agent-authored state.
type FactoryConfig struct {
	Hosts           map[string]FactoryHost     `toml:"hosts"`
	Enabled         bool                       `toml:"enabled"`
	Foreman         string                     `toml:"foreman"`
	DefaultPipeline string                     `toml:"default_pipeline"`
	Projects        map[string]FactoryProject  `toml:"projects"`
	Agents          map[string]FactoryAgent    `toml:"agents"`
	Pipelines       map[string]FactoryPipeline `toml:"pipelines"`
}
type FactoryHost struct {
	ToolsPort        int      `toml:"tools_port" json:"tools_port,omitempty"`
	MachinistCommand string   `toml:"machinist_command" json:"machinist_command,omitempty"`
	Name             string   `toml:"name" json:"name"`
	SSH              string   `toml:"ssh" json:"ssh,omitempty"`
	ACPCommand       []string `toml:"acp_command" json:"acp_command,omitempty"`
}
type FactoryProject struct {
	Host   string `toml:"host" json:"host"`
	Name   string `toml:"name" json:"name"`
	Path   string `toml:"path" json:"path"`
	GitHub string `toml:"github" json:"github,omitempty"`
}
type FactoryAgent struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`
	PromptFile  string `toml:"prompt_file"`
	Runtime     string `toml:"runtime"`
	Model       string `toml:"model"`
	Timeout     string `toml:"timeout"`
}
type ResolvedAgent struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Prompt      string        `json:"prompt"`
	Runtime     string        `json:"runtime"`
	Model       string        `json:"model,omitempty"`
	Timeout     time.Duration `json:"timeout"`
}
type FactoryPipeline struct {
	Name  string        `toml:"name" json:"name"`
	Steps []FactoryStep `toml:"steps" json:"steps"`
}
type FactoryStep struct {
	ID      string   `toml:"id" json:"id"`
	Name    string   `toml:"name" json:"name"`
	Stage   string   `toml:"stage" json:"stage"`
	Type    string   `toml:"type" json:"type"`
	Agent   string   `toml:"agent" json:"agent,omitempty"`
	Command []string `toml:"command" json:"command,omitempty"`
	Timeout string   `toml:"timeout" json:"timeout,omitempty"`
	Subject string   `toml:"subject" json:"subject,omitempty"`
}

// ResolvedFactory owns prompt contents and copied definitions suitable for a task snapshot.
type ResolvedFactory struct {
	Enabled         bool                       `json:"enabled"`
	Foreman         string                     `json:"foreman"`
	DefaultPipeline string                     `json:"default_pipeline"`
	Projects        map[string]FactoryProject  `json:"projects"`
	Hosts           map[string]FactoryHost     `json:"hosts"`
	Agents          map[string]ResolvedAgent   `json:"agents"`
	Pipelines       map[string]FactoryPipeline `json:"pipelines"`
}

func LoadFactory(path string) (ResolvedFactory, error) {
	c, err := LoadDefinitions(path)
	if err != nil {
		return ResolvedFactory{}, err
	}
	return c.ResolveFactory()
}
func (c Config) ResolveFactory() (ResolvedFactory, error) {
	f := c.Factory
	r := ResolvedFactory{Enabled: f.Enabled, Foreman: f.Foreman, DefaultPipeline: f.DefaultPipeline, Projects: map[string]FactoryProject{}, Agents: map[string]ResolvedAgent{}, Pipelines: map[string]FactoryPipeline{}}
	if !f.Enabled {
		return r, nil
	}
	fail := func(message string, args ...any) (ResolvedFactory, error) {
		return ResolvedFactory{}, fmt.Errorf("factory: "+message, args...)
	}
	r.Hosts = map[string]FactoryHost{"local": {Name: "Local"}}
	for id, h := range f.Hosts {
		if id == "" {
			return fail("host ID is required")
		}
		if id == "local" && h.SSH != "" {
			return fail("local host cannot use SSH")
		}
		if id != "local" && h.SSH == "" {
			return fail("remote host %q requires ssh", id)
		}
		if strings.HasPrefix(h.SSH, "-") || strings.ContainsAny(h.SSH, " \t\r\n\x00;|&$`\"'()<>\\") {
			return fail("host %q ssh target is invalid", id)
		}
		if h.MachinistCommand == "" {
			h.MachinistCommand = "machinist"
		}
		if strings.TrimSpace(h.MachinistCommand) == "" || strings.ContainsRune(h.MachinistCommand, '\x00') {
			return fail("host %q machinist_command is invalid", id)
		}
		if id != "local" {
			if h.ToolsPort == 0 {
				h.ToolsPort = 7332
			}
			if h.ToolsPort < 1024 || h.ToolsPort > 65535 {
				return fail("host %q tools_port must be between 1024 and 65535", id)
			}
			if len(h.ACPCommand) == 0 {
				return fail("remote host %q requires acp_command", id)
			}
		}
		if len(h.ACPCommand) > 0 {
			if err := validateCommand(id, h.ACPCommand); err != nil {
				return fail("host %q ACP command: %v", id, err)
			}
		}
		if h.Name == "" {
			h.Name = id
		}
		h.ACPCommand = append([]string(nil), h.ACPCommand...)
		r.Hosts[id] = h
	}
	if len(f.Projects) == 0 {
		return fail("at least one project is required")
	}
	for id, p := range f.Projects {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(p.Path) == "" {
			return fail("project names and paths must be non-empty")
		}
		if p.Host == "" {
			p.Host = "local"
		}
		if _, ok := r.Hosts[p.Host]; !ok {
			return fail("project %q host %q is undefined", id, p.Host)
		}
		if p.Host == "local" {
			path, err := resolveConfigPath(p.Path, filepath.Dir(c.path))
			if err != nil {
				return fail("project %q: %v", id, err)
			}
			p.Path = path
		} else if !filepath.IsAbs(p.Path) || strings.ContainsAny(p.Path, "\x00\r\n") {
			return fail("remote project %q path must be absolute", id)
		}
		if p.Name == "" {
			p.Name = id
		}
		if p.GitHub != "" && (len(strings.Split(p.GitHub, "/")) != 2 || strings.ContainsAny(p.GitHub, " \r\n\x00")) {
			return fail("project %q github must be owner/repository", id)
		}
		r.Projects[id] = p
	}
	for id, a := range f.Agents {
		if strings.TrimSpace(id) == "" {
			return fail("agent ID must be non-empty")
		}
		if a.Runtime == "" {
			a.Runtime = "claude"
		}
		if a.Runtime != "claude" {
			return fail("agent %q runtime %q is unsupported", id, a.Runtime)
		}
		if strings.TrimSpace(a.PromptFile) == "" {
			return fail("agent %q prompt_file is required", id)
		}
		path, err := resolveConfigPath(a.PromptFile, filepath.Dir(c.path))
		if err != nil {
			return fail("agent %q prompt: %v", id, err)
		}
		body, err := readBoundedFile(path, maxPromptBytes)
		if err != nil {
			return fail("agent %q prompt: %v", id, err)
		}
		if strings.TrimSpace(string(body)) == "" {
			return fail("agent %q prompt is empty", id)
		}
		timeout := defaultTimeout
		if a.Timeout != "" {
			timeout, err = time.ParseDuration(a.Timeout)
			if err != nil || timeout <= 0 {
				return fail("agent %q timeout must be a positive duration", id)
			}
		}
		if len(a.Model) > 128 || strings.ContainsAny(a.Model, "\x00\r\n") {
			return fail("agent %q model is invalid", id)
		}
		if a.Name == "" {
			a.Name = id
		}
		r.Agents[id] = ResolvedAgent{Name: a.Name, Description: a.Description, Prompt: string(body), Runtime: a.Runtime, Model: a.Model, Timeout: timeout}
	}
	if _, ok := r.Agents[f.Foreman]; !ok {
		return fail("foreman agent %q is undefined", f.Foreman)
	}
	for id, p := range f.Pipelines {
		if id == "" || len(p.Steps) == 0 {
			return fail("pipeline ID and steps are required")
		}
		if p.Name == "" {
			p.Name = id
		}
		p.Steps = append([]FactoryStep(nil), p.Steps...)
		ids := map[string]bool{}
		rank := 0
		design, build, review, checks, code := false, false, false, false, false
		pendingDesign := false
		for i, s := range p.Steps {
			if s.ID == "" || ids[s.ID] {
				return fail("pipeline %q step IDs must be unique and non-empty", id)
			}
			ids[s.ID] = true
			next := map[string]int{"design": 1, "build": 2, "review": 3}[s.Stage]
			if next == 0 || next < rank {
				return fail("pipeline %q stages must follow design, build, review", id)
			}
			rank = next
			if s.Name == "" {
				s.Name = s.ID
			}
			switch s.Type {
			case "agent":
				if _, ok := r.Agents[s.Agent]; !ok {
					return fail("pipeline %q agent %q is undefined", id, s.Agent)
				}
				if len(s.Command) > 0 || s.Subject != "" {
					return fail("agent step %q has unrelated configuration", s.ID)
				}
				if s.Stage == "design" {
					design = false
					pendingDesign = true
				}
				if s.Stage == "build" {
					if !design {
						return fail("pipeline %q requires design approval before build", id)
					}
					build = true
					checks = false
				}
				if s.Stage == "review" {
					if !checks {
						return fail("pipeline %q requires checks before review", id)
					}
					review = true
				}
			case "script":
				if err := validateCommand(s.ID, s.Command); err != nil {
					return fail("script step %q: %v", s.ID, err)
				}
				if s.Agent != "" || s.Subject != "" {
					return fail("script step %q has unrelated configuration", s.ID)
				}
				if s.Stage != "build" || !build {
					return fail("pipeline %q scripts must follow the builder", id)
				}
				checks = true
			case "approval":
				if s.Agent != "" || len(s.Command) > 0 {
					return fail("approval step %q has unrelated configuration", s.ID)
				}
				if s.Subject == "design" && s.Stage == "design" && i > 0 && pendingDesign {
					design = true
					pendingDesign = false
				} else if s.Subject == "code" && s.Stage == "review" && review {
					code = true
				} else {
					return fail("approval step %q has invalid subject or order", s.ID)
				}
			default:
				return fail("step %q type must be agent, script, or approval", s.ID)
			}
			if s.Timeout != "" {
				d, err := time.ParseDuration(s.Timeout)
				if err != nil || d <= 0 {
					return fail("step %q timeout must be a positive duration", s.ID)
				}
			}
			s.Command = append([]string(nil), s.Command...)
			p.Steps[i] = s
		}
		if !design || !build || !checks || !review || !code || p.Steps[len(p.Steps)-1].Subject != "code" {
			return fail("pipeline %q requires design approval, build, checks, review, and final code approval", id)
		}
		r.Pipelines[id] = p
	}
	if _, ok := r.Pipelines[f.DefaultPipeline]; !ok {
		return fail("default_pipeline %q is undefined", f.DefaultPipeline)
	}
	return r, nil
}
