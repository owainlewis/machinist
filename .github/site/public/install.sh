#!/usr/bin/env sh
#
# Install Machinist from GitHub Releases and verify its SHA-256 checksum.
#
#   curl -fsSL https://machinist.sh/install.sh | sh
#   curl -fsSL https://machinist.sh/install.sh | MACHINIST_CHANNEL=nightly sh
#   curl -fsSL https://machinist.sh/install.sh | MACHINIST_VERSION=v0.5.0 sh
#
# Environment variables:
#   MACHINIST_CHANNEL      stable (default) or nightly
#   MACHINIST_VERSION      exact release tag, e.g. v0.5.0 (cannot combine with nightly)
#   MACHINIST_INSTALL_DIR  target directory (default /usr/local/bin if writable, else ~/.local/bin)
#   MACHINIST_API_BASE     override GitHub API base URL (for testing/mirrors)
#   MACHINIST_RELEASE_BASE override GitHub Releases download base URL

set -eu

API_BASE=${MACHINIST_API_BASE:-https://api.github.com/repos/owainlewis/machinist}
RELEASE_BASE=${MACHINIST_RELEASE_BASE:-https://github.com/owainlewis/machinist/releases/download}
CHANNEL=${MACHINIST_CHANNEL:-}
VERSION=${MACHINIST_VERSION:-}
INSTALL_DIR=${MACHINIST_INSTALL_DIR:-}

fail() {
  printf 'machinist install: %s\n' "$1" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

fetch() {
  curl -fsSL \
    -H "Accept: application/vnd.github+json" \
    -H "User-Agent: machinist-installer" \
    "$1"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    fail "need sha256sum or shasum to verify download checksum"
  fi
}

validate_version() {
  case "$1" in
    v[0-9]*.[0-9]*.[0-9]*)
      printf '%s\n' "$1" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$' \
        || fail "invalid release version: $1"
      ;;
    *)
      fail "invalid release version: $1"
      ;;
  esac
}

extract_latest_stable() {
  if command -v python3 >/dev/null 2>&1; then
    python3 -c 'import json, sys; print(json.load(sys.stdin).get("tag_name", ""))'
  elif command -v jq >/dev/null 2>&1; then
    jq -r '.tag_name // empty'
  elif command -v node >/dev/null 2>&1; then
    node -e 'const fs = require("fs"); const d = JSON.parse(fs.readFileSync(0, "utf8")); if (d.tag_name) console.log(d.tag_name);'
  else
    grep -Eo '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' | head -n 1 | sed 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/'
  fi
}

extract_latest_nightly() {
  if command -v python3 >/dev/null 2>&1; then
    python3 -c '
import json, re, sys
pattern = re.compile(r"^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$")
matches = [
    rel for rel in json.load(sys.stdin)
    if not rel.get("draft")
    and rel.get("prerelease")
    and "-nightly." in rel.get("tag_name", "")
    and pattern.match(rel.get("tag_name", ""))
]
if matches:
    best = max(matches, key=lambda r: r.get("published_at") or r.get("created_at") or "")
    print(best["tag_name"])
'
  elif command -v jq >/dev/null 2>&1; then
    jq -r '[.[] | select(.draft == false and .prerelease == true and (.tag_name | contains("-nightly.")) and (.tag_name | test("^v[0-9]+\\.[0-9]+\\.[0-9]+([.-][0-9A-Za-z.-]+)?$")))] | sort_by(.published_at // .created_at // "") | last.tag_name // empty'
  elif command -v node >/dev/null 2>&1; then
    node -e '
const fs = require("fs");
const re = /^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$/;
const list = JSON.parse(fs.readFileSync(0, "utf8"));
const matches = list.filter((r) => !r.draft && r.prerelease && typeof r.tag_name === "string" && r.tag_name.includes("-nightly.") && re.test(r.tag_name));
if (matches.length) {
  matches.sort((a, b) => String(b.published_at || b.created_at || "").localeCompare(String(a.published_at || a.created_at || "")));
  console.log(matches[0].tag_name);
}
'
  else
    fail "need python3, jq, or node to resolve the latest nightly release (or set MACHINIST_VERSION)"
  fi
}

need_cmd curl
need_cmd tar

os_raw=$(uname -s)
case "$os_raw" in
  Linux)  os=linux ;;
  Darwin) os=darwin ;;
  *)      fail "unsupported operating system: $os_raw" ;;
esac

arch_raw=$(uname -m)
case "$arch_raw" in
  x86_64|amd64)  arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *)             fail "unsupported architecture: $arch_raw" ;;
esac

if [ -n "$CHANNEL" ]; then
  CHANNEL=$(printf '%s' "$CHANNEL" | tr 'A-Z' 'a-z')
fi

if [ -n "$VERSION" ] && [ -n "$CHANNEL" ] && [ "$CHANNEL" != "stable" ]; then
  fail "cannot set both MACHINIST_VERSION and MACHINIST_CHANNEL=$CHANNEL"
fi

if [ -z "$CHANNEL" ]; then
  CHANNEL=stable
fi

case "$CHANNEL" in
  stable|nightly) ;;
  *) fail "invalid MACHINIST_CHANNEL \"$CHANNEL\"; expected stable or nightly" ;;
esac

api_base_clean=$(printf '%s' "$API_BASE" | sed 's:/*$::')
release_base_clean=$(printf '%s' "$RELEASE_BASE" | sed 's:/*$::')

if [ -z "$VERSION" ]; then
  if [ "$CHANNEL" = "nightly" ]; then
    VERSION=$(fetch "$api_base_clean/releases?per_page=30" | extract_latest_nightly)
    [ -n "$VERSION" ] || fail "no nightly release found"
  else
    VERSION=$(fetch "$api_base_clean/releases/latest" | extract_latest_stable)
    [ -n "$VERSION" ] || fail "could not determine latest stable release"
  fi
fi

validate_version "$VERSION"

if [ -z "$INSTALL_DIR" ]; then
  if [ "$(id -u)" -eq 0 ] || [ -w /usr/local/bin ]; then
    INSTALL_DIR=/usr/local/bin
  else
    INSTALL_DIR="${HOME:-.}/.local/bin"
  fi
fi

release_name=${VERSION#v}
archive_name="machinist_${release_name}_${os}_${arch}.tar.gz"
base_url="${release_base_clean}/${VERSION}"

tmp_dir=$(mktemp -d)
staged=""
trap 'rm -rf "$tmp_dir"; [ -n "$staged" ] && rm -f "$staged"' EXIT

printf 'Downloading Machinist %s (%s/%s)...\n' "$VERSION" "$os" "$arch"
fetch "${base_url}/checksums.txt" > "${tmp_dir}/checksums.txt"
fetch "${base_url}/${archive_name}" > "${tmp_dir}/${archive_name}"

want_sum=$(awk -v name="$archive_name" '
  {
    f = $2
    sub(/^\*/, "", f)
    if (NF == 2 && f == name) { print $1; exit }
  }
' "${tmp_dir}/checksums.txt")
[ -n "$want_sum" ] || fail "checksums.txt does not contain ${archive_name}"

got_sum=$(sha256_file "${tmp_dir}/${archive_name}")
want_lower=$(printf '%s' "$want_sum" | tr 'A-F' 'a-f')
got_lower=$(printf '%s' "$got_sum" | tr 'A-F' 'a-f')
if [ "$got_lower" != "$want_lower" ]; then
  fail "checksum mismatch for ${archive_name} (expected ${want_lower}, got ${got_lower})"
fi

tar -C "$tmp_dir" -xzf "${tmp_dir}/${archive_name}" machinist
[ -f "${tmp_dir}/machinist" ] || fail "archive did not contain machinist binary"

mkdir -p "$INSTALL_DIR"
staged="${INSTALL_DIR}/.machinist-install.$$"
cp "${tmp_dir}/machinist" "$staged"
chmod 0755 "$staged"
mv "$staged" "${INSTALL_DIR}/machinist"
staged=""

printf 'Installed machinist %s to %s/machinist\n' "$VERSION" "$INSTALL_DIR"
case ":${PATH:-}:" in
  *":${INSTALL_DIR}:"*) ;;
  *) printf 'Add %s to your PATH: export PATH="%s:$PATH"\n' "$INSTALL_DIR" "$INSTALL_DIR" ;;
esac
