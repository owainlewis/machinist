package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

type attemptHandle struct {
	context context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	// heartbeatDone is closed after the heartbeat goroutine has stopped all
	// manifest writes. doneOnce lets terminal handoff join it before runAttempt
	// performs its final deferred cleanup.
	heartbeatDone chan struct{}
	doneOnce      sync.Once

	mutex         sync.Mutex
	reason        string
	expiry        time.Time
	supervisor    *supervisorProcess
	manifestReady bool
}

func (manager *Manager) runAttempt(parent context.Context, claim protocol.Claim, token string) {
	attemptContext, cancel := context.WithCancel(parent)
	handle := &attemptHandle{
		context: attemptContext, cancel: cancel, done: make(chan struct{}),
		heartbeatDone: make(chan struct{}), expiry: claim.Attempt.LeaseExpiresAt,
	}
	manager.stateMutex.Lock()
	manager.active[claim.Attempt.ID] = handle
	manager.stateMutex.Unlock()
	if parent.Err() != nil {
		handle.stop("cancelled")
	}
	defer func() {
		handle.stopHeartbeat()
		cancel()
		manager.stateMutex.Lock()
		delete(manager.active, claim.Attempt.ID)
		manager.stateMutex.Unlock()
	}()
	go manager.heartbeatAttempt(handle, claim.Attempt.ID, token)

	sessionDeadline := claim.Attempt.CreatedAt.Add(time.Duration(claim.Session.TimeoutSeconds) * time.Second)
	sessionTimer := time.AfterFunc(time.Until(sessionDeadline), func() {
		handle.stop("timeout")
	})
	defer sessionTimer.Stop()
	if err := manager.validateClaim(claim); err != nil {
		manager.finishWithoutWorktree(claim, token, handle, "failed", err)
		return
	}
	repository, err := manager.repositoryForClaim(handle.context, claim)
	if err != nil {
		manager.finishWithoutWorktree(claim, token, handle, terminalForStop(handle), stoppedAttemptError(handle, err))
		return
	}
	repositoryKey := repositoryCoordinationKey(repository)
	releaseRepository, err := manager.repositoryLocks.acquire(handle.context, repositoryKey)
	if err != nil {
		manager.finishWithoutWorktree(claim, token, handle, terminalForStop(handle), stoppedAttemptError(handle, err))
		return
	}
	worktreeRoot := filepath.Join(manager.dataDirectory, "worktrees")
	value, err := prepareWorktree(handle.context, manager.options.GitExecutable, worktreeRoot,
		repository, claim.Session.ID, claim.Attempt.ID)
	if err != nil {
		releaseRepository()
		manager.finishWithoutWorktree(claim, token, handle, terminalForStop(handle), stoppedAttemptError(handle, err))
		return
	}
	manifest := attemptManifest{
		SessionID: claim.Session.ID, ExecutionID: claim.Execution.ID, AttemptID: claim.Attempt.ID,
		RepositoryID: claim.Repository.ID, RepositoryKey: repository.Key,
		RepositoryPath: repository.Path, RemoteIdentity: repository.RemoteIdentity,
		BaseBranch: value.BaseBranch, BaseCommit: value.BaseCommit,
		WorktreePath: value.Path, Branch: value.Branch,
		LeaseDeadline: claim.Attempt.LeaseExpiresAt, Lifecycle: manifestPreparing,
	}
	if err := manager.manifests.create(manifest); err != nil {
		releaseRepository()
		manager.markUnhealthy("manifest_write", err)
		manager.finishWithoutWorktree(claim, token, handle, "failed", err)
		return
	}
	handle.setManifestReady()
	if err := addPreparedWorktree(handle.context, manager.options.GitExecutable, repository, value); err != nil {
		state := "failed"
		if handle.stopReason() == "cancelled" {
			state = "cancelled"
		}
		err = stoppedAttemptError(handle, err)
		persisted, loadErr := manager.manifests.load(claim.Attempt.ID)
		inspectionContext, cancelInspection := context.WithTimeout(context.Background(), 2*gitCommandTimeout)
		inspection, inspectErr := inspectManifestWorktree(
			inspectionContext, manager.options.GitExecutable, manager.dataDirectory, persisted)
		cancelInspection()
		if loadErr != nil || inspectErr != nil {
			identityErr := errors.Join(loadErr, inspectErr)
			_ = manager.persistLifecycle(claim.Attempt.ID, manifestInconsistent, func(manifest *attemptManifest) {
				manifest.RetentionReason = boundedText(identityErr.Error(), 1000)
			})
			manager.markUnhealthy("worktree_identity", identityErr)
			manager.repositoryLocks.poison(repositoryKey, identityErr)
			releaseRepository()
			manager.complete(claim.Attempt.ID, token, state, "", err.Error(), handle)
			return
		}
		if inspection.PathExists && inspection.Registered {
			releaseRepository()
			manager.finishWithWorktree(claim, token, handle, repository, value, state, "", err.Error())
			return
		}
		if inspection.PathExists || inspection.Registered {
			identityErr := errors.New("worktree creation left partial filesystem or Git registration state")
			_ = manager.persistLifecycle(claim.Attempt.ID, manifestInconsistent, func(manifest *attemptManifest) {
				manifest.RetentionReason = identityErr.Error()
			})
			manager.markUnhealthy("worktree_identity", identityErr)
			manager.repositoryLocks.poison(repositoryKey, identityErr)
			releaseRepository()
			manager.complete(claim.Attempt.ID, token, state, "", err.Error(), handle)
			return
		}
		releaseRepository()
		manager.finishWithoutWorktree(claim, token, handle, state, err)
		return
	}
	releaseRepository()
	if err := manager.persistLifecycle(claim.Attempt.ID, manifestWorktreeCreated, nil); err != nil {
		manager.markUnhealthy("manifest_write", err)
		manager.finishWithWorktree(claim, token, handle, repository, value, "failed", "", err.Error())
		return
	}
	path, err := resultPath(manager.dataDirectory, claim.Attempt.ID)
	if err != nil {
		manager.finishWithWorktree(claim, token, handle, repository, value, "failed", "", err.Error())
		return
	}
	defer os.Remove(path)
	sender := newEventSender(handle.context, manager.client, claim.Attempt.ID, token, claim.Execution.RequiredRuntime)
	stages := claim.Session.Stages
	if len(stages) == 0 {
		stages = []protocol.StageRun{{Position: 0, Name: "Do the task", Prompt: claim.Session.Prompt}}
	}
	lastResult := ""
	for index, stage := range stages {
		prompt := buildStagePrompt(claim, value, stage)
		if len([]byte(value.Branch)) > protocol.MaxAgentBranchBytes ||
			len([]byte(value.BaseBranch)) > protocol.MaxAgentBranchBytes ||
			len([]byte(prompt)) > protocol.MaxAgentPromptBytes {
			sender.closeAndWait(5 * time.Second)
			err := errors.New("worktree branch metadata makes the agent prompt exceed its 72 KiB bound")
			manager.finishWithWorktree(claim, token, handle, repository, value, "failed", "", err.Error())
			return
		}
		process, err := startSupervisor(manager.options.SupervisorCommand, supervisorInit{
			Runtime: claim.Execution.RequiredRuntime, RuntimeExecutable: manager.runtimeExecutable(claim.Execution.RequiredRuntime),
			Worktree: value.Path, ResultPath: path, Prompt: prompt,
			TimeoutSeconds: remainingTimeoutSeconds(sessionDeadline), RunID: claim.Session.RunID, SessionID: claim.Session.ID,
		}, os.Stderr)
		if err != nil {
			sender.closeAndWait(5 * time.Second)
			manager.finishWithWorktree(claim, token, handle, repository, value, "failed", "", err.Error())
			return
		}
		if err := process.awaitReady(handle.context); err != nil {
			_ = process.closeControl()
			sender.closeAndWait(5 * time.Second)
			manager.finishWithWorktree(claim, token, handle, repository, value,
				terminalForStop(handle), "", stoppedAttemptError(handle, err).Error())
			return
		}
		if _, err := manager.manifests.update(claim.Attempt.ID, func(manifest *attemptManifest) error {
			manifest.SupervisorPID = process.supervisorPID
			manifest.SupervisorIdentity = process.supervisorIdentity
			manifest.ProcessGroupID = process.processGroupID
			manifest.ProcessGroupIdentity = process.groupIdentity
			manifest.ProcessActive = true
			manifest.LeaseDeadline = handle.leaseExpiry()
			manifest.Lifecycle = manifestSupervisorReady
			return nil
		}); err != nil {
			manager.emergencyStop(process, err)
			manager.markUnhealthy("manifest_write", err)
			sender.closeAndWait(5 * time.Second)
			manager.finishWithWorktree(claim, token, handle, repository, value, "failed", "", err.Error())
			return
		}
		handle.setSupervisor(process)
		if index == 0 {
			started, err := manager.client.start(handle.context, claim.Attempt.ID, supervisorStartRequest(process, token))
			if err != nil {
				reason := handle.stopReason()
				var apiError *APIError
				if errors.As(err, &apiError) && apiError.Code == "lease_not_owner" {
					reason = "lease_lost"
				}
				if reason == "" {
					reason = "failed"
				}
				handle.stop(reason)
				message := manager.waitForSupervisor(process)
				sender.closeAndWait(5 * time.Second)
				errorText := err.Error()
				if reason == "timeout" {
					errorText = "Session timeout reached"
				}
				manager.finishWithWorktree(claim, token, handle, repository, value,
					terminalState(message), message.Result, firstNonEmpty(errorText, message.Error))
				return
			}
			if started.State != "running" {
				handle.stop("lease_lost")
				message := manager.waitForSupervisor(process)
				sender.closeAndWait(5 * time.Second)
				manager.finishWithWorktree(claim, token, handle, repository, value, "failed", message.Result,
					"control plane did not accept the running transition")
				return
			}
			manager.logger.Info("attempt_started", "attempt_id", claim.Attempt.ID, "repository", repository.Key,
				"process", processSummary(process))
		}
		_, err = manager.client.startStage(handle.context, claim.Attempt.ID, stage.Position, stageStartRequest(process, token))
		if err != nil {
			reason := stageStartFailureReason(handle.stopReason(), err)
			handle.stop(reason)
			message := manager.waitForSupervisor(process)
			sender.closeAndWait(5 * time.Second)
			manager.finishWithWorktree(claim, token, handle, repository, value, terminalState(message), message.Result, err.Error())
			return
		}
		if err := manager.persistLifecycle(claim.Attempt.ID, manifestRunning, nil); err != nil {
			handle.stop("failed")
			manager.emergencyStop(process, err)
			manager.markUnhealthy("manifest_write", err)
			sender.closeAndWait(5 * time.Second)
			manager.finishWithWorktree(claim, token, handle, repository, value, "failed", "", err.Error())
			return
		}
		if err := process.send("start"); err != nil {
			manager.emergencyStop(process, err)
			sender.closeAndWait(5 * time.Second)
			manager.finishWithWorktree(claim, token, handle, repository, value, "failed", "", err.Error())
			return
		}
		manager.logger.Info("pipeline_stage_started", "attempt_id", claim.Attempt.ID, "stage", stage.Name, "position", stage.Position)
		message := manager.waitForSupervisorWithEvents(process, sender)
		stageState := protocol.StageSucceeded
		if terminalState(message) == "cancelled" {
			stageState = protocol.StageCancelled
		} else if terminalState(message) != "succeeded" {
			stageState = protocol.StageFailed
		}
		_, completeErr := manager.client.completeStage(handle.context, claim.Attempt.ID, stage.Position, protocol.CompleteStageRequest{
			LeaseToken: token, State: stageState, Result: message.Result, Error: message.Error,
		})
		if completeErr != nil || stageState != protocol.StageSucceeded {
			var apiError *APIError
			if errors.As(completeErr, &apiError) && apiError.Code == "lease_not_owner" {
				handle.stop("lease_lost")
			}
			sender.closeAndWait(5 * time.Second)
			attemptState := terminalState(message)
			if completeErr != nil && attemptState == "succeeded" {
				attemptState = "failed"
			}
			manager.finishWithWorktree(claim, token, handle, repository, value,
				attemptState, message.Result, firstNonEmpty(errorString(completeErr), message.Error))
			return
		}
		lastResult = message.Result
		manager.logger.Info("pipeline_stage_completed", "attempt_id", claim.Attempt.ID, "stage", stage.Name, "position", stage.Position)
	}
	sender.closeAndWait(5 * time.Second)
	manager.finishWithWorktree(claim, token, handle, repository, value, "succeeded", lastResult, "")
}

func (manager *Manager) validateClaim(claim protocol.Claim) error {
	if !uuidPattern.MatchString(claim.Attempt.ID) || !uuidPattern.MatchString(claim.Session.ID) ||
		!uuidPattern.MatchString(claim.Session.RunID) {
		return errors.New("claim contains invalid IDs")
	}
	if claim.Attempt.WorkerID != manager.id || claim.Execution.AssignedWorkerID != manager.id {
		return errors.New("claim is assigned to a different worker")
	}
	if !manager.supportsRuntime(claim.Execution.RequiredRuntime) {
		return errors.New("claim requires a runtime that is not ready on this worker")
	}
	if claim.Session.RepositoryID != claim.Repository.ID {
		return errors.New("claim repository IDs do not match")
	}
	if claim.Session.TimeoutSeconds < 1 || claim.Session.TimeoutSeconds > int(protocol.MaxTimeout/time.Second) {
		return errors.New("claim timeout is outside the supported range")
	}
	if len(claim.Session.Stages) == 0 {
		if !protocol.AgentPromptFits(claim.Session.TaskName, claim.Repository.RemoteIdentity, claim.Session.Prompt) {
			return errors.New("claim agent prompt exceeds 72 KiB")
		}
		return nil
	}
	if len(claim.Session.Stages) > protocol.MaxPipelineStages {
		return errors.New("claim Pipeline must contain 1 through 20 stages")
	}
	for _, stage := range claim.Session.Stages {
		if !protocol.AgentPromptFits(claim.Session.TaskName, claim.Repository.RemoteIdentity, stage.Prompt) {
			return errors.New("claim Pipeline stage prompt exceeds 72 KiB")
		}
	}
	return nil
}

func (manager *Manager) supportsRuntime(runtime string) bool {
	if manager.runtimeExecutable(runtime) == "" {
		return false
	}
	manager.stateMutex.Lock()
	defer manager.stateMutex.Unlock()
	if len(manager.health.Capabilities) == 0 {
		return runtime == manager.config.Runtime
	}
	return capabilityReady(manager.health.Capabilities, protocol.CapabilityKindRuntime, runtime)
}

func (manager *Manager) heartbeatAttempt(handle *attemptHandle, attemptID, token string) {
	defer close(handle.heartbeatDone)
	delay := manager.options.LeaseRenewInterval
	for {
		timer := time.NewTimer(delay)
		select {
		case <-handle.done:
			timer.Stop()
			return
		case <-timer.C:
		}
		requestContext, cancel := context.WithTimeout(context.Background(), requestTimeout)
		heartbeat, err := manager.client.heartbeat(requestContext, attemptID, token)
		cancel()
		if err == nil {
			if handle.hasManifest() {
				if _, persistErr := manager.manifests.update(attemptID, func(manifest *attemptManifest) error {
					manifest.LeaseDeadline = heartbeat.LeaseExpiresAt
					return nil
				}); persistErr != nil {
					manager.markUnhealthy("manifest_write", persistErr)
					handle.stop("failed")
					return
				}
			}
			handle.updateExpiry(heartbeat.LeaseExpiresAt)
			if heartbeat.CancellationRequested {
				handle.stop("cancelled")
				return
			}
			delay = manager.options.LeaseRenewInterval
			continue
		}
		var apiError *APIError
		if errors.As(err, &apiError) && apiError.Code == "lease_not_owner" {
			handle.stop("lease_lost")
			return
		}
		if time.Now().After(handle.leaseExpiry()) {
			handle.stop("lease_lost")
			return
		}
		delay = manager.options.LeaseRetryInterval
	}
}

func (handle *attemptHandle) stopHeartbeat() {
	if handle.done == nil {
		return
	}
	handle.doneOnce.Do(func() {
		close(handle.done)
	})
	if handle.heartbeatDone != nil {
		<-handle.heartbeatDone
	}
}

func (handle *attemptHandle) setSupervisor(process *supervisorProcess) {
	handle.mutex.Lock()
	handle.supervisor = process
	reason := handle.reason
	expiry := handle.expiry
	handle.mutex.Unlock()
	if reason != "" {
		_ = process.send(stopCommand(reason))
	} else {
		_ = process.renew(expiry)
	}
}

func (handle *attemptHandle) setManifestReady() {
	handle.mutex.Lock()
	handle.manifestReady = true
	handle.mutex.Unlock()
}

func (handle *attemptHandle) hasManifest() bool {
	handle.mutex.Lock()
	defer handle.mutex.Unlock()
	return handle.manifestReady
}

func (handle *attemptHandle) updateExpiry(expiry time.Time) {
	handle.mutex.Lock()
	handle.expiry = expiry
	process := handle.supervisor
	handle.mutex.Unlock()
	if process != nil {
		_ = process.renew(expiry)
	}
}

func (handle *attemptHandle) leaseExpiry() time.Time {
	handle.mutex.Lock()
	defer handle.mutex.Unlock()
	return handle.expiry
}

func (handle *attemptHandle) stop(reason string) {
	handle.mutex.Lock()
	if handle.reason != "" {
		handle.mutex.Unlock()
		return
	}
	handle.reason = reason
	process := handle.supervisor
	handle.mutex.Unlock()
	handle.cancel()
	if process != nil {
		_ = process.send(stopCommand(reason))
	}
}

func (handle *attemptHandle) stopReason() string {
	handle.mutex.Lock()
	defer handle.mutex.Unlock()
	return handle.reason
}

func (manager *Manager) waitForSupervisorWithEvents(
	process *supervisorProcess,
	sender *eventSender,
) supervisorMessage {
	for {
		message := manager.waitForSupervisorMessage(process)
		if message.Type == "output" {
			sender.enqueue(message.Stream, message.Text, message.Truncated)
			continue
		}
		return message
	}
}

func (manager *Manager) waitForSupervisor(process *supervisorProcess) supervisorMessage {
	for {
		message := manager.waitForSupervisorMessage(process)
		if message.Type != "output" {
			return message
		}
	}
}

func (manager *Manager) waitForSupervisorMessage(process *supervisorProcess) supervisorMessage {
	for {
		select {
		case message := <-process.messages:
			if message.Type == "exit" || message.Type == "output" {
				if message.Type == "exit" {
					_ = process.closeControl()
					process.markStopped()
				}
				return message
			}
		case err := <-process.decodeErrors:
			select {
			case message := <-process.messages:
				if message.Type == "exit" || message.Type == "output" {
					if message.Type == "exit" {
						process.markStopped()
					}
					return message
				}
			default:
			}
			manager.emergencyStop(process, err)
			manager.markUnhealthy("attempt_supervisor", err)
			return supervisorMessage{
				Type:     "exit",
				Reason:   "supervisor_error",
				ExitCode: -1,
				Error:    "attempt supervisor output ended unexpectedly",
			}
		}
	}
}

func (manager *Manager) emergencyStop(process *supervisorProcess, cause error) {
	_ = process.closeControl()
	if process.processGroupID > 0 && process.groupIdentity != "" {
		if err := stopOwnedProcessGroup(int(process.processGroupID), process.groupIdentity, terminationGrace); err != nil {
			manager.markUnhealthy("process_group_stop", errors.Join(cause, err))
			return
		}
		process.markStopped()
	}
}

func (manager *Manager) finishWithoutWorktree(
	claim protocol.Claim,
	token string,
	handle *attemptHandle,
	state string,
	cause error,
) {
	manager.beginCapacityHandoff()
	defer manager.finishCapacityHandoff(handle)

	errorText := ""
	if cause != nil {
		errorText = boundedText(cause.Error(), protocol.MaxErrorBytes)
	}
	if err := manager.recordDisposed(claim.Attempt.ID); err != nil {
		manager.markUnhealthy("disposal_journal", err)
		return
	}
	if _, err := manager.manifests.load(claim.Attempt.ID); err == nil {
		if persistErr := manager.persistLifecycle(claim.Attempt.ID, manifestNotCreated, func(manifest *attemptManifest) {
			manifest.TerminalState = state
			manifest.RetentionReason = ""
			manifest.ProcessActive = false
		}); persistErr != nil {
			manager.markUnhealthy("manifest_write", persistErr)
		}
	}
	manager.complete(claim.Attempt.ID, token, state, "", errorText, handle)
}

func (manager *Manager) finishWithWorktree(
	claim protocol.Claim,
	token string,
	handle *attemptHandle,
	repository Repository,
	value worktree,
	state string,
	result string,
	errorText string,
) {
	manager.beginCapacityHandoff()
	defer manager.finishCapacityHandoff(handle)

	result = boundedText(result, protocol.MaxResultBytes)
	errorText = boundedText(errorText, protocol.MaxErrorBytes)
	if err := manager.persistLifecycle(claim.Attempt.ID, manifestCompleted, func(manifest *attemptManifest) {
		manifest.TerminalState = state
		manifest.ProcessActive = handle.processStillActive()
	}); err != nil {
		manager.markUnhealthy("manifest_write", err)
		errorText = firstNonEmpty(errorText, err.Error())
	}
	completed := manager.complete(claim.Attempt.ID, token, state, result, errorText, handle)
	if completed && state == "succeeded" {
		err := manager.cleanCompletedWorktree(claim.Attempt.ID)
		if err == nil {
			if err := manager.recordDisposed(claim.Attempt.ID); err != nil {
				manager.markUnhealthy("disposal_journal", err)
			}
			manager.logger.Info(
				"attempt_worktree_cleaned",
				"attempt_id", claim.Attempt.ID,
				"repository", repository.Key,
			)
			return
		}
		if manifest, loadErr := manager.manifests.load(claim.Attempt.ID); loadErr == nil &&
			manifest.Lifecycle == manifestCleanupStarted {
			manager.markUnhealthy("worktree_cleanup", err)
			return
		}
		errorText = err.Error()
	}
	reason := firstNonEmpty(errorText, state+" attempt retained for inspection")
	manager.retain(claim, repository, value, reason)
}

func (manager *Manager) registerAfterAttempt(handle *attemptHandle) {
	handle.stopHeartbeat()
	registerContext, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	manager.registerLocked(registerContext)
}

func (manager *Manager) beginCapacityHandoff() {
	manager.registrationMutex.Lock()
	manager.capacityHandoffs++
	manager.registrationMutex.Unlock()
}

func (manager *Manager) finishCapacityHandoff(handle *attemptHandle) {
	handle.stopHeartbeat()
	manager.registrationMutex.Lock()
	defer manager.registrationMutex.Unlock()
	manager.capacityHandoffs--
	if manager.capacityHandoffs == 0 {
		manager.registerAfterAttempt(handle)
	}
}

func (handle *attemptHandle) processStillActive() bool {
	handle.mutex.Lock()
	process := handle.supervisor
	handle.mutex.Unlock()
	return process != nil && !process.isStopped()
}

func (manager *Manager) complete(
	attemptID string,
	token string,
	state string,
	result string,
	errorText string,
	handle *attemptHandle,
) bool {
	timeout := requestTimeout
	if remaining := time.Until(handle.leaseExpiry()); remaining > 0 && remaining < timeout {
		timeout = remaining
	}
	if timeout <= 0 {
		manager.logger.Warn(
			"attempt_completion_not_recorded",
			"attempt_id", attemptID,
			"error_class", "lease_expired",
		)
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, err := manager.client.complete(ctx, attemptID, protocol.CompleteAttemptRequest{
		LeaseToken: token, State: state, Result: result, Error: errorText,
	})
	if err != nil {
		manager.logger.Warn(
			"attempt_completion_not_recorded",
			"attempt_id", attemptID,
			"error_class", apiErrorClass(err),
		)
		return false
	}
	manager.logger.Info("attempt_completed", "attempt_id", attemptID, "state", state)
	return true
}

func (manager *Manager) retain(claim protocol.Claim, repository Repository, value worktree, reason string) {
	manifest, err := manager.manifests.load(claim.Attempt.ID)
	if err != nil {
		manager.markUnhealthy("manifest_read", err)
		return
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), repositoryAcquisitionTimeout)
	releaseRepository, lockErr := manager.repositoryLocks.acquire(
		waitContext, manager.coordinationKeyForManifest(manifest),
	)
	cancelWait()
	if lockErr != nil {
		manager.markUnhealthy("worktree_identity", lockErr)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*gitCommandTimeout)
	inspection, inspectErr := inspectManifestWorktree(
		ctx,
		manager.options.GitExecutable,
		manager.dataDirectory,
		manifest,
	)
	releaseRepository()
	cancel()
	if inspectErr != nil || !inspection.PathExists || !inspection.Registered {
		if inspectErr == nil {
			inspectErr = errors.New("worktree exists in only one of the filesystem and Git worktree registry")
		}
		_ = manager.persistLifecycle(claim.Attempt.ID, manifestInconsistent, func(value *attemptManifest) {
			value.RetentionReason = boundedText(inspectErr.Error(), 1000)
		})
		manager.markUnhealthy("worktree_identity", inspectErr)
		return
	}
	updated, err := manager.manifests.update(claim.Attempt.ID, func(manifest *attemptManifest) error {
		manifest.Lifecycle = manifestRetained
		manifest.RetentionReason = boundedText(reason, 1000)
		return nil
	})
	if err != nil {
		manager.markUnhealthy("manifest_write", err)
		return
	}
	manager.recordRetained(updated)
	manager.logger.Info("attempt_worktree_retained", "attempt_id", claim.Attempt.ID, "repository", repository.Key)
}

func terminalForStop(handle *attemptHandle) string {
	if handle.stopReason() == "cancelled" {
		return "cancelled"
	}
	return "failed"
}

func stoppedAttemptError(handle *attemptHandle, fallback error) error {
	switch handle.stopReason() {
	case "cancelled":
		return errors.New("attempt cancelled")
	case "timeout":
		return errors.New("Session timeout reached")
	case "lease_lost":
		return errors.New("control-plane lease was lost")
	default:
		return fallback
	}
}

func terminalState(message supervisorMessage) string {
	if message.Reason == "cancelled" {
		return "cancelled"
	}
	if message.Reason == "exited" && message.ExitCode == 0 {
		return "succeeded"
	}
	return "failed"
}

func stageStartFailureReason(current string, err error) string {
	if current != "" {
		return current
	}
	var apiError *APIError
	if errors.As(err, &apiError) {
		switch apiError.Code {
		case "lease_not_owner":
			return "lease_lost"
		case "cancellation_requested":
			return "cancelled"
		}
	}
	return "failed"
}

func buildStagePrompt(claim protocol.Claim, value worktree, stage protocol.StageRun) string {
	return protocol.FormatAgentPrompt(
		claim.Session.TaskName,
		claim.Repository.RemoteIdentity,
		value.Branch,
		value.BaseBranch,
		stage.Prompt,
	)
}

func buildPrompt(claim protocol.Claim, value worktree) string {
	return buildStagePrompt(claim, value, protocol.StageRun{Prompt: claim.Session.Prompt})
}

func stageStartRequest(process *supervisorProcess, token string) protocol.StartStageRequest {
	return protocol.StartStageRequest{
		LeaseToken: token, SupervisorPID: &process.supervisorPID,
		ProcessIdentity: process.supervisorIdentity, ProcessGroupID: &process.processGroupID,
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func apiErrorClass(err error) string {
	var apiError *APIError
	if errors.As(err, &apiError) && apiError.Code != "" {
		return apiError.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "transport"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stopCommand(reason string) string {
	if reason == "cancelled" {
		return "cancel"
	}
	if reason == "failed" {
		return "fail"
	}
	return reason
}

func remainingTimeoutSeconds(deadline time.Time) int {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 1
	}
	seconds := int((remaining + time.Second - 1) / time.Second)
	maximum := int(protocol.MaxTimeout / time.Second)
	if seconds > maximum {
		return maximum
	}
	return seconds
}
