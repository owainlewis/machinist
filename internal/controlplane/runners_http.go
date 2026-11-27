package controlplane

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/owainlewis/machinist/internal/config"
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
	labels, err := config.NormalizeLabels(request.Labels)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("labels: %w", err))
		return
	}
	if err := s.store.RecordRunner(r.Context(), tokenName, request.Kind, request.Harnesses, labels, request.Slots, request.Probed); err != nil {
		writeRunError(w, err)
		return
	}
	run, err := s.engine.Assign(r.Context(), tokenName, request.Harnesses, labels, request.Probed)
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
			job.Agents = s.currentAgents(job.Preset, job.Step, job.Harness, job.Model)
		}
	}
	if queue.Waiting > 0 {
		specs, err := s.store.QueuedSpecs(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		queue.Blocked = s.blocked(runners, specs)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runners": runners, "queue": queue})
}

// blocked groups the queued runs that no online runner can take by the
// harnesses and host labels they wait for. specs counts the queued runs by
// preset and ticket-level overrides.
func (s *Server) blocked(runners []tickets.Runner, specs []tickets.QueuedSpec) []tickets.HarnessWait {
	online := s.onlineRunners(runners)
	byMissing := map[string]*tickets.HarnessWait{}
	for _, spec := range specs {
		missingH, missingL := waitingForFrom(s.state().factory, online, spec.Preset, spec.Harness, spec.Model, spec.RunsOn)
		if len(missingH) == 0 && len(missingL) == 0 {
			continue
		}
		key := strings.Join(missingH, ",") + "|" + strings.Join(missingL, ",")
		if byMissing[key] == nil {
			byMissing[key] = &tickets.HarnessWait{Harnesses: missingH, Labels: missingL}
		}
		byMissing[key].Waiting += spec.Waiting
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

// waitingForFrom names the harnesses and runs_on labels that keep every online
// runner from taking a job of this preset and ticket-level overrides. Both are
// empty when an online runner can take one, when no runner is online, or when
// the preset is gone, since such a job is claimed only to fail.
func waitingForFrom(loaded *factory.Factory, online []tickets.Runner, presetName, harnessName, model string, ticketRunsOn []string) (missingH, missingL []string) {
	if loaded == nil {
		return nil, nil
	}
	preset, ok := loaded.Preset(presetName)
	if !ok {
		return nil, nil
	}
	needsHarnesses := loaded.Needs(preset.WithOverride(harnessName, model))
	needsLabels := engine.EffectiveRunsOn(preset, ticketRunsOn)
	return missingRequirements(online, needsHarnesses, needsLabels)
}

// missingHarnesses names the harnesses in needs that the closest online runner
// lacks.
func missingHarnesses(online []tickets.Runner, needs []string) []string {
	missingH, _ := missingRequirements(online, needs, nil)
	return missingH
}

// missingRequirements names the harnesses and host labels that the closest
// online runner lacks, the fewest total missing items (preferring runners with
// fewer missing labels on a tie). Both are nil when an online runner satisfies
// every requirement, or when no runner is online. A runner that did not report
// its harnesses, such as a Kubernetes runner, may have any harness, but must
// still advertise every required runs_on label.
func missingRequirements(online []tickets.Runner, needsHarnesses, needsLabels []string) (fewestH, fewestL []string) {
	bestTotal := -1
	for _, runner := range online {
		var missingL []string
		for _, label := range needsLabels {
			if !slices.Contains(runner.Labels, label) {
				missingL = append(missingL, label)
			}
		}
		var missingH []string
		if runner.Probed {
			for _, name := range needsHarnesses {
				if !slices.Contains(runner.Harnesses, name) {
					missingH = append(missingH, name)
				}
			}
		}
		total := len(missingH) + len(missingL)
		if total == 0 {
			return nil, nil
		}
		if bestTotal < 0 || total < bestTotal || (total == bestTotal && len(missingL) < len(fewestL)) {
			bestTotal = total
			fewestH = missingH
			fewestL = missingL
		}
	}
	return fewestH, fewestL
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
func (s *Server) currentAgents(presetName, step, harnessName, model string) []protocol.Agent {
	loaded := s.state().factory
	if loaded == nil {
		return nil
	}
	preset, ok := loaded.Preset(presetName)
	if !ok {
		return nil
	}
	preset = preset.WithOverride(harnessName, model)
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
