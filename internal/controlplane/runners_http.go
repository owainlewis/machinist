package controlplane

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/owainlewis/machinist/internal/engine"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/tickets"
)

func (s *Server) poll(w http.ResponseWriter, r *http.Request, tokenName string) {
	var request protocol.PollRequest
	if !decodeJSON(w, r, maxRequestBytes, &request) {
		return
	}
	if err := s.store.RecordRunner(r.Context(), tokenName, request.Kind, request.Harnesses, request.Slots, request.Probed); err != nil {
		writeRunError(w, err)
		return
	}
	run, err := s.engine.Assign(r.Context(), tokenName, request.Harnesses, request.Probed)
	if err != nil {
		writeRunError(w, err)
		return
	}
	if run == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) listRunners(w http.ResponseWriter, r *http.Request, _ string) {
	runners, queue, err := s.store.Fleet(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for index := range runners {
		for jobIndex := range runners[index].Jobs {
			job := &runners[index].Jobs[jobIndex]
			job.Agents = s.currentAgents(job.Preset, job.Step)
		}
	}
	if queue.Waiting > 0 {
		presets, err := s.store.QueuedPresets(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		queue.Blocked = s.blocked(runners, presets)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runners": runners, "queue": queue})
}

// blocked groups the queued runs that no online runner can take by the
// harnesses they wait for. presets counts the queued runs of each preset.
func (s *Server) blocked(runners []tickets.Runner, presets map[string]int) []tickets.HarnessWait {
	online := s.onlineRunners(runners)
	byMissing := map[string]*tickets.HarnessWait{}
	for name, count := range presets {
		missing := s.waitingFor(online, name)
		if len(missing) == 0 {
			continue
		}
		key := strings.Join(missing, ",")
		if byMissing[key] == nil {
			byMissing[key] = &tickets.HarnessWait{Harnesses: missing}
		}
		byMissing[key].Waiting += count
	}
	keys := make([]string, 0, len(byMissing))
	for key := range byMissing {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	waits := make([]tickets.HarnessWait, 0, len(keys))
	for _, key := range keys {
		waits = append(waits, *byMissing[key])
	}
	return waits
}

// onlineRunners keeps the runners that made contact recently.
func (s *Server) onlineRunners(runners []tickets.Runner) []tickets.Runner {
	var online []tickets.Runner
	for _, runner := range runners {
		if s.now().Sub(runner.LastContact) < runnerWindow {
			online = append(online, runner)
		}
	}
	return online
}

// waitingFor names the harnesses that keep every online runner from taking a
// job of this preset. It is empty when an online runner can take one, when no
// runner is online, or when the preset is gone, since such a job is claimed
// only to fail.
func (s *Server) waitingFor(online []tickets.Runner, presetName string) []string {
	return waitingForFrom(s.state().factory, online, presetName)
}

func waitingForFrom(loaded *factory.Factory, online []tickets.Runner, presetName string) []string {
	if loaded == nil {
		return nil
	}
	preset, ok := loaded.Preset(presetName)
	if !ok {
		return nil
	}
	return missingHarnesses(online, loaded.Needs(preset))
}

// missingHarnesses names the harnesses in needs that the closest online runner
// lacks, the fewest any one runner is missing, so installing them there lets
// it take the job. It is empty when an online runner has them all, or when no
// runner is online. A runner that did not report its harnesses, such as a
// Kubernetes runner, may have any of them.
func missingHarnesses(online []tickets.Runner, needs []string) []string {
	var fewest []string
	for index, runner := range online {
		if !runner.Probed {
			return nil
		}
		var missing []string
		for _, name := range needs {
			if !slices.Contains(runner.Harnesses, name) {
				missing = append(missing, name)
			}
		}
		if len(missing) == 0 {
			return nil
		}
		if index == 0 || len(missing) < len(fewest) {
			fewest = missing
		}
	}
	return fewest
}

// removeRunner forgets a runner that is offline and holds no running job, so
// a machine taken out of service stops counting in the fleet.
func (s *Server) removeRunner(w http.ResponseWriter, r *http.Request, tokenName string) {
	name := r.PathValue("name")
	err := s.store.RemoveRunner(r.Context(), name, s.now().Add(-runnerWindow))
	switch {
	case errors.Is(err, tickets.ErrNotFound):
		writeError(w, http.StatusNotFound, fmt.Errorf("no runner is named %q", name))
	case errors.Is(err, tickets.ErrRunnerBusy):
		writeError(w, http.StatusConflict, fmt.Errorf("runner %q has running jobs, so it cannot be removed until they end", name))
	case errors.Is(err, tickets.ErrRunnerOnline):
		writeError(w, http.StatusConflict, fmt.Errorf("runner %q is online, so it cannot be removed; stop it first", name))
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
	default:
		fmt.Fprintf(s.log, "machinist: %s removed runner %q\n", tokenName, name)
		s.listRunners(w, r, tokenName)
	}
}

// ownsRun checks that the run was given to this runner, so one runner cannot
// report on, or stop, another's work.
func (s *Server) ownsRun(w http.ResponseWriter, r *http.Request, tokenName string) bool {
	run, err := s.store.Run(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRunError(w, err)
		return false
	}
	if run.Runner != tokenName {
		writeError(w, http.StatusForbidden, fmt.Errorf("run %s belongs to another runner", run.ID))
		return false
	}
	return true
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request, tokenName string) {
	if !s.ownsRun(w, r, tokenName) {
		return
	}
	if err := s.store.TouchRunner(r.Context(), tokenName); err != nil {
		writeRunError(w, err)
		return
	}
	cancel, err := s.engine.Heartbeat(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRunError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.HeartbeatResponse{Cancel: cancel})
}

// runToken gives a runner a new GitHub token for one of its own active runs.
func (s *Server) runToken(w http.ResponseWriter, r *http.Request, tokenName string) {
	if !s.ownsRun(w, r, tokenName) {
		return
	}
	token, err := s.engine.RunToken(r.Context(), r.PathValue("id"))
	if errors.Is(err, engine.ErrNoGitHubApp) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeRunError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.GitHubToken{Token: token})
}

func (s *Server) ingestEvents(w http.ResponseWriter, r *http.Request, tokenName string) {
	if !s.ownsRun(w, r, tokenName) {
		return
	}
	var batch protocol.RunEventBatch
	if !decodeJSON(w, r, protocol.MaxRunEventBatchBytes, &batch) {
		return
	}
	cursor, err := s.store.IngestRunEvents(r.Context(), r.PathValue("id"), batch.Events)
	if err != nil {
		if errors.Is(err, tickets.ErrRunNotActive) || errors.Is(err, tickets.ErrNotFound) {
			writeRunError(w, err)
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.RunEventPage{Cursor: cursor})
}

func (s *Server) complete(w http.ResponseWriter, r *http.Request, tokenName string) {
	if !s.ownsRun(w, r, tokenName) {
		return
	}
	var completion protocol.Completion
	// A completion can carry a saved patch, base64 in JSON.
	if !decodeJSON(w, r, maxRequestBytes+2*protocol.MaxPatchBytes, &completion) {
		return
	}
	if err := s.engine.Complete(r.Context(), r.PathValue("id"), completion); err != nil {
		// The runner retries a failed completion until it succeeds, so the job
		// shows as running meanwhile. Say why here, where someone can fix it.
		if status := runErrorStatus(err); status >= http.StatusInternalServerError {
			fmt.Fprintf(s.log, "machinist: run %s finished, but recording it failed and the runner will retry: %v\n", r.PathValue("id"), err)
		}
		writeRunError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// currentAgents names the agents a running job's current step runs.
func (s *Server) currentAgents(presetName, step string) []protocol.Agent {
	loaded := s.state().factory
	if loaded == nil {
		return nil
	}
	preset, ok := loaded.Preset(presetName)
	if !ok {
		return nil
	}
	for _, candidate := range preset.Steps {
		if candidate.Name == step {
			return loaded.StepAgents(preset.EffectiveStep(candidate))
		}
	}
	return nil
}

func writeRunError(w http.ResponseWriter, err error) {
	writeError(w, runErrorStatus(err), err)
}

func runErrorStatus(err error) int {
	switch {
	case errors.Is(err, tickets.ErrRunNotActive):
		return http.StatusConflict
	case errors.Is(err, tickets.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, engine.ErrInvalidCompletion):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
