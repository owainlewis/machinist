"""Tests for nightly versioning and the shell installer script.

Each test runs offline against temporary git repositories or a local HTTP
server so the scripts run as they do in CI and production without the network.
"""

import hashlib
import http.server
import io
import json
import os
import pathlib
import platform
import re
import subprocess
import tarfile
import tempfile
import threading
import unittest

SCRIPTS = pathlib.Path(__file__).resolve().parent
REPO_ROOT = SCRIPTS.parent


def host_target():
    sys_name = platform.system()
    if sys_name == "Linux":
        goos = "linux"
    elif sys_name == "Darwin":
        goos = "darwin"
    else:
        raise unittest.SkipTest(f"unsupported OS for install.sh: {sys_name}")

    machine = platform.machine().lower()
    if machine in ("x86_64", "amd64"):
        goarch = "amd64"
    elif machine in ("arm64", "aarch64"):
        goarch = "arm64"
    else:
        raise unittest.SkipTest(f"unsupported architecture for install.sh: {machine}")

    return goos, goarch


def build_tarball(binary_bytes):
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tf:
        info = tarfile.TarInfo(name="machinist")
        info.size = len(binary_bytes)
        info.mode = 0o755
        tf.addfile(info, io.BytesIO(binary_bytes))
    return buf.getvalue()


class NightlyVersionTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.repo = pathlib.Path(self.tmp.name) / "repo"
        self.repo.mkdir()
        self.env = {
            **os.environ,
            "GIT_AUTHOR_NAME": "Test",
            "GIT_AUTHOR_EMAIL": "test@example.com",
            "GIT_COMMITTER_NAME": "Test",
            "GIT_COMMITTER_EMAIL": "test@example.com",
            "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_CONFIG_NOSYSTEM": "1",
        }
        self.git("init", "-q", "-b", "main")

    def tearDown(self):
        self.tmp.cleanup()

    def git(self, *args):
        result = subprocess.run(
            ["git", *args],
            cwd=self.repo,
            env=self.env,
            text=True,
            capture_output=True,
        )
        if result.returncode != 0:
            self.fail(f"git {' '.join(args)} failed:\n{result.stderr}")
        return result.stdout.strip()

    def commit(self, message):
        self.git("commit", "-q", "--allow-empty", "-m", message)

    def run_version(self, *args):
        return subprocess.run(
            ["bash", str(SCRIPTS / "nightly-version.sh"), *args],
            cwd=self.repo,
            env=self.env,
            text=True,
            capture_output=True,
        )

    def test_starts_from_v0_0_1_without_stable_tags(self):
        self.commit("initial")
        sha = self.git("rev-parse", "--short=7", "HEAD")
        res = self.run_version("20261124")
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(res.stdout.strip(), f"v0.0.1-nightly.20261124.{sha}")

    def test_bumps_patch_after_latest_stable_tag_and_ignores_prereleases(self):
        self.commit("initial")
        self.git("tag", "-a", "v0.5.0", "-m", "v0.5.0")
        self.commit("after stable")
        self.git("tag", "v0.5.1-nightly.20261123.1111111")
        self.commit("after nightly")
        self.git("tag", "-a", "v0.6.0-rc.1", "-m", "rc1")
        self.commit("latest")

        res = self.run_version("20261124", "abc1234")
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(res.stdout.strip(), "v0.5.1-nightly.20261124.abc1234")

    def test_empty_first_arg_defaults_to_utc_date(self):
        self.commit("initial")
        self.git("tag", "-a", "v1.2.3", "-m", "v1.2.3")
        self.commit("next")

        res = self.run_version("", "deadbee")
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertRegex(res.stdout.strip(), r"^v1\.2\.4-nightly\.[0-9]{8}\.deadbee$")

    def test_rejects_invalid_date_and_sha(self):
        self.commit("initial")
        bad_date = self.run_version("2026-11-24", "abc1234")
        self.assertNotEqual(bad_date.returncode, 0)
        self.assertIn("invalid date stamp", bad_date.stderr)

        bad_sha = self.run_version("20261124", "NOTHEX!")
        self.assertNotEqual(bad_sha.returncode, 0)
        self.assertIn("invalid short commit SHA", bad_sha.stderr)


class InstallScriptTest(unittest.TestCase):
    def setUp(self):
        self.goos, self.goarch = host_target()
        self.tmp = tempfile.TemporaryDirectory()
        self.install_dir = pathlib.Path(self.tmp.name) / "bin"

        self.routes = {}
        routes = self.routes

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                entry = routes.get(self.path)
                if entry is None:
                    self.send_response(404)
                    self.end_headers()
                    return
                body = entry if isinstance(entry, bytes) else entry.encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, format, *args):
                pass

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.port = self.server.server_address[1]
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
        self.tmp.cleanup()

    def register_release(self, version, payload, corrupt_checksum=False):
        release_name = version.removeprefix("v")
        archive_name = f"machinist_{release_name}_{self.goos}_{self.goarch}.tar.gz"
        archive = build_tarball(payload)
        digest = "0" * 64 if corrupt_checksum else hashlib.sha256(archive).hexdigest()
        self.routes[f"/downloads/{version}/checksums.txt"] = f"{digest}  {archive_name}\n"
        self.routes[f"/downloads/{version}/{archive_name}"] = archive

    def run_installer(self, extra_env=None):
        env = {
            **os.environ,
            "MACHINIST_API_BASE": f"http://127.0.0.1:{self.port}/api",
            "MACHINIST_RELEASE_BASE": f"http://127.0.0.1:{self.port}/downloads",
            "MACHINIST_INSTALL_DIR": str(self.install_dir),
        }
        if extra_env:
            env.update(extra_env)
        return subprocess.run(
            ["sh", str(SCRIPTS / "install.sh")],
            env=env,
            text=True,
            capture_output=True,
        )

    def test_installs_latest_stable_release(self):
        self.routes["/api/releases/latest"] = json.dumps({"tag_name": "v0.5.0"})
        self.register_release("v0.5.0", b"#!/bin/sh\necho stable-0.5.0\n")

        res = self.run_installer()
        self.assertEqual(res.returncode, 0, res.stderr)
        installed = self.install_dir / "machinist"
        self.assertEqual(installed.read_bytes(), b"#!/bin/sh\necho stable-0.5.0\n")
        self.assertEqual(installed.stat().st_mode & 0o777, 0o755)
        self.assertIn("Installed machinist v0.5.0", res.stdout)

    def test_installs_latest_nightly_release(self):
        nightly = "v0.5.1-nightly.20261124.abc1234"
        self.routes["/api/releases?per_page=30"] = json.dumps([
            {"tag_name": "v0.5.1-nightly.20261125.draft00", "draft": True, "prerelease": True, "published_at": "2026-11-25T04:00:00Z"},
            {"tag_name": "v0.5.0", "draft": False, "prerelease": False, "published_at": "2026-11-24T20:00:00Z"},
            {"tag_name": "v0.6.0-rc.1", "draft": False, "prerelease": True, "published_at": "2026-11-24T19:00:00Z"},
            {"tag_name": "bad-nightly.1", "draft": False, "prerelease": True, "published_at": "2026-11-24T18:00:00Z"},
            {"tag_name": "v0.5.1-nightly.20261124.7c30eab", "draft": False, "prerelease": True, "published_at": "2026-11-24T11:14:17Z"},
            {"tag_name": nightly, "draft": False, "prerelease": True, "published_at": "2026-11-24T15:10:24Z"},
            {"tag_name": "v0.5.1-nightly.20261123.def5678", "draft": False, "prerelease": True, "published_at": "2026-11-23T04:00:00Z"},
        ])
        self.register_release(nightly, b"#!/bin/sh\necho nightly\n")

        res = self.run_installer({"MACHINIST_CHANNEL": "nightly"})
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual((self.install_dir / "machinist").read_bytes(), b"#!/bin/sh\necho nightly\n")
        self.assertIn(f"Installed machinist {nightly}", res.stdout)

    def test_installs_pinned_version(self):
        self.register_release("v0.4.2", b"pinned-binary")
        res = self.run_installer({"MACHINIST_VERSION": "v0.4.2"})
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual((self.install_dir / "machinist").read_bytes(), b"pinned-binary")

    def test_checksum_mismatch_leaves_existing_binary_untouched(self):
        self.install_dir.mkdir(parents=True)
        existing = self.install_dir / "machinist"
        existing.write_bytes(b"original-binary")
        self.register_release("v0.5.0", b"tampered-binary", corrupt_checksum=True)

        res = self.run_installer({"MACHINIST_VERSION": "v0.5.0"})
        self.assertNotEqual(res.returncode, 0)
        self.assertIn("checksum mismatch", res.stderr)
        self.assertEqual(existing.read_bytes(), b"original-binary")

    def test_rejects_invalid_channel_and_conflicting_env(self):
        bad_channel = self.run_installer({"MACHINIST_CHANNEL": "beta"})
        self.assertNotEqual(bad_channel.returncode, 0)
        self.assertIn("invalid MACHINIST_CHANNEL", bad_channel.stderr)

        conflict = self.run_installer({
            "MACHINIST_VERSION": "v0.5.0",
            "MACHINIST_CHANNEL": "nightly",
        })
        self.assertNotEqual(conflict.returncode, 0)
        self.assertIn("cannot set both MACHINIST_VERSION and MACHINIST_CHANNEL=nightly", conflict.stderr)

    def test_site_public_install_script_matches_scripts_copy(self):
        scripts_copy = (SCRIPTS / "install.sh").read_bytes()
        site_copy = (REPO_ROOT / ".github" / "site" / "public" / "install.sh").read_bytes()
        self.assertEqual(
            scripts_copy,
            site_copy,
            ".github/site/public/install.sh must match scripts/install.sh",
        )


if __name__ == "__main__":
    unittest.main()
