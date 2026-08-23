package factorycli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

type agentUpdateFailure struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retriable bool   `json:"retriable"`
	} `json:"error"`
}

type agentUpdateError struct {
	status    int
	code      string
	message   string
	retriable bool
}

func (err *agentUpdateError) Error() string {
	return fmt.Sprintf("Worker update endpoint returned %d %s: %s", err.status, err.code, err.message)
}

func (c command) agentUpdate(status, message, pullRequest string, jsonOutput bool) error {
	workID := strings.TrimSpace(c.getenv("FACTORY_WORK_ID"))
	attemptID := strings.TrimSpace(c.getenv("FACTORY_ATTEMPT_ID"))
	socket := strings.TrimSpace(c.getenv("FACTORY_UPDATE_SOCKET"))
	token := c.getenv("FACTORY_UPDATE_TOKEN")
	if workID == "" || attemptID == "" || socket == "" || token == "" {
		return errors.New("update requires an injected active agent-update Attempt context")
	}
	updateStatus := protocol.WorkUpdateStatus(strings.TrimSpace(status))
	if !protocol.SupportedWorkUpdateStatus(updateStatus) {
		return &usageError{message: "--status must be running, ready, needs-input, failed, or no-change"}
	}
	if updateStatus == protocol.WorkUpdateNeedsInput {
		return &usageError{message: "needs-input is unavailable until verified checkpoint support is enabled"}
	}
	if !utf8.ValidString(message) || strings.TrimSpace(message) == "" {
		return &usageError{message: "--message must be non-empty UTF-8 text"}
	}
	limit := protocol.MaxOutcomeBytes
	if updateStatus == protocol.WorkUpdateRunning {
		limit = protocol.MaxProgressBytes
	}
	if len([]byte(message)) > limit {
		return &usageError{message: fmt.Sprintf("--message exceeds the %d-byte limit for %s", limit, updateStatus)}
	}
	if updateStatus == protocol.WorkUpdateReady && strings.TrimSpace(pullRequest) == "" {
		return &usageError{message: "ready requires --pr"}
	}
	if updateStatus != protocol.WorkUpdateReady && pullRequest != "" {
		return &usageError{message: "--pr is accepted only with ready"}
	}
	requestID, err := randomUpdateRequestID()
	if err != nil {
		return fmt.Errorf("generate update request ID: %w", err)
	}
	input := protocol.AgentUpdateRequest{
		WorkID: workID, AttemptID: attemptID, UpdateToken: token, RequestID: requestID,
		Status: updateStatus, Message: message, PullRequestURL: pullRequest,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	update, err := sendAgentUpdate(ctx, socket, input)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(c.stdout, update)
	}
	_, err = fmt.Fprintf(c.stdout, "Factory accepted %s update for Work %s.\n", update.Status, update.WorkID)
	return err
}

func randomUpdateRequestID() (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func sendAgentUpdate(
	ctx context.Context,
	socket string,
	input protocol.AgentUpdateRequest,
) (protocol.WorkUpdate, error) {
	if !strings.HasPrefix(socket, "/") {
		return protocol.WorkUpdate{}, errors.New("FACTORY_UPDATE_SOCKET must be an absolute path")
	}
	info, err := os.Lstat(socket)
	if err != nil {
		return protocol.WorkUpdate{}, fmt.Errorf("inspect Factory update socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 {
		return protocol.WorkUpdate{}, errors.New("Factory update socket must be a private Unix socket")
	}
	body, err := json.Marshal(input)
	if err != nil {
		return protocol.WorkUpdate{}, fmt.Errorf("encode agent update: %w", err)
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt != 0 {
			timer := time.NewTimer(time.Duration(attempt) * 200 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return protocol.WorkUpdate{}, ctx.Err()
			case <-timer.C:
			}
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://factory.local/update", bytes.NewReader(body))
		if err != nil {
			return protocol.WorkUpdate{}, fmt.Errorf("create agent update request: %w", err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json")
		response, err := client.Do(request)
		if err != nil {
			lastErr = fmt.Errorf("send agent update: %w", err)
			continue
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, protocol.MaxBodyBytes+1))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			lastErr = fmt.Errorf("read agent update response: %w", errors.Join(readErr, closeErr))
			continue
		}
		if len(responseBody) > protocol.MaxBodyBytes {
			return protocol.WorkUpdate{}, errors.New("agent update response exceeds 1 MiB")
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			var update protocol.WorkUpdate
			if err := json.Unmarshal(responseBody, &update); err != nil {
				return protocol.WorkUpdate{}, fmt.Errorf("decode agent update response: %w", err)
			}
			return update, nil
		}
		var failure agentUpdateFailure
		if err := json.Unmarshal(responseBody, &failure); err != nil || failure.Error.Code == "" {
			return protocol.WorkUpdate{}, fmt.Errorf("Worker update endpoint returned %s", response.Status)
		}
		lastErr = &agentUpdateError{
			status: response.StatusCode, code: failure.Error.Code,
			message: failure.Error.Message, retriable: failure.Error.Retriable,
		}
		if !failure.Error.Retriable {
			return protocol.WorkUpdate{}, lastErr
		}
	}
	return protocol.WorkUpdate{}, lastErr
}
