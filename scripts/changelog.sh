#!/usr/bin/env bash
#
# Print Markdown release notes for the pull requests merged between two tags.
#
#   scripts/changelog.sh <previous tag> <new tag> [end ref]
#
# The end ref defaults to the new tag. Pass HEAD to write notes for a tag that
# does not exist yet. An empty previous tag means the first release, so every
# commit up to the end ref counts.
#
# Pull requests are grouped by label: feature (or enhancement) is Features, bug
# is Fixes. Without those labels, a title whose first word is Add or Show is a
# feature and Fix is a fix. Everything else is Other.
#
# Needs git and an authenticated gh. GH_REPO picks the repository.

set -euo pipefail

previous=${1-}
version=${2-}
end_ref=${3:-$version}

if [[ $# -lt 2 || -z "$version" ]]; then
  echo "usage: $0 <previous tag> <new tag> [end ref]" >&2
  exit 2
fi

repo=${GH_REPO:-owainlewis/machinist}
server=${GITHUB_SERVER_URL:-https://github.com}
repo_url="$server/$repo"

commit_date() {
  TZ=UTC git log -1 --format=%cd --date=format-local:%Y-%m-%d "$1^{commit}"
}

end_date=$(commit_date "$end_ref")
if [[ -n "$previous" ]]; then
  start_date=$(commit_date "$previous")
  range="$previous..$end_ref"
else
  start_date=1970-01-01
  range=$end_ref
fi

work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT

git rev-list "$range" >"$work_dir/commits"

# The search narrows the list by date. The commit range decides what is in it.
gh pr list \
  --repo "$repo" \
  --state merged \
  --search "merged:$start_date..$end_date" \
  --limit 1000 \
  --json number,title,labels,mergeCommit \
  --jq '.[] | [(.mergeCommit.oid // ""), (.number | tostring), .title, ([.labels[].name] | join(","))] | @tsv' \
  >"$work_dir/pulls"

printf '## %s (%s)\n' "$version" "$end_date"

awk -F '\t' -v repo_url="$repo_url" '
  FNR == NR { in_range[$1] = 1; next }
  !($1 in in_range) { next }
  {
    number = $2
    title = $3
    group = ""
    split($4, labels, ",")
    for (i in labels) {
      if (labels[i] == "feature" || labels[i] == "enhancement") group = "Features"
    }
    if (group == "") {
      for (i in labels) {
        if (labels[i] == "bug") group = "Fixes"
      }
    }
    if (group == "") {
      word = tolower(title)
      sub(/[^a-z].*/, "", word)
      if (word == "add" || word == "show") group = "Features"
      else if (word == "fix") group = "Fixes"
      else group = "Other"
    }
    lines[group] = lines[group] sprintf("- %s ([#%s](%s/pull/%s))\n", title, number, repo_url, number)
    count++
  }
  END {
    if (count == 0) {
      printf "\nNo pull requests were merged.\n"
    }
    split("Features Fixes Other", order, " ")
    for (i = 1; i <= 3; i++) {
      if (order[i] in lines) printf "\n### %s\n\n%s", order[i], lines[order[i]]
    }
  }
' "$work_dir/commits" <(sort -t "$(printf '\t')" -k2,2n "$work_dir/pulls")

if [[ -n "$previous" ]]; then
  printf '\n**Full changelog:** %s/compare/%s...%s\n' "$repo_url" "$previous" "$version"
fi
