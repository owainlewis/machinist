package worker

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

const agentUpdatePath = "/update"

type agentUpdateServer struct {
	server     *http.Server
	listener   net.Listener
	socketPath string
	token      string
	closeOnce  sync.Once
}

type updateValidationError struct {
	code      string
	message   string
	retriable bool
	err       error
}

func (err *updateValidationError) Error() string {
	if err.err != nil {
		return err.message + ": " + err.err.Error()
	}
	return err.message
}

func (manager *Manager) startAgentUpdateServer(
	claim protocol.Claim,
	leaseToken string,
	handle *attemptHandle,
	repository Repository,
	value worktree,
) (*agentUpdateServer, error) {
	token, err := manager.randomSecret()
	if err != nil {
		return nil, fmt.Errorf("generate agent update token: %w", err)
	}
	directory := filepath.Join(manager.dataDirectory, "updates")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create agent update directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("agent update directory must be a real directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("protect agent update directory: %w", err)
	}
	socketPath := filepath.Join(directory, "u-"+strings.ReplaceAll(claim.Attempt.ID, "-", "")[:16]+".sock")
	if len(socketPath) >= 104 {
		return nil, errors.New("Worker data directory is too long for the agent update socket")
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale agent update socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on agent update socket: %w", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		listener.Close()
		_ = os.Remove(socketPath)
		return nil, fmt.Errorf("protect agent update socket: %w", err)
	}

	updateServer := &agentUpdateServer{listener: listener, socketPath: socketPath, token: token}
	digest := sha256.Sum256([]byte(token))
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		manager.handleAgentUpdate(writer, request, claim, leaseToken, digest, handle, repository, value)
	})
	updateServer.server = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       5 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		if serveErr := updateServer.server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			manager.logger.Error("agent_update_server_failed", "attempt_id", claim.Attempt.ID, "error", serveErr)
			handle.stop("failed")
		}
	}()
	return updateServer, nil
}

func (server *agentUpdateServer) close() {
	if server == nil {
		return
	}
	server.closeOnce.Do(func() {
		_ = server.server.Close()
		_ = server.listener.Close()
		_ = os.Remove(server.socketPath)
	})
}

func (manager *Manager) handleAgentUpdate(
	writer http.ResponseWriter,
	request *http.Request,
	claim protocol.Claim,
	leaseToken string,
	tokenDigest [sha256.Size]byte,
	handle *attemptHandle,
	repository Repository,
	value worktree,
) {
	writer.Header().Set("Content-Type", "application/json")
	if request.Method != http.MethodPost || request.URL.Path != agentUpdatePath || request.URL.RawQuery != "" {
		writeAgentUpdateError(writer, http.StatusNotFound, "not_found", "agent update endpoint not found", false)
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeAgentUpdateError(writer, http.StatusUnsupportedMediaType, "json_required", "Content-Type must be application/json", false)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, protocol.MaxBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input protocol.AgentUpdateRequest
	if err := decoder.Decode(&input); err != nil {
		writeAgentUpdateError(writer, http.StatusBadRequest, "invalid_json", "request body must be one JSON object", false)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeAgentUpdateError(writer, http.StatusBadRequest, "invalid_json", "request body must contain one JSON object", false)
		return
	}
	presentedDigest := sha256.Sum256([]byte(input.UpdateToken))
	if subtle.ConstantTimeCompare(tokenDigest[:], presentedDigest[:]) != 1 {
		writeAgentUpdateError(writer, http.StatusUnauthorized, "update_token_invalid", "the update token does not own this Attempt", false)
		return
	}
	if input.WorkID != claim.Session.ID || input.AttemptID != claim.Attempt.ID {
		writeAgentUpdateError(writer, http.StatusForbidden, "update_scope_mismatch", "the update capability is scoped to another Work or Attempt", false)
		return
	}
	if input.Status == protocol.WorkUpdateNeedsInput {
		writeAgentUpdateError(writer, http.StatusConflict, "checkpoint_required", "needs-input requires a verified durable checkpoint", false)
		return
	}

	forward := protocol.AttemptUpdateRequest{
		LeaseToken: leaseToken, RequestID: input.RequestID, Status: input.Status,
		Message: input.Message, PullRequestURL: input.PullRequestURL,
	}
	replayed, replayErr := manager.client.update(request.Context(), claim.Attempt.ID, protocol.AttemptUpdateRequest{
		LeaseToken: leaseToken, ReplayOnly: true, RequestID: input.RequestID, Status: input.Status,
		Message: input.Message, PullRequestURL: input.PullRequestURL,
	})
	if replayErr == nil {
		respondAcceptedAgentUpdate(writer, replayed, handle)
		return
	}
	var replayAPIError *APIError
	if !errors.As(replayErr, &replayAPIError) || replayAPIError.Code != "update_request_not_found" {
		writeControlPlaneUpdateError(writer, replayErr)
		return
	}
	if input.Status == protocol.WorkUpdateReady {
		evidence, validationErr := manager.validateReadyDelivery(request.Context(), claim, repository, value, input.PullRequestURL)
		if validationErr != nil {
			status := http.StatusConflict
			if validationErr.retriable {
				status = http.StatusServiceUnavailable
			}
			writeAgentUpdateError(writer, status, validationErr.code, validationErr.message, validationErr.retriable)
			return
		}
		forward.PullRequestHeadBranch = evidence.HeadBranch
		forward.PullRequestHeadSHA = evidence.HeadSHA
	}
	update, err := manager.client.update(request.Context(), claim.Attempt.ID, forward)
	if err != nil {
		writeControlPlaneUpdateError(writer, err)
		return
	}
	respondAcceptedAgentUpdate(writer, update, handle)
}

func writeControlPlaneUpdateError(writer http.ResponseWriter, err error) {
	var apiError *APIError
	if errors.As(err, &apiError) {
		writeAgentUpdateError(writer, apiError.Status, apiError.Code, apiError.Message, apiError.Status >= 500)
		return
	}
	writeAgentUpdateError(writer, http.StatusServiceUnavailable, "control_plane_unavailable", "the control plane could not accept the update", true)
}

func respondAcceptedAgentUpdate(writer http.ResponseWriter, update protocol.WorkUpdate, handle *attemptHandle) {
	if update.Status != protocol.WorkUpdateRunning {
		handle.recordOutcome(update)
	}
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(update)
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	if update.Status != protocol.WorkUpdateRunning {
		handle.stopForOutcome()
	}
}

func writeAgentUpdateError(writer http.ResponseWriter, status int, code, message string, retriable bool) {
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retriable bool   `json:"retriable"`
		} `json:"error"`
	}{Error: struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retriable bool   `json:"retriable"`
	}{Code: code, Message: message, Retriable: retriable}})
}

type readyDeliveryEvidence struct {
	HeadBranch string
	HeadSHA    string
}

type gitHubPullRequest struct {
	HTMLURL string `json:"html_url"`
	Head    struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
}

func (manager *Manager) validateReadyDelivery(
	ctx context.Context,
	claim protocol.Claim,
	repository Repository,
	value worktree,
	pullRequestURL string,
) (readyDeliveryEvidence, *updateValidationError) {
	owner, name, number, err := parseGitHubPullRequestURL(pullRequestURL)
	if err != nil {
		return readyDeliveryEvidence{}, &updateValidationError{code: "invalid_pull_request", message: err.Error()}
	}
	expectedRepository := strings.TrimPrefix(strings.ToLower(claim.Repository.RemoteIdentity), "github.com/")
	if !strings.EqualFold(owner+"/"+name, expectedRepository) {
		return readyDeliveryEvidence{}, &updateValidationError{
			code: "delivery_repository_mismatch", message: "the pull request belongs to a different repository",
		}
	}
	stdout, stderr, commandErr := runCommand(ctx, manager.options.GitHubExecutable, value.Path, 256<<10,
		"api", "--method", "GET", "repos/"+owner+"/"+name+"/pulls/"+strconv.FormatInt(number, 10))
	if commandErr != nil {
		return readyDeliveryEvidence{}, &updateValidationError{
			code: "github_validation_unavailable", message: "GitHub pull request validation is temporarily unavailable",
			retriable: true, err: commandFailure("read pull request", stdout, stderr, commandErr),
		}
	}
	var pullRequest gitHubPullRequest
	if err := json.Unmarshal(stdout, &pullRequest); err != nil || pullRequest.Head.Ref == "" ||
		!commitPattern.MatchString(pullRequest.Head.SHA) || pullRequest.Head.Repo.FullName == "" {
		return readyDeliveryEvidence{}, &updateValidationError{
			code: "github_validation_unavailable", message: "GitHub returned incomplete pull request evidence", retriable: true,
		}
	}
	if !strings.EqualFold(pullRequest.Head.Repo.FullName, expectedRepository) {
		return readyDeliveryEvidence{}, &updateValidationError{
			code: "delivery_repository_mismatch", message: "the pull request head belongs to a different repository",
		}
	}
	if pullRequest.Head.Ref != claim.Session.Target.PublishBranch {
		return readyDeliveryEvidence{}, &updateValidationError{
			code: "delivery_branch_mismatch", message: "the pull request head branch does not match the Work publish branch",
		}
	}

	release, err := manager.repositoryLocks.acquire(ctx, repositoryCoordinationKey(repository))
	if err != nil {
		return readyDeliveryEvidence{}, &updateValidationError{
			code: "delivery_validation_unavailable", message: "repository delivery validation is temporarily unavailable", retriable: true, err: err,
		}
	}
	defer release()
	if err := validateRegisteredOrigin(ctx, manager.options.GitExecutable, repository); err != nil {
		return readyDeliveryEvidence{}, &updateValidationError{
			code: "delivery_validation_unavailable", message: "repository origin validation failed", retriable: true, err: err,
		}
	}
	remoteSHA, err := remotePublishCommit(ctx, manager.options.GitExecutable, repository, claim.Session.Target.PublishBranch)
	if err != nil {
		return readyDeliveryEvidence{}, &updateValidationError{
			code: "publish_ref_unavailable", message: "the Work publish branch could not be fetched", retriable: true, err: err,
		}
	}
	stdout, stderr, err = runGitCommand(ctx, manager.options.GitExecutable, value.Path, 64<<10, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return readyDeliveryEvidence{}, &updateValidationError{
			code: "delivery_validation_unavailable", message: "local HEAD could not be resolved", retriable: true,
			err: commandFailure("resolve local HEAD", stdout, stderr, err),
		}
	}
	localSHA := strings.TrimSpace(string(stdout))
	if localSHA != remoteSHA || localSHA != pullRequest.Head.SHA {
		return readyDeliveryEvidence{}, &updateValidationError{
			code: "delivery_head_mismatch", message: "local HEAD, the fetched publish ref, and the pull request head SHA must match",
		}
	}
	return readyDeliveryEvidence{HeadBranch: pullRequest.Head.Ref, HeadSHA: pullRequest.Head.SHA}, nil
}

func parseGitHubPullRequestURL(value string) (string, string, int64, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", 0, errors.New("ready requires an HTTPS github.com pull request URL")
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] != "pull" {
		return "", "", 0, errors.New("ready requires a URL shaped like https://github.com/OWNER/REPOSITORY/pull/NUMBER")
	}
	owner, ownerErr := url.PathUnescape(parts[0])
	name, nameErr := url.PathUnescape(parts[1])
	number, numberErr := strconv.ParseInt(parts[3], 10, 64)
	if ownerErr != nil || nameErr != nil || numberErr != nil || number < 1 || strings.ContainsAny(owner+name, "/\\") {
		return "", "", 0, errors.New("pull request URL contains invalid repository or number fields")
	}
	return owner, name, number, nil
}

func remotePublishCommit(
	ctx context.Context,
	gitExecutable string,
	repository Repository,
	branch string,
) (string, error) {
	if err := validateBaseBranch(ctx, gitExecutable, repository, branch); err != nil {
		return "", err
	}
	stdout, stderr, err := runGitCommand(ctx, gitExecutable, repository.Path, 64<<10,
		"ls-remote", "--refs", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", commandFailure("resolve publish branch", stdout, stderr, err)
	}
	fields := strings.Fields(string(stdout))
	if len(fields) != 2 || fields[1] != "refs/heads/"+branch || !commitPattern.MatchString(fields[0]) {
		return "", errors.New("publish branch does not exist")
	}
	commit := fields[0]
	stdout, stderr, err = runGitCommand(ctx, gitExecutable, repository.Path, 256<<10,
		"fetch", "--no-tags", "--no-write-fetch-head", "--refmap=", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", commandFailure("fetch publish branch", stdout, stderr, err)
	}
	stdout, stderr, err = runGitCommand(ctx, gitExecutable, repository.Path, 64<<10,
		"rev-parse", "--verify", commit+"^{commit}")
	if err != nil || strings.TrimSpace(string(stdout)) != commit {
		if err == nil {
			err = errors.New("fetched publish branch did not contain its advertised commit")
		}
		return "", commandFailure("verify fetched publish branch", stdout, stderr, err)
	}
	return commit, nil
}
