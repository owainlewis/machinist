// Package githubapp mints short-lived GitHub tokens for one repository from a
// GitHub App. A run gets a token that can reach its own repository and nothing
// else, and that stops working an hour after it was made.
package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// TokenLifetime is how long GitHub keeps an installation token valid.
	TokenLifetime  = time.Hour
	defaultBaseURL = "https://api.github.com"
	maxKeyBytes    = 16 << 10
	maxReplyBytes  = 1 << 20
	requestTimeout = 30 * time.Second
)

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)

// Permissions are what a run's token may do: push branches, open and update
// pull requests, comment, and read check results. It cannot change workflows,
// settings, secrets or any other repository.
var Permissions = map[string]string{
	"contents":      "write",
	"pull_requests": "write",
	"issues":        "write",
	"checks":        "read",
	"statuses":      "read",
}

// App mints tokens as one GitHub App.
type App struct {
	id      int64
	key     *rsa.PrivateKey
	baseURL string
	client  *http.Client
	now     func() time.Time

	mu            sync.Mutex
	installations map[string]int64
	cached        map[string]token
	identity      *Identity
}

// Identity is the app's bot account, which commits made with its tokens should
// name so GitHub shows them as the app's.
type Identity struct {
	Slug  string
	Name  string
	Email string
}

// InstallURL is the page where a person installs the app on more
// repositories.
func (i Identity) InstallURL() string {
	return "https://github.com/apps/" + i.Slug + "/installations/new"
}

type token struct {
	value   string
	expires time.Time
}

// cacheMargin is how long a cached token must still have left to be reused.
const cacheMargin = 10 * time.Minute

// Load reads the app's private key. GitHub gives it as a PKCS#1 PEM file.
func Load(id int64, keyFile string) (*App, error) {
	file, err := os.Open(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read the GitHub App private key: %w", err)
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxKeyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read the GitHub App private key: %w", err)
	}
	if len(body) > maxKeyBytes {
		return nil, fmt.Errorf("the GitHub App private key %s is larger than %d bytes", keyFile, maxKeyBytes)
	}
	key, err := parseKey(body)
	if err != nil {
		return nil, fmt.Errorf("the GitHub App private key %s: %w", keyFile, err)
	}
	return New(id, key), nil
}

// New returns an app that talks to api.github.com.
func New(id int64, key *rsa.PrivateKey) *App {
	return &App{id: id, key: key, baseURL: defaultBaseURL, client: &http.Client{Timeout: requestTimeout}, now: time.Now, installations: map[string]int64{}, cached: map[string]token{}}
}

// WithBaseURL points the app at another API server, for tests.
func (a *App) WithBaseURL(url string, client *http.Client) *App {
	a.baseURL = strings.TrimSuffix(url, "/")
	a.client = client
	return a
}

func parseKey(body []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(body)
	if block == nil {
		return nil, errors.New("is not a PEM file")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("is not an RSA private key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("is not an RSA private key")
	}
	return key, nil
}

// jwt is the app's own short credential, used only to ask for tokens. GitHub
// allows ten minutes at most, and the issue time is set back to allow for
// clock drift.
func (a *App) jwt() (string, error) {
	now := a.now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": strconv.FormatInt(a.id, 10)})
	if err != nil {
		return "", err
	}
	signed := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signed))
	signature, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// Token returns a new token for repository, as owner/name, that can reach
// that repository alone with Permissions. A run gets a new one, so it has the
// whole hour.
func (a *App) Token(ctx context.Context, repository string) (string, error) {
	minted, err := a.mint(ctx, repository)
	return minted.value, err
}

// CachedToken is Token for the control plane's own short calls. It reuses a
// token while it has more than ten minutes left.
func (a *App) CachedToken(ctx context.Context, repository string) (string, error) {
	a.mu.Lock()
	cached, ok := a.cached[repository]
	a.mu.Unlock()
	if ok && cached.expires.Sub(a.now()) > cacheMargin {
		return cached.value, nil
	}
	minted, err := a.mint(ctx, repository)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.cached[repository] = minted
	a.mu.Unlock()
	return minted.value, nil
}

func (a *App) mint(ctx context.Context, repository string) (token, error) {
	if !repositoryPattern.MatchString(repository) {
		return token{}, fmt.Errorf("invalid repository %q", repository)
	}
	_, name, _ := strings.Cut(repository, "/")
	installation, err := a.installation(ctx, repository)
	if err != nil {
		return token{}, err
	}
	request := map[string]any{"repositories": []string{name}, "permissions": Permissions}
	var reply struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	status, err := a.call(ctx, http.MethodPost, "/app/installations/"+strconv.FormatInt(installation, 10)+"/access_tokens", request, &reply)
	if status == http.StatusNotFound || status == http.StatusUnprocessableEntity {
		// The app was removed from the repository, or lost a permission.
		a.forget(repository)
	}
	if err != nil {
		return token{}, fmt.Errorf("get a token for %s: %w", repository, err)
	}
	if reply.Token == "" {
		return token{}, fmt.Errorf("get a token for %s: GitHub returned no token", repository)
	}
	return token{value: reply.Token, expires: reply.ExpiresAt}, nil
}

func (a *App) installation(ctx context.Context, repository string) (int64, error) {
	a.mu.Lock()
	id, ok := a.installations[repository]
	a.mu.Unlock()
	if ok {
		return id, nil
	}
	var reply struct {
		ID int64 `json:"id"`
	}
	status, err := a.call(ctx, http.MethodGet, "/repos/"+repository+"/installation", nil, &reply)
	if status == http.StatusNotFound {
		return 0, fmt.Errorf("the GitHub App is not installed on %s", repository)
	}
	if err != nil {
		return 0, fmt.Errorf("find the GitHub App's installation on %s: %w", repository, err)
	}
	a.mu.Lock()
	a.installations[repository] = reply.ID
	a.mu.Unlock()
	return reply.ID, nil
}

// Installed reports, as an error, why the app cannot reach a repository. It is
// nil when the app is installed there.
func (a *App) Installed(ctx context.Context, repository string) error {
	if !repositoryPattern.MatchString(repository) {
		return fmt.Errorf("%q is not a repository name like owner/name", repository)
	}
	_, err := a.installation(ctx, repository)
	return err
}

func (a *App) forget(repository string) {
	a.mu.Lock()
	delete(a.installations, repository)
	a.mu.Unlock()
}

// Identity looks up the app's bot account once: GitHub names it after the
// app's slug, and its commit email carries the bot user's ID.
func (a *App) Identity(ctx context.Context) (Identity, error) {
	a.mu.Lock()
	known := a.identity
	a.mu.Unlock()
	if known != nil {
		return *known, nil
	}
	var app struct {
		Slug string `json:"slug"`
	}
	if _, err := a.call(ctx, http.MethodGet, "/app", nil, &app); err != nil {
		return Identity{}, fmt.Errorf("look up the GitHub App: %w", err)
	}
	if app.Slug == "" {
		return Identity{}, errors.New("look up the GitHub App: GitHub gave no slug")
	}
	name := app.Slug + "[bot]"
	var user struct {
		ID int64 `json:"id"`
	}
	// The users API takes no app JWT, and a bot's account is public.
	if _, err := a.request(ctx, http.MethodGet, "/users/"+url.PathEscape(name), "", nil, &user); err != nil {
		return Identity{}, fmt.Errorf("look up the GitHub App's bot user %s: %w", name, err)
	}
	if user.ID == 0 {
		return Identity{}, fmt.Errorf("look up the GitHub App's bot user %s: GitHub gave no ID", name)
	}
	identity := Identity{Slug: app.Slug, Name: name, Email: fmt.Sprintf("%d+%s@users.noreply.github.com", user.ID, name)}
	a.mu.Lock()
	a.identity = &identity
	a.mu.Unlock()
	return identity, nil
}

func (a *App) call(ctx context.Context, method, path string, body, reply any) (int, error) {
	token, err := a.jwt()
	if err != nil {
		return 0, err
	}
	return a.request(ctx, method, path, token, body, reply)
}

func (a *App) request(ctx context.Context, method, path, token string, body, reply any) (int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, reader)
	if err != nil {
		return 0, err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := a.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxReplyBytes))
	if err != nil {
		return response.StatusCode, err
	}
	if response.StatusCode/100 != 2 {
		// GitHub's error replies hold a message and a docs link, never a token.
		var failure struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &failure)
		if failure.Message == "" {
			failure.Message = http.StatusText(response.StatusCode)
		}
		return response.StatusCode, fmt.Errorf("GitHub replied %d: %s", response.StatusCode, failure.Message)
	}
	if err := json.Unmarshal(data, reply); err != nil {
		return response.StatusCode, fmt.Errorf("read GitHub's reply: %w", err)
	}
	return response.StatusCode, nil
}
