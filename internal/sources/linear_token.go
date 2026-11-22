package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// LinearTokenEndpoint is where Linear hands out OAuth access tokens.
const LinearTokenEndpoint = "https://api.linear.app/oauth/token"

// linearScope is what the Machinist app asks for: it reads issues and writes
// their states and comments, and app:assignable and app:mentionable let people
// delegate or assign issues to it. Without those two, Linear refuses to give
// the app an issue.
const linearScope = "read,write,app:assignable,app:mentionable"

// maxRenewMargin is how long before a token expires Machinist gets a new one.
// A short-lived token is renewed at half its life instead.
const maxRenewMargin = time.Hour

// LinearCredentials gets the Machinist app's Linear access token with the
// client_credentials grant, and gets a new one before it expires or when
// Linear turns it down. Tokens are held in memory only.
type LinearCredentials struct {
	ClientID     string
	ClientSecret string
	// Endpoint is Linear's token endpoint. It defaults to LinearTokenEndpoint.
	Endpoint string
	Client   *http.Client

	now     func() time.Time
	mu      sync.Mutex
	token   string
	renewAt time.Time
}

// Token returns a token that is not near expiry, and fetches one when it must.
func (c *LinearCredentials) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.clock().Before(c.renewAt) {
		return c.token, nil
	}
	return c.fetch(ctx)
}

// Renew fetches a new token in place of stale, the one Linear turned down. If
// another caller has already replaced stale, its token is used instead, so one
// rejection fetches one token.
func (c *LinearCredentials) Renew(ctx context.Context, stale string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.token != stale && c.clock().Before(c.renewAt) {
		return c.token, nil
	}
	return c.fetch(ctx)
}

// fetch asks Linear for a new token. The caller holds mu.
func (c *LinearCredentials) fetch(ctx context.Context) (string, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
		"scope":         {linearScope},
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = LinearTokenEndpoint
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("linear: get an access token: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("linear: get an access token: %w", err)
	}
	var answer struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(data, &answer)
	if response.StatusCode != http.StatusOK || answer.AccessToken == "" {
		reason := answer.Description
		if reason == "" {
			reason = answer.Error
		}
		if reason == "" {
			reason = fmt.Sprintf("status %d", response.StatusCode)
		}
		return "", fmt.Errorf("linear: get an access token from the client credentials: %s", reason)
	}
	if answer.ExpiresIn <= 0 {
		return "", errors.New("linear: the access token came without an expiry")
	}
	lifetime := time.Duration(answer.ExpiresIn) * time.Second
	c.token = answer.AccessToken
	c.renewAt = c.clock().Add(lifetime - min(maxRenewMargin, lifetime/2))
	return c.token, nil
}

func (c *LinearCredentials) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}
