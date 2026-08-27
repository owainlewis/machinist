package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultGitHubCommandTimeout = 30 * time.Second
	maxGitHubCandidates         = 100
	defaultGitHubArgumentBytes  = 96 << 10
	maxGitHubErrorBytes         = 512
)

var (
	githubRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,99})/[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,99})$`)
	githubActorPattern      = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	githubSecretPattern     = regexp.MustCompile(`(?i)(github_pat_[A-Za-z0-9_]+|gh[pousr]_[A-Za-z0-9_]+)`)
)

// GitHubCLI invokes the installed GitHub CLI directly. It never uses a shell.
type GitHubCLI struct {
	executable       string
	timeout          time.Duration
	maxArgumentBytes int
	runner           githubCommandRunner
}

type githubCommandRunner interface {
	Run(context.Context, string, []string) ([]byte, []byte, error)
}

type directGitHubCommandRunner struct{}

func (directGitHubCommandRunner) Run(ctx context.Context, executable string, args []string) ([]byte, []byte, error) {
	command := exec.CommandContext(ctx, executable, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// NewGitHubCLI returns a testable adapter around the installed gh executable.
func NewGitHubCLI(executable string, timeout time.Duration) *GitHubCLI {
	if strings.TrimSpace(executable) == "" {
		executable = "gh"
	}
	if timeout <= 0 {
		timeout = defaultGitHubCommandTimeout
	}
	return &GitHubCLI{
		executable:       executable,
		timeout:          timeout,
		maxArgumentBytes: defaultGitHubArgumentBytes,
		runner:           directGitHubCommandRunner{},
	}
}

type GitHubCandidate struct {
	Repository    string    `json:"repository"`
	Number        int       `json:"number"`
	URL           string    `json:"url"`
	State         string    `json:"state"`
	IsPullRequest bool      `json:"is_pull_request"`
	CreatedAt     time.Time `json:"created_at"`
}

type GitHubLabelEvent struct {
	ID            string    `json:"id"`
	Actor         string    `json:"actor"`
	CreatedAt     time.Time `json:"created_at"`
	OccurrenceKey string    `json:"occurrence_key"`
}

type GitHubIssueDetails struct {
	GitHubCandidate
	Labels         []string          `json:"labels"`
	RequestedEvent *GitHubLabelEvent `json:"requested_event,omitempty"`
}

type GitHubCLIErrorKind string

const (
	GitHubCLIErrorCommand   GitHubCLIErrorKind = "command"
	GitHubCLIErrorTimeout   GitHubCLIErrorKind = "timeout"
	GitHubCLIErrorAuth      GitHubCLIErrorKind = "authentication"
	GitHubCLIErrorRateLimit GitHubCLIErrorKind = "rate_limit"
	GitHubCLIErrorMalformed GitHubCLIErrorKind = "malformed_output"
)

type GitHubCLIError struct {
	Kind      GitHubCLIErrorKind
	Operation string
	Detail    string
	Err       error
}

func (e *GitHubCLIError) Error() string {
	message := "github CLI " + e.Operation + " failed (" + string(e.Kind) + ")"
	if e.Detail != "" {
		message += ": " + e.Detail
	}
	return message
}

func (e *GitHubCLIError) Unwrap() error { return e.Err }

// SearchRequestedIssues returns at most the 100 oldest matching issues across
// all configured repositories. Repositories are sorted and combined into one
// gh search whenever the process argument limit permits it.
func (g *GitHubCLI) SearchRequestedIssues(ctx context.Context, repositories []string, label string, limit int) ([]GitHubCandidate, error) {
	repositories, err := normalizeGitHubRepositories(repositories)
	if err != nil {
		return nil, err
	}
	if len(repositories) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(label) == "" || strings.ContainsRune(label, '\x00') {
		return nil, errors.New("github search label must not be empty")
	}
	if limit <= 0 || limit > maxGitHubCandidates {
		limit = maxGitHubCandidates
	}

	baseArgs := []string{
		"search", "issues", "--label", label,
		"--state", "open",
		"--sort", "created", "--order", "asc", "--limit", strconv.Itoa(maxGitHubCandidates),
		"--json", "number,repository,state,url,isPullRequest,createdAt",
	}
	batches := batchGitHubRepositories(repositories, baseArgs, g.maxArgumentBytes)
	all := make([]GitHubCandidate, 0, limit)
	for _, batch := range batches {
		args := append([]string(nil), baseArgs...)
		for _, repository := range batch {
			args = append(args, "--repo", repository)
		}
		stdout, err := g.run(ctx, "search issues", args)
		if err != nil {
			return nil, err
		}
		candidates, err := parseGitHubCandidates(stdout)
		if err != nil {
			return nil, malformedGitHubOutput("search issues", err, stdout)
		}
		all = append(all, candidates...)
	}

	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.Before(all[j].CreatedAt)
		}
		left, right := strings.ToLower(all[i].Repository), strings.ToLower(all[j].Repository)
		if left != right {
			return left < right
		}
		if all[i].Number != all[j].Number {
			return all[i].Number < all[j].Number
		}
		return all[i].URL < all[j].URL
	})
	all = deduplicateGitHubCandidates(all)
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// IssueDetails reads the issue and its complete label-event timeline. The
// latest matching label event identifies a distinct request occurrence.
func (g *GitHubCLI) IssueDetails(ctx context.Context, repository string, number int, requestedLabel string) (GitHubIssueDetails, error) {
	repository, err := normalizeGitHubRepository(repository)
	if err != nil {
		return GitHubIssueDetails{}, err
	}
	if number <= 0 {
		return GitHubIssueDetails{}, errors.New("github issue number must be positive")
	}
	if strings.TrimSpace(requestedLabel) == "" || strings.ContainsRune(requestedLabel, '\x00') {
		return GitHubIssueDetails{}, errors.New("github requested label must not be empty")
	}
	endpoint := fmt.Sprintf("repos/%s/issues/%d", repository, number)
	stdout, err := g.run(ctx, "read issue", []string{"api", "--method", "GET", endpoint})
	if err != nil {
		return GitHubIssueDetails{}, err
	}
	details, err := parseGitHubIssueDetails(repository, stdout)
	if err != nil {
		return GitHubIssueDetails{}, malformedGitHubOutput("read issue", err, stdout)
	}
	if details.Number != number {
		return GitHubIssueDetails{}, malformedGitHubOutput("read issue", fmt.Errorf("returned issue number %d, want %d", details.Number, number), stdout)
	}

	timeline, err := g.run(ctx, "read issue timeline", []string{
		"api", "--method", "GET", "--paginate", "--slurp",
		"-H", "Accept: application/vnd.github+json", endpoint + "/timeline?per_page=100",
	})
	if err != nil {
		return GitHubIssueDetails{}, err
	}
	event, err := parseLatestGitHubLabelEvent(timeline, requestedLabel)
	if err != nil {
		return GitHubIssueDetails{}, malformedGitHubOutput("read issue timeline", err, timeline)
	}
	details.RequestedEvent = event
	return details, nil
}

// Permission returns the repository permission GitHub reports for an actor.
func (g *GitHubCLI) Permission(ctx context.Context, repository, actor string) (string, error) {
	repository, err := normalizeGitHubRepository(repository)
	if err != nil {
		return "", err
	}
	actor = strings.TrimSpace(actor)
	if !githubActorPattern.MatchString(actor) {
		return "", errors.New("github actor must be a path-safe login")
	}
	endpoint := fmt.Sprintf("repos/%s/collaborators/%s/permission", repository, actor)
	stdout, err := g.run(ctx, "read actor permission", []string{"api", "--method", "GET", endpoint})
	if err != nil {
		return "", err
	}
	var response struct {
		Permission string `json:"permission"`
	}
	if err := decodeSingleJSON(stdout, &response); err != nil {
		return "", malformedGitHubOutput("read actor permission", err, stdout)
	}
	permission := strings.ToLower(strings.TrimSpace(response.Permission))
	switch permission {
	case "admin", "maintain", "write", "triage", "read", "none":
		return permission, nil
	default:
		return "", malformedGitHubOutput("read actor permission", fmt.Errorf("unknown permission %q", permission), stdout)
	}
}

func GitHubPermissionCanWrite(permission string) bool {
	switch strings.ToLower(strings.TrimSpace(permission)) {
	case "admin", "maintain", "write":
		return true
	default:
		return false
	}
}

// ReplaceRequestLabel adds the queued label before removing the request label.
// It verifies the admitted label event before and after removal so a newer request
// created during admission remains visible to the next poll.
func (g *GitHubCLI) ReplaceRequestLabel(ctx context.Context, repository string, number int, requestedLabel, queuedLabel, admittedOccurrenceKey string) error {
	repository, err := normalizeGitHubRepository(repository)
	if err != nil {
		return err
	}
	if number <= 0 {
		return errors.New("github issue number must be positive")
	}
	if err := validateGitHubLabel(requestedLabel); err != nil {
		return fmt.Errorf("requested label: %w", err)
	}
	if err := validateGitHubLabel(queuedLabel); err != nil {
		return fmt.Errorf("queued label: %w", err)
	}
	if strings.EqualFold(requestedLabel, queuedLabel) {
		return errors.New("requested and queued labels must differ")
	}
	if strings.TrimSpace(admittedOccurrenceKey) == "" || strings.ContainsRune(admittedOccurrenceKey, '\x00') {
		return errors.New("admitted occurrence key is required")
	}
	issueURL := fmt.Sprintf("https://github.com/%s/issues/%d", repository, number)
	if _, err := g.run(ctx, "add queued label", []string{"issue", "edit", issueURL, "--add-label", queuedLabel}); err != nil {
		return err
	}
	before, err := g.IssueDetails(ctx, repository, number, requestedLabel)
	if err != nil {
		return fmt.Errorf("verify request label before replacement: %w", err)
	}
	if before.RequestedEvent == nil || before.RequestedEvent.OccurrenceKey != admittedOccurrenceKey || !hasGitHubLabel(before.Labels, requestedLabel) {
		return nil
	}
	if _, err := g.run(ctx, "remove request label", []string{"issue", "edit", issueURL, "--remove-label", requestedLabel}); err != nil {
		return err
	}
	after, err := g.IssueDetails(ctx, repository, number, requestedLabel)
	if err != nil {
		return fmt.Errorf("verify request label after replacement: %w", err)
	}
	if after.RequestedEvent != nil && after.RequestedEvent.OccurrenceKey != admittedOccurrenceKey && !hasGitHubLabel(after.Labels, requestedLabel) {
		if _, err := g.run(ctx, "restore newer request label", []string{"issue", "edit", issueURL, "--add-label", requestedLabel}); err != nil {
			return err
		}
	}
	return nil
}

// GitHubIssueIsEligible applies local intake guards before admission.
func GitHubIssueIsEligible(issue GitHubIssueDetails, configuredRepositories []string) bool {
	if !strings.EqualFold(issue.State, "open") || issue.IsPullRequest || issue.RequestedEvent == nil {
		return false
	}
	for _, repository := range configuredRepositories {
		if strings.EqualFold(strings.TrimSpace(repository), issue.Repository) {
			return true
		}
	}
	return false
}

func (g *GitHubCLI) run(ctx context.Context, operation string, args []string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	stdout, stderr, err := g.runner.Run(commandCtx, g.executable, args)
	if err == nil {
		return stdout, nil
	}
	kind := classifyGitHubCLIError(commandCtx, stderr, err)
	detail := sanitizeGitHubOutput(stderr)
	if detail == "" {
		detail = sanitizeGitHubOutput([]byte(err.Error()))
	}
	return nil, &GitHubCLIError{Kind: kind, Operation: operation, Detail: detail, Err: err}
}

func classifyGitHubCLIError(ctx context.Context, stderr []byte, err error) GitHubCLIErrorKind {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return GitHubCLIErrorTimeout
	}
	message := strings.ToLower(string(stderr) + " " + err.Error())
	if strings.Contains(message, "rate limit") || strings.Contains(message, "secondary rate") || strings.Contains(message, "abuse detection") {
		return GitHubCLIErrorRateLimit
	}
	if strings.Contains(message, "authentication") || strings.Contains(message, "not logged in") || strings.Contains(message, "bad credentials") || strings.Contains(message, "http 401") {
		return GitHubCLIErrorAuth
	}
	return GitHubCLIErrorCommand
}

func malformedGitHubOutput(operation string, err error, output []byte) error {
	detail := err.Error()
	if sample := sanitizeGitHubOutput(output); sample != "" {
		detail += ": " + sample
	}
	return &GitHubCLIError{Kind: GitHubCLIErrorMalformed, Operation: operation, Detail: detail, Err: err}
}

func sanitizeGitHubOutput(output []byte) string {
	text := strings.Join(strings.Fields(string(output)), " ")
	text = githubSecretPattern.ReplaceAllString(text, "[REDACTED]")
	if len(text) > maxGitHubErrorBytes {
		text = text[:maxGitHubErrorBytes] + "..."
	}
	return text
}

func normalizeGitHubRepositories(repositories []string) ([]string, error) {
	unique := make(map[string]string, len(repositories))
	for _, repository := range repositories {
		normalized, err := normalizeGitHubRepository(repository)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(normalized)
		if _, exists := unique[key]; !exists {
			unique[key] = normalized
		}
	}
	result := make([]string, 0, len(unique))
	for _, repository := range unique {
		result = append(result, repository)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	return result, nil
}

func normalizeGitHubRepository(repository string) (string, error) {
	repository = strings.TrimSpace(repository)
	if !githubRepositoryPattern.MatchString(repository) || strings.Contains(repository, "..") {
		return "", fmt.Errorf("invalid GitHub repository slug %q", repository)
	}
	return repository, nil
}

func validateGitHubLabel(label string) error {
	if strings.TrimSpace(label) == "" || strings.ContainsRune(label, '\x00') {
		return errors.New("GitHub label must not be empty")
	}
	return nil
}

func batchGitHubRepositories(repositories, baseArgs []string, maxBytes int) [][]string {
	if maxBytes <= 0 {
		maxBytes = defaultGitHubArgumentBytes
	}
	baseBytes := argumentBytes(baseArgs)
	var result [][]string
	var batch []string
	batchBytes := baseBytes
	for _, repository := range repositories {
		addition := len("--repo") + 1 + len(repository) + 1
		if len(batch) > 0 && batchBytes+addition > maxBytes {
			result = append(result, batch)
			batch = nil
			batchBytes = baseBytes
		}
		batch = append(batch, repository)
		batchBytes += addition
	}
	if len(batch) > 0 {
		result = append(result, batch)
	}
	return result
}

func argumentBytes(args []string) int {
	total := 0
	for _, arg := range args {
		total += len(arg) + 1
	}
	return total
}

type githubSearchCandidate struct {
	Number        int             `json:"number"`
	Repository    json.RawMessage `json:"repository"`
	State         string          `json:"state"`
	URL           string          `json:"url"`
	IsPullRequest bool            `json:"isPullRequest"`
	CreatedAt     string          `json:"createdAt"`
}

func parseGitHubCandidates(output []byte) ([]GitHubCandidate, error) {
	var raw []githubSearchCandidate
	if err := decodeSingleJSON(output, &raw); err != nil {
		return nil, err
	}
	result := make([]GitHubCandidate, 0, len(raw))
	for index, candidate := range raw {
		repository, err := parseGitHubSearchRepository(candidate.Repository)
		if err != nil {
			return nil, fmt.Errorf("candidate %d repository: %w", index, err)
		}
		createdAt, err := time.Parse(time.RFC3339Nano, candidate.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("candidate %d createdAt: %w", index, err)
		}
		if candidate.Number <= 0 || strings.TrimSpace(candidate.URL) == "" || strings.TrimSpace(candidate.State) == "" {
			return nil, fmt.Errorf("candidate %d is missing required fields", index)
		}
		result = append(result, GitHubCandidate{
			Repository: repository, Number: candidate.Number, URL: candidate.URL,
			State: candidate.State, IsPullRequest: candidate.IsPullRequest, CreatedAt: createdAt,
		})
	}
	return result, nil
}

func parseGitHubSearchRepository(raw json.RawMessage) (string, error) {
	var object struct {
		NameWithOwner string `json:"nameWithOwner"`
	}
	if err := json.Unmarshal(raw, &object); err == nil && object.NameWithOwner != "" {
		return normalizeGitHubRepository(object.NameWithOwner)
	}
	var repository string
	if err := json.Unmarshal(raw, &repository); err != nil {
		return "", errors.New("missing nameWithOwner")
	}
	return normalizeGitHubRepository(repository)
}

func deduplicateGitHubCandidates(candidates []GitHubCandidate) []GitHubCandidate {
	seen := make(map[string]bool, len(candidates))
	result := candidates[:0]
	for _, candidate := range candidates {
		key := strings.ToLower(candidate.Repository) + "#" + strconv.Itoa(candidate.Number)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, candidate)
	}
	return result
}

type githubIssueResponse struct {
	Number      int             `json:"number"`
	HTMLURL     string          `json:"html_url"`
	State       string          `json:"state"`
	PullRequest json.RawMessage `json:"pull_request"`
	CreatedAt   string          `json:"created_at"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func parseGitHubIssueDetails(repository string, output []byte) (GitHubIssueDetails, error) {
	var raw githubIssueResponse
	if err := decodeSingleJSON(output, &raw); err != nil {
		return GitHubIssueDetails{}, err
	}
	createdAt, err := time.Parse(time.RFC3339Nano, raw.CreatedAt)
	if err != nil {
		return GitHubIssueDetails{}, fmt.Errorf("created_at: %w", err)
	}
	if raw.Number <= 0 || strings.TrimSpace(raw.HTMLURL) == "" || strings.TrimSpace(raw.State) == "" {
		return GitHubIssueDetails{}, errors.New("issue is missing required fields")
	}
	labels := make([]string, 0, len(raw.Labels))
	for _, label := range raw.Labels {
		if strings.TrimSpace(label.Name) == "" {
			return GitHubIssueDetails{}, errors.New("issue contains an empty label")
		}
		labels = append(labels, label.Name)
	}
	sort.Strings(labels)
	isPullRequest := len(raw.PullRequest) > 0 && string(raw.PullRequest) != "null"
	return GitHubIssueDetails{
		GitHubCandidate: GitHubCandidate{
			Repository: repository, Number: raw.Number, URL: raw.HTMLURL, State: raw.State,
			IsPullRequest: isPullRequest, CreatedAt: createdAt,
		},
		Labels: labels,
	}, nil
}

type githubTimelineEvent struct {
	ID        json.RawMessage `json:"id"`
	Event     string          `json:"event"`
	CreatedAt string          `json:"created_at"`
	Actor     *struct {
		Login string `json:"login"`
	} `json:"actor"`
	Label *struct {
		Name string `json:"name"`
	} `json:"label"`
}

func parseLatestGitHubLabelEvent(output []byte, requestedLabel string) (*GitHubLabelEvent, error) {
	var pages [][]githubTimelineEvent
	if err := decodeSingleJSON(output, &pages); err != nil {
		var flat []githubTimelineEvent
		if flatErr := decodeSingleJSON(output, &flat); flatErr != nil {
			return nil, err
		}
		pages = [][]githubTimelineEvent{flat}
	}
	var latest *GitHubLabelEvent
	for _, page := range pages {
		for _, event := range page {
			if event.Event != "labeled" || event.Label == nil || !strings.EqualFold(event.Label.Name, requestedLabel) {
				continue
			}
			id, err := parseGitHubEventID(event.ID)
			if err != nil {
				return nil, err
			}
			createdAt, err := time.Parse(time.RFC3339Nano, event.CreatedAt)
			if err != nil {
				return nil, fmt.Errorf("label event %s created_at: %w", id, err)
			}
			actor := ""
			if event.Actor != nil {
				actor = strings.TrimSpace(event.Actor.Login)
			}
			candidate := &GitHubLabelEvent{ID: id, Actor: actor, CreatedAt: createdAt, OccurrenceKey: "github.com:" + id}
			if latest == nil || candidate.CreatedAt.After(latest.CreatedAt) || (candidate.CreatedAt.Equal(latest.CreatedAt) && candidate.ID > latest.ID) {
				latest = candidate
			}
		}
	}
	return latest, nil
}

func parseGitHubEventID(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if strings.TrimSpace(text) == "" {
			return "", errors.New("label event has empty id")
		}
		return text, nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil || number.String() == "" {
		return "", errors.New("label event has invalid id")
	}
	if _, err := strconv.ParseUint(number.String(), 10, 64); err != nil {
		return "", errors.New("label event has invalid numeric id")
	}
	return number.String(), nil
}

func decodeSingleJSON(output []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("unexpected trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
