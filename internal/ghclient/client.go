// Package ghclient reads and writes GitHub through the installed gh command.
// It never uses a shell.
package ghclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
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
}

func New() *Client { return &Client{executable: "gh", timeout: commandTimeout} }

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxOutputBytes {
		return 0, errors.New("gh output exceeds 8 MiB")
	}
	return b.Buffer.Write(p)
}

func (c *Client) run(ctx context.Context, operation string, input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	command := exec.CommandContext(ctx, c.executable, args...)
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	var stdout limitedBuffer
	var stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
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

// Comment is one issue or PR conversation comment.
type Comment struct {
	ID     int64
	Body   string
	Author string
	Number int
	OnPR   bool
}

type commentResponse struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	User    *struct {
		Login string `json:"login"`
	} `json:"user"`
}

// commentTarget reads a comment's issue or PR number from its web address. The
// comment list has no other field that separates the two.
func commentTarget(address string) (int, bool, error) {
	parsed, err := url.Parse(address)
	if err != nil {
		return 0, false, err
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || (parts[2] != "issues" && parts[2] != "pull") {
		return 0, false, fmt.Errorf("unexpected comment address %q", address)
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number < 1 {
		return 0, false, fmt.Errorf("unexpected comment address %q", address)
	}
	return number, parts[2] == "pull", nil
}

// Comments lists issue and PR conversation comments updated since a time.
func (c *Client) Comments(ctx context.Context, repository string, since time.Time) ([]Comment, error) {
	if err := validRepository(repository); err != nil {
		return nil, err
	}
	query := url.Values{"since": {since.UTC().Format(time.RFC3339)}, "sort": {"updated"}, "direction": {"asc"}, "per_page": {"100"}}
	output, err := c.run(ctx, "list comments", nil, "api", "--method", "GET", "--paginate", "--slurp", "repos/"+repository+"/issues/comments?"+query.Encode())
	if err != nil {
		return nil, err
	}
	var pages [][]commentResponse
	if err := decode("list comments", output, &pages); err != nil {
		return nil, err
	}
	comments := []Comment{}
	for _, page := range pages {
		for _, raw := range page {
			number, onPR, err := commentTarget(raw.HTMLURL)
			if err != nil {
				return nil, &Error{Kind: ErrorMalformed, Operation: "list comments", Detail: err.Error(), Err: err}
			}
			comment := Comment{ID: raw.ID, Body: raw.Body, Number: number, OnPR: onPR}
			if raw.User != nil {
				comment.Author = raw.User.Login
			}
			comments = append(comments, comment)
		}
	}
	return comments, nil
}

type Issue struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Issue reads the title and text of an issue.
func (c *Client) Issue(ctx context.Context, repository string, number int) (Issue, error) {
	var issue Issue
	if err := validRepository(repository); err != nil {
		return issue, err
	}
	output, err := c.run(ctx, "read issue", nil, "api", "--method", "GET", fmt.Sprintf("repos/%s/issues/%d", repository, number))
	if err != nil {
		return issue, err
	}
	return issue, decode("read issue", output, &issue)
}

// Permission returns the permission GitHub reports for a login.
func (c *Client) Permission(ctx context.Context, repository, login string) (string, error) {
	if err := validRepository(repository); err != nil {
		return "", err
	}
	if !loginPattern.MatchString(login) {
		return "", fmt.Errorf("invalid GitHub login %q", login)
	}
	output, err := c.run(ctx, "read permission", nil, "api", "--method", "GET", "repos/"+repository+"/collaborators/"+url.PathEscape(login)+"/permission")
	if err != nil {
		var failure *Error
		if errors.As(err, &failure) && failure.Kind == ErrorCommand && strings.Contains(strings.ToLower(failure.Detail), "http 404") {
			return "none", nil
		}
		return "", err
	}
	var response struct {
		Permission string `json:"permission"`
	}
	if err := decode("read permission", output, &response); err != nil {
		return "", err
	}
	return strings.ToLower(response.Permission), nil
}

// CanWrite reports whether a permission lets a login trigger work.
func CanWrite(permission string) bool {
	return permission == "admin" || permission == "maintain" || permission == "write"
}

// CurrentUser returns the login that GH_TOKEN belongs to.
func (c *Client) CurrentUser(ctx context.Context) (string, error) {
	output, err := c.run(ctx, "read current user", nil, "api", "--method", "GET", "user")
	if err != nil {
		return "", err
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := decode("read current user", output, &user); err != nil {
		return "", err
	}
	if user.Login == "" {
		return "", errors.New("GitHub returned no login for this token")
	}
	return user.Login, nil
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
	_, err = c.run(ctx, "post comment", input, "api", "--method", "POST", fmt.Sprintf("repos/%s/issues/%d/comments", repository, number), "--input", "-")
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
	output, err := c.run(ctx, "find PR", nil, "pr", "list", "--repo", repository, "--head", branch, "--state", "all", "--limit", "100", "--json", "number,state,isCrossRepository")
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

// Review is one submitted PR review.
type Review struct {
	ID     string
	Author string
	State  string
	URL    string
}

// PullRequest is what the follow-up poller reads for one PR. FailedChecks is
// empty until every check on the head commit has finished.
type PullRequest struct {
	State        string
	ClosedAt     string
	MergedAt     *time.Time
	HeadSHA      string
	FailedChecks []string
	Reviews      []Review
}

const pullRequestQuery = `query MachinistPullRequest($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){state closedAt mergedAt headRefOid commits(last:1){nodes{commit{statusCheckRollup{contexts(first:100){nodes{__typename ... on CheckRun{name status conclusion} ... on StatusContext{context state}} pageInfo{hasNextPage}}}}}} reviews(last:100){nodes{id state url author{login}}}}}}`

type checkContext struct {
	Type       string `json:"__typename"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Context    string `json:"context"`
	State      string `json:"state"`
}

type checkRollup struct {
	Contexts struct {
		Nodes    []checkContext `json:"nodes"`
		PageInfo struct {
			HasNextPage bool `json:"hasNextPage"`
		} `json:"pageInfo"`
	} `json:"contexts"`
}

type pullRequestResponse struct {
	State      string     `json:"state"`
	ClosedAt   string     `json:"closedAt"`
	MergedAt   *time.Time `json:"mergedAt"`
	HeadRefOID string     `json:"headRefOid"`
	Commits    struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *checkRollup `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Reviews struct {
		Nodes []struct {
			ID     string `json:"id"`
			State  string `json:"state"`
			URL    string `json:"url"`
			Author *struct {
				Login string `json:"login"`
			} `json:"author"`
		} `json:"nodes"`
	} `json:"reviews"`
}

// failedChecks names the failures once every check has finished. A commit with
// more checks than one page holds reports none, because the rest are unknown.
func failedChecks(rollup *checkRollup) []string {
	if rollup == nil || rollup.Contexts.PageInfo.HasNextPage {
		return nil
	}
	failed := []string{}
	for _, check := range rollup.Contexts.Nodes {
		switch check.Type {
		case "CheckRun":
			if check.Status != "COMPLETED" {
				return nil
			}
			switch check.Conclusion {
			case "FAILURE", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE":
				failed = append(failed, check.Name)
			}
		case "StatusContext":
			switch check.State {
			case "PENDING", "EXPECTED":
				return nil
			case "FAILURE", "ERROR":
				failed = append(failed, check.Context)
			}
		}
	}
	return failed
}

// PullRequest reads a PR's state, head commit checks and reviews.
func (c *Client) PullRequest(ctx context.Context, repository string, number int) (PullRequest, error) {
	if err := validRepository(repository); err != nil {
		return PullRequest{}, err
	}
	owner, name, _ := strings.Cut(repository, "/")
	input, err := json.Marshal(map[string]any{"query": pullRequestQuery, "variables": map[string]any{"owner": owner, "name": name, "number": number}})
	if err != nil {
		return PullRequest{}, err
	}
	output, err := c.run(ctx, "read PR", input, "api", "--method", "POST", "graphql", "--input", "-")
	if err != nil {
		return PullRequest{}, err
	}
	var envelope struct {
		Data struct {
			Repository *struct {
				PullRequest *pullRequestResponse `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := decode("read PR", output, &envelope); err != nil {
		return PullRequest{}, err
	}
	if envelope.Data.Repository == nil || envelope.Data.Repository.PullRequest == nil {
		return PullRequest{}, &Error{Kind: ErrorMalformed, Operation: "read PR", Detail: fmt.Sprintf("no data for PR %d", number)}
	}
	response := *envelope.Data.Repository.PullRequest
	pr := PullRequest{State: response.State, ClosedAt: response.ClosedAt, MergedAt: response.MergedAt, HeadSHA: response.HeadRefOID}
	if len(response.Commits.Nodes) > 0 {
		pr.FailedChecks = failedChecks(response.Commits.Nodes[0].Commit.StatusCheckRollup)
	}
	for _, review := range response.Reviews.Nodes {
		if review.Author != nil {
			pr.Reviews = append(pr.Reviews, Review{ID: review.ID, Author: review.Author.Login, State: review.State, URL: review.URL})
		}
	}
	return pr, nil
}

// CommitEmails lists the author email of each commit on a PR.
func (c *Client) CommitEmails(ctx context.Context, repository string, number int) ([]string, error) {
	if err := validRepository(repository); err != nil {
		return nil, err
	}
	output, err := c.run(ctx, "list PR commits", nil, "api", "--method", "GET", "--paginate", "--slurp", fmt.Sprintf("repos/%s/pulls/%d/commits?per_page=100", repository, number))
	if err != nil {
		return nil, err
	}
	var pages [][]struct {
		Commit struct {
			Author struct {
				Email string `json:"email"`
			} `json:"author"`
		} `json:"commit"`
	}
	if err := decode("list PR commits", output, &pages); err != nil {
		return nil, err
	}
	emails := []string{}
	for _, page := range pages {
		for _, commit := range page {
			emails = append(emails, commit.Commit.Author.Email)
		}
	}
	return emails, nil
}

// Access is what the login can do to a repository's default branch.
type Access struct {
	DefaultBranch string
	Protected     bool
	Admin         bool
}

// RepositoryAccess reads the default branch's protection and the login's rights.
func (c *Client) RepositoryAccess(ctx context.Context, repository string) (Access, error) {
	if err := validRepository(repository); err != nil {
		return Access{}, err
	}
	output, err := c.run(ctx, "read repository", nil, "api", "--method", "GET", "repos/"+repository)
	if err != nil {
		return Access{}, err
	}
	var details struct {
		DefaultBranch string `json:"default_branch"`
		Permissions   struct {
			Admin bool `json:"admin"`
		} `json:"permissions"`
	}
	if err := decode("read repository", output, &details); err != nil {
		return Access{}, err
	}
	output, err = c.run(ctx, "read default branch", nil, "api", "--method", "GET", "repos/"+repository+"/branches/"+url.PathEscape(details.DefaultBranch))
	if err != nil {
		return Access{}, err
	}
	var branch struct {
		Protected bool `json:"protected"`
	}
	if err := decode("read default branch", output, &branch); err != nil {
		return Access{}, err
	}
	return Access{DefaultBranch: details.DefaultBranch, Protected: branch.Protected, Admin: details.Permissions.Admin}, nil
}
