package controlplane

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestGitHubIntakeDisposableRepository is intentionally opt-in because it creates and
// closes two issues in a real disposable repository. The repository must have the
// shipped comment-intake workflow installed on its default branch and its dedicated
// collaborator token configured as MACHINIST_INTAKE_TOKEN.
func TestGitHubIntakeDisposableRepository(t *testing.T) {
	repository := strings.TrimSpace(os.Getenv("MACHINIST_GITHUB_INTEGRATION_REPOSITORY"))
	if repository == "" {
		t.Skip("set MACHINIST_GITHUB_INTEGRATION_REPOSITORY to an OWNER/REPO disposable repository")
	}
	if _, err := normalizeGitHubRepository(repository); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	for _, label := range []struct {
		name, color, description string
	}{
		{"machinist:requested", "1d76db", "Request Machinist intake"},
		{"machinist:queued", "5319e7", "Machinist admitted this request"},
	} {
		runIntegrationGH(t, ctx, "label", "create", label.name, "--repo", repository, "--color", label.color, "--description", label.description, "--force")
	}
	actor := runIntegrationGH(t, ctx, "api", "user", "--jq", ".login")
	adapter := NewGitHubCLI("gh", 30*time.Second)

	directURL := createDisposableIssue(t, ctx, repository, "direct-label")
	runIntegrationGH(t, ctx, "issue", "edit", directURL, "--add-label", "machinist:requested")
	assertDisposableIntake(t, ctx, adapter, repository, directURL, actor)

	commentURL := createDisposableIssue(t, ctx, repository, "comment-to-label")
	runIntegrationGH(t, ctx, "issue", "comment", commentURL, "--body", "@machinist\n\nDisposable integration test")
	waitForRequestedLabel(t, ctx, adapter, repository, commentURL)
	assertDisposableIntake(t, ctx, adapter, repository, commentURL, "")
}

func createDisposableIssue(t *testing.T, ctx context.Context, repository, path string) string {
	t.Helper()
	title := fmt.Sprintf("Machinist disposable intake test: %s %d", path, time.Now().UnixNano())
	issueURL := runIntegrationGH(t, ctx, "issue", "create", "--repo", repository, "--title", title, "--body", "Created by the opt-in Machinist GitHub intake integration test.")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanupCtx, "gh", "issue", "close", issueURL, "--comment", "Disposable integration test cleanup.").Run()
	})
	return issueURL
}

func waitForRequestedLabel(t *testing.T, ctx context.Context, adapter *GitHubCLI, repository, issueURL string) {
	t.Helper()
	number := integrationIssueNumber(t, issueURL)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		details, err := adapter.IssueDetails(ctx, repository, number, "machinist:requested")
		if err == nil && details.RequestedEvent != nil && containsFold(details.Labels, "machinist:requested") {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("comment intake did not apply machinist:requested; verify the shipped workflow and MACHINIST_INTAKE_TOKEN in %s: %v", repository, ctx.Err())
		case <-ticker.C:
		}
	}
}

func assertDisposableIntake(t *testing.T, ctx context.Context, adapter *GitHubCLI, repository, issueURL, wantActor string) {
	t.Helper()
	number := integrationIssueNumber(t, issueURL)
	candidates, err := adapter.SearchRequestedIssues(ctx, []string{repository}, "machinist:requested", maxGitHubCandidates)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range candidates {
		found = found || candidate.URL == issueURL
	}
	if !found {
		t.Fatalf("%s was not returned by combined label search", issueURL)
	}
	details, err := adapter.IssueDetails(ctx, repository, number, "machinist:requested")
	if err != nil {
		t.Fatal(err)
	}
	if details.RequestedEvent == nil {
		t.Fatal("requested label event was not found")
	}
	if wantActor != "" && details.RequestedEvent.Actor != wantActor {
		t.Fatalf("label actor = %q, want %q", details.RequestedEvent.Actor, wantActor)
	}
	permission, err := adapter.Permission(ctx, repository, details.RequestedEvent.Actor)
	if err != nil {
		t.Fatal(err)
	}
	if !GitHubPermissionCanWrite(permission) {
		t.Fatalf("label actor %q has permission %q", details.RequestedEvent.Actor, permission)
	}
	if err := adapter.ReplaceRequestLabel(ctx, repository, number, "machinist:requested", "machinist:queued"); err != nil {
		t.Fatal(err)
	}
	details, err = adapter.IssueDetails(ctx, repository, number, "machinist:requested")
	if err != nil {
		t.Fatal(err)
	}
	if containsFold(details.Labels, "machinist:requested") || !containsFold(details.Labels, "machinist:queued") {
		t.Fatalf("labels after admission = %v", details.Labels)
	}
}

func integrationIssueNumber(t *testing.T, issueURL string) int {
	t.Helper()
	parts := strings.Split(strings.TrimRight(issueURL, "/"), "/")
	number, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || number <= 0 {
		t.Fatalf("invalid issue URL %q", issueURL)
	}
	return number
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

func runIntegrationGH(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	output, err := exec.CommandContext(ctx, "gh", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("gh %s failed: %v: %s", strings.Join(args, " "), err, sanitizeGitHubOutput(output))
	}
	return strings.TrimSpace(string(output))
}
