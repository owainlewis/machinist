"""Tests for the release notes scripts.

Each test builds a small git repository with tags and puts a fake gh on PATH,
so the scripts run as they do in a release, without the network.
"""

import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

SCRIPTS = pathlib.Path(__file__).resolve().parent
URL = "https://github.com/acme/widget"


class ReleaseNotesTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = pathlib.Path(self.tmp.name)
        self.repo = root / "repo"
        self.repo.mkdir()
        bin_dir = root / "bin"
        bin_dir.mkdir()
        self.pulls = root / "pulls.tsv"
        self.pulls.write_text("")
        self.gh_args = root / "gh-args"
        fake_gh = bin_dir / "gh"
        fake_gh.write_text(
            textwrap.dedent(
                """\
                #!/usr/bin/env bash
                printf '%s\\n' "$@" >"$FAKE_GH_ARGS"
                cat "$FAKE_GH_PULLS"
                """
            )
        )
        fake_gh.chmod(0o755)
        self.env = {
            **os.environ,
            "PATH": f"{bin_dir}{os.pathsep}{os.environ['PATH']}",
            "GH_REPO": "acme/widget",
            "GITHUB_SERVER_URL": "https://github.com",
            "FAKE_GH_ARGS": str(self.gh_args),
            "FAKE_GH_PULLS": str(self.pulls),
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
            ["git", *args], cwd=self.repo, env=self.env, check=True, capture_output=True, text=True
        )
        return result.stdout.strip()

    def commit(self, message, date="2026-09-01T12:00:00Z"):
        env = {**self.env, "GIT_AUTHOR_DATE": date, "GIT_COMMITTER_DATE": date}
        subprocess.run(
            ["git", "commit", "-q", "--allow-empty", "-m", message],
            cwd=self.repo, env=env, check=True,
        )
        return self.git("rev-parse", "HEAD")

    def tag(self, name):
        self.git("tag", "-a", name, "-m", name)

    def set_pulls(self, *pulls):
        """Each pull is (merge commit, number, title, comma-separated labels)."""
        self.pulls.write_text("".join("\t".join(map(str, pull)) + "\n" for pull in pulls))

    def run_script(self, name, *args, check=True):
        return subprocess.run(
            ["bash", str(SCRIPTS / name), *args],
            cwd=self.repo, env=self.env, check=check, capture_output=True, text=True,
        )

    def test_groups_pull_requests_by_label_then_title(self):
        before = self.commit("Start", "2026-09-01T12:00:00Z")
        self.tag("v0.1.0")
        commits = [self.commit(f"Change {i}", "2026-09-05T12:00:00Z") for i in range(9)]
        self.tag("v0.2.0")
        self.set_pulls(
            (commits[0], 10, "Refactor the store", "feature"),
            (commits[1], 11, "Speed up polling", "enhancement,go"),
            (commits[2], 12, "Add a pause button", "bug"),
            (commits[3], 13, "Stop leaking tokens", "bug"),
            (commits[4], 14, "Add a runner page", ""),
            (commits[5], 15, "show cost on a job", "documentation"),
            (commits[6], 16, "Fix the retry loop", ""),
            (commits[7], 17, "fix(ui): align the header", ""),
            (commits[8], 9, "Document upgrades", "dependencies"),
            (before, 3, "Add something from the last release", "feature"),
            ("", 4, "Closed without a merge commit", ""),
        )

        notes = self.run_script("changelog.sh", "v0.1.0", "v0.2.0").stdout

        self.assertEqual(
            notes,
            textwrap.dedent(
                f"""\
                ## v0.2.0 (2026-09-05)

                ### Features

                - Refactor the store ([#10]({URL}/pull/10))
                - Speed up polling ([#11]({URL}/pull/11))
                - Add a runner page ([#14]({URL}/pull/14))
                - show cost on a job ([#15]({URL}/pull/15))

                ### Fixes

                - Add a pause button ([#12]({URL}/pull/12))
                - Stop leaking tokens ([#13]({URL}/pull/13))
                - Fix the retry loop ([#16]({URL}/pull/16))
                - fix(ui): align the header ([#17]({URL}/pull/17))

                ### Other

                - Document upgrades ([#9]({URL}/pull/9))

                **Full changelog:** {URL}/compare/v0.1.0...v0.2.0
                """
            ),
        )
        args = self.gh_args.read_text().splitlines()
        self.assertIn("merged:2026-09-01..2026-09-05", args)
        self.assertEqual(args[args.index("--repo") + 1], "acme/widget")

    def test_first_word_must_match_exactly(self):
        self.commit("Start")
        self.tag("v0.1.0")
        added = self.commit("Change")
        self.tag("v0.2.0")
        self.set_pulls((added, 5, "Additional logging and Fix wording", ""))

        notes = self.run_script("changelog.sh", "v0.1.0", "v0.2.0").stdout

        self.assertIn("### Other\n\n- Additional logging and Fix wording", notes)
        self.assertNotIn("### Features", notes)
        self.assertNotIn("### Fixes", notes)

    def test_first_release_covers_all_history(self):
        first = self.commit("Start")
        second = self.commit("Change")
        self.tag("v0.1.0")
        self.set_pulls((first, 1, "Add the CLI", ""), (second, 2, "Fix the CLI", ""))

        notes = self.run_script("changelog.sh", "", "v0.1.0").stdout

        self.assertIn("- Add the CLI", notes)
        self.assertIn("- Fix the CLI", notes)
        self.assertNotIn("Full changelog", notes)
        self.assertIn("merged:1970-01-01..", self.gh_args.read_text())

    def test_notes_for_an_untagged_version_end_at_the_given_ref(self):
        self.commit("Start")
        self.tag("v0.1.0")
        merged = self.commit("Change")
        self.set_pulls((merged, 7, "Add exports", ""))

        notes = self.run_script("changelog.sh", "v0.1.0", "v0.2.0", "HEAD").stdout

        self.assertTrue(notes.startswith("## v0.2.0 (2026-09-01)\n"))
        self.assertIn("- Add exports", notes)

    def test_no_pull_requests(self):
        self.commit("Start")
        self.tag("v0.1.0")
        self.commit("Direct push")
        self.tag("v0.1.1")

        notes = self.run_script("changelog.sh", "v0.1.0", "v0.1.1").stdout

        self.assertIn("No pull requests were merged.", notes)
        self.assertNotIn("###", notes)

    def test_usage_without_a_new_tag(self):
        result = self.run_script("changelog.sh", "v0.1.0", check=False)

        self.assertEqual(result.returncode, 2)
        self.assertIn("usage:", result.stderr)

    def test_previous_tag(self):
        self.commit("One")
        self.assertEqual(self.run_script("previous-tag.sh", "v0.1.0", "HEAD").stdout, "")
        self.tag("v0.1.0")
        self.commit("Two")
        self.tag("v0.2.0-rc.1")
        self.commit("Three")
        self.tag("not-a-release")
        self.commit("Four")
        self.tag("v0.2.0")

        self.assertEqual(self.run_script("previous-tag.sh", "v0.1.0").stdout, "")
        self.assertEqual(self.run_script("previous-tag.sh", "v0.2.0").stdout.strip(), "v0.1.0")
        self.assertEqual(
            self.run_script("previous-tag.sh", "v0.2.1-rc.1", "HEAD").stdout.strip(), "v0.2.0"
        )
        # Preparing a release with no commits since the last tag finds that tag.
        self.assertEqual(self.run_script("previous-tag.sh", "v0.3.0", "HEAD").stdout.strip(), "v0.2.0")
        self.commit("Five")
        self.assertEqual(self.run_script("previous-tag.sh", "v0.3.0", "HEAD").stdout.strip(), "v0.2.0")

    def test_release_notes_prepend_the_new_section(self):
        self.commit("Start")
        self.tag("v0.1.0")
        merged = self.commit("Change")
        self.set_pulls((merged, 8, "Add a changelog", ""))
        changelog = self.repo / "CHANGELOG.md"
        changelog.write_text("# Changelog\n\nIntro.\n\n## v0.1.0 (2026-08-01)\n\nOld notes.\n")

        self.run_script("release-notes.sh", "v0.2.0")

        self.assertEqual(
            changelog.read_text(),
            textwrap.dedent(
                f"""\
                # Changelog

                Intro.

                ## v0.2.0 (2026-09-01)

                ### Features

                - Add a changelog ([#8]({URL}/pull/8))

                **Full changelog:** {URL}/compare/v0.1.0...v0.2.0

                ## v0.1.0 (2026-08-01)

                Old notes.
                """
            ),
        )

        again = self.run_script("release-notes.sh", "v0.2.0", check=False)
        self.assertEqual(again.returncode, 1)
        self.assertIn("already has a section for v0.2.0", again.stderr)

    def test_release_notes_create_the_changelog(self):
        merged = self.commit("Start")
        self.set_pulls((merged, 1, "Add the CLI", ""))

        self.run_script("release-notes.sh", "v0.1.0")

        text = (self.repo / "CHANGELOG.md").read_text()
        self.assertTrue(text.startswith("# Changelog\n\n"))
        self.assertIn("\n## v0.1.0 (2026-09-01)\n\n### Features\n\n- Add the CLI", text)

    def test_release_notes_reject_a_bad_version(self):
        result = self.run_script("release-notes.sh", "0.2.0", check=False)

        self.assertEqual(result.returncode, 2)


if __name__ == "__main__":
    unittest.main()
