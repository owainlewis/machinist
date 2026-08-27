#!/usr/bin/env bash

set -euo pipefail

skill_root=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
helper="$skill_root/scripts/delegate.sh"
temporary_root=$(mktemp -d)
trap 'rm -rf "$temporary_root"' EXIT

test "$(sed -n '1p' "$skill_root/SKILL.md")" = '---'
grep -q '^name: machinist$' "$skill_root/SKILL.md"
grep -q 'MACHINIST_RUN_ID' "$skill_root/SKILL.md"
test -x "$helper"

repository="$temporary_root/repository with spaces"
mkdir -p "$repository"
git -C "$repository" init -q
repository_root=$(git -C "$repository" rev-parse --show-toplevel)

fake_machinist="$temporary_root/fake machinist"
capture="$temporary_root/capture"
cat >"$fake_machinist" <<'EOF'
#!/bin/sh
printf '<%s>\n' "$@" >"$MACHINIST_TEST_CAPTURE"
EOF
chmod +x "$fake_machinist"

issue_url=https://github.com/example/project/issues/123
MACHINIST_BIN="$fake_machinist" MACHINIST_TEST_CAPTURE="$capture" \
	"$helper" "$repository" "$issue_url"

cat >"$temporary_root/expected" <<EOF
<run>
<--agent=foreman>
<--repo=$repository_root>
<--prompt=Complete $issue_url>
EOF
diff -u "$temporary_root/expected" "$capture"

rm -f "$capture"
if MACHINIST_RUN_ID=run_0123456789abcdef01234567 \
	MACHINIST_BIN="$fake_machinist" MACHINIST_TEST_CAPTURE="$capture" \
	"$helper" "$repository" "$issue_url" >"$temporary_root/recursive.stdout" 2>"$temporary_root/recursive.stderr"; then
	printf '%s\n' 'expected recursive delegation to fail' >&2
	exit 1
fi
test ! -e "$capture"
grep -q 'refusing recursive delegation' "$temporary_root/recursive.stderr"

if MACHINIST_BIN="$fake_machinist" MACHINIST_TEST_CAPTURE="$capture" \
	"$helper" "$repository" https://github.com/example/project/issues/not-a-number \
	>"$temporary_root/url.stdout" 2>"$temporary_root/url.stderr"; then
	printf '%s\n' 'expected invalid issue URL to fail' >&2
	exit 1
fi
test ! -e "$capture"
grep -q 'expected a numeric GitHub issue URL' "$temporary_root/url.stderr"

if MACHINIST_BIN="$fake_machinist" MACHINIST_TEST_CAPTURE="$capture" \
	"$helper" "$repository" https://github.com/example/project/pull/issues/123 \
	>"$temporary_root/path.stdout" 2>"$temporary_root/path.stderr"; then
	printf '%s\n' 'expected an issue URL with extra path segments to fail' >&2
	exit 1
fi
test ! -e "$capture"
grep -q 'expected a GitHub issue URL' "$temporary_root/path.stderr"

for malformed_url in example/project/issues/123 git@github.com:example/project/issues/123; do
	if MACHINIST_BIN="$fake_machinist" MACHINIST_TEST_CAPTURE="$capture" \
		"$helper" "$repository" "$malformed_url" \
		>"$temporary_root/prefix.stdout" 2>"$temporary_root/prefix.stderr"; then
		printf 'expected malformed issue URL to fail: %s\n' "$malformed_url" >&2
		exit 1
	fi
	test ! -e "$capture"
	grep -q 'expected a GitHub issue URL' "$temporary_root/prefix.stderr"
done

printf '%s\n' 'machinist skill tests passed'
