#!/bin/sh

set -eu

repository=${MACHINIST_REPOSITORY:-owainlewis/machinist}
version=${MACHINIST_VERSION:-}
install_dir=${MACHINIST_INSTALL_DIR:-}

fail() {
  printf 'machinist installer: %s\n' "$*" >&2
  exit 1
}

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"

case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "unsupported operating system: $(uname -s)" ;;
esac

case $(uname -m) in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

if [ -z "$version" ]; then
  metadata=$(curl -fsSL -H 'Accept: application/vnd.github+json' \
    -H 'User-Agent: machinist-installer' \
    "https://api.github.com/repos/$repository/releases/latest") || \
    fail "could not find a published release"
  version=$(printf '%s\n' "$metadata" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
fi

printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$' ||
  fail "invalid release version: $version"

if [ -z "$install_dir" ]; then
  if [ -w /usr/local/bin ]; then
    install_dir=/usr/local/bin
  else
    install_dir=${HOME:?HOME is required}/.local/bin
  fi
fi

release_name=${version#v}
archive_name="machinist_${release_name}_${os}_${arch}.tar.gz"
release_url="https://github.com/$repository/releases/download/$version"
temporary_dir=$(mktemp -d)
trap 'rm -rf "$temporary_dir"' EXIT HUP INT TERM

curl -fsSL "$release_url/checksums.txt" -o "$temporary_dir/checksums.txt" || \
  fail "could not download checksums for $version"
curl -fsSL "$release_url/$archive_name" -o "$temporary_dir/$archive_name" || \
  fail "could not download $archive_name"

expected=$(awk -v name="$archive_name" '$2 == name || $2 == "*" name { print $1; exit }' "$temporary_dir/checksums.txt")
[ -n "$expected" ] || fail "release checksums do not contain $archive_name"

if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$temporary_dir/$archive_name" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$temporary_dir/$archive_name" | awk '{print $1}')
else
  fail "sha256sum or shasum is required"
fi
[ "$actual" = "$expected" ] || fail "checksum mismatch for $archive_name"

tar -xzf "$temporary_dir/$archive_name" -C "$temporary_dir" machinist
mkdir -p "$install_dir"
install -m 0755 "$temporary_dir/machinist" "$install_dir/machinist"

printf 'installed machinist %s to %s/machinist\n' "$version" "$install_dir"
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) printf 'add %s to PATH before running machinist\n' "$install_dir" ;;
esac
