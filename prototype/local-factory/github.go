package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

type githubClient interface {
	Issue(context.Context, string, int) (issue, error)
	LabeledIssues(context.Context, string, string) ([]issue, error)
	CommentIssue(context.Context, issue, string) error
	FindOpenPR(context.Context, issue, string) (pullRequest, bool, error)
	EnsureDraftPR(context.Context, issue, string, string, string, string, string) (string, error)
}

type ghClient struct{}

type ghIssue struct {
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	URL         string          `json:"html_url"`
	IssueURL    string          `json:"url"`
	PullRequest json.RawMessage `json:"pull_request"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

type pullRequest struct {
	URL               string `json:"url"`
	HeadRefName       string `json:"headRefName"`
	HeadSHA           string `json:"headRefOid"`
	BaseRefName       string `json:"baseRefName"`
	IsCrossRepository bool   `json:"isCrossRepository"`
}

func (ghClient) Issue(ctx context.Context, repository string, number int) (issue, error) {
	body, err := commandOutput(ctx, "", nil, "gh", "issue", "view", strconv.Itoa(number), "--repo", repository, "--json", "number,title,body,url,labels")
	if err != nil {
		return issue{}, err
	}
	var value ghIssue
	if err := json.Unmarshal(body, &value); err != nil {
		return issue{}, fmt.Errorf("decode gh issue response: %w", err)
	}
	return fromGHIssue(repository, value), nil
}

func (ghClient) LabeledIssues(ctx context.Context, repository, label string) ([]issue, error) {
	body, err := commandOutput(ctx, "", nil, "gh", "api", "--method", "GET", "--paginate", "--slurp", "repos/"+repository+"/issues", "-f", "state=open", "-f", "labels="+label, "-f", "per_page=100")
	if err != nil {
		return nil, err
	}
	var pages [][]ghIssue
	if err := json.Unmarshal(body, &pages); err != nil {
		return nil, fmt.Errorf("decode gh issue list response: %w", err)
	}
	var result []issue
	for _, page := range pages {
		for _, value := range page {
			if len(value.PullRequest) != 0 && string(value.PullRequest) != "null" {
				continue
			}
			result = append(result, fromGHIssue(repository, value))
		}
	}
	return result, nil
}

func (ghClient) CommentIssue(ctx context.Context, value issue, body string) error {
	_, err := commandOutput(ctx, "", []byte(body), "gh", "issue", "comment", strconv.Itoa(value.Number), "--repo", value.Repository, "--body-file", "-")
	return err
}

func (client ghClient) FindOpenPR(ctx context.Context, value issue, branch string) (pullRequest, bool, error) {
	output, err := commandOutput(ctx, "", nil, "gh", "pr", "list", "--repo", value.Repository, "--head", branch, "--state", "open", "--limit", "10", "--json", "url,headRefName,headRefOid,baseRefName,isCrossRepository")
	if err != nil {
		return pullRequest{}, false, err
	}
	var candidates []pullRequest
	if err := json.Unmarshal(output, &candidates); err != nil {
		return pullRequest{}, false, fmt.Errorf("decode gh pull request list: %w", err)
	}
	var matches []pullRequest
	for _, candidate := range candidates {
		if candidate.HeadRefName == branch && !candidate.IsCrossRepository {
			matches = append(matches, candidate)
		}
	}
	if len(matches) > 1 {
		return pullRequest{}, false, fmt.Errorf("found %d open pull requests for branch %q", len(matches), branch)
	}
	if len(matches) == 0 {
		return pullRequest{}, false, nil
	}
	return matches[0], true, nil
}

func (client ghClient) EnsureDraftPR(ctx context.Context, value issue, branch, expectedSHA, base, title, body string) (string, error) {
	if existing, found, err := client.FindOpenPR(ctx, value, branch); err != nil {
		return "", err
	} else if found {
		return verifiedPRURL(existing, expectedSHA, base)
	}
	output, err := commandOutput(ctx, "", []byte(body), "gh", "pr", "create", "--repo", value.Repository, "--head", branch, "--base", base, "--title", title, "--body-file", "-", "--draft")
	if err != nil {
		if existing, found, reconcileErr := client.FindOpenPR(ctx, value, branch); reconcileErr == nil && found {
			return verifiedPRURL(existing, expectedSHA, base)
		} else if reconcileErr != nil {
			return "", fmt.Errorf("create draft pull request: %v; reconcile outcome: %w", err, reconcileErr)
		}
		return "", err
	}
	createdURL := strings.TrimSpace(string(output))
	created, found, err := client.FindOpenPR(ctx, value, branch)
	if err != nil {
		return "", fmt.Errorf("draft pull request %s was created but could not be reconciled: %w", createdURL, err)
	}
	if !found {
		return "", fmt.Errorf("draft pull request %s was created but was not found by its head branch", createdURL)
	}
	return verifiedPRURL(created, expectedSHA, base)
}

func verifiedPRURL(value pullRequest, expectedSHA, expectedBase string) (string, error) {
	if value.HeadSHA != expectedSHA {
		return "", fmt.Errorf("pull request %s head is %s, expected verified SHA %s", value.URL, value.HeadSHA, expectedSHA)
	}
	if value.BaseRefName != expectedBase {
		return "", fmt.Errorf("pull request %s targets %s, expected base branch %s", value.URL, value.BaseRefName, expectedBase)
	}
	return value.URL, nil
}

func fromGHIssue(repository string, value ghIssue) issue {
	labels := make([]string, 0, len(value.Labels))
	for _, label := range value.Labels {
		labels = append(labels, label.Name)
	}
	url := value.URL
	if url == "" {
		url = value.IssueURL
	}
	return issue{Repository: repository, Number: value.Number, Title: value.Title, Body: value.Body, URL: url, Labels: labels}
}

var issueReferencePattern = regexp.MustCompile(`^(?:https://github\.com/)?([^/#\s]+/[^/#\s]+)(?:/issues/|#)([1-9][0-9]*)/?$`)

func parseIssueReference(value string) (string, int, error) {
	matches := issueReferencePattern.FindStringSubmatch(strings.TrimSpace(value))
	if matches == nil {
		return "", 0, errors.New("issue must be owner/repository#123 or a GitHub issue URL")
	}
	number, err := strconv.Atoi(matches[2])
	if err != nil {
		return "", 0, err
	}
	return matches[1], number, nil
}

func commandOutput(ctx context.Context, directory string, stdin []byte, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("%s: %s", name, message)
	}
	return stdout.Bytes(), nil
}
