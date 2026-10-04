package factory

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

func configurationFixture(t *testing.T) *Service {
	t.Helper()
	s, _ := fixture(t)
	s.SetCSRFToken("browser-secret")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Agents["reviewer"] = config.ResolvedAgent{Runtime: "claude", Prompt: "Review"}
	for id, a := range s.cfg.Agents {
		a.Name = id
		a.Timeout = time.Minute
		a.Model = "old-model"
		s.cfg.Agents[id] = a
	}
	return s
}
func configurationCall(s *Service, method string, in any) *httptest.ResponseRecorder {
	body := ""
	if in != nil {
		raw, _ := json.Marshal(in)
		body = string(raw)
	}
	r := httptest.NewRequest(method, "http://127.0.0.1:7333/api/factory/configuration", strings.NewReader(body))
	r.Header.Set("X-Machinist-CSRF", "browser-secret")
	r.Header.Set("Origin", "http://127.0.0.1:7333")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func currentConfiguration(t *testing.T, s *Service) factoryConfiguration {
	t.Helper()
	w := configurationCall(s, "GET", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var in factoryConfiguration
	if e := json.Unmarshal(w.Body.Bytes(), &in); e != nil {
		t.Fatal(e)
	}
	return in
}
func TestConfigurationBrowserAuthority(t *testing.T) {
	s := configurationFixture(t)
	for _, method := range []string{"GET", "PUT"} {
		for _, kind := range []string{"token", "origin", "worker", "cross-site", "missing-origin"} {
			t.Run(method+kind, func(t *testing.T) {
				r := httptest.NewRequest(method, "http://127.0.0.1:7333/api/factory/configuration", strings.NewReader(`{}`))
				r.Header.Set("X-Machinist-CSRF", "browser-secret")
				r.Header.Set("Origin", "http://127.0.0.1:7333")
				switch kind {
				case "token":
					r.Header.Del("X-Machinist-CSRF")
				case "origin":
					r.Header.Set("Origin", "http://elsewhere")
				case "worker":
					r.Header.Set("Authorization", "Bearer worker")
				case "cross-site":
					r.Header.Set("Sec-Fetch-Site", "cross-site")
				case "missing-origin":
					r.Header.Del("Origin")
				}
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				if method == "GET" && kind == "missing-origin" {
					if w.Code != 200 {
						t.Fatal(w.Code)
					}
				} else if w.Code != 403 {
					t.Fatal(w.Code, w.Body.String())
				}
			})
		}
	}
}
func TestConfigurationAtomicRestartAndSnapshots(t *testing.T) {
	s := configurationFixture(t)
	s.mu.Lock()
	baseline := s.cfg
	old, e := s.create("project", "Old task", "Original brief", "")
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	in := currentConfiguration(t, s)
	a := in.Agents["planner"]
	a.Prompt = "New instructions"
	a.Name = "New planner"
	a.Model = "new-model"
	a.Timeout = "2m"
	in.Agents["planner"] = a
	p := in.Pipelines["default"]
	p.Steps[3].Command = []string{"git", "diff", "--check"}
	p.Steps[0].Name = "New design"
	in.Pipelines["default"] = p
	if _, e = s.db.Exec(`CREATE TRIGGER reject_configuration BEFORE INSERT ON factory_records WHEN NEW.kind='configuration' BEGIN SELECT RAISE(ABORT,'disk failed'); END`); e != nil {
		t.Fatal(e)
	}
	if w := configurationCall(s, "PUT", in); w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
	if got := currentConfiguration(t, s); got.Revision != 0 || got.Agents["planner"].Prompt == a.Prompt {
		t.Fatal("failed save published configuration")
	}
	s.db.Exec(`DROP TRIGGER reject_configuration`)
	w := configurationCall(s, "PUT", in)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := configurationCall(s, "PUT", in); w.Code != 409 {
		t.Fatal("stale revision accepted")
	}
	s.mu.Lock()
	fresh, e := s.create("project", "New task", "New brief", "")
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	if old.Agents["planner"].Prompt == a.Prompt || old.Steps[0].Name == p.Steps[0].Name || old.Steps[3].Command[1] != "status" {
		t.Fatal("old task changed")
	}
	if fresh.Agents["planner"].Prompt != a.Prompt || fresh.Steps[3].Command[1] != "diff" {
		t.Fatal("new task did not use saved settings")
	}
	restarted, e := New(s.db, baseline, "machinist")
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.Close()
	restarted.SetCSRFToken("browser-secret")
	got := currentConfiguration(t, restarted)
	if got.Revision != 1 || got.Agents["planner"].Prompt != a.Prompt {
		t.Fatal("restart ignored browser settings")
	}
	if restarted.tasks[old.ID].Agents["planner"].Prompt == a.Prompt || restarted.tasks[fresh.ID].Agents["planner"].Prompt != a.Prompt {
		t.Fatal("restart changed task snapshots")
	}
	raw, _ := json.Marshal(restarted.sessions)
	if strings.Contains(string(raw), "ForemanProfile") {
		t.Fatal("private snapshot exposed")
	}
}
func TestConfigurationRejectsInvalidOrStructuralEdits(t *testing.T) {
	s := configurationFixture(t)
	tests := map[string]func(*factoryConfiguration){
		"runtime": func(in *factoryConfiguration) {
			a := in.Agents["planner"]
			a.Runtime = "codex"
			in.Agents["planner"] = a
		},
		"prompt":        func(in *factoryConfiguration) { a := in.Agents["planner"]; a.Prompt = " "; in.Agents["planner"] = a },
		"timeout":       func(in *factoryConfiguration) { a := in.Agents["planner"]; a.Timeout = "0s"; in.Agents["planner"] = a },
		"model":         func(in *factoryConfiguration) { a := in.Agents["planner"]; a.Model = "x\n"; in.Agents["planner"] = a },
		"removed-agent": func(in *factoryConfiguration) { delete(in.Agents, "builder") },
		"new-agent":     func(in *factoryConfiguration) { in.Agents["new"] = in.Agents["builder"] },
		"approval": func(in *factoryConfiguration) {
			p := in.Pipelines["default"]
			p.Steps[1].Subject = "code"
			in.Pipelines["default"] = p
		},
		"type": func(in *factoryConfiguration) {
			p := in.Pipelines["default"]
			p.Steps[1].Type = "script"
			in.Pipelines["default"] = p
		},
		"reorder": func(in *factoryConfiguration) {
			p := in.Pipelines["default"]
			p.Steps[0], p.Steps[2] = p.Steps[2], p.Steps[0]
			in.Pipelines["default"] = p
		},
		"unknown-agent": func(in *factoryConfiguration) {
			p := in.Pipelines["default"]
			p.Steps[0].Agent = "missing"
			in.Pipelines["default"] = p
		},
		"empty-command": func(in *factoryConfiguration) {
			p := in.Pipelines["default"]
			p.Steps[3].Command = nil
			in.Pipelines["default"] = p
		},
		"command-nul": func(in *factoryConfiguration) {
			p := in.Pipelines["default"]
			p.Steps[3].Command = []string{"git", "\x00"}
			in.Pipelines["default"] = p
		},
		"unknown-default": func(in *factoryConfiguration) { in.DefaultPipeline = "missing" },
		"oversized": func(in *factoryConfiguration) {
			a := in.Agents["planner"]
			a.Prompt = strings.Repeat("p", configurationLimit+1)
			in.Agents["planner"] = a
		},
	}
	for name, edit := range tests {
		t.Run(name, func(t *testing.T) {
			in := currentConfiguration(t, s)
			edit(&in)
			w := configurationCall(s, "PUT", in)
			if w.Code != 409 && w.Code != 400 {
				t.Fatal(w.Code, w.Body.String())
			}
			if currentConfiguration(t, s).Revision != 0 {
				t.Fatal("invalid settings published")
			}
		})
	}
}
func TestForemanAcceptedProfileSurvivesSettingsAndRecovery(t *testing.T) {
	s := configurationFixture(t)
	s.mu.Lock()
	original := s.cfg
	s.active = "other-session"
	v, e := s.foreman("project")
	if e == nil {
		e = s.enqueue(v, "Accepted old turn", "old")
	}
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	in := currentConfiguration(t, s)
	a := in.Agents["foreman"]
	a.Prompt = "New coordinator"
	a.Model = "new-model"
	a.Timeout = "2m"
	in.Agents["foreman"] = a
	if w := configurationCall(s, "PUT", in); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	s.mu.Lock()
	if v.ForemanProfile.Prompt == a.Prompt || v.ForemanProfile.Model == a.Model {
		t.Fatal("queued profile changed")
	}
	s.active = ""
	s.queue = nil
	s.mu.Unlock()
	restarted, e := New(s.db, original, "machinist")
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.Close()
	captured := make(chan RunRequest, 2)
	restarted.SetRunner(func(ctx context.Context, r RunRequest, emit func(Event), permission func(context.Context, string) (bool, error)) (string, error) {
		captured <- r
		return "provider", nil
	})
	restarted.mu.Lock()
	restored := restarted.sessions[v.ID]
	if restored.Status != "interrupted" {
		t.Fatal(restored.Status)
	}
	e = restarted.acceptConfirmedTurn(restored, restored.Pending, "resume", "resume-key", true, nil)
	restarted.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	idle(t, restarted)
	r := <-captured
	if r.Model == a.Model || !strings.Contains(r.SystemPrompt, "Foreman") || strings.Contains(r.SystemPrompt, a.Prompt) {
		t.Fatal("recovery changed accepted profile", r)
	}
	restarted.mu.Lock()
	e = restarted.enqueue(restored, "Future turn", "future")
	restarted.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	idle(t, restarted)
	r = <-captured
	if r.Model != a.Model || !strings.Contains(r.SystemPrompt, a.Prompt) || r.SessionID != "provider" {
		t.Fatal("future reloaded turn ignored new profile", r)
	}
	raw, _ := json.Marshal(restored)
	if strings.Contains(string(raw), a.Prompt) {
		t.Fatal("public session exposes prompt snapshot")
	}
}

func TestConfigurationQueuedWorkerAndResumeKeepTaskProfile(t *testing.T) {
	s := configurationFixture(t)
	s.mu.Lock()
	baseline := s.cfg
	t1, e := s.create("project", "Saved work", "Brief", "")
	s.active = "busy"
	var worker *Session
	if e == nil {
		worker, e = s.start(t1)
	}
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	in := currentConfiguration(t, s)
	a := in.Agents["planner"]
	a.Prompt = "Different plan"
	a.Model = "different-model"
	in.Agents["planner"] = a
	if w := configurationCall(s, "PUT", in); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	s.mu.Lock()
	s.active = ""
	s.queue = nil
	s.mu.Unlock()
	restarted, e := New(s.db, baseline, "machinist")
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.Close()
	got := make(chan RunRequest, 1)
	restarted.SetRunner(func(ctx context.Context, r RunRequest, emit func(Event), permission func(context.Context, string) (bool, error)) (string, error) {
		if strings.Contains(r.SystemPrompt, "original planning instructions") {
			got <- r
		}
		return "saved-worker", nil
	})
	w := call(restarted, "POST", "sessions/"+worker.ID+"/resume", `{"confirmed_stopped":true}`, "")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	idle(t, restarted)
	r := <-got
	if !strings.Contains(r.SystemPrompt, "original planning instructions") || strings.Contains(r.SystemPrompt, a.Prompt) || r.Model != t1.Agents["planner"].Model || !r.ReadOnly {
		t.Fatal("saved worker resumed with edited profile", r)
	}
}

func TestLegacyForemanProfileMigrationUsesInitialDefinitions(t *testing.T) {
	s := configurationFixture(t)
	s.mu.Lock()
	baseline := s.cfg
	v, e := s.foreman("project")
	v.Status = "interrupted"
	v.Pending = "Legacy pending"
	v.RequestID = "legacy"
	e = s.saveSession(v)
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	in := currentConfiguration(t, s)
	a := in.Agents["foreman"]
	a.Prompt = "Browser override"
	in.Agents["foreman"] = a
	if w := configurationCall(s, "PUT", in); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	restarted, e := New(s.db, baseline, "machinist")
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.Close()
	profile := restarted.sessions[v.ID].ForemanProfile
	if profile == nil || profile.Prompt != baseline.Agents[baseline.Foreman].Prompt {
		t.Fatal("legacy accepted turn migrated to browser override", profile)
	}
	var raw string
	if e = s.db.QueryRow(`SELECT data FROM factory_records WHERE kind='session' AND id=?`, v.ID).Scan(&raw); e != nil || !strings.Contains(raw, "ForemanProfile") {
		t.Fatal("migration was not persisted", e)
	}
}
