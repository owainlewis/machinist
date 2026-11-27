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
"repo clone")
  # gh repo clone REPO DIR -- GIT-FLAGS clones the remote the test set.
  target=$4
  shift 4
  if [ "$1" = -- ]; then shift; fi
  exec git clone "$@" "$(cat "$dir/remote")" "$target";;
"pr view")
  # A step names the branch, and the control plane names the number.
  case "$3" in
  *[!0-9]*)
    file="$dir/prlist-$(printf %%s "$3" | tr / _)"
    number=$(sed -n 's/.*"number":\([0-9]*\).*/\1/p' "$file" 2>/dev/null);;
  *)
    file=$(grep -l "\"number\":$3," "$dir"/prlist-* 2>/dev/null | head -n 1)
    number=; [ -n "$file" ] && number=$3;;
  esac
  if [ -z "$number" ]; then echo "no pull requests found for branch \"$3\"" >&2; exit 1; fi
  case "$*" in
  *--json*)
    state=$(sed -n 's/.*"number":'"$number"',"state":"\([A-Z]*\)".*/\1/p' "$file")
    printf '{"number":%%s,"url":"https://github.com/owner/repo/pull/%%s","state":"%%s","mergeStateStatus":"CLEAN","headRefOid":"fakehead"}\n' "$number" "$number" "$state";;
  *) echo "https://github.com/owner/repo/pull/$number";;
  esac
  exit 0;;
"pr list")
  head=
  while [ $# -gt 0 ]; do if [ "$1" = --head ]; then head=$2; fi; shift; done
  show "$dir/prlist-$(printf %%s "$head" | tr / _)" '[]'
  exit 0;;
"pr create")
  number=$(cat "$dir/next-pr")
  echo $((number + 1)) > "$dir/next-pr"
  while [ $# -gt 0 ]; do if [ "$1" = --body-file ]; then cp "$2" "$dir/pr-body-$number"; fi; shift; done
  branch=$(git branch --show-current)
  printf '[{"number":%%s,"state":"OPEN","isCrossRepository":false}]' "$number" > "$dir/prlist-$(printf %%s "$branch" | tr / _)"
  cp "$dir/open-pr" "$dir/pr-$number"
  echo "https://github.com/owner/repo/pull/$number"
  exit 0;;
"pr checks")
  if [ "$(cat "$dir/checks" 2>/dev/null)" = none ]; then echo "no checks reported on the 'machinist/t_1' branch" >&2; exit 1; fi
  show "$dir/checks" '[{"name":"test","bucket":"pass","link":"https://ci.example.test/1"}]'
  # gh exits 8 while checks are pending.
  if grep -q '"pending"' "$dir/checks" 2>/dev/null; then exit 8; fi
  exit 0;;
"pr update-branch")
  exit 0;;
"pr merge")
  number=$3
  if [ -f "$dir/merge-refused-$number" ]; then cat "$dir/merge-refused-$number" >&2; exit 1; fi
  echo "$number" >> "$dir/merged"
  for list in "$dir"/prlist-*; do
    [ -f "$list" ] && sed -i.bak "s/\"number\":$number,\"state\":\"OPEN\"/\"number\":$number,\"state\":\"MERGED\",\"mergedAt\":\"2026-01-02T03:04:05Z\"/" "$list" && rm -f "$list.bak"
  done
  exit 0;;
"pr comment")
  number=$3
  body=
  while [ $# -gt 0 ]; do if [ "$1" = --body ]; then body=$2; fi; shift; done
  printf '%%s\t%%s\n' "$number" "$body" >> "$dir/pr-comments"
  exit 0;;
esac
include=
method=GET
endpoint=
while [ $# -gt 0 ]; do
  case "$1" in
  --include) include=true;;
  --method) method=$2; shift;;
  repos/*|user|graphql) endpoint=$1;;
  esac
  shift
done
case "$method $endpoint" in
"GET user")
  if [ "$include" = true ]; then
    printf 'HTTP/2.0 200 OK\r\n'
    if [ -f "$dir/scopes" ]; then printf 'X-OAuth-Scopes: %%s\r\n' "$(cat "$dir/scopes")"; fi
    printf '\r\n'
  fi
  cat "$dir/user";;
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
"GET repos/"*"/pulls/"*"/reviews"*)
  number=$(printf %%s "$endpoint" | sed -n 's/.*pulls\/\([0-9]*\)\/reviews.*/\1/p')
  show "$dir/reviews-$number" '[[]]';;
"GET repos/"*"/branches/"*) show "$dir/branch" '{"protected":true}';;
"GET repos/"*"/issues/"*)
  number=${endpoint##*/}
  if [ "$(cat "$dir/issue-$number" 2>/dev/null)" = missing ]; then echo 'gh: Not Found (HTTP 404)' >&2; exit 1; fi
  show "$dir/issue-$number" '{"title":"An issue","body":"The issue text"}';;
"GET repos/"*) show "$dir/repository" '{"default_branch":"main","permissions":{"admin":false}}';;
*) echo "fake gh: unknown call: $method $endpoint" >&2; exit 1;;
esac
`

// Post is a comment the code under test posted.
type Post struct {
	Number int
	Body   string
}

// GitHub is one fake repository at owner/repo.
type GitHub struct {
	t   testing.TB
	dir string
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
	github.write("open-pr", map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"pullRequest": map[string]any{
					"state":      "OPEN",
					"headRefOid": "abc123",
					"reviews":    map[string]any{"nodes": []map[string]any{}},
					"commits": map[string]any{
						"nodes": []map[string]any{{
							"commit": map[string]any{
								"statusCheckRollup": map[string]any{
									"contexts": map[string]any{
										"nodes":    []map[string]any{},
										"pageInfo": map[string]bool{"hasNextPage": false},
									},
								},
							},
						}},
					},
				},
			},
		},
	})
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

// SetReviews sets a PR's reviews, as GitHub's REST API lists them.
func (g *GitHub) SetReviews(number int, reviews ...map[string]any) {
	g.t.Helper()
	g.write("reviews-"+strconv.Itoa(number), [][]map[string]any{reviews})
}

// SetAccess sets the default branch's protection and the login's rights.
func (g *GitHub) SetAccess(protected, admin bool) {
	g.t.Helper()
	g.write("branch", map[string]bool{"protected": protected})
	g.write("repository", map[string]any{"default_branch": "main", "permissions": map[string]bool{"admin": admin}})
}

// SetRepository sets what the repository's API answer holds, such as its
// merge settings.
func (g *GitHub) SetRepository(fields map[string]any) {
	g.t.Helper()
	g.write("repository", fields)
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

// SetRemote makes gh repo clone clone this Git remote.
func (g *GitHub) SetRemote(remote string) {
	g.t.Helper()
	g.writeText("remote", remote)
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

// SetChecks sets what gh pr checks --json prints, such as a failed check.
func (g *GitHub) SetChecks(checks []map[string]string) {
	g.t.Helper()
	g.write("checks", checks)
}

// SetNoChecks makes gh pr checks say no checks were reported.
func (g *GitHub) SetNoChecks() {
	g.t.Helper()
	g.writeText("checks", "none")
}

// Merged returns the PRs that were merged.
func (g *GitHub) Merged() []string { return lines(g.read("merged")) }

// PRComments returns the comments agents posted with gh pr comment.
func (g *GitHub) PRComments() []string { return lines(g.read("pr-comments")) }

// PRBody is the body a step gave pr create for a pull request.
func (g *GitHub) PRBody(number int) string { return g.read("pr-body-" + strconv.Itoa(number)) }

func lines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(text, "\n"), "\n")
}

// SetScopes sets the classic token scopes reported in API response headers.
func (g *GitHub) SetScopes(scopes string) {
	g.t.Helper()
	g.writeText("scopes", scopes)
}
