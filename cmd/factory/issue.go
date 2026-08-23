package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// target is the work a dispatch runs against. Number is zero for a phase that
// targets a repository rather than an issue, such as audit.
type target struct {
	Owner  string
	Repo   string
	Number int
}

// Identity is the control plane's managed repository identity.
func (t target) Identity() string {
	return fmt.Sprintf("github.com/%s/%s", t.Owner, t.Repo)
}

// Slug is the owner/repo form that gh accepts.
func (t target) Slug() string { return t.Owner + "/" + t.Repo }

// Reference is the display form, such as acme/api#412.
func (t target) Reference() string {
	if t.Number == 0 {
		return t.Slug()
	}
	return fmt.Sprintf("%s#%d", t.Slug(), t.Number)
}

var (
	urlPattern   = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/issues/(\d+)/?$`)
	shortPattern = regexp.MustCompile(`^([^/\s]+)/([^/#\s]+)#(\d+)$`)
	slugPattern  = regexp.MustCompile(`^([^/\s]+)/([^/\s]+)$`)
)

// parseTarget resolves the work argument.
//
// Three forms are accepted. A bare number with --repo matches how gh is used. A
// pasted issue URL is unambiguous and is what is usually on the clipboard.
// owner/repo#number is kept because it is how GitHub renders cross-repository
// references, even though no CLI parses it.
func parseTarget(work, repo, defaultOwner string) (target, error) {
	work = strings.TrimSpace(work)
	repo = strings.TrimSpace(repo)

	if matches := urlPattern.FindStringSubmatch(work); matches != nil {
		number, _ := strconv.Atoi(matches[3])
		return target{Owner: matches[1], Repo: matches[2], Number: number}, nil
	}
	if matches := shortPattern.FindStringSubmatch(work); matches != nil {
		number, _ := strconv.Atoi(matches[3])
		return target{Owner: matches[1], Repo: matches[2], Number: number}, nil
	}

	owner, name, err := splitRepo(repo, defaultOwner)
	if err != nil {
		return target{}, err
	}
	if work == "" {
		return target{Owner: owner, Repo: name}, nil
	}
	number, err := strconv.Atoi(work)
	if err != nil || number <= 0 {
		return target{}, fmt.Errorf("cannot read %q as work: use an issue number with --repo, a GitHub issue URL, or owner/repo#number", work)
	}
	return target{Owner: owner, Repo: name, Number: number}, nil
}

func splitRepo(repo, defaultOwner string) (string, string, error) {
	if repo == "" {
		return "", "", errors.New("--repo is required unless the work is a GitHub issue URL or owner/repo#number")
	}
	if matches := slugPattern.FindStringSubmatch(repo); matches != nil {
		return matches[1], matches[2], nil
	}
	if strings.ContainsAny(repo, "/ \t") {
		return "", "", fmt.Errorf("cannot read %q as a repository: use owner/name or a bare name with --owner", repo)
	}
	if defaultOwner == "" {
		return "", "", fmt.Errorf("--repo %q has no owner: use owner/%s, or set --owner", repo, repo)
	}
	return defaultOwner, repo, nil
}

// issueDetail is the tracker text a phase body can reference.
type issueDetail struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	URL    string `json:"url"`
	State  string `json:"state"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// fetchIssue reads an issue through gh.
//
// Shelling out to gh rather than calling the GitHub API keeps Factory free of a
// token, a client library, and an auth story: gh is already a documented
// requirement and is already authenticated on the operator's machine.
func fetchIssue(ctx context.Context, work target) (issueDetail, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return issueDetail{}, errors.New("gh is not installed, and it is needed to read issue text")
	}
	command := exec.CommandContext(ctx, "gh", "issue", "view",
		strconv.Itoa(work.Number),
		"--repo", work.Slug(),
		"--json", "title,body,url,state,labels")

	output, err := command.Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			message := strings.TrimSpace(string(exitError.Stderr))
			if message == "" {
				message = exitError.String()
			}
			return issueDetail{}, fmt.Errorf("gh could not read %s: %s", work.Reference(), message)
		}
		return issueDetail{}, err
	}

	var detail issueDetail
	if err := json.Unmarshal(output, &detail); err != nil {
		return issueDetail{}, fmt.Errorf("gh returned output that could not be read: %w", err)
	}
	if strings.TrimSpace(detail.Title) == "" {
		return issueDetail{}, fmt.Errorf("%s has no title", work.Reference())
	}
	return detail, nil
}

func (d issueDetail) labelNames() string {
	names := make([]string, 0, len(d.Labels))
	for _, label := range d.Labels {
		names = append(names, label.Name)
	}
	return strings.Join(names, ", ")
}
