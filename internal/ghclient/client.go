// Package ghclient reads and writes GitHub through the installed gh command.
// It never uses a shell.
package ghclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	commandTimeout = 30 * time.Second
	maxErrorBytes  = 512
	maxOutputBytes = 8 << 20

	StateOpen   = "OPEN"
	StateClosed = "CLOSED"
	StateMerged = "MERGED"

	ReviewChangesRequested = "CHANGES_REQUESTED"
)

var (
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,99})/[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,99})$`)
	// loginPattern is loose on purpose. Old accounts end in a hyphen and managed
	// accounts contain an underscore, and refusing a real login would retry its
	// comment forever.
	loginPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,98}(?:\[bot\])?$`)
	secretPattern = regexp.MustCompile(`(?i)(github_pat_[A-Za-z0-9_]+|gh[pousr]_[A-Za-z0-9_]+)`)
)

type ErrorKind string

const (
	ErrorCommand   ErrorKind = "command"
	ErrorTimeout   ErrorKind = "timeout"
	ErrorAuth      ErrorKind = "authentication"
	ErrorRateLimit ErrorKind = "rate_limit"
	ErrorMalformed ErrorKind = "malformed_output"
)

// Error is a failed gh command, with secrets removed from its detail.
type Error struct {
	Kind      ErrorKind
	Operation string
	Detail    string
	Err       error
}

func (e *Error) Error() string {
	message := "gh " + e.Operation + " failed (" + string(e.Kind) + ")"
	if e.Detail != "" {
		message += ": " + e.Detail
	}
	return message
}

func (e *Error) Unwrap() error { return e.Err }

// Client runs the gh executable found on PATH, which reads GH_TOKEN.
type Client struct {
	executable string
	timeout    time.Duration
	token      func(ctx context.Context, repository string) (string, error)
}

func New() *Client { return &Client{executable: "gh", timeout: commandTimeout} }

// WithToken gives each command a token for the repository it works on, in
// place of the process's GH_TOKEN.
func (c *Client) WithToken(token func(ctx context.Context, repository string) (string, error)) *Client {
	c.token = token
	return c
}

// environment is the process's own, with GH_TOKEN replaced by a token for
// repository when the client has a token source.
func (c *Client) environment(ctx context.Context, repository string) ([]string, error) {
	if c.token == nil || repository == "" {
		return nil, nil
	}
	token, err := c.token(ctx, repository)
	if err != nil {
		return nil, &Error{Kind: ErrorAuth, Operation: "get a token for " + repository, Detail: sanitize([]byte(err.Error())), Err: err}
	}
	environment := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GH_TOKEN=") && !strings.HasPrefix(entry, "GITHUB_TOKEN=") {
			environment = append(environment, entry)
		}
	}
	return append(environment, "GH_TOKEN="+token), nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxOutputBytes {
		return 0, errors.New("gh output exceeds 8 MiB")
	}
	return b.Buffer.Write(p)
}

func (c *Client) run(ctx context.Context, repository, operation string, input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	environment, err := c.environment(ctx, repository)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, c.executable, args...)
	command.Env = environment
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	var stdout limitedBuffer
	var stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	detail := sanitize(stderr.Bytes())
	if detail == "" {
		detail = sanitize([]byte(err.Error()))
	}
	return nil, &Error{Kind: classify(ctx, stderr.Bytes(), err), Operation: operation, Detail: detail, Err: err}
}

func classify(ctx context.Context, stderr []byte, err error) ErrorKind {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return ErrorTimeout
	}
	message := strings.ToLower(string(stderr) + " " + err.Error())
	if strings.Contains(message, "rate limit") || strings.Contains(message, "secondary rate") || strings.Contains(message, "abuse detection") {
		return ErrorRateLimit
	}
	if strings.Contains(message, "authentication") || strings.Contains(message, "not logged in") || strings.Contains(message, "bad credentials") || strings.Contains(message, "http 401") {
		return ErrorAuth
	}
	return ErrorCommand
}

func sanitize(output []byte) string {
	text := secretPattern.ReplaceAllString(strings.Join(strings.Fields(string(output)), " "), "[REDACTED]")
	if len(text) > maxErrorBytes {
		text = strings.ToValidUTF8(text[:maxErrorBytes], "") + "..."
	}
	return text
}

func decode(operation string, output []byte, target any) error {
	if err := json.Unmarshal(output, target); err != nil {
		return &Error{Kind: ErrorMalformed, Operation: operation, Detail: err.Error(), Err: err}
	}
	return nil
}

func validRepository(repository string) error {
	if !repositoryPattern.MatchString(repository) || strings.Contains(repository, "..") {
		return fmt.Errorf("invalid GitHub repository %q", repository)
	}
	return nil
}

// IsNotFound reports whether GitHub said the thing does not exist.
func IsNotFound(err error) bool {
	var failure *Error
	return errors.As(err, &failure) && failure.Kind == ErrorCommand && strings.Contains(strings.ToLower(failure.Detail), "http 404")
}

type Issue struct {
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	PullRequest json.RawMessage `json:"pull_request"`
}

// IsPR reports whether the number belongs to a PR. GitHub lists PRs as issues.
func (i Issue) IsPR() bool { return len(i.PullRequest) > 0 && string(i.PullRequest) != "null" }

// Issue reads the title and text of an issue.
func (c *Client) Issue(ctx context.Context, repository string, number int) (Issue, error) {
	var issue Issue
	if err := validRepository(repository); err != nil {
		return issue, err
	}
	output, err := c.run(ctx, repository, "read issue", nil, "api", "--method", "GET", fmt.Sprintf("repos/%s/issues/%d", repository, number))
	if err != nil {
		return issue, err
	}
	return issue, decode("read issue", output, &issue)
}

// PostComment adds a comment to an issue or PR.
func (c *Client) PostComment(ctx context.Context, repository string, number int, body string) error {
	if err := validRepository(repository); err != nil {
		return err
	}
	input, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return err
	}
	_, err = c.run(ctx, repository, "post comment", input, "api", "--method", "POST", fmt.Sprintf("repos/%s/issues/%d/comments", repository, number), "--input", "-")
	return err
}

// PRRef is the PR found for a branch.
type PRRef struct {
	Number int
	State  string
}

// FindPR returns the open PR for a branch, or otherwise the newest. PRs from
// forks are ignored, because anyone can name a fork's branch the same way.
func (c *Client) FindPR(ctx context.Context, repository, branch string) (*PRRef, error) {
	if err := validRepository(repository); err != nil {
		return nil, err
	}
	output, err := c.run(ctx, repository, "find PR", nil, "pr", "list", "--repo", repository, "--head", branch, "--state", "all", "--limit", "100", "--json", "number,state,isCrossRepository")
	if err != nil {
		return nil, err
	}
	var candidates []struct {
		Number            int    `json:"number"`
		State             string `json:"state"`
		IsCrossRepository bool   `json:"isCrossRepository"`
	}
	if err := decode("find PR", output, &candidates); err != nil {
		return nil, err
	}
	var found *PRRef
	for _, candidate := range candidates {
		if candidate.IsCrossRepository {
			continue
		}
		reference := &PRRef{Number: candidate.Number, State: candidate.State}
		if reference.State == StateOpen {
			return reference, nil
		}
		if found == nil || reference.Number > found.Number {
			found = reference
		}
	}
	return found, nil
}

// TokenScopes returns the classic token scopes reported by GitHub. Fine-grained
// tokens do not report scopes.
func (c *Client) TokenScopes(ctx context.Context) ([]string, error) {
	output, err := c.run(ctx, "", "read token scopes", nil, "api", "--method", "GET", "--include", "user")
	if err != nil {
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(output)), nil)
	if err != nil {
		return nil, &Error{Kind: ErrorMalformed, Operation: "read token scopes", Detail: "invalid response headers"}
	}
	defer response.Body.Close()
	var scopes []string
	for _, value := range response.Header.Values("X-OAuth-Scopes") {
		for _, scope := range strings.Split(value, ",") {
			if scope = strings.TrimSpace(scope); scope != "" {
				scopes = append(scopes, scope)
			}
		}
	}
	return scopes, nil
}
