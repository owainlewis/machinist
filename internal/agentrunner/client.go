package agentrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxResponseBytes = 32 << 20

// Client calls the control plane API with the runner's token.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// ResponseError reports a non-2xx control plane response.
type ResponseError struct {
	Status int
	Body   string
}

func (err *ResponseError) Error() string {
	message := fmt.Sprintf("control plane returned HTTP %d", err.Status)
	if text := http.StatusText(err.Status); text != "" {
		message += " " + text
	}
	if err.Body != "" {
		message += ": " + err.Body
	}
	return message
}

// Retryable reports whether the request may succeed later. Client errors other
// than timeouts and rate limits are permanent.
func (err *ResponseError) Retryable() bool {
	return err.Status < 400 || err.Status >= 500 || err.Status == http.StatusRequestTimeout || err.Status == http.StatusTooManyRequests
}

// Stopped reports that the control plane no longer wants this run.
func (err *ResponseError) Stopped() bool {
	return err.Status == http.StatusConflict || err.Status == http.StatusNotFound
}

func NewClient(base, token string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Timeout: 15 * time.Second}}
}

// Post sends input as JSON. A 204 response leaves output unchanged.
func (c *Client) Post(ctx context.Context, path string, input, output any) error {
	encoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		return &ResponseError{Status: response.StatusCode, Body: strings.TrimSpace(string(message))}
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(output); err != nil {
		return fmt.Errorf("decode control plane response: %w", err)
	}
	return nil
}
