package managedworker

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

	"github.com/owainlewis/machinist/internal/config"
)

// Client calls the control plane API with the shared worker token. Both the
// managed worker and the submit command use it.
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

func NewClient(workerConfig config.Worker) (*Client, error) {
	base, err := controlPlaneEndpoint(workerConfig.ControlPlane.URL)
	if err != nil {
		return nil, err
	}
	token, err := workerConfig.WorkerToken()
	if err != nil {
		return nil, err
	}
	return newClient(base, token, &http.Client{Timeout: 15 * time.Second}), nil
}

func newClient(base, token string, httpClient *http.Client) *Client {
	return &Client{base: strings.TrimRight(base, "/"), token: token, http: httpClient}
}

func (c *Client) Get(ctx context.Context, path string, output any) error {
	return c.do(ctx, http.MethodGet, path, nil, output)
}

func (c *Client) Post(ctx context.Context, path string, input, output any) error {
	return c.do(ctx, http.MethodPost, path, input, output)
}

func (c *Client) do(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
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
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output); err != nil {
		return fmt.Errorf("decode control plane response: %w", err)
	}
	return nil
}

func controlPlaneEndpoint(raw string) (string, error) {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return "", fmt.Errorf("invalid control_plane.url %q", raw)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return "", errors.New("control_plane.url must use http or https")
	}
	if endpoint.Scheme == "http" && !loopbackHost(endpoint.Hostname()) {
		return "", errors.New("control_plane.url must use https for a non-loopback host")
	}
	return strings.TrimRight(raw, "/"), nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
