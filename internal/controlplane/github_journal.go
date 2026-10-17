package controlplane

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"time"

	machinistexamples "github.com/owainlewis/machinist/examples"
	"github.com/owainlewis/machinist/internal/config"
)

var errAgentConflict = errors.New("agent prompt changed; reload before saving")

const GitHubJournalMarker = "<!-- machinist:v1 -->\n"

type GitHubIssue struct {
	Maintenance *maintenanceSnapshot `json:"machinist_maintenance,omitempty"`
	Number      int                  `json:"number"`
	Title       string               `json:"title"`
	Body        string               `json:"body"`
	HTMLURL     string               `json:"html_url"`
	State       string               `json:"state"`
	UpdatedAt   time.Time            `json:"updated_at"`
	CreatedAt   time.Time            `json:"created_at"`
	PullRequest json.RawMessage      `json:"pull_request,omitempty"`
}
type GitHubActor struct {
	Login string `json:"login"`
}
type GitHubComment struct {
	ID        int64       `json:"id"`
	Body      string      `json:"body"`
	User      GitHubActor `json:"user"`
	CreatedAt time.Time   `json:"created_at"`
}
type GitHubFactoryClient interface {
	ListTasks(context.Context, string) ([]GitHubIssue, error)
	GetTask(context.Context, string, int) (GitHubIssue, error)
	AdoptTask(context.Context, string, int) error
	CreateTask(context.Context, string, string, string) (GitHubIssue, error)
	Comments(context.Context, string, int) ([]GitHubComment, error)
	PostComment(context.Context, string, int, string) (GitHubComment, error)
	AgentPrompt(context.Context, string, string) (string, string, error)
	SaveAgentPrompt(context.Context, string, string, string, string) (string, error)
	CurrentUser(context.Context) (string, error)
}

type githubFactoryAPI func(context.Context, string, string, []byte) ([]byte, error)
type ghFactoryClient struct{ api githubFactoryAPI }

func NewGitHubFactoryClient() GitHubFactoryClient { return &ghFactoryClient{api: runFactoryGitHubAPI} }

func runFactoryGitHubAPI(ctx context.Context, method, path string, input []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := []string{"api", "--method", method, path}
	if strings.Contains(path, "/merge-async") {
		args = append(args, "-H", "X-GitHub-Api-Version: 2026-03-10")
	}
	if input != nil {
		args = append(args, "--input", "-")
	}
	command := exec.CommandContext(ctx, "gh", args...)
	command.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	var out limitedGitHubBuffer
	command.Stdout = &out
	err := command.Run()
	if err != nil {
		// gh includes the HTTP status in stderr. Preserve status for contents 404 handling.
		return nil, fmt.Errorf("GitHub API %s %s: %w: %s", method, path, err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}

type limitedGitHubBuffer struct{ bytes.Buffer }

func (b *limitedGitHubBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 8<<20 {
		return 0, errors.New("GitHub API response exceeds 8 MiB")
	}
	return b.Buffer.Write(p)
}

func githubRepositoryPath(repo string) (string, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return "", errors.New("GitHub repository must be owner/name")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "?#%\\\x00\r\n") {
			return "", errors.New("invalid GitHub repository")
		}
	}
	return "repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]), nil
}
func (g *ghFactoryClient) ListTasks(ctx context.Context, repo string) ([]GitHubIssue, error) {
	root, err := githubRepositoryPath(repo)
	if err != nil {
		return nil, err
	}
	tasks := []GitHubIssue{}
	for page := 1; ; page++ {
		raw, err := g.api(ctx, "GET", fmt.Sprintf("%s/issues?state=all&labels=machinist%%3Atask&per_page=100&page=%d", root, page), nil)
		if err != nil {
			return nil, err
		}
		var batch []GitHubIssue
		if err = json.Unmarshal(raw, &batch); err != nil {
			return nil, err
		}
		for _, issue := range batch {
			if len(issue.PullRequest) == 0 || string(issue.PullRequest) == "null" {
				tasks = append(tasks, issue)
			}
		}
		if len(batch) < 100 {
			return tasks, nil
		}
	}
}
func (g *ghFactoryClient) GetTask(ctx context.Context, repo string, number int) (GitHubIssue, error) {
	var issue GitHubIssue
	root, err := githubRepositoryPath(repo)
	if err != nil {
		return issue, err
	}
	if number <= 0 {
		return issue, errors.New("issue number must be positive")
	}
	raw, err := g.api(ctx, "GET", fmt.Sprintf("%s/issues/%d", root, number), nil)
	if err != nil {
		return issue, err
	}
	if err = json.Unmarshal(raw, &issue); err != nil {
		return issue, err
	}
	if len(issue.PullRequest) > 0 && string(issue.PullRequest) != "null" {
		return issue, errors.New("pull request cannot be adopted as a task issue")
	}
	return issue, nil
}
func (g *ghFactoryClient) AdoptTask(ctx context.Context, repo string, number int) error {
	root, err := githubRepositoryPath(repo)
	if err != nil {
		return err
	}
	if number <= 0 {
		return errors.New("issue number must be positive")
	}
	if err = g.ensureTaskLabel(ctx, root); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"labels": []string{"machinist:task"}})
	_, err = g.api(ctx, "POST", fmt.Sprintf("%s/issues/%d/labels", root, number), body)
	return err
}

func (g *ghFactoryClient) ensureTaskLabel(ctx context.Context, root string) error {
	_, err := g.api(ctx, "GET", root+"/labels/machinist%3Atask", nil)
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		return err
	}
	body, _ := json.Marshal(map[string]string{"name": "machinist:task", "color": "57606a", "description": "Machinist factory task"})
	_, err = g.api(ctx, "POST", root+"/labels", body)
	if err != nil && strings.Contains(err.Error(), "HTTP 422") {
		// Another foreman may have created the shared label concurrently.
		_, err = g.api(ctx, "GET", root+"/labels/machinist%3Atask", nil)
	}
	return err
}

func (g *ghFactoryClient) CreateTask(ctx context.Context, repo, title, body string) (GitHubIssue, error) {
	var issue GitHubIssue
	root, err := githubRepositoryPath(repo)
	if err != nil {
		return issue, err
	}
	if err = g.ensureTaskLabel(ctx, root); err != nil {
		return issue, err
	}
	input, _ := json.Marshal(map[string]any{"title": title, "body": body, "labels": []string{"machinist:task"}})
	raw, err := g.api(ctx, "POST", root+"/issues", input)
	if err != nil {
		return issue, err
	}
	err = json.Unmarshal(raw, &issue)
	return issue, err
}
func (g *ghFactoryClient) Comments(ctx context.Context, repo string, issue int) ([]GitHubComment, error) {
	root, err := githubRepositoryPath(repo)
	if err != nil {
		return nil, err
	}
	if issue <= 0 {
		return nil, errors.New("issue number must be positive")
	}
	comments := []GitHubComment{}
	for page := 1; ; page++ {
		raw, err := g.api(ctx, "GET", fmt.Sprintf("%s/issues/%d/comments?per_page=100&page=%d", root, issue, page), nil)
		if err != nil {
			return nil, err
		}
		var batch []GitHubComment
		if err = json.Unmarshal(raw, &batch); err != nil {
			return nil, err
		}
		comments = append(comments, batch...)
		if len(batch) < 100 {
			sort.Slice(comments, func(i, j int) bool { return comments[i].ID < comments[j].ID })
			return comments, nil
		}
	}
}
func (g *ghFactoryClient) PostComment(ctx context.Context, repo string, issue int, body string) (GitHubComment, error) {
	var comment GitHubComment
	root, err := githubRepositoryPath(repo)
	if err != nil {
		return comment, err
	}
	if issue <= 0 {
		return comment, errors.New("issue number must be positive")
	}
	input, _ := json.Marshal(map[string]string{"body": body})
	raw, err := g.api(ctx, "POST", fmt.Sprintf("%s/issues/%d/comments", root, issue), input)
	if err != nil {
		return comment, err
	}
	err = json.Unmarshal(raw, &comment)
	return comment, err
}
func (g *ghFactoryClient) CurrentUser(ctx context.Context) (string, error) {
	raw, err := g.api(ctx, "GET", "user", nil)
	if err != nil {
		return "", err
	}
	var actor GitHubActor
	if err = json.Unmarshal(raw, &actor); err != nil {
		return "", err
	}
	if actor.Login == "" {
		return "", errors.New("GitHub authenticated user has no login")
	}
	return actor.Login, nil
}
func agentContentsPath(repo, role string) (string, error) {
	root, err := githubRepositoryPath(repo)
	if err != nil {
		return "", err
	}
	if role != "triage" && role != "spec" && role != "implement" && role != "review" {
		return "", fmt.Errorf("unknown factory role %q", role)
	}
	return root + "/contents/.machinist/agents/" + role + ".md", nil
}
func (g *ghFactoryClient) AgentPrompt(ctx context.Context, repo, role string) (string, string, error) {
	path, err := agentContentsPath(repo, role)
	if err != nil {
		return "", "", err
	}
	raw, err := g.api(ctx, "GET", path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			content, readErr := machinistexamples.Files.ReadFile("prompts/" + role + ".md")
			return string(content), "", readErr
		}
		return "", "", err
	}
	var file struct {
		Content  string
		SHA      string
		Encoding string
	}
	if err = json.Unmarshal(raw, &file); err != nil {
		return "", "", err
	}
	if file.Encoding != "base64" || file.SHA == "" {
		return "", "", errors.New("invalid GitHub agent prompt file")
	}
	content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil {
		return "", "", err
	}
	if len(content) > 256<<10 {
		return "", "", errors.New("agent prompt exceeds 256 KiB")
	}
	return string(content), file.SHA, nil
}
func (g *ghFactoryClient) SaveAgentPrompt(ctx context.Context, repo, role, prompt, expectedSHA string) (string, error) {
	path, err := agentContentsPath(repo, role)
	if err != nil {
		return "", err
	}
	if len(prompt) > 256<<10 || strings.TrimSpace(prompt) == "" {
		return "", errors.New("prompt must contain text and be at most 256 KiB")
	}
	if err = config.ValidatePromptParameters(role, prompt); err != nil {
		return "", err
	}
	// Read failures must never masquerade as an absent file. A missing file returns an empty SHA.
	_, current, err := g.AgentPrompt(ctx, repo, role)
	if err != nil {
		return "", err
	}
	if current != expectedSHA {
		return "", errAgentConflict
	}
	input := map[string]string{"message": "chore: update Machinist " + role + " prompt", "content": base64.StdEncoding.EncodeToString([]byte(prompt))}
	if expectedSHA != "" {
		input["sha"] = expectedSHA
	}
	body, _ := json.Marshal(input)
	raw, err := g.api(ctx, "PUT", path, body)
	if err != nil {
		return "", err
	}
	var saved struct{ Content struct{ SHA string } }
	if err = json.Unmarshal(raw, &saved); err != nil {
		return "", err
	}
	if saved.Content.SHA == "" {
		return "", errors.New("GitHub returned no saved prompt SHA")
	}
	return saved.Content.SHA, nil
}

// TrustedJournalComments filters provenance before the caller decodes journal events.
func TrustedJournalComments(comments []GitHubComment, login string) []GitHubComment {
	trusted := []GitHubComment{}
	if login == "" {
		return trusted
	}
	for _, comment := range comments {
		if strings.EqualFold(comment.User.Login, login) && strings.HasPrefix(comment.Body, GitHubJournalMarker) {
			trusted = append(trusted, comment)
		}
	}
	return trusted
}
