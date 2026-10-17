package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type GitHubConnectionStatus struct {
	Configured bool   `json:"configured"`
	Connected  bool   `json:"connected"`
	ClientID   string `json:"client_id"`
	InstallURL string `json:"install_url"`
	Account    string `json:"account,omitempty"`
}
type GitHubDeviceAuthorization struct {
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}
type GitHubConnectionPoll struct {
	Status  string `json:"status"`
	Account string `json:"account,omitempty"`
}
type GitHubConnectedRepository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
	CanWrite      bool   `json:"can_write"`
	AccessError   string `json:"access_error,omitempty"`
	Permissions   struct {
		Push bool `json:"push"`
	} `json:"permissions"`
}
type githubCredentials struct {
	ClientID     string    `json:"client_id"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	Account      string    `json:"account"`
}
type GitHubConnection struct {
	mu                        sync.Mutex
	dir, clientID, installURL string
	client                    *http.Client
	apiURL, oauthURL          string
	credentials               githubCredentials
	deviceCode                string
	deadline, nextPoll        time.Time
	interval                  time.Duration
	allowed                   map[string]bool
}

func NewGitHubConnection(dataDir, clientID, installURL string) (*GitHubConnection, error) {
	c := &GitHubConnection{dir: filepath.Join(dataDir, "github"), client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, apiURL: "https://api.github.com", oauthURL: "https://github.com", allowed: map[string]bool{}}
	if err := c.Configure(clientID, installURL); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(c.dir, "credentials.json"))
	if err == nil {
		if err = json.Unmarshal(raw, &c.credentials); err != nil {
			return nil, errors.New("invalid saved GitHub credentials")
		}
		if c.credentials.ClientID != c.clientID {
			c.credentials = githubCredentials{}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return c, nil
}
func (c *GitHubConnection) Configure(clientID, installURL string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if clientID != "" {
		if strings.ContainsAny(clientID, " \t\r\n/") || len(clientID) > 200 {
			return errors.New("invalid GitHub App client ID")
		}
		u, err := url.Parse(installURL)
		if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || !strings.HasPrefix(u.Path, "/apps/") || !strings.HasSuffix(u.Path, "/installations/new") || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("invalid GitHub App installation URL")
		}
	}
	if c.clientID != "" && c.clientID != clientID && c.credentials.AccessToken != "" {
		return errors.New("disconnect before changing GitHub App")
	}
	c.clientID, c.installURL = clientID, installURL
	return nil
}
func (c *GitHubConnection) Status(context.Context) (GitHubConnectionStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return GitHubConnectionStatus{c.clientID != "", c.credentials.AccessToken != "", c.clientID, c.installURL, c.credentials.Account}, nil
}
func (c *GitHubConnection) Start(ctx context.Context) (GitHubDeviceAuthorization, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out GitHubDeviceAuthorization
	if c.clientID == "" {
		return out, errors.New("configure a GitHub App first")
	}
	raw, err := c.oauth(ctx, "/login/device/code", url.Values{"client_id": {c.clientID}})
	if err != nil {
		return out, err
	}
	var reply struct {
		GitHubDeviceAuthorization
		DeviceCode string `json:"device_code"`
	}
	if err = json.Unmarshal(raw, &reply); err != nil {
		return out, err
	}
	u, e := url.Parse(reply.VerificationURI)
	if e != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || reply.DeviceCode == "" || reply.UserCode == "" || reply.ExpiresIn <= 0 {
		return out, errors.New("invalid GitHub device authorization response")
	}
	if reply.Interval < 1 {
		reply.Interval = 5
	}
	c.deviceCode = reply.DeviceCode
	c.deadline = time.Now().Add(time.Duration(reply.ExpiresIn) * time.Second)
	c.interval = time.Duration(reply.Interval) * time.Second
	c.nextPoll = time.Now().Add(c.interval)
	return reply.GitHubDeviceAuthorization, nil
}
func (c *GitHubConnection) Poll(ctx context.Context) (GitHubConnectionPoll, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.deviceCode == "" {
		return GitHubConnectionPoll{}, errors.New("start GitHub sign-in first")
	}
	if time.Now().After(c.deadline) {
		c.deviceCode = ""
		return GitHubConnectionPoll{Status: "expired"}, nil
	}
	if time.Now().Before(c.nextPoll) {
		return GitHubConnectionPoll{Status: "pending"}, nil
	}
	c.nextPoll = time.Now().Add(c.interval)
	raw, err := c.oauth(ctx, "/login/oauth/access_token", url.Values{"client_id": {c.clientID}, "device_code": {c.deviceCode}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}})
	if err != nil {
		return GitHubConnectionPoll{}, err
	}
	var reply githubTokenReply
	if err = json.Unmarshal(raw, &reply); err != nil {
		return GitHubConnectionPoll{}, err
	}
	switch reply.Error {
	case "authorization_pending":
		return GitHubConnectionPoll{Status: "pending"}, nil
	case "slow_down":
		c.interval += 5 * time.Second
		c.nextPoll = time.Now().Add(c.interval)
		return GitHubConnectionPoll{Status: "pending"}, nil
	case "access_denied":
		c.deviceCode = ""
		return GitHubConnectionPoll{Status: "denied"}, nil
	case "expired_token":
		c.deviceCode = ""
		return GitHubConnectionPoll{Status: "expired"}, nil
	}
	if reply.Error != "" || reply.AccessToken == "" {
		return GitHubConnectionPoll{}, errors.New("GitHub sign-in failed")
	}
	cred := reply.credentials()
	raw, err = c.request(ctx, "GET", "/user", nil, cred.AccessToken)
	if err != nil {
		return GitHubConnectionPoll{}, err
	}
	var user GitHubActor
	if json.Unmarshal(raw, &user) != nil || user.Login == "" {
		return GitHubConnectionPoll{}, errors.New("GitHub account unavailable")
	}
	cred.Account = user.Login
	if err = c.save(cred); err != nil {
		return GitHubConnectionPoll{}, err
	}
	c.credentials = cred
	c.deviceCode = ""
	return GitHubConnectionPoll{Status: "connected", Account: user.Login}, nil
}

type githubTokenReply struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
}

func (t githubTokenReply) credentials() githubCredentials {
	c := githubCredentials{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken}
	if t.ExpiresIn > 0 {
		c.ExpiresAt = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	}
	return c
}
func (c *GitHubConnection) save(v githubCredentials) error {
	v.ClientID = c.clientID
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(c.dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(c.dir, "credentials-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		err = json.NewEncoder(f).Encode(v)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, filepath.Join(c.dir, "credentials.json"))
}
func (c *GitHubConnection) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := os.Remove(filepath.Join(c.dir, "credentials.json"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	c.credentials = githubCredentials{}
	c.deviceCode = ""
	c.allowed = map[string]bool{}
	return nil
}
func (c *GitHubConnection) token(ctx context.Context) (string, error) {
	if c.credentials.AccessToken == "" {
		return "", errors.New("connect GitHub first")
	}
	if !c.credentials.ExpiresAt.IsZero() && time.Now().Add(time.Minute).After(c.credentials.ExpiresAt) {
		if c.credentials.RefreshToken == "" {
			return "", errors.New("GitHub connection expired; reconnect")
		}
		raw, err := c.oauth(ctx, "/login/oauth/access_token", url.Values{"client_id": {c.clientID}, "grant_type": {"refresh_token"}, "refresh_token": {c.credentials.RefreshToken}})
		if err != nil {
			return "", err
		}
		var reply githubTokenReply
		if json.Unmarshal(raw, &reply) != nil || reply.Error != "" || reply.AccessToken == "" || reply.RefreshToken == "" {
			return "", errors.New("GitHub refresh failed; reconnect")
		}
		next := reply.credentials()
		next.Account = c.credentials.Account
		if err = c.save(next); err != nil {
			return "", err
		}
		c.credentials = next
	}
	return c.credentials.AccessToken, nil
}
func (c *GitHubConnection) API(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	path = "/" + strings.TrimPrefix(path, "/")
	u, err := url.Parse(path)
	if err != nil || u.IsAbs() || u.Host != "" || (strings.Contains(u.Path, "..") && !(method == "GET" && maintenanceComparePath.MatchString(u.Path))) || u.RawPath != "" {
		return nil, errors.New("invalid GitHub API path")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "repos" {
		if !c.allowed[strings.ToLower(parts[1]+"/"+parts[2])] {
			return nil, errors.New("repository is not available to this GitHub App installation")
		}
	} else if u.Path == "/graphql" && method == "POST" {
		var input struct {
			Query     string `json:"query"`
			Variables struct {
				Owner, Name string
				Number      int
			} `json:"variables"`
		}
		if json.Unmarshal(body, &input) != nil || input.Query != maintenancePRQuery || input.Variables.Number < 1 || !c.allowed[strings.ToLower(input.Variables.Owner+"/"+input.Variables.Name)] {
			return nil, errors.New("unsupported or unscoped GitHub GraphQL operation")
		}
		// Forward only the validated, canonical fields. JSON's case-insensitive
		// struct decoding must not authorize a different lowercase wire query.
		body, _ = json.Marshal(map[string]any{"query": maintenancePRQuery, "variables": map[string]any{"owner": input.Variables.Owner, "name": input.Variables.Name, "number": input.Variables.Number}})
	} else if method != "GET" || !(u.Path == "/user" || u.Path == "/user/installations" || (len(parts) == 3 && parts[0] == "user" && parts[1] == "installations")) {
		return nil, errors.New("unsupported GitHub API path")
	}
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	return c.request(ctx, method, path, body, token)
}
func (c *GitHubConnection) Repositories(ctx context.Context) ([]GitHubConnectedRepository, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// A failed access refresh must not leave a stale repository allowlist usable.
	c.allowed = map[string]bool{}
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	var repos []GitHubConnectedRepository
	allowed := map[string]bool{}
	for page := 1; ; page++ {
		if page > 100 {
			return nil, errors.New("too many GitHub App installation pages")
		}
		raw, err := c.request(ctx, "GET", fmt.Sprintf("/user/installations?per_page=100&page=%d", page), nil, token)
		if err != nil {
			return nil, err
		}
		var list struct {
			Installations []struct {
				ID          int64             `json:"id"`
				Permissions map[string]string `json:"permissions"`
			} `json:"installations"`
		}
		if err = json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
		for _, install := range list.Installations {
			for rp := 1; ; rp++ {
				if rp > 100 {
					return nil, errors.New("too many installed repository pages")
				}
				raw, err = c.request(ctx, "GET", fmt.Sprintf("/user/installations/%d/repositories?per_page=100&page=%d", install.ID, rp), nil, token)
				if err != nil {
					return nil, err
				}
				var batch struct {
					Repositories []GitHubConnectedRepository `json:"repositories"`
				}
				if err = json.Unmarshal(raw, &batch); err != nil {
					return nil, err
				}
				for _, repo := range batch.Repositories {
					repo.CanWrite = false
					repo.AccessError = githubInstallationAccessError(install.Permissions, repo.Permissions.Push)
					repo.CanWrite = repo.AccessError == ""
					if len(strings.Split(repo.FullName, "/")) != 2 {
						return nil, errors.New("invalid installed repository")
					}
					if !allowed[strings.ToLower(repo.FullName)] {
						repos = append(repos, repo)
						allowed[strings.ToLower(repo.FullName)] = true
					}
				}
				if len(batch.Repositories) < 100 {
					break
				}
			}
		}
		if len(list.Installations) < 100 {
			break
		}
	}
	c.allowed = allowed
	return repos, nil
}
func (c *GitHubConnection) oauth(ctx context.Context, path string, values url.Values) ([]byte, error) {
	return c.do(ctx, "POST", c.oauthURL+path, []byte(values.Encode()), "application/x-www-form-urlencoded", "")
}
func (c *GitHubConnection) request(ctx context.Context, method, path string, body []byte, token string) ([]byte, error) {
	return c.do(ctx, method, c.apiURL+path, body, "application/json", token)
}
func (c *GitHubConnection) do(ctx context.Context, method, target string, body []byte, contentType, token string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if strings.Contains(req.URL.Path, "/merge-async") {
		req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	}
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, errors.New("GitHub request failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
	if err != nil {
		return nil, errors.New("GitHub response failed")
	}
	if len(raw) > 8<<20 {
		return nil, errors.New("GitHub response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub request failed (HTTP %d)", resp.StatusCode)
	}
	return raw, nil
}

// Token is for execution-host credential injection only. Never expose its result through HTTP.
func (c *GitHubConnection) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token(ctx)
}

// Both installation grants and the signing-in user's repository access apply
// to GitHub App user tokens. Readiness must prove both boundaries.
func githubInstallationAccessError(permissions map[string]string, push bool) string {
	var missing []string
	for _, permission := range []string{"contents", "issues", "pull_requests"} {
		if permissions[permission] != "write" {
			missing = append(missing, permission+":write")
		}
	}
	for _, permission := range []string{"checks", "statuses"} {
		if permissions[permission] != "read" && permissions[permission] != "write" {
			missing = append(missing, permission+":read")
		}
	}
	if len(missing) > 0 {
		return "GitHub App installation needs permission approval: " + strings.Join(missing, ", ")
	}
	if !push {
		return "Your GitHub account needs write access to this repository"
	}
	return ""
}
