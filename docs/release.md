# Install and release Factory

Factory publishes one archive for each supported operating system and CPU:

- Linux amd64 and arm64
- macOS amd64 and arm64

Each archive contains `factory`, `factory-server`, `factory-worker`, the project
license, this guide, the changelog, and generated third-party notices. The
release also publishes one SPDX 2.3 SBOM per archive, `SHA256SUMS`, a release
manifest, and a standalone copy of the third-party notices.

## Install a release

Download the archive and `SHA256SUMS` from the same GitHub release. Verify the
download before extracting it. Set the exact downloaded archive name. On
macOS:

```sh
archive=factory_1.2.3_darwin_arm64.tar.gz
awk -v file="$archive" '$2 == file' SHA256SUMS | shasum -a 256 -c -
```

On Linux:

```sh
archive=factory_1.2.3_linux_amd64.tar.gz
awk -v file="$archive" '$2 == file' SHA256SUMS | sha256sum -c -
```

The checksum file covers every published file except itself. Extract the
verified archive for your platform and install all three binaries:

```sh
tar -xzf factory_1.2.3_linux_amd64.tar.gz
install -m 0755 factory_1.2.3_linux_amd64/factory ~/.local/bin/factory
install -m 0755 factory_1.2.3_linux_amd64/factory-server ~/.local/bin/factory-server
install -m 0755 factory_1.2.3_linux_amd64/factory-worker ~/.local/bin/factory-worker
factory version
factory-server version
factory-worker version
```

All three commands must report the release tag and the same full source commit shown
by the GitHub release. Factory stores configuration and state outside the
archive, under `~/.factory` by default.

## Upgrade and compatibility

Read the target version in `CHANGELOG.md`, including its compatibility section,
before upgrading. Factory does not promise mixed-version operation between the
server and workers. Upgrade them as one unit:

1. Stop every Factory worker and then stop the server.
2. Back up the closed SQLite database and configuration. With the default path:

   ```sh
   cp -p ~/.factory/server/factory.sqlite3 ~/.factory/server/factory.sqlite3.before-v1.2.3
   for file in config.toml worker.toml; do
     [ ! -f "$HOME/.factory/$file" ] || \
       cp -p "$HOME/.factory/$file" "$HOME/.factory/$file.before-v1.2.3"
   done
   ```

3. Replace all three binaries from the verified new archive.
4. Confirm all three `version` commands show the same tag and commit.
5. Start the server, check `/healthz`, then start the workers and confirm they
   are healthy in the UI.

SQLite migrations only move forward. A previous binary must not open a database
that a newer release has migrated unless that changelog explicitly says the
rollback is compatible.

## Roll back

Stop the workers and server first. If the failed version did not migrate the
database, reinstall all three binaries from the prior verified archive and
restart.
If it did migrate the database, move the failed database aside and restore the
closed pre-upgrade copy before reinstalling the prior binaries:

```sh
mv ~/.factory/server/factory.sqlite3 ~/.factory/server/factory.sqlite3.failed
cp -p ~/.factory/server/factory.sqlite3.before-v1.2.3 ~/.factory/server/factory.sqlite3
```

Keep the failed database until the rollback has been verified. Restore the
matching configuration backups if the changelog identifies a configuration
change. Start the prior server, check `/healthz`, then start its matching
workers.

## Reproduce release artifacts

Release inputs are the tagged source commit, the exact Go patch version in
`.release/go-version`, the committed Go and npm lockfiles, the committed npm
license map, and the commit timestamp. The builder disables CGO and VCS
stamping, ignores user Go environments and workspaces, fixes amd64 and arm64
feature baselines, removes build paths and Go build IDs, sorts archive entries,
and fixes archive metadata to the source commit time.

From a clean checkout of the tag:

```sh
commit=$(git rev-parse HEAD)
./scripts/release.sh v1.2.3 "$commit" dist
```

The script selects the pinned Go toolchain automatically. Run it twice in clean
checkouts of the same commit and compare `SHA256SUMS`; the files must be byte
identical. Verify a target without executing it:

```sh
./scripts/verify-release.sh dist v1.2.3 "$commit" linux/amd64
```

Pass `execute` as the final argument on a matching host to install-extract and
run all three version commands.

## Publish a release

Maintainers prepare a release pull request that moves the relevant Unreleased
entries into a versioned changelog section with the release date and explicit
compatibility notes. After it merges:

1. Create an annotated `vMAJOR.MINOR.PATCH` tag on the reviewed main-branch
   commit.
2. Push the tag. The release workflow verifies that the remote annotated tag
   still points to the workflow commit and that the commit belongs to `main`,
   then builds all four archives once with isolated module and compiler caches.
3. Native Linux and macOS runners verify checksums, SPDX documents, archive
   contents, binary target metadata, and the embedded version and commit.
4. Only after every verification job succeeds does the workflow publish the
   immutable GitHub release assets with generated GitHub release notes.

Test tags use the prerelease form `v0.0.0-test.NAME`. They exercise the same
build and native verification jobs but do not publish a GitHub release.
