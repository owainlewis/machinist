package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func repositoryFor(cfg loadedConfig, name string) (repositoryConfig, error) {
	for _, repository := range cfg.Config.Repositories {
		if strings.EqualFold(repository.GitHub, name) {
			path := repository.Path
			if !filepath.IsAbs(path) {
				path = filepath.Join(cfg.Directory, path)
			}
			repository.Path = filepath.Clean(path)
			return repository, nil
		}
	}
	return repositoryConfig{}, fmt.Errorf("repository %q is not configured", name)
}

func prepareWorkspace(ctx context.Context, cfg loadedConfig, item work) (string, string, error) {
	repository, err := repositoryFor(cfg, item.Issue.Repository)
	if err != nil {
		return "", "", err
	}
	if _, err := os.Stat(filepath.Join(repository.Path, ".git")); err != nil {
		return "", "", fmt.Errorf("repository path %q is not a git checkout", repository.Path)
	}
	workspace := filepath.Join(cfg.Config.StateDirectory, "checkouts", item.ID, fmt.Sprintf("attempt-%d", item.Attempt), "work")
	branch := fmt.Sprintf("factory/%s-attempt-%d", item.ID, item.Attempt)
	if strings.HasPrefix(repository.BaseRef, "origin/") {
		remoteBranch := strings.TrimPrefix(repository.BaseRef, "origin/")
		if _, err := commandOutput(ctx, repository.Path, nil, "git", "fetch", "origin", remoteBranch); err != nil {
			return "", "", fmt.Errorf("fetch base branch: %w", err)
		}
	}
	baseOutput, err := commandOutput(ctx, repository.Path, nil, "git", "rev-parse", "--verify", repository.BaseRef+"^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("resolve base_ref %q: %w", repository.BaseRef, err)
	}
	baseSHA := strings.TrimSpace(string(baseOutput))
	if _, err := os.Stat(workspace); err == nil {
		if validationErr := validateReusableWorkspace(ctx, workspace, branch, baseSHA); validationErr != nil {
			if item.Workspace != "" || item.Branch != "" || item.HeadSHA != "" {
				return "", "", fmt.Errorf("existing attempt workspace is not safe to reuse: %w", validationErr)
			}
			if err := cleanupPartialAttemptWorkspace(ctx, repository.Path, workspace, branch); err != nil {
				return "", "", fmt.Errorf("recover partial attempt workspace: %w", err)
			}
		} else {
			return workspace, branch, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	} else if item.Workspace == "" && item.Branch == "" && item.HeadSHA == "" {
		if err := cleanupPartialAttemptWorkspace(ctx, repository.Path, workspace, branch); err != nil {
			return "", "", fmt.Errorf("recover partial attempt registration: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(workspace), 0o755); err != nil {
		return "", "", err
	}
	if _, err := commandOutput(ctx, repository.Path, nil, "git", "worktree", "add", "-b", branch, workspace, baseSHA); err != nil {
		return "", "", fmt.Errorf("create worktree: %w", err)
	}
	return workspace, branch, nil
}

func cleanupPartialAttemptWorkspace(ctx context.Context, repositoryPath, workspace, branch string) error {
	_, _ = commandOutput(ctx, repositoryPath, nil, "git", "worktree", "remove", "--force", workspace)
	if err := os.RemoveAll(workspace); err != nil {
		return err
	}
	if _, err := commandOutput(ctx, repositoryPath, nil, "git", "worktree", "prune"); err != nil {
		return err
	}
	if _, err := commandOutput(ctx, repositoryPath, nil, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		if _, err := commandOutput(ctx, repositoryPath, nil, "git", "branch", "-D", branch); err != nil {
			return err
		}
	}
	return nil
}

func validateReusableWorkspace(ctx context.Context, workspace, expectedBranch, expectedSHA string) error {
	topLevel, err := commandOutput(ctx, workspace, nil, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	actualTopLevel, err := filepath.Abs(strings.TrimSpace(string(topLevel)))
	if err != nil {
		return err
	}
	expectedTopLevel, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	actualTopLevel, err = filepath.EvalSymlinks(actualTopLevel)
	if err != nil {
		return err
	}
	expectedTopLevel, err = filepath.EvalSymlinks(expectedTopLevel)
	if err != nil {
		return err
	}
	if filepath.Clean(actualTopLevel) != filepath.Clean(expectedTopLevel) {
		return fmt.Errorf("checkout root is %q, expected %q", actualTopLevel, expectedTopLevel)
	}
	branch, err := commandOutput(ctx, workspace, nil, "git", "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return err
	}
	if actualBranch := strings.TrimSpace(string(branch)); actualBranch != expectedBranch {
		return fmt.Errorf("branch is %q, expected %q", actualBranch, expectedBranch)
	}
	head, err := commandOutput(ctx, workspace, nil, "git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if actualSHA := strings.TrimSpace(string(head)); actualSHA != expectedSHA {
		return fmt.Errorf("HEAD is %s, expected base %s", actualSHA, expectedSHA)
	}
	if err := ensureClean(ctx, workspace); err != nil {
		return err
	}
	return nil
}

func checkpoint(ctx context.Context, workspace, message string) (string, error) {
	if _, err := commandOutput(ctx, workspace, nil, "git", "add", "--all"); err != nil {
		return "", err
	}
	changed, err := commandOutput(ctx, workspace, nil, "git", "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if len(changed) != 0 {
		if _, err := commandOutput(ctx, workspace, nil, "git", "-c", "user.name=Factory", "-c", "user.email=factory@localhost", "commit", "-m", message); err != nil {
			return "", err
		}
	}
	sha, err := commandOutput(ctx, workspace, nil, "git", "rev-parse", "HEAD")
	return strings.TrimSpace(string(sha)), err
}

func prepareVerificationWorkspace(ctx context.Context, cfg loadedConfig, item work, sha string) (string, error) {
	repository, err := repositoryFor(cfg, item.Issue.Repository)
	if err != nil {
		return "", err
	}
	workspace := filepath.Join(cfg.Config.StateDirectory, "checkouts", item.ID, "verify", fmt.Sprintf("attempt-%d-run-%d", item.Attempt, item.VerifyRuns+1))
	if err := os.MkdirAll(filepath.Dir(workspace), 0o755); err != nil {
		return "", err
	}
	if _, err := commandOutput(ctx, repository.Path, nil, "git", "worktree", "add", "--detach", workspace, sha); err != nil {
		return "", fmt.Errorf("create verification worktree: %w", err)
	}
	return workspace, nil
}

func removeVerificationWorkspace(ctx context.Context, cfg loadedConfig, item work, workspace string) error {
	repository, err := repositoryFor(cfg, item.Issue.Repository)
	if err != nil {
		return err
	}
	if _, err := commandOutput(ctx, repository.Path, nil, "git", "worktree", "remove", "--force", workspace); err != nil {
		return fmt.Errorf("remove verification worktree: %w", err)
	}
	return nil
}

func pushBranch(ctx context.Context, workspace, branch, expectedRepository string) error {
	remoteOutput, err := commandOutput(ctx, workspace, nil, "git", "remote", "get-url", "--push", "origin")
	if err != nil {
		return fmt.Errorf("resolve origin push URL: %w", err)
	}
	actualRepository, err := githubRepositoryFromRemote(strings.TrimSpace(string(remoteOutput)))
	if err != nil {
		return fmt.Errorf("validate origin push URL: %w", err)
	}
	if !strings.EqualFold(actualRepository, expectedRepository) {
		return fmt.Errorf("origin points to GitHub repository %q, expected %q", actualRepository, expectedRepository)
	}
	remote := strings.TrimSpace(string(remoteOutput))
	refspec := "refs/heads/" + branch + ":refs/heads/" + branch
	_, err = commandOutput(ctx, workspace, nil, "git", "push", remote, refspec)
	return err
}

func githubRepositoryFromRemote(remote string) (string, error) {
	var repositoryPath string
	const scpPrefix = "git@github.com:"
	if len(remote) >= len(scpPrefix) && strings.EqualFold(remote[:len(scpPrefix)], scpPrefix) {
		repositoryPath = remote[len(scpPrefix):]
	} else {
		remoteURL, err := url.Parse(remote)
		if err != nil || !strings.EqualFold(remoteURL.Hostname(), "github.com") || remoteURL.RawQuery != "" || remoteURL.Fragment != "" {
			return "", errors.New("origin push URL is not a GitHub repository")
		}
		repositoryPath = remoteURL.Path
	}
	repositoryPath = strings.TrimSuffix(strings.Trim(strings.TrimSpace(repositoryPath), "/"), ".git")
	parts := strings.Split(repositoryPath, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", errors.New("origin push URL must identify one GitHub owner and repository")
	}
	return parts[0] + "/" + parts[1], nil
}

func ensureClean(ctx context.Context, workspace string) error {
	output, err := commandOutput(ctx, workspace, nil, "git", "status", "--porcelain")
	if err != nil {
		return err
	}
	if len(output) != 0 {
		return errors.New("verifier changed the checkout; verification agents must not modify product files")
	}
	return nil
}

func ensureExactHead(ctx context.Context, workspace, expectedSHA string) error {
	if err := ensureClean(ctx, workspace); err != nil {
		return err
	}
	output, err := commandOutput(ctx, workspace, nil, "git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	actualSHA := strings.TrimSpace(string(output))
	if actualSHA != expectedSHA {
		return fmt.Errorf("checkout HEAD changed during verification: expected %s, got %s", expectedSHA, actualSHA)
	}
	return nil
}
