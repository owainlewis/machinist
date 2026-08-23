// Package factoryclient talks to the Factory control plane over its loopback
// operator API. It exists so the CLI never opens the SQLite database directly:
// the running server owns that file, and two writers would fight.
package factoryclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

// DefaultAddress is the loopback address the control plane listens on.
const DefaultAddress = "127.0.0.1:7337"

// Client calls the operator API.
type Client struct {
	base string
	http *http.Client
}

// New returns a client for address, which may be a bare host:port or a URL.
func New(address string) (*Client, error) {
	if strings.TrimSpace(address) == "" {
		address = DefaultAddress
	}
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}
	parsed, err := url.Parse(address)
	if err != nil {
		return nil, fmt.Errorf("invalid server address %q: %w", address, err)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("invalid server address %q: no host", address)
	}
	return &Client{
		base: strings.TrimSuffix(parsed.Scheme+"://"+parsed.Host, "/"),
		http: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// APIError is a structured error returned by the control plane. Callers match
// on Code so that a message change never breaks behaviour.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("control plane returned %d", e.Status)
	}
	return fmt.Sprintf("%s (%s)", e.Message, e.Code)
}

// Code reports the control-plane error code for err, or "" when err did not
// come from the API.
func Code(err error) string {
	var apiError *APIError
	if errors.As(err, &apiError) {
		return apiError.Code
	}
	return ""
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		var opError *net.OpError
		if errors.As(err, &opError) {
			return fmt.Errorf("cannot reach the Factory server at %s: start it with `just run`", c.base)
		}
		return err
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode >= 400 {
		apiError := &APIError{Status: response.StatusCode}
		var decoded protocol.ErrorBody
		if json.Unmarshal(payload, &decoded) == nil {
			apiError.Code = decoded.Error.Code
			apiError.Message = decoded.Error.Message
		}
		if apiError.Message == "" {
			apiError.Message = strings.TrimSpace(string(payload))
		}
		return apiError
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(payload, out)
}

// Repositories lists the managed repositories the control plane knows about.
func (c *Client) Repositories(ctx context.Context) ([]protocol.ManagedRepository, error) {
	var page struct {
		Repositories []protocol.ManagedRepository `json:"repositories"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/repositories", nil, &page); err != nil {
		return nil, err
	}
	return page.Repositories, nil
}

// EnsureRepository returns the managed repository for identity, registering it
// when the control plane has not seen it before. Registration is idempotent on
// the remote identity, so repeated dispatches converge on one repository.
func (c *Client) EnsureRepository(ctx context.Context, identity string) (protocol.ManagedRepository, error) {
	existing, err := c.Repositories(ctx)
	if err != nil {
		return protocol.ManagedRepository{}, err
	}
	for _, repository := range existing {
		if strings.EqualFold(repository.RemoteIdentity, identity) {
			return repository, nil
		}
	}
	var created protocol.ManagedRepository
	body := map[string]string{"remote_identity": identity}
	if err := c.do(ctx, http.MethodPost, "/api/v1/repositories", body, &created); err != nil {
		return protocol.ManagedRepository{}, err
	}
	return created, nil
}

// CreateTask records a dispatch. One dispatch is one task carrying the frozen
// rendered prompt.
func (c *Client) CreateTask(ctx context.Context, request protocol.SaveTaskRequest) (protocol.Task, error) {
	var task protocol.Task
	if err := c.do(ctx, http.MethodPost, "/api/v1/tasks", request, &task); err != nil {
		return protocol.Task{}, err
	}
	return task, nil
}

// StartTask admits a task for execution. requestKey makes the call idempotent:
// replaying it returns the run that already exists rather than starting a
// second one.
func (c *Client) StartTask(ctx context.Context, taskID, requestKey string) (protocol.RunDetail, error) {
	var detail protocol.RunDetail
	body := protocol.RunTaskRequest{RequestKey: requestKey}
	path := "/api/v1/tasks/" + url.PathEscape(taskID) + "/run"
	if err := c.do(ctx, http.MethodPost, path, body, &detail); err != nil {
		return protocol.RunDetail{}, err
	}
	return detail, nil
}

// Runs lists runs, newest first.
func (c *Client) Runs(ctx context.Context, limit int) ([]protocol.Run, error) {
	path := "/api/v1/runs"
	if limit > 0 {
		path += "?limit=" + fmt.Sprint(limit)
	}
	var page protocol.RunPage
	if err := c.do(ctx, http.MethodGet, path, nil, &page); err != nil {
		return nil, err
	}
	return page.Runs, nil
}

// Run returns one run with its sessions and attempts.
func (c *Client) Run(ctx context.Context, runID string) (protocol.RunDetail, error) {
	var detail protocol.RunDetail
	path := "/api/v1/runs/" + url.PathEscape(runID)
	if err := c.do(ctx, http.MethodGet, path, nil, &detail); err != nil {
		return protocol.RunDetail{}, err
	}
	return detail, nil
}

// CancelRun asks the control plane to stop a run.
func (c *Client) CancelRun(ctx context.Context, runID string) error {
	path := "/api/v1/runs/" + url.PathEscape(runID) + "/cancel"
	return c.do(ctx, http.MethodPost, path, struct{}{}, nil)
}

// EventPage is one page of attempt events.
type EventPage struct {
	Events    []protocol.AttemptEvent `json:"events"`
	NextAfter int64                   `json:"next_after"`
	HasMore   bool                    `json:"has_more"`
}

// Events reads attempt events after a cursor. Following a run is a poll on
// NextAfter, which matches the no-webhooks posture of the rest of Factory.
func (c *Client) Events(ctx context.Context, attemptID string, after int64) (EventPage, error) {
	path := fmt.Sprintf("/api/v1/attempts/%s/events?after=%d", url.PathEscape(attemptID), after)
	var page EventPage
	if err := c.do(ctx, http.MethodGet, path, nil, &page); err != nil {
		return EventPage{}, err
	}
	return page, nil
}
