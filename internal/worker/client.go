package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

const requestTimeout = 10 * time.Second

type APIError struct {
	Status  int
	Code    string
	Message string
}

type requestTransportError struct{ error }
type retryableEnrollmentError struct{ error }

func (err requestTransportError) Unwrap() error    { return err.error }
func (err retryableEnrollmentError) Unwrap() error { return err.error }

func (err *APIError) Error() string {
	return fmt.Sprintf("control plane returned %d %s: %s", err.Status, err.Code, err.Message)
}

type client struct {
	baseURL    string
	http       *http.Client
	credential string
}

type storedWorkerCredential struct {
	Server     string `json:"server"`
	Credential string `json:"credential"`
}

func newClient(server string, httpClient *http.Client) *client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	safeClient := *httpClient
	safeClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &client{baseURL: strings.TrimRight(server, "/"), http: &safeClient}
}

func (client *client) enroll(ctx context.Context, workerID, enrollmentToken, credentialPath string) error {
	if !strings.HasPrefix(client.baseURL, "https://") {
		return nil
	}
	pendingPath := credentialPath + ".pending"
	if client.credential != "" {
		err := os.Remove(pendingPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale pending Worker credential: %w", err)
		}
		if err == nil {
			if err := syncDirectory(filepath.Dir(pendingPath)); err != nil {
				return fmt.Errorf("sync Worker credential directory: %w", err)
			}
		}
		return nil
	}
	if enrollmentToken == "" {
		return errors.New("remote Worker requires enrollment_token until its credential has been saved")
	}
	credential, err := loadCredentialFile(pendingPath, client.baseURL)
	if err != nil {
		return err
	}
	if credential == "" {
		credential, err = createPendingWorkerCredential(pendingPath, client.baseURL)
		if err != nil {
			return err
		}
	}
	var response protocol.WorkerCredential
	exchange := func() error {
		response = protocol.WorkerCredential{}
		_, requestErr := client.requestWithoutCredential(ctx, http.MethodPost, "/api/v1/worker-enrollments/exchange",
			protocol.ExchangeWorkerEnrollmentRequest{
				WorkerID: workerID, EnrollmentToken: enrollmentToken, Credential: credential,
			}, &response)
		return requestErr
	}
	err = exchange()
	var apiError *APIError
	if strings.HasPrefix(credential, "factory_runner_") && errors.As(err, &apiError) &&
		apiError.Code == "worker_credential_regeneration_required" {
		if err := os.Remove(pendingPath); err != nil {
			return fmt.Errorf("remove legacy pending Worker credential: %w", err)
		}
		if err := syncDirectory(filepath.Dir(pendingPath)); err != nil {
			return fmt.Errorf("sync Worker credential directory: %w", err)
		}
		credential, err = createPendingWorkerCredential(pendingPath, client.baseURL)
		if err != nil {
			return err
		}
		err = exchange()
	}
	if err != nil {
		wrapped := fmt.Errorf("enroll remote Worker: %w", err)
		var transportError requestTransportError
		if errors.As(err, &transportError) || (errors.As(err, &apiError) && apiError.Status >= 500) {
			return retryableEnrollmentError{error: wrapped}
		}
		return wrapped
	}
	if response.Credential != credential {
		return errors.New("enroll remote Worker: server returned an invalid credential")
	}
	if err := writeCredentialFile(credentialPath, client.baseURL, credential); err != nil {
		return err
	}
	client.credential = credential
	if err := os.Remove(pendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove pending Worker credential: %w", err)
	}
	if err := syncDirectory(filepath.Dir(pendingPath)); err != nil {
		return fmt.Errorf("sync Worker credential directory: %w", err)
	}
	return nil
}

func createPendingWorkerCredential(path, server string) (string, error) {
	body := make([]byte, 32)
	if _, err := rand.Read(body); err != nil {
		return "", fmt.Errorf("generate Worker credential: %w", err)
	}
	credential := "factory_worker_" + base64.RawURLEncoding.EncodeToString(body)
	if err := writeCredentialFile(path, server, credential); err != nil {
		return "", err
	}
	return credential, nil
}

func loadCredentialFile(path, server string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect Worker credential: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("Worker credential must be a regular non-symlink file readable only by its owner")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read Worker credential: %w", err)
	}
	var stored storedWorkerCredential
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return "", errors.New("Worker credential is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", errors.New("Worker credential is invalid")
	}
	if stored.Server != strings.TrimRight(server, "/") {
		return "", fmt.Errorf("Worker credential belongs to a different Factory server; remove %s and enroll this identity explicitly", filepath.Base(path))
	}
	credential := strings.TrimSpace(stored.Credential)
	if credential == "" || len(credential) > 1024 || credential != stored.Credential {
		return "", errors.New("Worker credential is invalid")
	}
	return credential, nil
}

func adoptLegacyWorkerCredentialFiles(directory, server string) error {
	for _, name := range []string{"", ".pending"} {
		legacyPath := filepath.Join(directory, "runner-credential"+name)
		workerPath := filepath.Join(directory, "worker-credential"+name)
		if err := adoptLegacyWorkerCredentialFile(legacyPath, workerPath, server); err != nil {
			return err
		}
	}
	return nil
}

func adoptLegacyWorkerCredentialFile(legacyPath, workerPath, server string) error {
	if _, err := os.Lstat(workerPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect Worker credential destination: %w", err)
	}
	credential, err := loadCredentialFile(legacyPath, server)
	if err != nil {
		return fmt.Errorf("validate legacy Worker credential: %w", err)
	}
	if credential == "" {
		return nil
	}
	if err := os.Link(legacyPath, workerPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return fmt.Errorf("adopt legacy Worker credential: %w", err)
	}
	if err := syncDirectory(filepath.Dir(workerPath)); err != nil {
		return fmt.Errorf("sync adopted Worker credential: %w", err)
	}
	if err := os.Remove(legacyPath); err != nil {
		return fmt.Errorf("remove legacy Worker credential: %w", err)
	}
	if err := syncDirectory(filepath.Dir(workerPath)); err != nil {
		return fmt.Errorf("sync Worker credential directory: %w", err)
	}
	return nil
}

func writeCredentialFile(path, server, credential string) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("create temporary Worker credential: %w", err)
	}
	temporaryPath := file.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	writeErr := error(nil)
	if err := json.NewEncoder(file).Encode(storedWorkerCredential{
		Server: strings.TrimRight(server, "/"), Credential: credential,
	}); err != nil {
		writeErr = err
	} else if err := file.Sync(); err != nil {
		writeErr = err
	}
	if err := file.Close(); err != nil && writeErr == nil {
		writeErr = err
	}
	if writeErr != nil {
		return fmt.Errorf("write Worker credential: %w", writeErr)
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return fmt.Errorf("install Worker credential: %w", err)
	}
	if err := os.Remove(temporaryPath); err != nil {
		return fmt.Errorf("remove temporary Worker credential: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync Worker credential directory: %w", err)
	}
	return nil
}

func (client *client) register(ctx context.Context, workerID string, input protocol.WorkerRegistration) (protocol.Worker, error) {
	var worker protocol.Worker
	_, err := client.request(ctx, http.MethodPut, "/api/v1/workers/"+url.PathEscape(workerID), input, &worker)
	return worker, err
}

func (client *client) heartbeatWorker(ctx context.Context, workerID string) (protocol.Worker, error) {
	var worker protocol.Worker
	_, err := client.request(ctx, http.MethodPut,
		"/api/v1/workers/"+url.PathEscape(workerID)+"/heartbeat", struct{}{}, &worker)
	return worker, err
}

func (client *client) claim(ctx context.Context, workerID string, input protocol.ClaimRequest, minimumBackoff, maximumBackoff time.Duration) (*protocol.Claim, error) {
	var claim protocol.Claim
	status, err := client.retry(ctx, http.MethodPost,
		"/api/v1/workers/"+url.PathEscape(workerID)+"/claims", input, &claim, minimumBackoff, maximumBackoff)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	return &claim, nil
}

func (client *client) start(ctx context.Context, attemptID string, input protocol.StartAttemptRequest) (protocol.Attempt, error) {
	var attempt protocol.Attempt
	_, err := client.retry(ctx, http.MethodPost, "/api/v1/attempts/"+url.PathEscape(attemptID)+"/start",
		input, &attempt, time.Second, 5*time.Second)
	return attempt, err
}

func (client *client) startStage(ctx context.Context, attemptID string, position int, input protocol.StartStageRequest) (protocol.StageRun, error) {
	var stage protocol.StageRun
	_, err := client.retry(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/attempts/%s/stages/%d/start", url.PathEscape(attemptID), position),
		input, &stage, time.Second, 5*time.Second)
	return stage, err
}

func (client *client) completeStage(ctx context.Context, attemptID string, position int, input protocol.CompleteStageRequest) (protocol.StageRun, error) {
	input = boundedStageCompletionRequest(input)
	var stage protocol.StageRun
	_, err := client.retry(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/attempts/%s/stages/%d/complete", url.PathEscape(attemptID), position),
		input, &stage, time.Second, 5*time.Second)
	return stage, err
}

func boundedStageCompletionRequest(input protocol.CompleteStageRequest) protocol.CompleteStageRequest {
	input.Result = boundedText(strings.ToValidUTF8(input.Result, "\uFFFD"), protocol.MaxResultBytes)
	input.Error = boundedText(strings.ToValidUTF8(input.Error, "\uFFFD"), protocol.MaxErrorBytes)
	if stageCompletionRequestFits(input) {
		return input
	}
	input.Result = largestStageCompletionField(input, input.Result, true)
	if stageCompletionRequestFits(input) {
		return input
	}
	input.Error = largestStageCompletionField(input, input.Error, false)
	return input
}

func largestStageCompletionField(input protocol.CompleteStageRequest, value string, result bool) string {
	low, high := 0, len(value)
	for low < high {
		middle := low + (high-low+1)/2
		candidate := boundedText(value, middle)
		if result {
			input.Result = candidate
		} else {
			input.Error = candidate
		}
		if stageCompletionRequestFits(input) {
			low = middle
		} else {
			high = middle - 1
		}
	}
	return boundedText(value, low)
}

func stageCompletionRequestFits(input protocol.CompleteStageRequest) bool {
	body, err := json.Marshal(input)
	return err == nil && len(body) <= protocol.MaxBodyBytes
}

func (client *client) heartbeat(ctx context.Context, attemptID, token string) (protocol.HeartbeatResponse, error) {
	var heartbeat protocol.HeartbeatResponse
	_, err := client.request(ctx, http.MethodPut, "/api/v1/attempts/"+url.PathEscape(attemptID)+"/heartbeat",
		protocol.LeaseRequest{LeaseToken: token}, &heartbeat)
	return heartbeat, err
}

func (client *client) attempt(ctx context.Context, attemptID string) (protocol.Attempt, error) {
	var attempt protocol.Attempt
	_, err := client.request(ctx, http.MethodGet, "/api/v1/attempts/"+url.PathEscape(attemptID), nil, &attempt)
	return attempt, err
}

func (client *client) events(ctx context.Context, attemptID string, input protocol.EventBatchRequest) error {
	_, err := client.request(ctx, http.MethodPost, "/api/v1/attempts/"+url.PathEscape(attemptID)+"/events", input, nil)
	return err
}

func (client *client) update(
	ctx context.Context,
	attemptID string,
	input protocol.AttemptUpdateRequest,
) (protocol.WorkUpdate, error) {
	var update protocol.WorkUpdate
	_, err := client.request(ctx, http.MethodPost,
		"/api/v1/attempts/"+url.PathEscape(attemptID)+"/updates", input, &update)
	return update, err
}

func (client *client) complete(ctx context.Context, attemptID string, input protocol.CompleteAttemptRequest) (protocol.Attempt, error) {
	input = boundedCompletionRequest(input)
	var attempt protocol.Attempt
	_, err := client.retry(ctx, http.MethodPost, "/api/v1/attempts/"+url.PathEscape(attemptID)+"/complete",
		input, &attempt, time.Second, 5*time.Second)
	return attempt, err
}

func boundedCompletionRequest(input protocol.CompleteAttemptRequest) protocol.CompleteAttemptRequest {
	input.Result = boundedText(strings.ToValidUTF8(input.Result, "\uFFFD"), protocol.MaxResultBytes)
	input.Error = boundedText(strings.ToValidUTF8(input.Error, "\uFFFD"), protocol.MaxErrorBytes)
	if completionRequestFits(input) {
		return input
	}
	input.Result = largestCompletionField(input, input.Result, true)
	if completionRequestFits(input) {
		return input
	}
	input.Error = largestCompletionField(input, input.Error, false)
	return input
}

func largestCompletionField(input protocol.CompleteAttemptRequest, value string, result bool) string {
	low, high := 0, len(value)
	for low < high {
		middle := low + (high-low+1)/2
		candidate := boundedText(value, middle)
		if result {
			input.Result = candidate
		} else {
			input.Error = candidate
		}
		if completionRequestFits(input) {
			low = middle
		} else {
			high = middle - 1
		}
	}
	return boundedText(value, low)
}

func completionRequestFits(input protocol.CompleteAttemptRequest) bool {
	body, err := json.Marshal(input)
	return err == nil && len(body) <= protocol.MaxBodyBytes
}

func (client *client) retry(
	ctx context.Context,
	method string,
	path string,
	input any,
	output any,
	minimumBackoff time.Duration,
	maximumBackoff time.Duration,
) (int, error) {
	backoff := minimumBackoff
	for {
		status, err := client.request(ctx, method, path, input, output)
		if err == nil {
			return status, nil
		}
		var apiError *APIError
		if errors.As(err, &apiError) && apiError.Status < 500 {
			return status, err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, ctx.Err()
		case <-timer.C:
		}
		backoff *= 2
		if backoff > maximumBackoff {
			backoff = maximumBackoff
		}
	}
}

func (client *client) request(ctx context.Context, method, path string, input any, output any) (int, error) {
	return client.requestWithCredential(ctx, method, path, input, output, client.credential)
}

func (client *client) requestWithoutCredential(ctx context.Context, method, path string, input any, output any) (int, error) {
	return client.requestWithCredential(ctx, method, path, input, output, "")
}

func (client *client) requestWithCredential(ctx context.Context, method, path string, input any, output any, credential string) (int, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return 0, fmt.Errorf("encode request: %w", err)
	}
	requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, method, client.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	response, err := client.http.Do(request)
	if err != nil {
		return 0, requestTransportError{error: fmt.Errorf("send request: %w", err)}
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, protocol.MaxBodyBytes+1))
	if err != nil {
		return response.StatusCode, requestTransportError{error: fmt.Errorf("read response: %w", err)}
	}
	if len(responseBody) > protocol.MaxBodyBytes {
		return response.StatusCode, errors.New("control-plane response exceeds 1 MiB")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var errorBody protocol.ErrorBody
		if err := json.Unmarshal(responseBody, &errorBody); err != nil {
			return response.StatusCode, &APIError{
				Status: response.StatusCode, Code: "invalid_error_response", Message: strings.TrimSpace(string(responseBody)),
			}
		}
		return response.StatusCode, &APIError{
			Status: response.StatusCode, Code: errorBody.Error.Code, Message: errorBody.Error.Message,
		}
	}
	if response.StatusCode == http.StatusNoContent || output == nil {
		return response.StatusCode, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return response.StatusCode, fmt.Errorf("decode response: %w", err)
	}
	return response.StatusCode, nil
}
