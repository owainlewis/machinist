// Package fakegh installs a fake gh executable for tests. GitHub's state lives
// in files, so the control plane and agent processes share one fake.
package fakegh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const script = `#!/bin/sh
dir=%q
printf '%%s\n' "$*" >> "$dir/calls"
if [ -f "$dir/fail" ]; then cat "$dir/fail" >&2; exit 1; fi
if [ -f "$dir/fail-on" ]; then
  pattern=$(head -n 1 "$dir/fail-on")
  case "$*" in *"$pattern"*) tail -n +2 "$dir/fail-on" >&2; exit 1;; esac
fi
show() { if [ -f "$1" ]; then cat "$1"; else echo "$2"; fi; }
case "$1 $2" in
"pr list")
  head=
  while [ $# -gt 0 ]; do if [ "$1" = --head ]; then head=$2; fi; shift; done
  show "$dir/prlist-$(printf %%s "$head" | tr / _)" '[]'
  exit 0;;
"pr create")
  number=$(cat "$dir/next-pr")
  echo $((number + 1)) > "$dir/next-pr"
  branch=$(git branch --show-current)
  printf '[{"number":%%s,"state":"OPEN","isCrossRepository":false}]' "$number" > "$dir/prlist-$(printf %%s "$branch" | tr / _)"
  cp "$dir/open-pr" "$dir/pr-$number"
  echo "https://github.com/owner/repo/pull/$number"
  exit 0;;
"pr comment")
  number=$3
  body=
  while [ $# -gt 0 ]; do if [ "$1" = --body ]; then body=$2; fi; shift; done
  printf '%%s\t%%s\n' "$number" "$body" >> "$dir/pr-comments"
  exit 0;;
esac
method=GET
endpoint=
while [ $# -gt 0 ]; do
  case "$1" in
  --method) method=$2; shift;;
  repos/*|user|graphql) endpoint=$1;;
  esac
  shift
done
case "$method $endpoint" in
"GET user") cat "$dir/user";;
"GET repos/"*"/issues/comments?"*) show "$dir/comments" '[[]]';;
"POST graphql")
  number=$(sed -n 's/.*"number":\([0-9]*\).*/\1/p')
  show "$dir/pr-$number" '{"data":{"repository":null}}';;
"POST repos/"*"/comments")
  number=$(printf %%s "$endpoint" | sed -n 's/.*issues\/\([0-9]*\)\/comments/\1/p')
  payload=$(cat)
  printf '%%s\t%%s\n' "$number" "$payload" >> "$dir/posted"
  echo '{}';;
"GET repos/"*"/permission")
  login=$(printf %%s "$endpoint" | sed -n 's/.*collaborators\/\(.*\)\/permission/\1/p')
  if [ -f "$dir/permission-$login" ]; then cat "$dir/permission-$login"; else echo 'gh: Not Found (HTTP 404)' >&2; exit 1; fi;;
"GET repos/"*"/pulls/"*"/commits"*)
  number=$(printf %%s "$endpoint" | sed -n 's/.*pulls\/\([0-9]*\)\/commits.*/\1/p')
  show "$dir/commits-$number" '[[]]';;
"GET repos/"*"/branches/"*) show "$dir/branch" '{"protected":true}';;
"GET repos/"*"/issues/"*)
  number=${endpoint##*/}
  if [ "$(cat "$dir/issue-$number" 2>/dev/null)" = missing ]; then echo 'gh: Not Found (HTTP 404)' >&2; exit 1; fi
  show "$dir/issue-$number" '{"title":"An issue","body":"The issue text"}';;
"GET repos/"*) show "$dir/repository" '{"default_branch":"main","permissions":{"admin":false}}';;
*) echo "fake gh: unknown call: $method $endpoint" >&2; exit 1;;
esac
`

// Comment is a comment to place on the fake repository.
type Comment struct {
	ID     int64
	Author string
	Body   string
	Number int
	OnPR   bool
}

// Check is one check on a PR's head commit.
type Check struct {
	Name       string
	Status     string
	Conclusion string
}

// Review is one submitted PR review.
type Review struct {
	ID     string
	Author string
	State  string
}

// PR is the state of a pull request.
type PR struct {
	State    string
	ClosedAt string
	MergedAt string
	HeadSHA  string
	Checks   []Check
	Reviews  []Review
}

// Post is a comment the code under test posted.
type Post struct {
	Number int
	Body   string
}

// GitHub is one fake repository at owner/repo.
type GitHub struct {
	t        testing.TB
	dir      string
	mu       sync.Mutex
	comments []map[string]any
}

// Install puts a fake gh first on PATH. The token belongs to login.
func Install(t testing.TB, login string) *GitHub {
	t.Helper()
	dir := t.TempDir()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), fmt.Appendf(nil, script, dir), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	github := &GitHub{t: t, dir: dir}
	github.write("user", map[string]string{"login": login})
	github.writeText("next-pr", "7\n")
	github.write("open-pr", prResponse(PR{State: "OPEN", HeadSHA: "abc123"}))
	return github
}

func (g *GitHub) writeText(name, body string) {
	g.t.Helper()
	// A rename is atomic, so a gh process never reads half a file.
	temporary := filepath.Join(g.dir, "."+name+".tmp")
	if err := os.WriteFile(temporary, []byte(body), 0o600); err != nil {
		g.t.Fatal(err)
	}
	if err := os.Rename(temporary, filepath.Join(g.dir, name)); err != nil {
		g.t.Fatal(err)
	}
}

func (g *GitHub) write(name string, value any) {
	g.t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		g.t.Fatal(err)
	}
	g.writeText(name, string(body))
}

func (g *GitHub) read(name string) string {
	body, err := os.ReadFile(filepath.Join(g.dir, name))
	if err != nil && !os.IsNotExist(err) {
		g.t.Fatal(err)
	}
	return string(body)
}

// AddComment places a comment on the repository.
func (g *GitHub) AddComment(comment Comment) {
	g.t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	kind := "issues"
	if comment.OnPR {
		kind = "pull"
	}
	g.comments = append(g.comments, map[string]any{
		"id": comment.ID, "body": comment.Body, "user": map[string]string{"login": comment.Author},
		"html_url": fmt.Sprintf("https://github.com/owner/repo/%s/%d#issuecomment-%d", kind, comment.Number, comment.ID),
	})
	g.write("comments", [][]map[string]any{g.comments})
}

// SetPermission sets a login's permission on the repository.
func (g *GitHub) SetPermission(login, permission string) {
	g.t.Helper()
	g.write("permission-"+login, map[string]string{"permission": permission})
}

// SetIssue sets an issue's title and text.
func (g *GitHub) SetIssue(number int, title, body string) {
	g.t.Helper()
	g.write("issue-"+strconv.Itoa(number), map[string]string{"title": title, "body": body})
}

// SetIssuePR makes an issue number belong to a PR.
func (g *GitHub) SetIssuePR(number int) {
	g.t.Helper()
	g.write("issue-"+strconv.Itoa(number), map[string]any{"title": "A PR", "body": "PR text", "pull_request": map[string]string{"url": "x"}})
}

// RemoveIssue makes an issue number not exist.
func (g *GitHub) RemoveIssue(number int) {
	g.t.Helper()
	g.writeText("issue-"+strconv.Itoa(number), "missing")
}

// SetBranchPR makes number the PR for a branch.
func (g *GitHub) SetBranchPR(branch string, number int, state string, fork bool) {
	g.t.Helper()
	g.write("prlist-"+strings.ReplaceAll(branch, "/", "_"), []map[string]any{{"number": number, "state": state, "isCrossRepository": fork}})
}

// SetBranchPRs sets the full PR list for a branch.
func (g *GitHub) SetBranchPRs(branch string, list []map[string]any) {
	g.t.Helper()
	g.write("prlist-"+strings.ReplaceAll(branch, "/", "_"), list)
}

func prResponse(pr PR) map[string]any {
	contexts := []map[string]any{}
	for _, check := range pr.Checks {
		contexts = append(contexts, map[string]any{"__typename": "CheckRun", "name": check.Name, "status": check.Status, "conclusion": check.Conclusion})
	}
	reviews := []map[string]any{}
	for _, review := range pr.Reviews {
		reviews = append(reviews, map[string]any{"id": review.ID, "state": review.State, "url": "https://github.com/owner/repo/pull/1#" + review.ID, "author": map[string]string{"login": review.Author}})
	}
	response := map[string]any{
		"state": pr.State, "headRefOid": pr.HeadSHA, "reviews": map[string]any{"nodes": reviews},
		"commits": map[string]any{"nodes": []map[string]any{{"commit": map[string]any{"statusCheckRollup": map[string]any{"contexts": map[string]any{"nodes": contexts, "pageInfo": map[string]bool{"hasNextPage": false}}}}}}},
	}
	if pr.ClosedAt != "" {
		response["closedAt"] = pr.ClosedAt
	}
	if pr.MergedAt != "" {
		response["mergedAt"] = pr.MergedAt
	}
	return map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": response}}}
}

// SetPR sets what a PR looks like to the follow-up poller.
func (g *GitHub) SetPR(number int, pr PR) {
	g.t.Helper()
	g.write("pr-"+strconv.Itoa(number), prResponse(pr))
}

// SetCommits sets the author emails of a PR's commits.
func (g *GitHub) SetCommits(number int, emails ...string) {
	g.t.Helper()
	commits := []map[string]any{}
	for _, email := range emails {
		commits = append(commits, map[string]any{"commit": map[string]any{"author": map[string]string{"email": email}}})
	}
	g.write("commits-"+strconv.Itoa(number), [][]map[string]any{commits})
}

// SetAccess sets the default branch's protection and the login's rights.
func (g *GitHub) SetAccess(protected, admin bool) {
	g.t.Helper()
	g.write("branch", map[string]bool{"protected": protected})
	g.write("repository", map[string]any{"default_branch": "main", "permissions": map[string]bool{"admin": admin}})
}

// Fail makes every gh call fail with this error text.
func (g *GitHub) Fail(message string) {
	g.t.Helper()
	g.writeText("fail", message+"\n")
}

// FailOn makes calls containing a pattern fail with this error text.
func (g *GitHub) FailOn(pattern, message string) {
	g.t.Helper()
	g.writeText("fail-on", pattern+"\n"+message+"\n")
}

// Recover undoes Fail and FailOn.
func (g *GitHub) Recover() {
	g.t.Helper()
	for _, name := range []string{"fail", "fail-on"} {
		if err := os.Remove(filepath.Join(g.dir, name)); err != nil && !os.IsNotExist(err) {
			g.t.Fatal(err)
		}
	}
}

// Calls returns each gh command line so far.
func (g *GitHub) Calls() []string { return lines(g.read("calls")) }

// Posted returns the comments posted through the API, oldest first.
func (g *GitHub) Posted() []Post {
	g.t.Helper()
	posts := []Post{}
	for _, line := range lines(g.read("posted")) {
		number, body, _ := strings.Cut(line, "\t")
		parsed, err := strconv.Atoi(number)
		if err != nil {
			g.t.Fatalf("posted line %q: %v", line, err)
		}
		var payload struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			g.t.Fatalf("posted line %q: %v", line, err)
		}
		posts = append(posts, Post{Number: parsed, Body: payload.Body})
	}
	return posts
}

// PRComments returns the comments agents posted with gh pr comment.
func (g *GitHub) PRComments() []string { return lines(g.read("pr-comments")) }

func lines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(text, "\n"), "\n")
}
