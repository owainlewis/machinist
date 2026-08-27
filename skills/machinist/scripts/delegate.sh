#!/bin/sh

set -eu

usage() {
	printf '%s\n' 'usage: delegate.sh <repository> <github-issue-url>' >&2
}

if [ -n "${MACHINIST_RUN_ID:-}" ]; then
	printf '%s\n' 'delegate.sh: refusing recursive delegation from a Machinist executor' >&2
	exit 64
fi

if [ "$#" -ne 2 ]; then
	usage
	exit 64
fi

repository_input=$1
issue_url=$2
issue_parent=${issue_url%/*}
issue_number=${issue_url##*/}

case "$issue_parent" in
	https://github.com/*) ;;
	*)
		printf 'delegate.sh: expected a GitHub issue URL, got %s\n' "$issue_url" >&2
		exit 64
		;;
esac

github_path=${issue_parent#https://github.com/}
owner=${github_path%%/*}
repository_and_suffix=${github_path#*/}
repository_name=${repository_and_suffix%%/*}
suffix=${repository_and_suffix#*/}

case "$owner:$repository_name:$suffix" in
	?*:?*:issues) ;;
	*)
		printf 'delegate.sh: expected a GitHub issue URL, got %s\n' "$issue_url" >&2
		exit 64
		;;
esac

case "$issue_number" in
	''|*[!0-9]*)
		printf 'delegate.sh: expected a numeric GitHub issue URL, got %s\n' "$issue_url" >&2
		exit 64
		;;
esac

if ! repository=$(git -C "$repository_input" rev-parse --show-toplevel 2>/dev/null); then
	printf 'delegate.sh: %s is not inside a Git worktree\n' "$repository_input" >&2
	exit 64
fi

script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
source_root=$(CDPATH= cd -- "$script_directory/../../.." && pwd -P)

if [ -n "${MACHINIST_BIN:-}" ]; then
	machinist_bin=$MACHINIST_BIN
elif machinist_bin=$(command -v machinist 2>/dev/null); then
	:
elif [ -x "$source_root/bin/machinist" ]; then
	machinist_bin=$source_root/bin/machinist
else
	printf '%s\n' 'delegate.sh: machinist is not on PATH and this source checkout has no bin/machinist' >&2
	printf '%s\n' 'build Machinist or set MACHINIST_BIN to its absolute path' >&2
	exit 69
fi

if [ ! -x "$machinist_bin" ]; then
	printf 'delegate.sh: Machinist executable is not executable: %s\n' "$machinist_bin" >&2
	exit 69
fi

exec "$machinist_bin" run \
	--agent=foreman \
	--repo="$repository" \
	--prompt="Complete $issue_url"
