package controlplane

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"
)

// commitProject shares the existing setup and execution paths. It never changes
// the global agent/target when adding a project to a completed setup.
// configureMu is held by its caller; s.mu excludes new job claims during restart.
func (o *onboarding) commitProject(repo, path string, initial bool) error {
	s := o.server
	s.mu.Lock()
	if len(s.liveLeases()) != 0 {
		s.mu.Unlock()
		return errors.New("wait for running jobs and connection checks to finish before changing projects")
	}
	o.mu.Lock()
	checking := o.health != nil
	o.mu.Unlock()
	if checking {
		s.mu.Unlock()
		return errors.New("wait for the connection check to finish before changing projects")
	}
	previous := o.snapshot()
	if !initial && !previous.Finished {
		s.mu.Unlock()
		return errors.New("complete setup before adding another project")
	}
	o.stopWorker()
	o.mu.Lock()
	o.settings.Repositories = previous.projectPaths()
	o.settings.Repositories[repo] = path
	if initial {
		o.settings.Selection = setupSelection{Repository: repo, Path: path}
		o.settings.Check = setupCheck{}
		o.settings.Finished = false
	}
	err := o.saveLocked()
	if err != nil {
		o.settings = previous
	}
	o.mu.Unlock()
	if err == nil {
		for configured := range o.snapshot().projectPaths() {
			s.repositories[configured] = configured
		}
	}
	s.mu.Unlock()
	if err == nil && !initial && previous.Finished {
		err = o.startWorker()
	}
	if err == nil {
		return nil
	}
	// Restore both the persistent definition and the previous managed runner if
	// preparing the new configuration failed. Existing projects remain usable.
	s.mu.Lock()
	o.stopWorker()
	o.mu.Lock()
	o.settings = previous
	restoreErr := o.saveLocked()
	o.mu.Unlock()
	if _, existed := previous.projectPaths()[repo]; !existed {
		delete(s.repositories, repo)
	}
	s.mu.Unlock()
	workerErr := o.startWorker()
	if restoreErr != nil || workerErr != nil {
		o.mu.Lock()
		o.settings.Finished = false
		o.settings.Check = setupCheck{"failed", "Project update failed; verify the runner before resuming."}
		_ = o.saveLocked()
		o.mu.Unlock()
	}
	return errors.Join(fmt.Errorf("project update failed: %w", err), restoreErr, workerErr)
}

func (s *GitHubFactoryServer) addProject(w http.ResponseWriter, r *http.Request) {
	if s.setup == nil {
		writeError(w, 404, errors.New("complete GitHub setup before adding projects"))
		return
	}
	if !limitRequestBody(w, r, maxRequestBytes) {
		return
	}
	var input struct {
		Repository string `json:"repository"`
		Path       string `json:"path"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	o := s.setup
	o.configureMu.Lock()
	defer o.configureMu.Unlock()
	state := o.snapshot()
	if !state.Finished {
		writeError(w, 409, errors.New("complete setup before adding another project"))
		return
	}
	if path, exists := state.projectPaths()[input.Repository]; exists {
		if input.Path != "" && input.Path != path {
			writeError(w, 409, errors.New("project already uses a different checkout"))
			return
		}
		writeJSON(w, 200, map[string]any{"repository": input.Repository, "remote_setup_required": state.Selection.Host == "remote"})
		return
	}
	// Fail early rather than cloning a repository while running work needs this
	// runner. commitProject repeats the check before the atomic settings write.
	if len(s.liveLeases()) != 0 {
		writeError(w, 409, errors.New("wait for running jobs to finish before adding a project"))
		return
	}
	path, err := o.prepareRepository(r.Context(), input.Repository, input.Path)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	if err = o.commitProject(input.Repository, path, false); err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 201, map[string]any{"repository": input.Repository, "remote_setup_required": state.Selection.Host == "remote"})
}

// updateProjectDefaults changes submission metadata only: no checkout changes,
// worker restarts, or mutations of already admitted work.
func (s *GitHubFactoryServer) updateProjectDefaults(w http.ResponseWriter, r *http.Request) {
	if s.setup == nil {
		writeError(w, 404, errors.New("project settings unavailable"))
		return
	}
	if !limitRequestBody(w, r, maxRequestBytes) {
		return
	}
	var input struct {
		Repository string `json:"repository"`
		projectDefaults
	}
	if err := decodeJSON(r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	o := s.setup
	o.configureMu.Lock()
	defer o.configureMu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.settings.Finished {
		writeError(w, 409, errors.New("complete setup first"))
		return
	}
	if _, ok := o.settings.projectPaths()[input.Repository]; !ok {
		writeError(w, 404, errors.New("unknown project"))
		return
	}
	if err := validateProjectDefaults(input.projectDefaults, o.settings.Selection); err != nil {
		writeError(w, 400, err)
		return
	}
	input.Instructions = strings.TrimSpace(input.Instructions)
	previous := o.settings.ProjectDefaults
	next := make(map[string]projectDefaults, len(previous)+1)
	for key, value := range previous {
		next[key] = value
	}
	next[input.Repository] = input.projectDefaults
	o.settings.ProjectDefaults = next
	if err := o.saveLocked(); err != nil {
		o.settings.ProjectDefaults = previous
		writeError(w, 500, errors.New("could not save project defaults"))
		return
	}
	writeJSON(w, 200, input.projectDefaults)
}

func validateProjectDefaults(defaults projectDefaults, selection setupSelection) error {
	switch defaults.Recipe {
	case "", "factory", "single", "native-v1":
	default:
		return errors.New("unsupported default run mode")
	}
	if selection.Host == "kubernetes" && defaults.Recipe == "native-v1" {
		return errors.New("Kubernetes requires the pipeline or single agent mode")
	}
	if !utf8.ValidString(defaults.Instructions) || utf8.RuneCountInString(defaults.Instructions) > 2000 {
		return errors.New("default instructions must be at most 2000 characters")
	}
	return nil
}
