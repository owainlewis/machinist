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
	UpdateIssueBody(context.Context, issue, string) error
	CommentIssue(context.Context, issue, string) error
	OpenDraftPR(context.Context, issue, string, string, string, string) (string, error)
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

func (ghClient) UpdateIssueBody(ctx context.Context, value issue, body string) error {
	_, err := commandOutput(ctx, "", []byte(body), "gh", "issue", "edit", strconv.Itoa(value.Number), "--repo", value.Repository, "--body-file", "-")
	return err
}

func (ghClient) CommentIssue(ctx context.Context, value issue, body string) error {
	_, err := commandOutput(ctx, "", []byte(body), "gh", "issue", "comment", strconv.Itoa(value.Number), "--repo", value.Repository, "--body-file", "-")
	return err
}

func (ghClient) OpenDraftPR(ctx context.Context, value issue, branch, base, title, body string) (string, error) {
	output, err := commandOutput(ctx, "", []byte(body), "gh", "pr", "create", "--repo", value.Repository, "--head", branch, "--base", base, "--title", title, "--body-file", "-", "--draft")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
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
