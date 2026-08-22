#!/bin/sh

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
temporary=$(mktemp -d)
cleanup() {
  chmod -R u+w "$temporary" 2>/dev/null || true
  rm -rf "$temporary"
}
trap cleanup EXIT
version=v0.0.0-test.local
commit=$(git -C "$root" rev-parse HEAD)

FACTORY_RELEASE_GOMODCACHE="$temporary/module-cache-first" \
  FACTORY_RELEASE_GOCACHE="$temporary/build-cache-first" \
  "$root/scripts/release.sh" "$version" "$commit" "$temporary/first"
GOOS=linux GOARCH=amd64 GOAMD64=v3 GOARM64=v9.5 \
  GODEBUG=installgoroot=all GOENV=/missing/go.env GOEXPERIMENT=none \
  GOFIPS140=latest GOFLAGS=-tags=ambient_release_tag GOWORK=/missing/go.work \
  FACTORY_RELEASE_GOMODCACHE="$temporary/module-cache-second" \
  FACTORY_RELEASE_GOCACHE="$temporary/build-cache-second" \
  "$root/scripts/release.sh" "$version" "$commit" "$temporary/second"
diff -r "$temporary/first" "$temporary/second"

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  "$root/scripts/verify-release.sh" "$temporary/first" "$version" "$commit" "$target"
done

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) native=linux/amd64 ;;
  Linux/aarch64 | Linux/arm64) native=linux/arm64 ;;
  Darwin/x86_64) native=darwin/amd64 ;;
  Darwin/arm64) native=darwin/arm64 ;;
  *) native= ;;
esac
if [ -n "$native" ]; then
  "$root/scripts/verify-release.sh" "$temporary/first" "$version" "$commit" "$native" execute

  native_os=${native%/*}
  native_arch=${native#*/}
  archive_root="factory_${version#v}_${native_os}_${native_arch}"
  mkdir -p "$temporary/rollback/archive" "$temporary/rollback/bin" "$temporary/rollback/state"
  tar -xzf "$temporary/first/$archive_root.tar.gz" -C "$temporary/rollback/archive"
  install -m 0755 "$temporary/rollback/archive/$archive_root/factory" "$temporary/rollback/bin/factory"
  install -m 0755 "$temporary/rollback/archive/$archive_root/factory-server" "$temporary/rollback/bin/factory-server"
  install -m 0755 "$temporary/rollback/archive/$archive_root/factory-worker" "$temporary/rollback/bin/factory-worker"
  printf 'pre-upgrade database\n' > "$temporary/rollback/state/factory.sqlite3"
  cp -p "$temporary/rollback/state/factory.sqlite3" "$temporary/rollback/state/factory.sqlite3.before-upgrade"
  printf 'failed upgraded database\n' > "$temporary/rollback/state/factory.sqlite3"
  mv "$temporary/rollback/state/factory.sqlite3" "$temporary/rollback/state/factory.sqlite3.failed"
  cp -p "$temporary/rollback/state/factory.sqlite3.before-upgrade" "$temporary/rollback/state/factory.sqlite3"
  cmp "$temporary/rollback/state/factory.sqlite3.before-upgrade" "$temporary/rollback/state/factory.sqlite3"
  "$temporary/rollback/bin/factory" version | grep -F "$version" >/dev/null
  "$temporary/rollback/bin/factory-server" version | grep -F "$version" >/dev/null
  "$temporary/rollback/bin/factory-worker" version | grep -F "$commit" >/dev/null
fi

echo "Release artifacts are reproducible, complete, checksummed, described by SPDX SBOMs, executable on the native target, and recoverable by the documented rollback steps."
