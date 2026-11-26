package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/tickets"
)

// The Settings pages show how Machinist is configured. config.yaml and the
// factory files stay the source of truth, so every route here reads, except
// managing tokens. A token's plaintext is only returned once, when it is
// created or regenerated; listing never returns a secret.

// GitHubSettings is the GitHub page: who Machinist acts as and what each
// repository lets it do.
type GitHubSettings struct {
	// Handle is github.handle, the login commits and comments are made under.
	Handle string `json:"handle"`
	// App is the GitHub App, or nil when runs use the runner's own GH_TOKEN.
	App          *AppView           `json:"app,omitempty"`
	AutoMerge    bool               `json:"auto_merge"`
	Repositories []RepositoryAccess `json:"repositories"`
}

// AppView is the GitHub App, by its ID and name. Its key stays on disk.
type AppView struct {
	ID         int64  `json:"id"`
	Name       string `json:"name,omitempty"`
	InstallURL string `json:"install_url,omitempty"`
	// Error says why Machinist cannot reach the app, if it can't.
	Error string `json:"error,omitempty"`
}

// RepositoryAccess is one project's default branch and what is wrong with it.
type RepositoryAccess struct {
	Repo          string `json:"repo"`
	DefaultBranch string `json:"default_branch,omitempty"`
	Protected     bool   `json:"protected"`
	Admin         bool   `json:"admin"`
	// Warnings say what could go wrong, such as a pull request merging
	// with no checks.
	Warnings []string `json:"warnings"`
	// Problem says why GitHub could not be asked, if it couldn't.
	Problem string `json:"problem,omitempty"`
}

func (s *Server) githubSettings(w http.ResponseWriter, r *http.Request, _ string) {
	st := s.state()
	settings := GitHubSettings{Handle: st.config.GitHub.Handle, AutoMerge: st.config.GitHub.AutoMerges(), Repositories: []RepositoryAccess{}}
	if st.config.GitHub.App != nil || st.app != nil {
		settings.App = &AppView{}
		if st.config.GitHub.App != nil {
			settings.App.ID = st.config.GitHub.App.ID
		}
		if st.app != nil {
			if identity, err := st.app.Identity(r.Context()); err != nil {
				settings.App.Error = err.Error()
			} else {
				settings.App.Name, settings.App.InstallURL = identity.Name, identity.InstallURL()
			}
		}
	}
	projects, err := s.projects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	settings.Repositories = make([]RepositoryAccess, len(projects))
	var wait sync.WaitGroup
	for index, project := range projects {
		settings.Repositories[index] = RepositoryAccess{Repo: project.Repo, Warnings: []string{}, Problem: project.Problem}
		if project.Problem != "" {
			continue
		}
		wait.Go(func() { settings.Repositories[index] = s.repositoryAccess(r, project.Repo, settings.AutoMerge) })
	}
	wait.Wait()
	writeJSON(w, http.StatusOK, settings)
}

// repositoryAccess asks GitHub about one repository and says what to watch for.
func (s *Server) repositoryAccess(r *http.Request, repository string, autoMerge bool) RepositoryAccess {
	view := RepositoryAccess{Repo: repository, Warnings: []string{}}
	access, err := s.client.RepositoryAccess(r.Context(), repository)
	if err != nil {
		view.Problem = "Machinist could not read this repository from GitHub: " + err.Error()
		return view
	}
	view.DefaultBranch, view.Protected, view.Admin = access.DefaultBranch, access.Protected, access.Admin
	if !access.Protected {
		warning := access.DefaultBranch + " has no branch protection, so nothing requires checks or a review before a pull request merges."
		if autoMerge {
			warning = access.DefaultBranch + " has no branch protection, and auto-merge is on, so a job can merge its own pull request with no checks."
		}
		view.Warnings = append(view.Warnings, warning)
	}
	if access.Admin {
		view.Warnings = append(view.Warnings, "The login is an admin here, so it can get around branch protection. Use a login with write access only.")
	}
	return view
}

// LinearSettings is the Linear page. The client secret and the access token
// are never shown.
type LinearSettings struct {
	Enabled      bool         `json:"enabled"`
	AppUserID    string       `json:"app_user_id,omitempty"`
	AllowedUsers []string     `json:"allowed_users"`
	Preset       string       `json:"preset,omitempty"`
	Teams        []LinearTeam `json:"teams"`
	// PollSeconds is how often Machinist asks Linear for new assignments.
	PollSeconds    int64 `json:"poll_seconds,omitempty"`
	MaxJobsPerHour int   `json:"max_jobs_per_hour,omitempty"`
	// CredentialEnv names the variables the app signs in with. While Linear is
	// off, they are the ones the setup guide asks for.
	CredentialEnv []string `json:"credential_env"`
	// Renews is true when Machinist gets and renews its own access token.
	Renews bool   `json:"renews,omitempty"`
	Docs   string `json:"docs"`
}

// LinearTeam is one Linear team and the repository its issues work in.
type LinearTeam struct {
	Team string `json:"team"`
	Repo string `json:"repo"`
}

func (s *Server) linearSettings(w http.ResponseWriter, _ *http.Request, _ string) {
	st := s.state()
	linear := st.config.Linear
	settings := LinearSettings{
		Enabled:       st.linear != nil,
		AllowedUsers:  []string{},
		Teams:         []LinearTeam{},
		CredentialEnv: []string{config.LinearClientIDEnv, config.LinearClientSecretEnv},
		Renews:        true,
		Docs:          docs + "linear.md#set-it-up",
	}
	if settings.Enabled && !linear.UsesClientCredentials() {
		settings.CredentialEnv = []string{config.LinearTokenEnv}
		settings.Renews = false
	}
	if settings.Enabled {
		settings.AppUserID = linear.AppUserID
		settings.AllowedUsers = append(settings.AllowedUsers, linear.AllowedUsers...)
		settings.Preset = linear.Preset
		if settings.Preset == "" {
			settings.Preset = st.config.Server.DefaultPreset
		}
		for team, repository := range linear.Teams {
			settings.Teams = append(settings.Teams, LinearTeam{Team: team, Repo: repository})
		}
		sort.Slice(settings.Teams, func(a, b int) bool { return settings.Teams[a].Team < settings.Teams[b].Team })
		settings.PollSeconds = int64(linear.Poll / time.Second)
		settings.MaxJobsPerHour = linear.MaxJobsPerHour
	}
	writeJSON(w, http.StatusOK, settings)
}

// TokenView is one API token, without the token itself.
type TokenView struct {
	Name       string     `json:"name"`
	Role       string     `json:"role"`
	Scope      string     `json:"scope,omitempty"`
	Prefix     string     `json:"prefix,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	// Current is true for the token that made this request.
	Current bool `json:"current,omitempty"`
}

// CreatedToken is returned once when a token is created or regenerated.
type CreatedToken struct {
	Token  string      `json:"token"`
	Name   string      `json:"name"`
	Role   string      `json:"role"`
	Scope  string      `json:"scope,omitempty"`
	Prefix string      `json:"prefix"`
	Tokens []TokenView `json:"tokens"`
}

// roleName is how a page names a role. A token from before roles can act as
// a person and as a runner.
func roleName(role string) string {
	if role == tickets.RoleAll {
		return "all"
	}
	return role
}

func (s *Server) tokenViews(ctx context.Context, tokenName string) ([]TokenView, error) {
	tokens, err := s.store.ListTokens(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]TokenView, 0, len(tokens))
	for _, token := range tokens {
		views = append(views, TokenView{
			Name:       token.Name,
			Role:       roleName(token.Role),
			Scope:      token.Scope,
			Prefix:     token.Prefix,
			CreatedAt:  token.CreatedAt,
			LastUsedAt: token.LastUsedAt,
			RevokedAt:  token.RevokedAt,
			Current:    token.RevokedAt == nil && token.Name == tokenName,
		})
	}
	return views, nil
}

func (s *Server) currentTokenName(r *http.Request, fallback string) string {
	if p, ok := principalFromRequest(r); ok {
		if p.Kind == "token" {
			return p.Name
		}
		return ""
	}
	return fallback
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request, tokenName string) {
	views, err := s.tokenViews(r.Context(), s.currentTokenName(r, tokenName))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": views})
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request, tokenName string) {
	var request struct {
		Name  string `json:"name"`
		Role  string `json:"role"`
		Scope string `json:"scope"`
	}
	if !decodeJSON(w, r, maxRequestBytes, &request) {
		return
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, errors.New("token name is required"))
		return
	}
	if strings.Contains(name, "/") {
		writeError(w, http.StatusBadRequest, errors.New("token name cannot contain /"))
		return
	}
	if request.Role != tickets.RoleOperator && request.Role != tickets.RoleRunner {
		writeError(w, http.StatusBadRequest, errors.New("token role must be operator or runner"))
		return
	}
	scope := strings.TrimSpace(request.Scope)
	if scope != tickets.ScopeAll && scope != tickets.ScopeRead && scope != tickets.ScopeRun {
		writeError(w, http.StatusBadRequest, errors.New("token scope must be read or run"))
		return
	}
	if scope != tickets.ScopeAll && request.Role != tickets.RoleOperator {
		writeError(w, http.StatusBadRequest, errors.New("runner tokens cannot have a scope"))
		return
	}
	token, err := s.store.CreateScopedToken(r.Context(), name, request.Role, scope)
	if errors.Is(err, tickets.ErrTokenExists) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	fmt.Fprintf(s.log, "machinist: %s created %s token %q from the web app\n", tokenName, request.Role, name)
	views, err := s.tokenViews(r.Context(), s.currentTokenName(r, tokenName))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var prefix string
	for _, view := range views {
		if view.Name == name && view.RevokedAt == nil {
			prefix = view.Prefix
			break
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, CreatedToken{
		Token:  token,
		Name:   name,
		Role:   roleName(request.Role),
		Scope:  scope,
		Prefix: prefix,
		Tokens: views,
	})
}

func (s *Server) regenerateToken(w http.ResponseWriter, r *http.Request, tokenName string) {
	name := r.PathValue("name")
	token, newTokenID, err := s.store.RegenerateTokenWithID(r.Context(), name)
	if errors.Is(err, tickets.ErrNotFound) {
		writeError(w, http.StatusNotFound, fmt.Errorf("no active token is named %q", name))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	fmt.Fprintf(s.log, "machinist: %s regenerated token %q from the web app\n", tokenName, name)
	if p, ok := principalFromRequest(r); ok && p.Kind == "token" && p.ViaCookie && p.Name == name {
		cfg := s.state().config
		secret, _, err := s.store.CreateTokenSession(r.Context(), newTokenID, r.UserAgent(), clientIP(r, cfg.Server.TrustedProxyHops))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		s.setSessionCookie(w, r, secret, sessionMaxAge)
	}
	views, err := s.tokenViews(r.Context(), s.currentTokenName(r, tokenName))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var role, scope, prefix string
	for _, view := range views {
		if view.Name == name && view.RevokedAt == nil {
			role = view.Role
			scope = view.Scope
			prefix = view.Prefix
			break
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, CreatedToken{
		Token:  token,
		Name:   name,
		Role:   role,
		Scope:  scope,
		Prefix: prefix,
		Tokens: views,
	})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, tokenName string) {
	name := r.PathValue("name")
	err := s.store.RevokeToken(r.Context(), name)
	if errors.Is(err, tickets.ErrNotFound) {
		writeError(w, http.StatusNotFound, fmt.Errorf("no active token is named %q", name))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	fmt.Fprintf(s.log, "machinist: %s revoked token %q from the web app\n", tokenName, name)
	if p, ok := principalFromRequest(r); ok && p.Kind == "token" && p.Name == name {
		// The workspace token that authenticated this request is gone, so its
		// sign-in ends here. OAuth sessions (p.Kind == "user") are never touched.
		s.setSessionCookie(w, r, "", -time.Second)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.listTokens(w, r, tokenName)
}

// AgentUse is one preset step that runs an agent, with the effective harness
// and model for that step after any preset or step overrides.
type AgentUse struct {
	Preset  string `json:"preset"`
	Step    string `json:"step"`
	Harness string `json:"harness"`
	Model   string `json:"model,omitempty"`
}

// AgentView is one agent file: its front matter (harness, model, optional
// command), its prompt body, and the presets and steps that run it.
type AgentView struct {
	Name    string     `json:"name"`
	Harness string     `json:"harness"`
	Model   string     `json:"model,omitempty"`
	Command string     `json:"command,omitempty"`
	Prompt  string     `json:"prompt"`
	Presets []string   `json:"presets"`
	Uses    []AgentUse `json:"uses"`
}

func (s *Server) listAgents(w http.ResponseWriter, _ *http.Request, _ string) {
	st := s.state()
	agents := []AgentView{}
	if st.factory != nil {
		usedBy := map[string][]string{}
		usesBy := map[string][]AgentUse{}
		for _, preset := range st.presets {
			for _, step := range preset.Steps {
				for _, agent := range step.Agents {
					if !slices.Contains(usedBy[agent.Name], preset.Name) {
						usedBy[agent.Name] = append(usedBy[agent.Name], preset.Name)
					}
					use := AgentUse{Preset: preset.Name, Step: step.Name, Harness: agent.Harness, Model: agent.Model}
					if !slices.Contains(usesBy[agent.Name], use) {
						usesBy[agent.Name] = append(usesBy[agent.Name], use)
					}
				}
			}
		}
		for name, agent := range st.factory.Agents {
			agents = append(agents, AgentView{
				Name:    name,
				Harness: agent.Harness,
				Model:   agent.Model,
				Command: agent.Program,
				Prompt:  agent.Prompt,
				Presets: append([]string{}, usedBy[name]...),
				Uses:    append([]AgentUse{}, usesBy[name]...),
			})
		}
		sort.Slice(agents, func(a, b int) bool { return agents[a].Name < agents[b].Name })
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}
