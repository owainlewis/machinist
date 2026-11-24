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
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
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
	secretPattern     = regexp.MustCompile(`(?i)(github_pat_[A-Za-z0-9_]+|gh[pousr]_[A-Za-z0-9_]+)`)
	httpStatusPattern = regexp.MustCompile(`(?i)\bhttp\s+([1-5][0-9]{2})\b`)
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
	// Some commands print their answer and still exit non-zero, such as gh pr
	// checks while checks are pending, so stdout comes back with the error.
	return stdout.Bytes(), &Error{Kind: classify(ctx, stderr.Bytes(), err), Operation: operation, Detail: detail, Err: err}
}

func classify(ctx context.Context, stderr []byte, err error) ErrorKind {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return ErrorTimeout
	}
	message := strings.ToLower(string(stderr) + " " + err.Error())
	if hasRateLimitMessage(message) {
		return ErrorRateLimit
	}
	if strings.Contains(message, "authentication") || strings.Contains(message, "not logged in") || strings.Contains(message, "bad credentials") || strings.Contains(message, "http 401") {
		return ErrorAuth
	}
	return ErrorCommand
}

func hasRateLimitMessage(message string) bool {
	return strings.Contains(message, "rate limit") ||
		strings.Contains(message, "secondary rate") ||
		strings.Contains(message, "abuse detection") ||
		strings.Contains(message, "too many requests") ||
		strings.Contains(message, "http 429")
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

// HTTPStatus extracts an HTTP status code reported by gh (such as "HTTP 404"),
// or 0 when the error does not carry one.
func HTTPStatus(err error) int {
	if err == nil {
		return 0
	}
	text := err.Error()
	var failure *Error
	if errors.As(err, &failure) && failure.Detail != "" {
		text = failure.Detail
	}
	match := httpStatusPattern.FindStringSubmatch(text)
	if len(match) != 2 {
		return 0
	}
	code, convErr := strconv.Atoi(match[1])
	if convErr != nil {
		return 0
	}
	return code
}

// IsRateLimit reports whether err indicates a GitHub rate limit.
func IsRateLimit(err error) bool {
	if err == nil {
		return false
	}
	var failure *Error
	if errors.As(err, &failure) && failure.Kind == ErrorRateLimit {
		return true
	}
	return hasRateLimitMessage(strings.ToLower(err.Error()))
}

// IsPermanentClientError reports whether err is a permanent GitHub 4xx client
// error that will not succeed on retry. Rate limits (429 and 403 rate limits),
// timeouts (408) and authentication failures (401) are treated as retryable.
func IsPermanentClientError(err error) bool {
	if err == nil || IsRateLimit(err) {
		return false
	}
	var failure *Error
	if errors.As(err, &failure) && failure.Kind != ErrorCommand {
		return false
	}
	status := HTTPStatus(err)
	return status >= 400 && status < 500 &&
		status != http.StatusUnauthorized &&
		status != http.StatusRequestTimeout &&
		status != http.StatusTooManyRequests
}

// IsTransient reports whether err is a temporary failure (timeout, rate limit,
// authentication error, GitHub 5xx, or network failure) that may succeed on
// retry rather than a refusal from GitHub.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	if IsRateLimit(err) {
		return true
	}
	var failure *Error
	if errors.As(err, &failure) {
		switch failure.Kind {
		case ErrorTimeout, ErrorRateLimit, ErrorAuth:
			return true
		}
	}
	status := HTTPStatus(err)
	if status >= 500 || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests {
		return true
	}
	if status == 0 {
		return hasTransientTransportMessage(strings.ToLower(err.Error()))
	}
	return false
}

func hasTransientTransportMessage(message string) bool {
	for _, needle := range []string{
		"connection reset",
		"connection refused",
		"i/o timeout",
		"tls handshake timeout",
		"no such host",
		"network is unreachable",
		"unexpected eof",
		"server closed idle connection",
		"error connecting to api.github.com",
		"temporary failure in name resolution",
	} {
		if strings.Contains(message, needle) {
			return true
		}
	}
	return false
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
	// MergedAt and ClosedAt are zero until the PR merges or closes.
	MergedAt  time.Time
	ClosedAt  time.Time
	CreatedAt time.Time
}

// FindPR returns the open PR for a branch, or otherwise the newest. PRs from
// forks are ignored, because anyone can name a fork's branch the same way.
func (c *Client) FindPR(ctx context.Context, repository, branch string) (*PRRef, error) {
	if err := validRepository(repository); err != nil {
		return nil, err
	}
	output, err := c.run(ctx, repository, "find PR", nil, "pr", "list", "--repo", repository, "--head", branch, "--state", "all", "--limit", "100", "--json", "number,state,isCrossRepository,mergedAt,closedAt,createdAt")
	if err != nil {
		return nil, err
	}
	var candidates []struct {
		Number            int        `json:"number"`
		State             string     `json:"state"`
		IsCrossRepository bool       `json:"isCrossRepository"`
		MergedAt          *time.Time `json:"mergedAt"`
		ClosedAt          *time.Time `json:"closedAt"`
		CreatedAt         *time.Time `json:"createdAt"`
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
		if candidate.MergedAt != nil {
			reference.MergedAt = *candidate.MergedAt
		}
		if candidate.ClosedAt != nil {
			reference.ClosedAt = *candidate.ClosedAt
		}
		if candidate.CreatedAt != nil {
			reference.CreatedAt = *candidate.CreatedAt
		}
		if reference.State == StateOpen {
			return reference, nil
		}
		if found == nil || reference.Number > found.Number {
			found = reference
		}
	}
	return found, nil
}

// PullRequest is one pull request as gh pr view reports it.
type PullRequest struct {
	Number           int    `json:"number"`
	URL              string `json:"url"`
	State            string `json:"state"`
	MergeStateStatus string `json:"mergeStateStatus"`
	HeadRefOid       string `json:"headRefOid"`
}

// PullRequest reads a pull request's state, whether its branch is behind and
// its head commit.
func (c *Client) PullRequest(ctx context.Context, repository string, number int) (PullRequest, error) {
	var pr PullRequest
	if err := validRepository(repository); err != nil {
		return pr, err
	}
	output, err := c.run(ctx, repository, "read pull request", nil, "pr", "view", strconv.Itoa(number), "--repo", repository, "--json", "number,url,state,mergeStateStatus,headRefOid")
	if err != nil {
		return pr, err
	}
	return pr, decode("read pull request", output, &pr)
}

// Check is one check on a pull request's head commit. Bucket is pass, fail,
// pending, skipping or cancel.
type Check struct {
	Name   string `json:"name"`
	Bucket string `json:"bucket"`
	Link   string `json:"link"`
}

// Checks lists the checks on a pull request's head commit. A pull request
// with none has an empty list.
func (c *Client) Checks(ctx context.Context, repository string, number int) ([]Check, error) {
	if err := validRepository(repository); err != nil {
		return nil, err
	}
	output, err := c.run(ctx, repository, "read checks", nil, "pr", "checks", strconv.Itoa(number), "--repo", repository, "--json", "name,bucket,link")
	var checks []Check
	if json.Unmarshal(output, &checks) == nil {
		// gh exits 8 while checks are pending, with the list on stdout.
		return checks, nil
	}
	var failure *Error
	if errors.As(err, &failure) && strings.Contains(failure.Detail, "no checks reported") {
		return nil, nil
	}
	if err == nil {
		err = &Error{Kind: ErrorMalformed, Operation: "read checks", Detail: "gh printed no checks"}
	}
	return nil, err
}

// MergePR merges a pull request and deletes its branch, only if its head is
// still head. It uses the first of a merge commit, a squash and a rebase that
// the repository allows, or a merge commit when the settings cannot be read,
// so GitHub's answer says what is wrong.
func (c *Client) MergePR(ctx context.Context, repository string, number int, head string) error {
	if err := validRepository(repository); err != nil {
		return err
	}
	_, err := c.run(ctx, repository, "merge pull request", nil, "pr", "merge", strconv.Itoa(number), "--repo", repository, "--"+c.mergeMethod(ctx, repository), "--delete-branch", "--match-head-commit", head)
	return err
}

func (c *Client) mergeMethod(ctx context.Context, repository string) string {
	const fallback = "merge"
	output, err := c.run(ctx, repository, "read merge settings", nil, "api", "--method", "GET", "repos/"+repository)
	if err != nil {
		return fallback
	}
	var settings struct {
		AllowMergeCommit *bool `json:"allow_merge_commit"`
		AllowSquashMerge *bool `json:"allow_squash_merge"`
		AllowRebaseMerge *bool `json:"allow_rebase_merge"`
	}
	if json.Unmarshal(output, &settings) != nil {
		return fallback
	}
	for _, method := range []struct {
		name    string
		allowed *bool
	}{
		{"merge", settings.AllowMergeCommit},
		{"squash", settings.AllowSquashMerge},
		{"rebase", settings.AllowRebaseMerge},
	} {
		if method.allowed != nil && *method.allowed {
			return method.name
		}
	}
	return fallback
}

// UpdateBranch brings a pull request's branch up to date with its base.
func (c *Client) UpdateBranch(ctx context.Context, repository string, number int) error {
	if err := validRepository(repository); err != nil {
		return err
	}
	_, err := c.run(ctx, repository, "update branch", nil, "pr", "update-branch", strconv.Itoa(number), "--repo", repository)
	return err
}

// Review is one submitted review of a PR.
type Review struct {
	Login       string
	Bot         bool
	SubmittedAt time.Time
}

// Reviews lists a PR's submitted reviews, oldest first. Pending reviews,
// which only their author can see, are left out.
func (c *Client) Reviews(ctx context.Context, repository string, number int) ([]Review, error) {
	if err := validRepository(repository); err != nil {
		return nil, err
	}
	output, err := c.run(ctx, repository, "list reviews", nil, "api", "--method", "GET", "--paginate", "--slurp", fmt.Sprintf("repos/%s/pulls/%d/reviews?per_page=100", repository, number))
	if err != nil {
		return nil, err
	}
	var pages [][]struct {
		User *struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
		State       string     `json:"state"`
		SubmittedAt *time.Time `json:"submitted_at"`
	}
	if err := decode("list reviews", output, &pages); err != nil {
		return nil, err
	}
	reviews := []Review{}
	for _, page := range pages {
		for _, review := range page {
			if review.State == "PENDING" || review.SubmittedAt == nil {
				continue
			}
			found := Review{SubmittedAt: *review.SubmittedAt}
			if review.User != nil {
				found.Login = review.User.Login
				found.Bot = review.User.Type == "Bot" || strings.HasSuffix(review.User.Login, "[bot]")
			}
			reviews = append(reviews, found)
		}
	}
	slices.SortStableFunc(reviews, func(a, b Review) int { return a.SubmittedAt.Compare(b.SubmittedAt) })
	return reviews, nil
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

// Access is what a login may do on a repository and whether its default
// branch is protected.
type Access struct {
	DefaultBranch string
	Protected     bool
	// Admin is true when the login can change settings, and so get around
	// branch protection.
	Admin bool
}

// RepositoryAccess reads a repository's default branch, whether that branch
// is protected and whether the login is an admin there.
func (c *Client) RepositoryAccess(ctx context.Context, repository string) (Access, error) {
	var access Access
	if err := validRepository(repository); err != nil {
		return access, err
	}
	output, err := c.run(ctx, repository, "read repository", nil, "api", "--method", "GET", "repos/"+repository)
	if err != nil {
		return access, err
	}
	var found struct {
		DefaultBranch string `json:"default_branch"`
		Permissions   struct {
			Admin bool `json:"admin"`
		} `json:"permissions"`
	}
	if err := decode("read repository", output, &found); err != nil {
		return access, err
	}
	access.DefaultBranch, access.Admin = found.DefaultBranch, found.Permissions.Admin
	if access.DefaultBranch == "" {
		return access, &Error{Kind: ErrorMalformed, Operation: "read repository", Detail: "no default branch"}
	}
	output, err = c.run(ctx, repository, "read branch", nil, "api", "--method", "GET", "repos/"+repository+"/branches/"+url.PathEscape(access.DefaultBranch))
	if err != nil {
		return access, err
	}
	var branch struct {
		Protected bool `json:"protected"`
	}
	if err := decode("read branch", output, &branch); err != nil {
		return access, err
	}
	access.Protected = branch.Protected
	return access, nil
}
