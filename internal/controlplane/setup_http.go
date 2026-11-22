package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/engine"
	"github.com/owainlewis/machinist/internal/githubapp"
	"github.com/owainlewis/machinist/internal/tickets"
)

// githubApp is what the setup page asks of the GitHub App.
type githubApp interface {
	Identity(ctx context.Context) (githubapp.Identity, error)
	Installed(ctx context.Context, repository string) error
}

// repositoryName is owner/name, as GitHub allows them.
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)

// docs is where the setup page sends people to read more.
const docs = "https://github.com/owainlewis/machinist/blob/main/docs/"

// runnerWindow is how recently a runner must have made contact to count as
// online. Runners poll every few seconds and send a heartbeat every 30.
const runnerWindow = 90 * time.Second

// SetupCheck is one line of the setup checklist.
type SetupCheck struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Required bool   `json:"required"`
	Detail   string `json:"detail"`
	// Fix says what to do when the check fails.
	Fix string `json:"fix,omitempty"`
	// Link is a page that helps, such as the app's install page.
	Link string `json:"link,omitempty"`
}

// ProjectView is one repository Machinist works on.
type ProjectView struct {
	Repo string `json:"repo"`
	// Source is config when the repository is listed in config.yaml, and app
	// when someone added it in the web app. Only app ones can be removed here.
	Source  string `json:"source"`
	AddedBy string `json:"added_by,omitempty"`
	// Problem says why the GitHub App cannot reach the repository, if it can't.
	Problem string `json:"problem,omitempty"`
}

// Setup is the setup page: the checklist and the projects.
type Setup struct {
	Ready      bool          `json:"ready"`
	Checks     []SetupCheck  `json:"checks"`
	Projects   []ProjectView `json:"projects"`
	InstallURL string        `json:"install_url,omitempty"`
}

func (s *Server) setup(ctx context.Context) (Setup, error) {
	setup := Setup{Checks: []SetupCheck{}}
	github := SetupCheck{Name: "GitHub", Required: true}
	if s.app == nil {
		github.OK = true
		github.Detail = "No GitHub App is set, so runs use the runner's own GH_TOKEN."
		github.Fix = "Set up a GitHub App so each run gets a short-lived token and commits as the app."
		github.Link = docs + "github-app.md"
	} else if identity, err := s.app.Identity(ctx); err != nil {
		github.Detail = "Machinist cannot reach the GitHub App: " + err.Error()
		github.Fix = "Check github.app.id and the private key file in config.yaml."
	} else {
		github.OK = true
		github.Detail = "Connected as " + identity.Name + "."
		setup.InstallURL = identity.InstallURL()
	}
	setup.Checks = append(setup.Checks, github)

	runners, _, err := s.store.Fleet(ctx)
	if err != nil {
		return setup, err
	}
	online := []string{}
	for _, runner := range runners {
		if s.now().Sub(runner.LastContact) < runnerWindow {
			online = append(online, runner.Name)
		}
	}
	runnerCheck := SetupCheck{Name: "Runner", Required: true, OK: len(online) > 0}
	switch {
	case len(online) > 0:
		runnerCheck.Detail = fmt.Sprintf("%s online.", strings.Join(online, ", "))
	case len(runners) > 0:
		runnerCheck.Detail = "No runner has made contact in the last 90 seconds."
		runnerCheck.Fix = "Start one with machinist runner start, and check it with machinist runner check."
	default:
		runnerCheck.Detail = "No runner has connected yet."
		runnerCheck.Fix = "Start one with machinist runner start, and check it with machinist runner check."
	}
	setup.Checks = append(setup.Checks, runnerCheck)
	if check, ok := s.harnessCheck(runners); ok {
		setup.Checks = append(setup.Checks, check)
	}

	setup.Projects, err = s.projects(ctx)
	if err != nil {
		return setup, err
	}
	projects := SetupCheck{Name: "Projects", Required: true, OK: len(setup.Projects) > 0}
	broken := 0
	for _, project := range setup.Projects {
		if project.Problem != "" {
			broken++
		}
	}
	switch {
	case len(setup.Projects) == 0:
		projects.Detail = "No repository is connected yet."
		projects.Fix = "Add one on the Projects page."
	case broken > 0:
		projects.OK = false
		projects.Detail = fmt.Sprintf("The GitHub App cannot reach %d of %d repositories.", broken, len(setup.Projects))
		projects.Fix = "Install the app on them."
		projects.Link = setup.InstallURL
	default:
		projects.Detail = fmt.Sprintf("%d connected.", len(setup.Projects))
	}
	setup.Checks = append(setup.Checks, projects)

	linear := SetupCheck{Name: "Linear", OK: s.linear != nil}
	if s.linear != nil {
		linear.Detail = "On. Issues assigned to the Machinist app start jobs."
	} else {
		linear.Detail = "Off. This is optional."
		linear.Fix = "Set linear.teams in config.yaml, and the Linear app's LINEAR_CLIENT_ID and LINEAR_CLIENT_SECRET, to start jobs from Linear."
		linear.Link = docs + "linear.md#set-it-up"
	}
	setup.Checks = append(setup.Checks, linear)

	setup.Ready = !slices.ContainsFunc(setup.Checks, func(check SetupCheck) bool { return check.Required && !check.OK })
	return setup, nil
}

// projects lists every connected repository, and asks the GitHub App about each.
func (s *Server) projects(ctx context.Context) ([]ProjectView, error) {
	added, err := s.store.Projects(ctx)
	if err != nil {
		return nil, err
	}
	views := []ProjectView{}
	for _, repository := range s.intake.Repositories {
		views = append(views, ProjectView{Repo: repository, Source: "config"})
	}
	for _, project := range added {
		if slices.ContainsFunc(views, func(view ProjectView) bool { return strings.EqualFold(view.Repo, project.Repo) }) {
			continue
		}
		views = append(views, ProjectView{Repo: project.Repo, Source: "app", AddedBy: project.AddedBy})
	}
	if s.app != nil {
		for index := range views {
			if err := s.app.Installed(ctx, views[index].Repo); err != nil {
				views[index].Problem = err.Error()
			}
		}
	}
	return views, nil
}

func (s *Server) getSetup(w http.ResponseWriter, r *http.Request, _ string) {
	setup, err := s.setup(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, setup)
}

func (s *Server) addProject(w http.ResponseWriter, r *http.Request, tokenName string) {
	var request struct {
		Repo string `json:"repo"`
	}
	if !decodeJSON(w, r, 4<<10, &request) {
		return
	}
	repository := strings.TrimSpace(request.Repo)
	repository = strings.TrimSuffix(strings.TrimPrefix(repository, "https://github.com/"), "/")
	if !repositoryName.MatchString(repository) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%q is not a repository name like owner/name", request.Repo))
		return
	}
	if s.app != nil {
		if err := s.app.Installed(r.Context(), repository); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	if err := s.store.AddProject(r.Context(), repository, tokenName); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.getSetup(w, r, tokenName)
}

var errConfigProject = errors.New("this repository is listed in config.yaml; remove it there")

func (s *Server) removeProject(w http.ResponseWriter, r *http.Request, tokenName string) {
	repository := r.PathValue("owner") + "/" + r.PathValue("name")
	if slices.ContainsFunc(s.intake.Repositories, func(listed string) bool { return strings.EqualFold(listed, repository) }) {
		writeError(w, http.StatusConflict, errConfigProject)
		return
	}
	removed, err := s.store.RemoveProject(r.Context(), repository)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, fmt.Errorf("%s is not a project", repository))
		return
	}
	s.getSetup(w, r, tokenName)
}

// harnessCheck says whether an online runner has the harnesses each preset's
// agents need. A runner that did not report harnesses, such as a Kubernetes
// runner, may have any of them. There is no check while no runner is online.
func (s *Server) harnessCheck(runners []tickets.Runner) (SetupCheck, bool) {
	var online []tickets.Runner
	for _, runner := range runners {
		if s.now().Sub(runner.LastContact) < runnerWindow {
			online = append(online, runner)
		}
	}
	if len(online) == 0 || s.factory == nil {
		return SetupCheck{}, false
	}
	check := SetupCheck{Name: "Agents", OK: true, Detail: "Every preset can run on an online runner."}
	names := make([]string, 0, len(s.factory.Presets))
	for name := range s.factory.Presets {
		names = append(names, name)
	}
	sort.Strings(names)
	var stuck []string
	for _, name := range names {
		needs := s.factory.Needs(s.factory.Presets[name])
		if !slices.ContainsFunc(online, func(runner tickets.Runner) bool {
			return !runner.Probed || engine.HasAll(runner.Harnesses, needs)
		}) {
			stuck = append(stuck, fmt.Sprintf("%s needs %s", name, strings.Join(needs, " and ")))
		}
	}
	if len(stuck) > 0 {
		check.OK = false
		check.Detail = "No online runner can run these presets, so their jobs wait: " + strings.Join(stuck, "; ") + "."
		check.Fix = "Install the missing harness on a runner and restart it, or change the presets' agents."
		check.Link = docs + "runners.md"
	}
	return check, true
}
