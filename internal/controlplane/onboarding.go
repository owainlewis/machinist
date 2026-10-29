package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/githubauth"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/pelletier/go-toml/v2"
)

const DefaultGitHubAppClientID = "Iv23liojlkYqrUIHYYXg"
const DefaultGitHubAppInstallURL = "https://github.com/apps/machinist-local/installations/new"

type setupSelection struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Agent      string `json:"agent"`
	Host       string `json:"host"`
}
type setupCheck struct {
	State   string `json:"state"`
	Message string `json:"message"`
}
type projectDefaults struct {
	Recipe       string `json:"recipe"`
	Instructions string `json:"instructions"`
}

type setupSettings struct {
	ProjectDefaults map[string]projectDefaults `json:"project_defaults,omitempty"`
	Repositories    map[string]string          `json:"repositories,omitempty"`
	ClientID        string                     `json:"client_id"`
	InstallURL      string                     `json:"install_url"`
	Selection       setupSelection             `json:"selection"`
	Check           setupCheck                 `json:"check"`
	Finished        bool                       `json:"finished"`
}
type LocalWorker interface {
	Run(context.Context) error
	SetGitHubTokenProvider(func(context.Context, string) (string, error))
}
type LocalWorkerFactory func(config.Worker) (LocalWorker, error)
type setupHealth struct {
	runtime  string
	spec     protocol.RunSpec
	instance string
	result   setupCheck
	done     chan struct{}
}

type onboarding struct {
	configureMu   sync.Mutex // Serializes setup and project changes, including worker restarts.
	mu            sync.Mutex
	settings      setupSettings
	dir, baseURL  string
	connection    *GitHubConnection
	repos         []GitHubConnectedRepository
	server        *GitHubFactoryServer
	cancel        context.CancelFunc
	done          chan struct{}
	runCheck      func(context.Context, setupSelection) setupCheck
	workerFactory LocalWorkerFactory
	health        *setupHealth
}

func newOnboarding(s *GitHubFactoryServer) (*onboarding, error) {
	o := &onboarding{server: s, dir: filepath.Join(s.dataDirectory, "onboarding"), settings: setupSettings{ClientID: DefaultGitHubAppClientID, InstallURL: DefaultGitHubAppInstallURL}}
	raw, err := os.ReadFile(filepath.Join(o.dir, "settings.json"))
	if err == nil {
		if err = json.Unmarshal(raw, &o.settings); err != nil {
			return nil, fmt.Errorf("read onboarding settings: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	o.connection, err = NewGitHubConnection(o.dir, o.settings.ClientID, o.settings.InstallURL)
	if err != nil {
		return nil, err
	}
	o.settings.Repositories = o.settings.projectPaths()
	for repo := range o.settings.Repositories {
		if _, err := githubRepositoryPath(repo); err != nil {
			return nil, fmt.Errorf("configured project: %w", err)
		}
		s.repositories[repo] = repo
	}
	o.runCheck = o.checkAgent
	return o, nil
}
func (o *onboarding) saveLocked() error {
	if err := os.MkdirAll(o.dir, 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(o.settings, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(o.dir, "settings-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(o.dir, "settings.json"))
}
func (state setupSettings) projectPaths() map[string]string {
	paths := maps.Clone(state.Repositories)
	if paths == nil {
		paths = map[string]string{}
	}
	if state.Selection.Repository != "" {
		if _, ok := paths[state.Selection.Repository]; !ok {
			paths[state.Selection.Repository] = state.Selection.Path
		}
	}
	return paths
}
func (o *onboarding) snapshot() setupSettings {
	o.mu.Lock()
	defer o.mu.Unlock()
	state := o.settings
	state.Repositories = state.projectPaths()
	state.ProjectDefaults = maps.Clone(state.ProjectDefaults)
	return state
}
func (o *onboarding) status(ctx context.Context) (map[string]any, error) {
	github, err := o.connection.Status(ctx)
	if err != nil {
		return nil, err
	}
	state := o.snapshot()
	o.mu.Lock()
	repos := append([]GitHubConnectedRepository{}, o.repos...)
	base := o.baseURL
	o.mu.Unlock()
	agents := []map[string]any{}
	for _, name := range []string{"codex", "claude"} {
		_, err := exec.LookPath(name)
		label := "Codex"
		if name == "claude" {
			label = "Claude Code"
		}
		agents = append(agents, map[string]any{"id": name, "name": label, "available": err == nil})
	}
	worker := map[string]any{"connected": false, "config_url": "/api/v1/onboarding/worker-config?host=remote", "command": "sh machinist-worker-setup.sh"}
	for _, w := range o.server.liveWorkers() {
		if time.Since(w.LastSeenAt) < workerAvailabilityWindow && slices.Contains(w.Repositories, state.Selection.Repository) && slices.Contains(w.Executors, state.Selection.Agent) && slices.Contains(w.ReadOnlyExecutors, state.Selection.Agent+"-review") {
			worker["connected"] = true
			worker["name"] = w.Name
		}
	}
	if base != "" {
		u, _ := urlForLocal(base)
		hostname, _ := os.Hostname()
		login, err := user.Current()
		target := hostname
		if err == nil {
			target = login.Username + "@" + hostname
		}
		worker["tunnel"] = "ssh -N -L 17341:127.0.0.1:" + u + " " + target
	}
	ready := github.Connected && state.Selection.Repository != "" && state.Selection.Agent != "" && state.Check.State == "passed"
	kubernetes, kubernetesAgent := false, ""
	if definition, err := config.LoadConfig(o.server.definitionPath); err == nil && definition.Server.Kubernetes != nil {
		kubernetes, kubernetesAgent = true, definition.Server.Kubernetes.AgentName()
	}
	return map[string]any{"project_defaults": state.ProjectDefaults, "kubernetes": kubernetes, "kubernetes_agent": kubernetesAgent, "required": !state.Finished || !ready, "github": github, "repositories": repos, "selection": state.Selection, "agents": agents, "worker": worker, "ready": ready, "check": state.Check}, nil
}
func urlForLocal(base string) (string, error) {
	_, port, err := net.SplitHostPort(strings.TrimPrefix(base, "http://"))
	return port, err
}
func (s *GitHubFactoryServer) onboardingStatus(w http.ResponseWriter, r *http.Request) {
	if s.setup == nil {
		writeError(w, 404, errors.New("onboarding unavailable"))
		return
	}
	result, err := s.setup.status(r.Context())
	if err != nil {
		writeError(w, 500, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (s *GitHubFactoryServer) onboardingAction(w http.ResponseWriter, r *http.Request) {
	if s.setup == nil {
		writeError(w, 404, errors.New("onboarding unavailable"))
		return
	}
	if !limitRequestBody(w, r, maxRequestBytes) {
		return
	}
	o := s.setup
	action := r.PathValue("action")
	if action == "check" || action == "finish" || action == "github/disconnect" || action == "github/configure" {
		o.configureMu.Lock()
		defer o.configureMu.Unlock()
	}
	var result any = map[string]bool{"ok": true}
	var err error
	switch action {
	case "github/configure":
		var in struct {
			ClientID   string `json:"client_id"`
			InstallURL string `json:"install_url"`
		}
		if err = decodeJSON(r, &in); err == nil {
			err = o.connection.Configure(in.ClientID, in.InstallURL)
		}
		if err == nil {
			o.mu.Lock()
			o.settings.ClientID = in.ClientID
			o.settings.InstallURL = in.InstallURL
			o.settings.Finished = false
			o.settings.Check = setupCheck{}
			err = o.saveLocked()
			o.mu.Unlock()
		}
	case "github/start":
		result, err = o.connection.Start(r.Context())
	case "github/poll":
		result, err = o.connection.Poll(r.Context())
		if err == nil {
			p := result.(GitHubConnectionPoll)
			if p.Status == "connected" {
				_, err = o.loadRepositories(r.Context())
			}
		}
	case "github/disconnect":
		o.stopWorker()
		err = o.connection.Disconnect()
		if err == nil {
			s.mu.Lock()
			for id := range s.liveLeases() {
				s.deleteLease(id)
			}
			s.loaded = time.Time{}
			s.mu.Unlock()
			o.mu.Lock()
			o.settings.Finished = false
			o.settings.Check = setupCheck{}
			o.repos = nil
			err = o.saveLocked()
			o.mu.Unlock()
		}
	case "repository":
		var in setupSelection
		if err = decodeJSON(r, &in); err == nil {
			err = o.selectRepository(r.Context(), in.Repository, in.Path)
		}
	case "agent":
		var in setupSelection
		if err = decodeJSON(r, &in); err == nil {
			err = o.selectAgent(in.Agent, in.Host)
		}
	case "check":
		state := o.snapshot()
		if state.Selection.Repository == "" || state.Selection.Agent == "" {
			err = errors.New("select a repository and agent first")
			break
		}
		check := o.runCheck(r.Context(), state.Selection)
		o.mu.Lock()
		if o.settings.Selection != state.Selection {
			err = errors.New("setup changed during verification; run the check again")
		} else {
			o.settings.Check = check
			err = o.saveLocked()
		}
		o.mu.Unlock()
		result = check
	case "finish":
		state := o.snapshot()
		github, _ := o.connection.Status(r.Context())
		if !github.Connected || state.Check.State != "passed" {
			err = errors.New("connect GitHub and pass the connection check first")
			break
		}
		if state.Selection.Host == "local" || state.Selection.Host == "kubernetes" {
			err = o.startWorker()
			if err != nil {
				break
			}
		}
		o.mu.Lock()
		if o.settings.Selection != state.Selection || o.settings.Check != state.Check {
			err = errors.New("setup changed before completion; verify the current selection again")
		} else {
			o.settings.Finished = true
			err = o.saveLocked()
		}
		o.mu.Unlock()
		if err != nil {
			o.stopWorker()
			break
		}
		result = map[string]bool{"ready": true}
	default:
		writeError(w, 404, errors.New("unknown setup action"))
		return
	}
	if err != nil {
		writeError(w, 400, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (o *onboarding) loadRepositories(ctx context.Context) ([]GitHubConnectedRepository, error) {
	repos, err := o.connection.Repositories(ctx)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	o.repos = repos
	o.mu.Unlock()
	return repos, nil
}
func (s *GitHubFactoryServer) onboardingRepositories(w http.ResponseWriter, r *http.Request) {
	if s.setup == nil {
		writeError(w, 404, errors.New("onboarding unavailable"))
		return
	}
	repos, err := s.setup.loadRepositories(r.Context())
	if err != nil {
		writeError(w, 400, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, repos)
}
func (o *onboarding) prepareRepository(ctx context.Context, repo, path string) (string, error) {
	if _, err := githubRepositoryPath(repo); err != nil {
		return "", err
	}
	repos, err := o.loadRepositories(ctx)
	if err != nil {
		return "", err
	}
	if !slices.ContainsFunc(repos, func(r GitHubConnectedRepository) bool { return r.FullName == repo }) {
		return "", errors.New("the GitHub App cannot access this repository")
	}
	for _, r := range repos {
		if r.FullName == repo && !r.CanWrite {
			reason := r.AccessError
			if reason == "" {
				reason = "GitHub write access is required for this repository"
			}
			return "", errors.New(reason)
		}
	}
	token, err := o.connection.Token(ctx)
	if err != nil {
		return "", err
	}
	env, err := githubauth.GitHubEnvironment(token, repo)
	if err != nil {
		return "", err
	}
	if path == "" {
		path = filepath.Join(o.dir, "checkouts", filepath.FromSlash(repo))
		if _, e := os.Stat(path); os.IsNotExist(e) {
			if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return "", err
			}
			cmd := exec.CommandContext(ctx, "git", "clone", "--", "https://github.com/"+repo+".git", path)
			cmd.Env = append(os.Environ(), env...)
			if err = cmd.Run(); err != nil {
				return "", errors.New("could not clone repository; verify GitHub App contents access")
			}
		}
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", errors.New("checkout path does not exist")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--show-toplevel")
	raw, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(raw)) != path {
		return "", errors.New("choose the root of an existing Git checkout")
	}
	cmd = exec.CommandContext(ctx, "git", "-C", path, "remote", "get-url", "origin")
	raw, err = cmd.Output()
	if err != nil || !sameGitHubRepository(strings.TrimSpace(string(raw)), repo) {
		return "", errors.New("checkout origin does not match the selected GitHub repository")
	}
	cmd = exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	raw, err = cmd.Output()
	if err != nil {
		return "", errors.New("could not locate checkout metadata")
	}
	probe, err := os.CreateTemp(strings.TrimSpace(string(raw)), "machinist-write-check-*")
	if err != nil {
		return "", errors.New("checkout Git metadata is not writable; leave the checkout path blank to create a managed clone")
	}
	probeName := probe.Name()
	_ = probe.Close()
	_ = os.Remove(probeName)
	// Verify actual Git access, not just API membership, before accepting the checkout.
	cmd = exec.CommandContext(ctx, "git", "-C", path, "ls-remote", "--exit-code", "origin", "HEAD")
	cmd.Env = append(os.Environ(), env...)
	if err = cmd.Run(); err != nil {
		return "", errors.New("Git could not access the selected repository with the GitHub App credential")
	}
	return path, nil
}

func (o *onboarding) selectRepository(ctx context.Context, repo, path string) error {
	o.configureMu.Lock()
	defer o.configureMu.Unlock()
	prepared, err := o.prepareRepository(ctx, repo, path)
	if err != nil {
		return err
	}
	return o.commitProject(repo, prepared, true)
}
func sameGitHubRepository(remote, repo string) bool {
	for _, prefix := range []string{"https://github.com/", "git@github.com:", "ssh://git@github.com/"} {
		if strings.HasPrefix(remote, prefix) {
			return strings.EqualFold(strings.TrimSuffix(strings.TrimPrefix(remote, prefix), ".git"), repo)
		}
	}
	return false
}
func (o *onboarding) selectAgent(agent, host string) error {
	o.configureMu.Lock()
	defer o.configureMu.Unlock()
	if agent != "codex" && agent != "claude" {
		return errors.New("select Codex or Claude Code")
	}
	if host != "local" && host != "remote" && host != "kubernetes" {
		return errors.New("select this computer, remote VM or Kubernetes")
	}
	if host == "kubernetes" {
		definition, err := config.LoadConfig(o.server.definitionPath)
		if err != nil {
			return err
		}
		if definition.Server.Kubernetes == nil {
			return errors.New("configure server.kubernetes before selecting the cluster runner")
		}
		if err = definition.Server.Kubernetes.Validate(); err != nil {
			return err
		}
		if want := definition.Server.Kubernetes.AgentName(); agent != want {
			return fmt.Errorf("the Kubernetes runner is configured for %s; set server.kubernetes.agent to change it", want)
		}
	}
	if host == "local" {
		if _, err := exec.LookPath(agent); err != nil {
			return fmt.Errorf("%s was not found on this computer", agent)
		}
		if agent == "claude" {
			if err := runner.CheckRestrictedClaude(context.Background(), setupExecutors(agent)["claude-review"].Command); err != nil {
				return err
			}
		}
	}
	o.stopWorker()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.settings.Selection.Repository == "" {
		return errors.New("select a repository first")
	}
	o.settings.Selection.Agent = agent
	o.settings.Selection.Host = host
	o.settings.Check = setupCheck{}
	o.settings.Finished = false
	return o.saveLocked()
}
func setupExecutors(agent string) map[string]config.Executor {
	if agent == "claude" {
		return map[string]config.Executor{"claude-native": {Adapter: "claude-stream", Command: []string{"claude", "--print", "--verbose", "--output-format", "stream-json", "--include-partial-messages", "--dangerously-skip-permissions"}}, "claude": {Command: []string{"claude", "--print", "--verbose", "--output-format", "stream-json", "--dangerously-skip-permissions"}}, "claude-review": {ReadOnly: true, Command: []string{"claude", "--print", "--verbose", "--output-format", "stream-json", "--safe-mode", "--restricted", "--strict-mcp-config", "--permission-mode", "plan", "--tools", "Read,Glob,Grep"}}}
	}
	return map[string]config.Executor{"codex-native": {Adapter: "codex-app-server", Command: []string{"codex", "app-server", "--stdio", "-c", "features.multi_agent=true", "-c", `sandbox_mode='danger-full-access'`}}, "codex": {Command: []string{"codex", "exec", "--json", "--sandbox", "danger-full-access", "-"}}, "codex-review": {ReadOnly: true, Command: []string{"codex", "exec", "--json", "--sandbox", "read-only", "-"}}}
}
func (o *onboarding) workerConfig(selection setupSelection) (config.Worker, error) {
	o.mu.Lock()
	base := o.baseURL
	o.mu.Unlock()
	if base == "" {
		return config.Worker{}, errors.New("server is not listening yet")
	}
	tokenPath := filepath.Join(o.dir, "worker.token")
	if err := os.WriteFile(tokenPath, []byte(o.server.auth.workerToken), 0600); err != nil {
		return config.Worker{}, err
	}
	repositories := map[string]config.Repository{}
	for repo, path := range o.snapshot().projectPaths() {
		repositories[repo] = config.Repository{Path: path}
	}
	cfg := config.Worker{Name: "machinist-local", DataDirectory: filepath.Join(o.dir, "worker"), ControlPlane: config.ControlPlane{URL: base, TokenFile: tokenPath}, Executors: setupExecutors(selection.Agent), Repositories: repositories}
	if selection.Host == "kubernetes" {
		definition, err := config.LoadConfig(o.server.definitionPath)
		if err != nil {
			return cfg, err
		}
		if definition.Server.Kubernetes == nil {
			return cfg, errors.New("Kubernetes runner configuration is missing")
		}
		for name, executor := range cfg.Executors {
			if executor.Adapter != "" {
				delete(cfg.Executors, name)
			}
		}
		cfg.Kubernetes = definition.Server.Kubernetes
		cfg.Name = "machinist-kubernetes"
	}
	return cfg, nil
}
func (o *onboarding) startWorker() error {
	state := o.snapshot()
	if state.Selection.Host != "local" && state.Selection.Host != "kubernetes" {
		return nil
	}
	o.mu.Lock()
	if o.cancel != nil {
		o.mu.Unlock()
		return nil
	}
	o.mu.Unlock()
	cfg, err := o.workerConfig(state.Selection)
	if err != nil {
		return err
	}
	o.mu.Lock()
	factory := o.workerFactory
	o.mu.Unlock()
	if factory == nil {
		return errors.New("local worker launcher is unavailable")
	}
	worker, err := factory(cfg)
	if err != nil {
		return err
	}
	worker.SetGitHubTokenProvider(func(ctx context.Context, alias string) (string, error) {
		if _, allowed := cfg.Repositories[alias]; !allowed {
			return "", errors.New("worker repository not selected")
		}
		repos, err := o.loadRepositories(ctx)
		if err != nil {
			return "", err
		}
		if !slices.ContainsFunc(repos, func(r GitHubConnectedRepository) bool { return r.FullName == alias && r.CanWrite }) {
			return "", errors.New("GitHub App repository access was revoked")
		}
		return o.connection.Token(ctx)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	o.mu.Lock()
	if o.cancel != nil {
		o.mu.Unlock()
		cancel()
		return nil
	}
	if o.settings.Selection != state.Selection || !maps.Equal(o.settings.projectPaths(), state.projectPaths()) {
		o.mu.Unlock()
		cancel()
		return errors.New("runner configuration changed; retry setup")
	}
	o.cancel = cancel
	o.done = done
	o.mu.Unlock()
	go func() {
		defer close(done)
		err := worker.Run(ctx)
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.done == done {
			o.cancel = nil
			o.done = nil
			if ctx.Err() == nil {
				o.settings.Finished = false
				o.settings.Check = setupCheck{"failed", "The local worker stopped. Check worker logs and run verification again."}
				_ = o.saveLocked()
				if err != nil {
					fmt.Fprintln(os.Stderr, "machinist: local worker stopped:", err)
				}
			}
		}
	}()
	return nil
}
func (o *onboarding) stopWorker() {
	o.mu.Lock()
	cancel, done := o.cancel, o.done
	timeout := 5 * time.Second
	if o.settings.Selection.Host == "kubernetes" {
		timeout = 50 * time.Second
	}
	o.cancel = nil
	o.done = nil
	o.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-time.After(timeout):
		}
	}
}
func (o *onboarding) checkAgent(ctx context.Context, selection setupSelection) setupCheck {
	timeout := 60 * time.Second
	if selection.Host == "kubernetes" {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if selection.Host == "local" {
		for _, program := range []string{"git", "gh", selection.Agent} {
			if _, err := exec.LookPath(program); err != nil {
				return setupCheck{"failed", program + " is not installed on this computer."}
			}
		}
	}
	repos, accessErr := o.loadRepositories(ctx)
	if accessErr != nil {
		return setupCheck{"failed", "Could not verify the GitHub App installation. Reconnect GitHub and retry."}
	}
	if !slices.ContainsFunc(repos, func(r GitHubConnectedRepository) bool { return r.FullName == selection.Repository && r.CanWrite }) {
		return setupCheck{"failed", "The GitHub App and your account need write access to the selected repository."}
	}
	if _, err := o.connection.API(ctx, "GET", "/repos/"+selection.Repository, nil); err != nil {
		return setupCheck{"failed", "GitHub repository access failed. Reconnect or update the App installation."}
	}
	if selection.Host == "remote" || selection.Host == "kubernetes" {
		if selection.Host == "kubernetes" {
			if err := o.startWorker(); err != nil {
				return setupCheck{"failed", err.Error()}
			}
		}
		return o.checkRemote(ctx, selection)
	}
	token, err := o.connection.Token(ctx)
	if err != nil {
		return setupCheck{"failed", err.Error()}
	}
	env, err := githubauth.GitHubEnvironment(token, selection.Repository)
	if err != nil {
		return setupCheck{"failed", err.Error()}
	}
	executor := setupExecutors(selection.Agent)[selection.Agent+"-review"]
	if selection.Agent == "codex" {
		executor = setupExecutors(selection.Agent)["codex-native"]
		for index, arg := range executor.Command {
			if strings.Contains(arg, "sandbox_mode=") {
				executor.Command[index] = `sandbox_mode='read-only'`
			}
		}
	} else {
		executor.Adapter = "claude-stream"
	}
	result, err := runner.Execute(ctx, runner.Options{Command: config.ResolvedCommand{Name: "connection-check", Executor: selection.Agent, Adapter: executor.Adapter, Command: executor.Command, Prompt: "Reply exactly PONG. Do not use tools, read files, or change anything.", Timeout: 45 * time.Second}, Repository: selection.Path, DataDirectory: filepath.Join(o.dir, "checks"), Stdout: io.Discard, Stderr: io.Discard, Environment: env})
	if err != nil {
		return setupCheck{"failed", err.Error()}
	}
	if strings.TrimSpace(result.FinalMessage) != "PONG" {
		return setupCheck{"failed", "The agent ran but did not return PONG. Check its CLI configuration and sign-in."}
	}
	return setupCheck{"passed", "PONG. Agent, GitHub access, and repository are ready. No issue was created."}
}
func (o *onboarding) checkRemote(ctx context.Context, selection setupSelection) setupCheck {
	connected := false
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, w := range o.server.liveWorkers() {
			expected := "machinist-local"
			if selection.Host == "kubernetes" {
				expected = "machinist-kubernetes"
			}
			if selection.Host == "kubernetes" && w.Name != expected {
				continue
			}
			if time.Since(w.LastSeenAt) < workerAvailabilityWindow && slices.Contains(w.Repositories, selection.Repository) && slices.Contains(w.Executors, selection.Agent) && slices.Contains(w.ReadOnlyExecutors, selection.Agent+"-review") {
				connected = true
			}
		}
		if connected || selection.Host != "kubernetes" || time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return setupCheck{"failed", "Runner connection check was cancelled."}
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !connected {
		return setupCheck{"failed", "No matching runner is connected. Check the selected execution target and retry."}
	}
	id, err := randomID("run", 12)
	if err != nil {
		return setupCheck{"failed", err.Error()}
	}
	token, err := randomID("lease", 24)
	if err != nil {
		return setupCheck{"failed", err.Error()}
	}
	h := &setupHealth{spec: protocol.RunSpec{ID: id, Command: "connection-check", Executor: selection.Agent + "-review", Repository: selection.Repository, HealthCheck: true, GitHubRepository: selection.Repository, RenderedPrompt: "Reply exactly PONG. Do not use tools, read files, or change anything.", TimeoutMillis: 45000, LeaseToken: token}, done: make(chan struct{})}
	if selection.Host == "kubernetes" {
		h.runtime = "kubernetes"
	}
	o.mu.Lock()
	if o.health != nil {
		o.mu.Unlock()
		return setupCheck{"failed", "A runner check is already running."}
	}
	o.health = h
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		if o.health == h {
			o.health = nil
		}
		o.mu.Unlock()
		o.server.deleteLease(id)
	}()
	select {
	case <-h.done:
		return h.result
	case <-ctx.Done():
		return setupCheck{"failed", "The runner connection check timed out. Check the selected target and its agent authentication, then retry."}
	}
}
func (o *onboarding) claimHealth(p protocol.PollRequest) *protocol.RunSpec {
	// The factory poll holds server.mu; health work consumes the same sandbox
	// capacity as jobs. Compute usage before taking onboarding.mu.
	usage := o.server.admissionUsage(context.Background())
	o.mu.Lock()
	defer o.mu.Unlock()
	h := o.health
	if h != nil && h.runtime != "" && p.Runtime != h.runtime {
		return nil
	}
	if h == nil || !p.HealthChecks || !slices.Contains(p.Repositories, h.spec.Repository) || !slices.Contains(p.ReadOnlyExecutors, h.spec.Executor) {
		return nil
	}
	if h.instance != "" && h.instance != p.InstanceID {
		return nil
	}
	if usage.workers[p.Name] || (o.server.maxConcurrent > 0 && usage.total >= o.server.maxConcurrent) || usage.uncertainRepositories[h.spec.Repository] || (o.server.maxPerRepository > 0 && usage.repositories[h.spec.Repository] >= o.server.maxPerRepository) {
		return nil
	}
	for id, worker := range o.server.liveWorkers() {
		if id != p.InstanceID && worker.Name == p.Name && time.Since(worker.LastSeenAt) < workerAvailabilityWindow {
			return nil
		}
	}
	h.instance = p.InstanceID
	o.server.liveMu.Lock()
	o.server.leases[h.spec.ID] = &githubLease{spec: h.spec, instance: p.InstanceID, last: time.Now()}
	o.server.liveMu.Unlock()
	spec := h.spec
	return &spec
}
func (o *onboarding) finishHealth(id string, p protocol.Completion) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	h := o.health
	if h == nil || h.spec.ID != id || h.instance != p.InstanceID || h.spec.LeaseToken != p.LeaseToken {
		return false
	}
	select {
	case <-h.done:
		return true
	default:
	}
	var result runner.Result
	if p.State == "succeeded" && p.ExitCode == 0 && json.Unmarshal(p.Result, &result) == nil && strings.TrimSpace(result.FinalMessage) == "PONG" {
		h.result = setupCheck{"passed", "PONG. The runner verified its repository, GitHub connection, and agent. No issue was created."}
	} else {
		message := p.Error
		if message == "" {
			message = "The runner did not return PONG. Check its CLI sign-in and permissions."
		}
		h.result = setupCheck{"failed", message}
	}
	close(h.done)
	return true
}
func (s *GitHubFactoryServer) SetLocalWorkerFactory(factory LocalWorkerFactory) {
	if s.setup != nil {
		s.setup.mu.Lock()
		s.setup.workerFactory = factory
		s.setup.mu.Unlock()
	}
}
func (o *onboarding) listening(address net.Addr) {
	o.mu.Lock()
	o.baseURL = "http://" + address.String()
	o.mu.Unlock()
	state := o.snapshot()
	if state.Finished {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := o.loadRepositories(ctx); err != nil {
				o.mu.Lock()
				o.settings.Finished = false
				o.settings.Check = setupCheck{"failed", "GitHub access needs to be reconnected."}
				_ = o.saveLocked()
				o.mu.Unlock()
			} else if state.Selection.Host == "local" || state.Selection.Host == "kubernetes" {
				if err := o.startWorker(); err != nil {
					o.mu.Lock()
					o.settings.Finished = false
					o.settings.Check = setupCheck{"failed", err.Error()}
					_ = o.saveLocked()
					o.mu.Unlock()
				}
			}
		}()
	}
}
func (s *GitHubFactoryServer) onboardingWorkerConfig(w http.ResponseWriter, r *http.Request) {
	if s.setup == nil {
		writeError(w, 404, errors.New("onboarding unavailable"))
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		writeError(w, 403, errors.New("download worker setup from Machinist"))
		return
	}
	selection := s.setup.snapshot().Selection
	if selection.Host != "remote" || selection.Repository == "" {
		writeError(w, 400, errors.New("select a repository and remote VM first"))
		return
	}
	repositories := map[string]config.Repository{}
	for repo := range s.setup.snapshot().projectPaths() {
		repositories[repo] = config.Repository{Path: "./repositories/" + repo}
	}
	cfg := config.Worker{Name: "machinist-vm", DataDirectory: "./machinist-worker", ControlPlane: config.ControlPlane{URL: "http://127.0.0.1:17341", TokenFile: "./worker.token"}, Executors: setupExecutors(selection.Agent), Repositories: repositories}
	raw, err := toml.Marshal(cfg)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "#!/bin/sh\nset -eu\numask 077\ncommand -v machinist >/dev/null\ncommand -v gh >/dev/null\ncommand -v " + selection.Agent + " >/dev/null\ngh auth status\nmkdir -p machinist-vm\ncd machinist-vm\nprintf '%s' " + q(s.auth.workerToken) + " > worker.token\ncat > worker.toml <<'MACHINIST_CONFIG'\n" + string(raw) + "MACHINIST_CONFIG\n"
	for _, repo := range slices.Sorted(maps.Keys(repositories)) {
		path := repositories[repo].Path
		script += "mkdir -p " + q(filepath.Dir(path)) + "\nif [ ! -d " + q(path+"/.git") + " ]; then gh repo clone " + q(repo) + " " + q(path) + "; fi\n"
	}
	script += "exec machinist worker start --config worker.toml\n"
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="machinist-worker-setup.sh"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(script))
}
