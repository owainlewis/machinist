package controlplane

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/tickets"
)

// The Settings pages show how Machinist is configured. config.yaml and the
// factory files stay the source of truth, so every route here reads, except
// revoking a token. None of them returns a secret: no token, key or
// environment value.

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
	settings := GitHubSettings{Handle: s.config.GitHub.Handle, AutoMerge: s.config.GitHub.AutoMerges(), Repositories: []RepositoryAccess{}}
	if s.config.GitHub.App != nil || s.app != nil {
		settings.App = &AppView{}
		if s.config.GitHub.App != nil {
			settings.App.ID = s.config.GitHub.App.ID
		}
		if s.app != nil {
			if identity, err := s.app.Identity(r.Context()); err != nil {
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

// LinearSettings is the Linear page. The access token is never shown.
type LinearSettings struct {
	Enabled      bool         `json:"enabled"`
	AppUserID    string       `json:"app_user_id,omitempty"`
	AllowedUsers []string     `json:"allowed_users"`
	Preset       string       `json:"preset,omitempty"`
	Teams        []LinearTeam `json:"teams"`
	// PollSeconds is how often Machinist asks Linear for new assignments.
	PollSeconds    int64  `json:"poll_seconds,omitempty"`
	MaxJobsPerHour int    `json:"max_jobs_per_hour,omitempty"`
	TokenEnv       string `json:"token_env"`
	Docs           string `json:"docs"`
}

// LinearTeam is one Linear team and the repository its issues work in.
type LinearTeam struct {
	Team string `json:"team"`
	Repo string `json:"repo"`
}

func (s *Server) linearSettings(w http.ResponseWriter, _ *http.Request, _ string) {
	linear := s.config.Linear
	settings := LinearSettings{Enabled: s.linear != nil, AllowedUsers: []string{}, Teams: []LinearTeam{}, TokenEnv: config.LinearTokenEnv, Docs: docs + "linear.md"}
	if settings.Enabled {
		settings.AppUserID = linear.AppUserID
		settings.AllowedUsers = append(settings.AllowedUsers, linear.AllowedUsers...)
		settings.Preset = linear.Preset
		if settings.Preset == "" {
			settings.Preset = s.config.Server.DefaultPreset
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
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	// Current is true for the token that made this request.
	Current bool `json:"current,omitempty"`
}

// roleName is how a page names a role. A token from before roles can act as
// a person and as a runner.
func roleName(role string) string {
	if role == tickets.RoleAll {
		return "all"
	}
	return role
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request, tokenName string) {
	tokens, err := s.store.ListTokens(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	views := make([]TokenView, 0, len(tokens))
	for _, token := range tokens {
		views = append(views, TokenView{
			Name:       token.Name,
			Role:       roleName(token.Role),
			CreatedAt:  token.CreatedAt,
			LastUsedAt: token.LastUsedAt,
			RevokedAt:  token.RevokedAt,
			Current:    token.RevokedAt == nil && token.Name == tokenName,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": views})
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
	if name == tokenName {
		// The token that asked is gone, so its sign-in ends here.
		s.setSessionCookie(w, "", -time.Second)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.listTokens(w, r, tokenName)
}

// AgentView is one agent file: its harness and model, and the presets whose
// steps run it. Its prompt stays in the file.
type AgentView struct {
	Name    string   `json:"name"`
	Harness string   `json:"harness"`
	Model   string   `json:"model,omitempty"`
	Presets []string `json:"presets"`
}

func (s *Server) listAgents(w http.ResponseWriter, _ *http.Request, _ string) {
	agents := []AgentView{}
	if s.factory != nil {
		usedBy := map[string][]string{}
		for _, preset := range s.presets {
			for _, step := range preset.Steps {
				for _, agent := range step.Agents {
					if !slices.Contains(usedBy[agent.Name], preset.Name) {
						usedBy[agent.Name] = append(usedBy[agent.Name], preset.Name)
					}
				}
			}
		}
		for name, agent := range s.factory.Agents {
			agents = append(agents, AgentView{Name: name, Harness: agent.Harness, Model: agent.Model, Presets: append([]string{}, usedBy[name]...)})
		}
		sort.Slice(agents, func(a, b int) bool { return agents[a].Name < agents[b].Name })
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}
