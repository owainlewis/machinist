package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var maintenanceComparePath = regexp.MustCompile(`^/repos/[^/]+/[^/]+/compare/[0-9a-fA-F]{40}\.\.\.[0-9a-fA-F]{40}$`)

// This read-only query is the only GraphQL operation accepted by the App
// connection. Its repository variables are checked against the installation.
const maintenancePRQuery = `query MachinistMaintenance($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){state isDraft headRefOid baseRefOid mergeStateStatus reviewDecision reviewThreads(first:100){nodes{isResolved} pageInfo{hasNextPage}} reviews(last:100){nodes{state author{login}} pageInfo{hasPreviousPage}} commits(last:1){nodes{commit{oid statusCheckRollup{state contexts(first:100){nodes{__typename ... on CheckRun{status conclusion} ... on StatusContext{state}} pageInfo{hasNextPage}}}}}}}}}`

type maintenanceReviewState struct {
	State                                                    string
	IsDraft                                                  bool
	HeadRefOid, BaseRefOid, MergeStateStatus, ReviewDecision string
	ReviewThreads                                            struct {
		Nodes    []struct{ IsResolved bool }
		PageInfo struct{ HasNextPage bool }
	}
	Reviews struct {
		Nodes []struct {
			State  string
			Author *struct{ Login string }
		}
		PageInfo struct{ HasPreviousPage bool }
	}
	Commits struct {
		Nodes []struct {
			Commit struct {
				Oid               string
				StatusCheckRollup *struct {
					State    string
					Contexts struct {
						Nodes []struct {
							Type                      string `json:"__typename"`
							Status, Conclusion, State string
						}
						PageInfo struct{ HasNextPage bool }
					}
				}
			}
		}
	}
}

func (g *ghFactoryClient) MaintenanceSnapshot(ctx context.Context, repo string) (*maintenanceSnapshot, error) {
	root, err := githubRepositoryPath(repo)
	if err != nil {
		return nil, err
	}
	catalog, err := g.RepositoryLabels(ctx, repo)
	if err != nil {
		return nil, err
	}
	snapshot := &maintenanceSnapshot{Repository: repo, Labels: catalog, Items: []maintenanceItem{}}
	// A bounded, explicit snapshot keeps agent input and completion contracts small.
	// Fetch a sentinel item to tell the operator when a repository exceeds the batch.
	for _, kind := range []string{"issue", "pr"} {
		endpoint := root + "/issues?state=open&sort=updated&direction=desc&per_page=100"
		if kind == "pr" {
			endpoint = root + "/pulls?state=open&sort=updated&direction=desc&per_page=11"
		}
		count := 0
		for page := 1; page <= 100; page++ {
			raw, readErr := g.api(ctx, "GET", fmt.Sprintf("%s&page=%d", endpoint, page), nil)
			if readErr != nil {
				return nil, readErr
			}
			var batch []struct {
				GitHubIssue
				Head   struct{ SHA string }
				Base   struct{ SHA string }
				Labels []struct{ Name string }
			}
			if err = json.Unmarshal(raw, &batch); err != nil {
				return nil, err
			}
			for _, item := range batch {
				if kind == "issue" && len(item.PullRequest) > 0 && string(item.PullRequest) != "null" {
					continue
				}
				count++
				if count > 10 {
					snapshot.Limited = true
					break
				}
				labels := []string{}
				for _, label := range item.Labels {
					labels = append(labels, label.Name)
				}
				body := []rune(item.Body)
				if len(body) > 4000 {
					body = body[:4000]
				}
				entry := maintenanceItem{Number: item.Number, Kind: kind, Title: item.Title, Body: string(body), URL: item.HTMLURL, UpdatedAt: item.UpdatedAt, HeadSHA: item.Head.SHA, BaseSHA: item.Base.SHA, Labels: labels}
				if kind == "pr" {
					if err = g.maintenanceEvidence(ctx, repo, &entry); err != nil {
						return nil, err
					}
				}
				snapshot.Items = append(snapshot.Items, entry)
			}
			if count > 10 || len(batch) < map[string]int{"issue": 100, "pr": 11}[kind] {
				break
			}
			if page == 100 {
				return nil, errors.New("too many GitHub pages to build maintenance snapshot")
			}
		}
	}
	return snapshot, nil
}
func (g *ghFactoryClient) MaintenanceItem(ctx context.Context, repo string, original maintenanceItem) (maintenanceItem, error) {
	root, err := githubRepositoryPath(repo)
	if err != nil {
		return maintenanceItem{}, err
	}
	endpoint := fmt.Sprintf("%s/issues/%d", root, original.Number)
	if original.Kind == "pr" {
		endpoint = fmt.Sprintf("%s/pulls/%d", root, original.Number)
	}
	raw, err := g.api(ctx, "GET", endpoint, nil)
	if err != nil {
		return maintenanceItem{}, err
	}
	var current struct {
		GitHubIssue
		Merged bool
		Head   struct{ SHA string }
		Base   struct{ SHA string }
	}
	if err = json.Unmarshal(raw, &current); err != nil {
		return maintenanceItem{}, err
	}
	if (current.State != "open" && !(original.Kind == "pr" && current.Merged)) || current.Number != original.Number {
		return maintenanceItem{}, errors.New("item is no longer open")
	}
	return maintenanceItem{Number: current.Number, Kind: original.Kind, UpdatedAt: current.UpdatedAt, HeadSHA: current.Head.SHA, BaseSHA: current.Base.SHA, Merged: current.Merged}, nil
}
func (g *ghFactoryClient) AddMaintenanceLabels(ctx context.Context, repo string, number int, suggestions []string) error {
	if len(suggestions) == 0 {
		return nil
	}
	root, err := githubRepositoryPath(repo)
	if err != nil {
		return err
	}
	catalog, err := g.RepositoryLabels(ctx, repo)
	if err != nil {
		return err
	}
	available := map[string]string{}
	for _, label := range catalog {
		available[strings.ToLower(label)] = label
	}
	add := []string{}
	seen := map[string]bool{}
	for _, label := range suggestions {
		key := strings.ToLower(label)
		if canonical, ok := available[key]; ok && !strings.HasPrefix(key, "machinist:") && !seen[key] {
			add = append(add, canonical)
			seen[key] = true
		}
	}
	if len(add) == 0 {
		return nil
	}
	body, _ := json.Marshal(map[string]any{"labels": add})
	_, err = g.api(ctx, "POST", fmt.Sprintf("%s/issues/%d/labels", root, number), body)
	return err
}
func (g *ghFactoryClient) maintenancePull(ctx context.Context, root string, number int) (maintenancePull, error) {
	var pr maintenancePull
	raw, err := g.api(ctx, "GET", fmt.Sprintf("%s/pulls/%d", root, number), nil)
	if err != nil {
		return pr, err
	}
	err = json.Unmarshal(raw, &pr)
	return pr, err
}
func maintenanceChecks(review maintenanceReviewState, head, base string) error {
	if review.State != "OPEN" || review.IsDraft || review.HeadRefOid != head || review.BaseRefOid != base || review.MergeStateStatus != "CLEAN" {
		return errors.New("PR is draft, changed, conflicted, behind, or blocked by GitHub rules")
	}
	if review.ReviewDecision == "CHANGES_REQUESTED" || review.ReviewDecision == "REVIEW_REQUIRED" {
		return errors.New("GitHub review requirements are not satisfied")
	}
	if review.ReviewThreads.PageInfo.HasNextPage || review.Reviews.PageInfo.HasPreviousPage {
		return errors.New("too much review history to verify safely")
	}
	for _, thread := range review.ReviewThreads.Nodes {
		if !thread.IsResolved {
			return errors.New("unresolved review conversation")
		}
	}
	latest := map[string]string{}
	for _, r := range review.Reviews.Nodes {
		if r.Author == nil {
			return errors.New("review author unavailable")
		}
		if r.State != "COMMENTED" && r.State != "PENDING" {
			latest[r.Author.Login] = r.State
		}
	}
	for _, state := range latest {
		if state == "CHANGES_REQUESTED" {
			return errors.New("reviewer requested changes")
		}
	}
	if len(review.Commits.Nodes) != 1 {
		return errors.New("current commit checks unavailable")
	}
	commit := review.Commits.Nodes[0].Commit
	rollup := commit.StatusCheckRollup
	if commit.Oid != head || rollup == nil || rollup.State != "SUCCESS" || len(rollup.Contexts.Nodes) == 0 || rollup.Contexts.PageInfo.HasNextPage {
		return errors.New("current revision must have passing checks")
	}
	for _, check := range rollup.Contexts.Nodes {
		switch check.Type {
		case "CheckRun":
			if check.Status != "COMPLETED" || check.Conclusion != "SUCCESS" {
				return errors.New("a check is pending, skipped, or unsuccessful")
			}
		case "StatusContext":
			if check.State != "SUCCESS" {
				return errors.New("a commit status is not successful")
			}
		default:
			return errors.New("unknown check type")
		}
	}
	return nil
}

// Verify ordinary Markdown blobs at both revisions. File extensions do not
// distinguish a document from a symlink, executable, submodule, or mode change.
func (g *ghFactoryClient) maintenanceBlob(ctx context.Context, root, revision, name string, cache map[string]map[string]struct{ Mode, Type, SHA string }) error {
	parts := strings.Split(name, "/")
	for i, part := range parts {
		tree, ok := cache[revision]
		if !ok {
			raw, err := g.api(ctx, "GET", root+"/git/trees/"+url.PathEscape(revision), nil)
			if err != nil {
				return err
			}
			var response struct {
				Truncated bool
				Tree      []struct{ Path, Mode, Type, SHA string }
			}
			if err = json.Unmarshal(raw, &response); err != nil {
				return err
			}
			if response.Truncated {
				return errors.New("Git tree is truncated")
			}
			tree = map[string]struct{ Mode, Type, SHA string }{}
			for _, entry := range response.Tree {
				tree[entry.Path] = struct{ Mode, Type, SHA string }{entry.Mode, entry.Type, entry.SHA}
			}
			cache[revision] = tree
		}
		entry, found := tree[part]
		if !found {
			return errors.New("document missing from Git tree")
		}
		if i == len(parts)-1 {
			if entry.Type != "blob" || entry.Mode != "100644" {
				return errors.New("only ordinary, non-executable Markdown files qualify")
			}
			return nil
		}
		if entry.Type != "tree" || entry.Mode != "040000" {
			return errors.New("document directory is not a Git tree")
		}
		revision = entry.SHA
	}
	return errors.New("invalid document path")
}
func (g *ghFactoryClient) MergeMaintenancePR(ctx context.Context, repo string, number int, head string) (maintenancePull, error) {
	root, err := githubRepositoryPath(repo)
	if err != nil {
		return maintenancePull{}, err
	}
	pr, err := g.maintenancePull(ctx, root, number)
	if err != nil {
		return pr, err
	}
	if pr.Head.SHA != head || pr.Head.Repo.FullName != repo || pr.Base.Repo.FullName != repo {
		return pr, errors.New("PR revision or repository changed")
	}
	if pr.Merged {
		return pr, nil
	} // Reconcile a lost successful merge response.
	if pr.State != "open" || pr.Draft {
		return pr, errors.New("PR must be open and ready")
	}
	raw, err := g.api(ctx, "GET", root, nil)
	if err != nil {
		return pr, err
	}
	var repository struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err = json.Unmarshal(raw, &repository); err != nil {
		return pr, err
	}
	if repository.DefaultBranch == "" || pr.Base.Ref != repository.DefaultBranch {
		return pr, errors.New("only the repository default branch qualifies")
	}
	// Server-side rules also close races with checks and review conversations
	// changing after inspection. Do not rely on an owner's ability to bypass them.
	raw, err = g.api(ctx, "GET", root+"/rules/branches/"+url.PathEscape(pr.Base.Ref), nil)
	if err != nil {
		return pr, errors.New("branch rules could not be verified")
	}
	var rules []struct {
		Type       string
		Parameters struct {
			RequiredReviewThreadResolution   bool              `json:"required_review_thread_resolution"`
			StrictRequiredStatusChecksPolicy bool              `json:"strict_required_status_checks_policy"`
			RequiredStatusChecks             []json.RawMessage `json:"required_status_checks"`
		}
	}
	if err = json.Unmarshal(raw, &rules); err != nil {
		return pr, err
	}
	checksRequired, conversationsRequired := false, false
	for _, rule := range rules {
		if rule.Type == "required_status_checks" && rule.Parameters.StrictRequiredStatusChecksPolicy && len(rule.Parameters.RequiredStatusChecks) > 0 {
			checksRequired = true
		}
		if rule.Type == "pull_request" && rule.Parameters.RequiredReviewThreadResolution {
			conversationsRequired = true
		}
	}
	if !checksRequired || !conversationsRequired {
		return pr, errors.New("auto-merge requires branch rules for checks, an up-to-date branch and resolved conversations")
	}
	raw, err = g.api(ctx, "GET", fmt.Sprintf("%s/pulls/%d/files?per_page=100", root, number), nil)
	if err != nil {
		return pr, err
	}
	var files []struct {
		Filename         string
		PreviousFilename string `json:"previous_filename"`
		Status           string
		Changes          int
	}
	if err = json.Unmarshal(raw, &files); err != nil {
		return pr, err
	}
	if len(files) == 0 || len(files) > 5 {
		return pr, errors.New("auto-merge allows at most five existing documentation files")
	}
	changes := 0
	cache := map[string]map[string]struct{ Mode, Type, SHA string }{}
	for _, file := range files {
		if !allowedMaintenanceFile(maintenanceFile{file.Filename, file.PreviousFilename, file.Status, file.Changes}) {
			return pr, errors.New("change is outside the documentation-only merge policy")
		}
		changes += file.Changes
		if file.Changes < 0 || changes > 200 {
			return pr, errors.New("documentation change exceeds 200 changed lines")
		}
		for _, revision := range []string{pr.Base.SHA, pr.Head.SHA} {
			if err = g.maintenanceBlob(ctx, root, revision, file.Filename, cache); err != nil {
				return pr, err
			}
		}
	}
	review, err := g.maintenanceReview(ctx, repo, number)
	if err != nil {
		return pr, err
	}
	if err = maintenanceChecks(*review, pr.Head.SHA, pr.Base.SHA); err != nil {
		return pr, err
	}
	// Re-read after inspection. SHA guard on the actual merge closes the head race;
	// CLEAN on this read catches a moving base or newly blocked GitHub requirement.
	fresh, err := g.maintenancePull(ctx, root, number)
	if err != nil {
		return pr, err
	}
	if fresh.Head.SHA != head || fresh.Base.SHA != pr.Base.SHA || fresh.Base.Ref != pr.Base.Ref || fresh.Head.Repo.FullName != repo || fresh.Base.Repo.FullName != repo || fresh.State != "open" || fresh.Draft || fresh.MergeableState != "clean" {
		return pr, errors.New("PR changed or became blocked during verification")
	}
	body, _ := json.Marshal(map[string]any{"sha": head, "merge_method": "squash", "merge_action": "default", "bypass_rules": false})
	raw, err = g.api(ctx, "PUT", fmt.Sprintf("%s/pulls/%d/merge-async", root, number), body)
	if err != nil {
		return pr, errors.New("GitHub declined the guarded merge; review its checks and branch protections")
	}
	var merged struct{ Status string }
	if err = json.Unmarshal(raw, &merged); err != nil {
		return pr, err
	}
	if merged.Status == "pending" || merged.Status == "enqueued" {
		return pr, nil
	}
	if merged.Status != "merged" {
		return pr, errors.New("GitHub did not confirm merge")
	}
	pr.Merged = true
	return pr, nil
}

func (g *ghFactoryClient) maintenanceReview(ctx context.Context, repo string, number int) (*maintenanceReviewState, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return nil, errors.New("invalid repository")
	}
	body, _ := json.Marshal(map[string]any{"query": maintenancePRQuery, "variables": map[string]any{"owner": parts[0], "name": parts[1], "number": number}})
	raw, err := g.api(ctx, "POST", "/graphql", body)
	if err != nil {
		return nil, err
	}
	var response struct {
		Errors []json.RawMessage
		Data   struct {
			Repository *struct{ PullRequest *maintenanceReviewState }
		}
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	if len(response.Errors) > 0 || response.Data.Repository == nil || response.Data.Repository.PullRequest == nil {
		return nil, errors.New("GitHub review and checks could not be verified")
	}
	return response.Data.Repository.PullRequest, nil
}

func (g *ghFactoryClient) maintenanceEvidence(ctx context.Context, repo string, item *maintenanceItem) error {
	if len(item.BaseSHA) != 40 || len(item.HeadSHA) != 40 {
		return errors.New("PR revision unavailable")
	}
	root, err := githubRepositoryPath(repo)
	if err != nil {
		return err
	}
	// Immutable revisions prevent a moving PR from changing the evidence shown to
	// the agent. The foreman checks those same revisions again before acting.
	raw, err := g.api(ctx, "GET", root+"/compare/"+url.PathEscape(item.BaseSHA+"..."+item.HeadSHA), nil)
	if err != nil {
		return err
	}
	var compare struct{ Files []maintenanceDiff }
	if err = json.Unmarshal(raw, &compare); err != nil {
		return err
	}
	complete := len(compare.Files) > 0 && len(compare.Files) <= 5
	changes, patchBytes := 0, 0
	for i, file := range compare.Files {
		if i >= 10 {
			break
		}
		changes += file.Changes
		patchBytes += len(file.Patch)
		if !allowedMaintenanceFile(maintenanceFile{Filename: file.Filename, Status: file.Status, Changes: file.Changes}) || file.Patch == "" || changes > 200 || patchBytes > 12000 {
			complete = false
		}
		if !complete {
			file.Patch = ""
		}
		item.Diff = append(item.Diff, file)
	}
	if !complete {
		for i := range item.Diff {
			item.Diff[i].Patch = ""
		}
	}
	review, err := g.maintenanceReview(ctx, repo, item.Number)
	if err != nil {
		return err
	}
	item.Checks = "Unavailable"
	if len(review.Commits.Nodes) == 1 && review.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
		item.Checks = review.Commits.Nodes[0].Commit.StatusCheckRollup.State
	}
	item.MergeEvidenceComplete = complete && review.HeadRefOid == item.HeadSHA && review.BaseRefOid == item.BaseSHA
	return nil
}
