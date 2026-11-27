package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GitHubAPIEndpoint is GitHub's REST API base URL.
const GitHubAPIEndpoint = "https://api.github.com"

const (
	githubPageSize         = 100
	maxGitHubPages         = 8
	maxGitHubTimelinePages = 20
	maxGitHubReplyPages    = 40
	maxGitHubAnswer        = 4 << 20
	defaultGitHubLabel     = "machinist"

	// githubCommentMarker is an invisible HTML comment appended to every
	// comment Machinist posts on GitHub Issues so Replies never treats
	// Machinist's own status comments as human follow-ups, even when GH_TOKEN
	// belongs to an allowed user.
	githubCommentMarker = "<!-- machinist -->"
	githubStartComment  = "Machinist started a job for this issue. It will comment here with the pull request, or with what it found if no change is needed."
	githubFinishComment = "Machinist's pull request was merged. Closing this issue."
)

var (
	githubRepoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
	errGitHubNotFound = errors.New("github: not found")
)

// GitHubIssues watches open GitHub issues carrying Label across Repositories,
// verifies the actor who applied Label via issue timeline events, reads human
// follow-up comments, and posts status updates back through the GitHub REST API.
// It implements Tracker.
type GitHubIssues struct {
	// Token is a fixed GitHub token (such as GH_TOKEN), used when TokenFor is nil.
	Token string
	// TokenFor, when set, returns a repository-scoped token (such as a GitHub
	// App installation token) for each API call.
	TokenFor func(ctx context.Context, repository string) (string, error)
	// Repositories are the owner/name repositories to watch.
	Repositories []string
	// Label is the trigger label name. Defaults to "machinist".
	Label string
	// InProgressLabel, when non-empty, is added on Start and removed on Return,
	// Fail, and Finish.
	InProgressLabel string
	// Endpoint is the GitHub REST API base URL. Defaults to GitHubAPIEndpoint.
	Endpoint string
	// Client is the HTTP client used for API requests.
	Client *http.Client
}

var _ Tracker = (*GitHubIssues)(nil)

func (g *GitHubIssues) Name() string { return "github" }

func (g *GitHubIssues) label() string {
	if strings.TrimSpace(g.Label) == "" {
		return defaultGitHubLabel
	}
	return strings.TrimSpace(g.Label)
}

func (g *GitHubIssues) endpoint() string {
	if strings.TrimSpace(g.Endpoint) == "" {
		return GitHubAPIEndpoint
	}
	return strings.TrimRight(strings.TrimSpace(g.Endpoint), "/")
}

func (g *GitHubIssues) httpClient() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

type githubUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type githubLabel struct {
	Name string `json:"name"`
}

type githubIssue struct {
	Number      int             `json:"number"`
	State       string          `json:"state"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	HTMLURL     string          `json:"html_url"`
	Labels      []githubLabel   `json:"labels"`
	PullRequest json.RawMessage `json:"pull_request"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

func (i githubIssue) isPR() bool {
	return len(i.PullRequest) > 0 && string(i.PullRequest) != "null"
}

type githubTimelineEvent struct {
	Event     string       `json:"event"`
	CreatedAt time.Time    `json:"created_at"`
	Actor     *githubUser  `json:"actor"`
	Label     *githubLabel `json:"label"`
}

type githubComment struct {
	ID        int64       `json:"id"`
	Body      string      `json:"body"`
	CreatedAt time.Time   `json:"created_at"`
	IssueURL  string      `json:"issue_url"`
	User      *githubUser `json:"user"`
}

// Assignments lists open issues carrying g.Label updated after since across all
// configured repositories, oldest first. For each issue it inspects the
// timeline for the latest labeled/unlabeled event matching g.Label to find who
// applied the label, failing closed (empty Actor) whenever the actor cannot be
// determined with certainty.
func (g *GitHubIssues) Assignments(ctx context.Context, since time.Time) ([]Assignment, error) {
	label := g.label()
	var assignments []Assignment
	for _, repo := range g.Repositories {
		if err := validateGitHubRepo(repo); err != nil {
			return nil, err
		}
		query := url.Values{
			"state":     {"open"},
			"labels":    {label},
			"since":     {since.UTC().Format(time.RFC3339)},
			"sort":      {"updated"},
			"direction": {"asc"},
			"per_page":  {strconv.Itoa(githubPageSize)},
		}
		nextURL := g.endpoint() + "/repos/" + repo + "/issues?" + query.Encode()
		for page := 0; page < maxGitHubPages && nextURL != ""; page++ {
			var issues []githubIssue
			link, err := g.doJSON(ctx, repo, http.MethodGet, nextURL, nil, &issues)
			if err != nil {
				return nil, err
			}
			for _, item := range issues {
				if item.isPR() || !strings.EqualFold(item.State, "open") {
					continue
				}
				if !item.UpdatedAt.After(since) {
					continue
				}
				if !hasGitHubLabel(item.Labels, label) {
					continue
				}
				actor, givenAt, err := g.labelActor(ctx, repo, item.Number, label)
				if err != nil {
					return nil, err
				}
				issueRef := formatGitHubIssue(repo, item.Number)
				issueURL := item.HTMLURL
				if issueURL == "" {
					issueURL = fmt.Sprintf("https://github.com/%s/issues/%d", repo, item.Number)
				}
				assignments = append(assignments, Assignment{
					Key:        "github:" + issueRef,
					Issue:      issueRef,
					Identifier: issueRef,
					Title:      item.Title,
					Body:       item.Body,
					URL:        issueURL,
					Repo:       repo,
					Actor:      actor,
					GivenAt:    givenAt,
					UpdatedAt:  item.UpdatedAt,
				})
			}
			nextURL = parseNextLink(link, g.endpoint())
		}
	}
	sort.SliceStable(assignments, func(a, b int) bool {
		return assignments[a].UpdatedAt.Before(assignments[b].UpdatedAt)
	})
	return assignments, nil
}

// labelActor reads the issue's timeline to find who most recently applied label.
// It fails closed (empty actor) if the timeline is truncated past the page cap,
// if the label was subsequently removed, or if the event has no actor login.
func (g *GitHubIssues) labelActor(ctx context.Context, repo string, number int, label string) (string, time.Time, error) {
	nextURL := fmt.Sprintf("%s/repos/%s/issues/%d/timeline?per_page=%d", g.endpoint(), repo, number, githubPageSize)
	var actor string
	var givenAt time.Time
	for page := 0; page < maxGitHubTimelinePages && nextURL != ""; page++ {
		var events []githubTimelineEvent
		link, err := g.doJSON(ctx, repo, http.MethodGet, nextURL, nil, &events)
		if err != nil {
			return "", time.Time{}, err
		}
		for _, ev := range events {
			if ev.Label == nil || !strings.EqualFold(ev.Label.Name, label) {
				continue
			}
			switch ev.Event {
			case "labeled":
				if !ev.CreatedAt.Before(givenAt) {
					if ev.Actor != nil {
						actor = ev.Actor.Login
					} else {
						actor = ""
					}
					givenAt = ev.CreatedAt
				}
			case "unlabeled":
				if !ev.CreatedAt.Before(givenAt) {
					actor = ""
					givenAt = ev.CreatedAt
				}
			}
		}
		nextURL = parseNextLink(link, g.endpoint())
	}
	// If more timeline pages remain, the newest labeling event may have been
	// left unread; fail closed instead of trusting an older actor.
	if nextURL != "" {
		return "", time.Time{}, nil
	}
	return actor, givenAt, nil
}

// Start ensures the trigger label (and optional in-progress label) is present
// and posts a concise comment saying Machinist started a job.
func (g *GitHubIssues) Start(ctx context.Context, issue string) error {
	repo, number, err := parseGitHubIssue(issue)
	if err != nil {
		return err
	}
	labels := []string{g.label()}
	if inProgress := strings.TrimSpace(g.InProgressLabel); inProgress != "" {
		labels = append(labels, inProgress)
	}
	endpoint := fmt.Sprintf("%s/repos/%s/issues/%d/labels", g.endpoint(), repo, number)
	if _, err := g.doJSON(ctx, repo, http.MethodPost, endpoint, map[string]any{"labels": labels}, nil); err != nil {
		return err
	}
	return g.Comment(ctx, issue, githubStartComment)
}

// Replies returns comments posted by people (excluding Machinist's own status
// comments) across all configured repositories, created after since and, when
// until is non-zero, no later than until, oldest first.
func (g *GitHubIssues) Replies(ctx context.Context, since, until time.Time) ([]Reply, error) {
	var replies []Reply
	for _, repo := range g.Repositories {
		if err := validateGitHubRepo(repo); err != nil {
			return nil, err
		}
		query := url.Values{
			"since":     {since.UTC().Format(time.RFC3339)},
			"sort":      {"created"},
			"direction": {"asc"},
			"per_page":  {strconv.Itoa(githubPageSize)},
		}
		nextURL := g.endpoint() + "/repos/" + repo + "/issues/comments?" + query.Encode()
		for page := 0; nextURL != ""; page++ {
			if page == maxGitHubReplyPages {
				return nil, fmt.Errorf("github: more than %d replies since %s: %w", maxGitHubReplyPages*githubPageSize, since.Format(time.RFC3339), ErrTooManyReplies)
			}
			var comments []githubComment
			link, err := g.doJSON(ctx, repo, http.MethodGet, nextURL, nil, &comments)
			if err != nil {
				return nil, err
			}
			pastUntil := false
			for _, comment := range comments {
				if !until.IsZero() && comment.CreatedAt.After(until) {
					pastUntil = true
					break
				}
				if !comment.CreatedAt.After(since) {
					continue
				}
				if comment.User == nil || comment.User.Login == "" || strings.TrimSpace(comment.Body) == "" {
					continue
				}
				if strings.Contains(comment.Body, githubCommentMarker) {
					continue
				}
				number, ok := issueNumberFromURL(comment.IssueURL)
				if !ok {
					continue
				}
				issueRef := formatGitHubIssue(repo, number)
				replies = append(replies, Reply{
					Key:       "github:" + issueRef,
					ID:        strconv.FormatInt(comment.ID, 10),
					Issue:     issueRef,
					Author:    comment.User.Login,
					Body:      comment.Body,
					CreatedAt: comment.CreatedAt,
				})
			}
			if pastUntil {
				break
			}
			nextURL = parseNextLink(link, g.endpoint())
		}
	}
	sort.SliceStable(replies, func(a, b int) bool {
		return replies[a].CreatedAt.Before(replies[b].CreatedAt)
	})
	return replies, nil
}

// Comment posts body on the GitHub issue, tagged with an invisible marker so
// Machinist never mistakes its own status comments for human replies.
func (g *GitHubIssues) Comment(ctx context.Context, issue, body string) error {
	repo, number, err := parseGitHubIssue(issue)
	if err != nil {
		return err
	}
	text := body
	if !strings.Contains(text, githubCommentMarker) {
		text = strings.TrimRight(text, "\n") + "\n\n" + githubCommentMarker
	}
	endpoint := fmt.Sprintf("%s/repos/%s/issues/%d/comments", g.endpoint(), repo, number)
	_, err = g.doJSON(ctx, repo, http.MethodPost, endpoint, map[string]string{"body": text}, nil)
	return err
}

// Return removes the optional in-progress label and comments on the issue when
// a job finished without opening a pull request.
func (g *GitHubIssues) Return(ctx context.Context, issue, comment string) error {
	repo, number, err := parseGitHubIssue(issue)
	if err != nil {
		return err
	}
	if inProgress := strings.TrimSpace(g.InProgressLabel); inProgress != "" {
		if err := g.removeLabel(ctx, repo, number, inProgress); err != nil {
			return err
		}
	}
	return g.Comment(ctx, issue, comment)
}

// Fail removes the trigger label (and optional in-progress label) and comments
// why the job failed.
func (g *GitHubIssues) Fail(ctx context.Context, issue, reason string) error {
	repo, number, err := parseGitHubIssue(issue)
	if err != nil {
		return err
	}
	if err := g.removeLabel(ctx, repo, number, g.label()); err != nil {
		return err
	}
	if inProgress := strings.TrimSpace(g.InProgressLabel); inProgress != "" {
		if err := g.removeLabel(ctx, repo, number, inProgress); err != nil {
			return err
		}
	}
	return g.Comment(ctx, issue, "Machinist's job failed: "+reason+"\n\nReply here to tell it what to change, and it will run again.")
}

// Finish removes the trigger label (and optional in-progress label), closes
// the issue as completed, and posts a concise closing comment.
func (g *GitHubIssues) Finish(ctx context.Context, issue string) error {
	repo, number, err := parseGitHubIssue(issue)
	if err != nil {
		return err
	}
	if err := g.removeLabel(ctx, repo, number, g.label()); err != nil {
		return err
	}
	if inProgress := strings.TrimSpace(g.InProgressLabel); inProgress != "" {
		if err := g.removeLabel(ctx, repo, number, inProgress); err != nil {
			return err
		}
	}
	endpoint := fmt.Sprintf("%s/repos/%s/issues/%d", g.endpoint(), repo, number)
	if _, err := g.doJSON(ctx, repo, http.MethodPatch, endpoint, map[string]string{
		"state":        "closed",
		"state_reason": "completed",
	}, nil); err != nil {
		return err
	}
	return g.Comment(ctx, issue, githubFinishComment)
}

// States checks whether each issue is still open (Active) and still carries
// the trigger label (Assigned). Deleted or missing issues (404) are omitted.
func (g *GitHubIssues) States(ctx context.Context, issues []string) (map[string]IssueState, error) {
	label := g.label()
	states := make(map[string]IssueState, len(issues))
	for _, issue := range issues {
		repo, number, err := parseGitHubIssue(issue)
		if err != nil {
			return nil, err
		}
		endpoint := fmt.Sprintf("%s/repos/%s/issues/%d", g.endpoint(), repo, number)
		var got githubIssue
		_, err = g.doJSON(ctx, repo, http.MethodGet, endpoint, nil, &got)
		if errors.Is(err, errGitHubNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		states[issue] = IssueState{
			Assigned: hasGitHubLabel(got.Labels, label),
			Active:   strings.EqualFold(got.State, "open"),
		}
	}
	return states, nil
}

func (g *GitHubIssues) removeLabel(ctx context.Context, repo string, number int, label string) error {
	endpoint := fmt.Sprintf("%s/repos/%s/issues/%d/labels/%s", g.endpoint(), repo, number, url.PathEscape(label))
	_, err := g.doJSON(ctx, repo, http.MethodDelete, endpoint, nil, nil)
	if errors.Is(err, errGitHubNotFound) {
		return nil
	}
	return err
}

func (g *GitHubIssues) token(ctx context.Context, repo string) (string, error) {
	if g.TokenFor != nil {
		tok, err := g.TokenFor(ctx, repo)
		if err != nil {
			return "", fmt.Errorf("github: get token for %s: %w", repo, err)
		}
		if tok == "" {
			return "", fmt.Errorf("github: empty token for %s", repo)
		}
		return tok, nil
	}
	if g.Token != "" {
		return g.Token, nil
	}
	return "", errors.New("github: no token configured")
}

func (g *GitHubIssues) doJSON(ctx context.Context, repo, method, targetURL string, payload, into any) (string, error) {
	tok, err := g.token(ctx, repo)
	if err != nil {
		return "", err
	}
	var bodyReader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		bodyReader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, bodyReader)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "machinist")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxGitHubAnswer))
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return "", fmt.Errorf("github: %w", ErrRateLimited)
	}
	if resp.StatusCode == http.StatusNotFound {
		return "", errGitHubNotFound
	}
	if resp.StatusCode/100 != 2 {
		var failure struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &failure)
		msg := strings.TrimSpace(failure.Message)
		if resp.StatusCode == http.StatusForbidden && isGitHubRateLimit(resp.Header, msg) {
			return "", fmt.Errorf("github: %w", ErrRateLimited)
		}
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return "", fmt.Errorf("github: status %d: %s", resp.StatusCode, msg)
	}
	if into != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, into); err != nil {
			return "", fmt.Errorf("github: decode response: %w", err)
		}
	}
	return resp.Header.Get("Link"), nil
}

func isGitHubRateLimit(headers http.Header, message string) bool {
	if headers.Get("X-RateLimit-Remaining") == "0" || headers.Get("Retry-After") != "" {
		return true
	}
	lower := strings.ToLower(message)
	return strings.Contains(lower, "rate limit") || strings.Contains(lower, "secondary rate") || strings.Contains(lower, "abuse detection")
}

func hasGitHubLabel(labels []githubLabel, want string) bool {
	for _, l := range labels {
		if strings.EqualFold(l.Name, want) {
			return true
		}
	}
	return false
}

func formatGitHubIssue(repo string, number int) string {
	return fmt.Sprintf("%s#%d", repo, number)
}

func validateGitHubRepo(repo string) error {
	if !githubRepoPattern.MatchString(repo) || strings.Contains(repo, "..") {
		return fmt.Errorf("github: invalid repository %q", repo)
	}
	return nil
}

func parseGitHubIssue(issue string) (string, int, error) {
	repo, numStr, ok := strings.Cut(issue, "#")
	if !ok {
		return "", 0, fmt.Errorf("github: invalid issue reference %q", issue)
	}
	if err := validateGitHubRepo(repo); err != nil {
		return "", 0, err
	}
	number, err := strconv.Atoi(numStr)
	if err != nil || number <= 0 {
		return "", 0, fmt.Errorf("github: invalid issue number in %q", issue)
	}
	return repo, number, nil
}

func issueNumberFromURL(raw string) (int, bool) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return 0, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 || parts[len(parts)-2] != "issues" {
		return 0, false
	}
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// parseNextLink extracts the rel="next" URL from a GitHub Link header and
// verifies it stays on the same scheme and host as baseEndpoint.
func parseNextLink(header, baseEndpoint string) string {
	if header == "" {
		return ""
	}
	base, err := url.Parse(baseEndpoint)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		sections := strings.Split(part, ";")
		if len(sections) < 2 {
			continue
		}
		isNext := false
		for _, param := range sections[1:] {
			if strings.TrimSpace(param) == `rel="next"` {
				isNext = true
				break
			}
		}
		if !isNext {
			continue
		}
		raw := strings.TrimSpace(sections[0])
		if !strings.HasPrefix(raw, "<") || !strings.HasSuffix(raw, ">") {
			continue
		}
		candidate := raw[1 : len(raw)-1]
		parsed, err := url.Parse(candidate)
		if err != nil {
			continue
		}
		if parsed.Scheme != base.Scheme || !strings.EqualFold(parsed.Host, base.Host) {
			continue
		}
		return candidate
	}
	return ""
}
