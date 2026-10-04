package factory

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

const configurationLimit = 1 << 20

type agentConfiguration struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	Runtime     string `json:"runtime"`
	Model       string `json:"model"`
	Timeout     string `json:"timeout"`
}
type factoryConfiguration struct {
	Revision        int                               `json:"revision"`
	Scope           string                            `json:"scope"`
	Foreman         string                            `json:"foreman"`
	DefaultPipeline string                            `json:"default_pipeline"`
	Agents          map[string]agentConfiguration     `json:"agents"`
	Pipelines       map[string]config.FactoryPipeline `json:"pipelines"`
}

func (s *Service) configurationSnapshot() factoryConfiguration {
	out := factoryConfiguration{Revision: s.configurationRevision, Scope: "global_future_tasks", Foreman: s.cfg.Foreman, DefaultPipeline: s.cfg.DefaultPipeline, Agents: map[string]agentConfiguration{}, Pipelines: map[string]config.FactoryPipeline{}}
	for id, a := range s.cfg.Agents {
		out.Agents[id] = agentConfiguration{a.Name, a.Description, a.Prompt, a.Runtime, a.Model, a.Timeout.String()}
	}
	for id, p := range s.cfg.Pipelines {
		p.Steps = append([]config.FactoryStep(nil), p.Steps...)
		for i := range p.Steps {
			p.Steps[i].Command = append([]string(nil), p.Steps[i].Command...)
		}
		out.Pipelines[id] = p
	}
	return out
}

func (in factoryConfiguration) resolved(base config.ResolvedFactory, fixed bool) (config.ResolvedFactory, error) {
	if in.Scope != "global_future_tasks" {
		return base, errors.New("configuration scope must be global_future_tasks")
	}
	if len(in.Agents) == 0 || len(in.Agents) > 64 || len(in.Pipelines) == 0 || len(in.Pipelines) > 32 {
		return base, errors.New("configuration requires bounded agent and pipeline definitions")
	}
	if fixed && (len(in.Agents) != len(base.Agents) || len(in.Pipelines) != len(base.Pipelines)) {
		return base, errors.New("adding or removing definitions is not supported")
	}
	agents := map[string]config.ResolvedAgent{}
	for id, a := range in.Agents {
		if fixed {
			if _, ok := base.Agents[id]; !ok {
				return base, fmt.Errorf("agent %q is not configured", id)
			}
		}
		if len(id) > 128 || len(a.Name) > 256 || len(a.Description) > 4096 || strings.ContainsRune(a.Name, '\x00') || strings.ContainsRune(a.Description, '\x00') {
			return base, errors.New("agent metadata is too large or invalid")
		}
		timeout, e := time.ParseDuration(a.Timeout)
		if e != nil || timeout <= 0 {
			return base, fmt.Errorf("agent %q timeout must be a positive duration", id)
		}
		agents[id] = config.ResolvedAgent{Name: a.Name, Description: a.Description, Prompt: a.Prompt, Runtime: a.Runtime, Model: a.Model, Timeout: timeout}
	}
	for id, p := range in.Pipelines {
		if len(id) > 128 || len(p.Name) > 256 || len(p.Steps) > 64 {
			return base, errors.New("pipeline metadata is too large")
		}
		old, ok := base.Pipelines[id]
		if fixed && (!ok || len(old.Steps) != len(p.Steps)) {
			return base, errors.New("adding, removing or reordering steps is not supported")
		}
		for i, step := range p.Steps {
			if fixed {
				before := old.Steps[i]
				if step.ID != before.ID || step.Type != before.Type || step.Stage != before.Stage || step.Subject != before.Subject {
					return base, errors.New("step identity, type, stage and approval gates must remain unchanged")
				}
			}
			if len(step.ID) > 128 || len(step.Name) > 256 || len(step.Command) > 128 {
				return base, errors.New("step metadata is too large")
			}
			for _, arg := range step.Command {
				if len(arg) > 8192 {
					return base, errors.New("script argument is too large")
				}
			}
		}
	}
	base.Foreman = in.Foreman
	base.DefaultPipeline = in.DefaultPipeline
	base.Agents = agents
	return config.ValidateFactorySettings(base, in.Pipelines)
}

func (s *Service) configuration(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	csrf := s.csrf
	available := s.cfg.Enabled && !s.closed
	s.mu.Unlock()
	if !browserAuthorized(r, csrf) {
		http.Error(w, "browser authorization required", 403)
		return
	}
	if !available {
		fail(w, errors.New("factory is unavailable"))
		return
	}
	if r.Method == http.MethodGet {
		s.mu.Lock()
		snapshot := s.configurationSnapshot()
		s.mu.Unlock()
		jsonReply(w, 200, snapshot)
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in factoryConfiguration
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, configurationLimit))
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(&in); e != nil {
		http.Error(w, "invalid or oversized configuration", 400)
		return
	}
	if e := decoder.Decode(new(any)); e != io.EOF {
		http.Error(w, "configuration must contain one JSON object", 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.Enabled || s.closed || s.csrf != csrf {
		fail(w, errors.New("factory changed while reading configuration"))
		return
	}
	if in.Revision != s.configurationRevision {
		fail(w, errors.New("configuration changed; reload before saving"))
		return
	}
	next, e := in.resolved(s.cfg, true)
	if e != nil {
		jsonReply(w, 400, map[string]string{"error": e.Error()})
		return
	}
	in.Revision++
	in.Agents = map[string]agentConfiguration{}
	for id, a := range next.Agents {
		in.Agents[id] = agentConfiguration{a.Name, a.Description, a.Prompt, a.Runtime, a.Model, a.Timeout.String()}
	}
	in.Pipelines = next.Pipelines
	raw, e := json.Marshal(in)
	if e != nil || len(raw) > configurationLimit {
		jsonReply(w, 400, map[string]string{"error": "configuration is too large"})
		return
	}
	if e = s.commitRecords([]recordWrite{{"configuration", "settings", in}}, "", Event{}); e != nil {
		jsonReply(w, 500, map[string]string{"error": "configuration could not be saved"})
		return
	}
	s.cfg.Agents = next.Agents
	s.cfg.Pipelines = next.Pipelines
	s.cfg.Foreman = next.Foreman
	s.cfg.DefaultPipeline = next.DefaultPipeline
	s.configurationRevision = in.Revision
	s.signal()
	jsonReply(w, 200, in)
}

func browserAuthorized(r *http.Request, csrf string) bool {
	if r.Header.Get("Authorization") != "" || (csrf != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Machinist-CSRF")), []byte(csrf)) != 1) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Method == http.MethodGet
	}
	parsed, e := url.Parse(origin)
	return e == nil && parsed.Scheme == "http" && strings.EqualFold(parsed.Host, r.Host)
}

// Capture accepted foreman instructions privately. Recovery keeps that turn's profile.
func (s *Service) captureForemanProfile(v *Session) {
	if v.Role == "foreman" {
		profile := s.cfg.Agents[s.cfg.Foreman]
		v.ForemanProfile = &profile
	}
}
