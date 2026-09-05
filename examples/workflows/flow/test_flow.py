"""Local integration tests: real Git, fake coding agent and GitHub CLI."""

import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from types import ModuleType, SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

# The script's SDK is installed by uv in production. Tests need no SDK or network.
sdk = ModuleType("openai_codex")
sdk.Codex = MagicMock()
sdk.Sandbox = str
sdk_types = ModuleType("openai_codex.types")
sdk_types.TurnStatus = SimpleNamespace(completed="completed")
with patch.dict(sys.modules, {"openai_codex": sdk, "openai_codex.types": sdk_types}):
    import flow


class FlowTests(unittest.TestCase):
    def setUp(self):
        self.root = Path(self.enterContext(tempfile.TemporaryDirectory())).resolve()
        self.repo = self.root / "checkout"
        self.remote = self.root / "origin.git"
        self.worktrees = self.root / "worktrees"
        self.repo.mkdir()
        self.git("init", "--quiet", "--bare", "--initial-branch=main", str(self.remote))
        self.git("init", "--quiet", "--initial-branch=main")
        self.git("config", "user.email", "test@example.com")
        self.git("config", "user.name", "Test")
        (self.repo / "README.md").write_text("base\n")
        self.git("add", ".")
        self.git("commit", "--quiet", "-m", "initial")
        self.git("remote", "add", "origin", str(self.remote))
        self.git("push", "--quiet", "origin", "main")
        self.base = self.git("rev-parse", "HEAD")
        # An unrelated local branch and dirty files must remain untouched.
        self.git("checkout", "--quiet", "-b", "local-work")
        (self.repo / "local.txt").write_text("unpublished work\n")
        self.git("add", ".")
        self.git("commit", "--quiet", "-m", "local only")
        (self.repo / "README.md").write_text("dirty local edit\n")
        (self.repo / "draft.txt").write_text("untracked work\n")
        self.original_head = self.git("rev-parse", "HEAD")
        self.original_status = self.git("status", "--porcelain")

        bin_dir = self.root / "bin"
        bin_dir.mkdir()
        gh = bin_dir / "gh"
        gh.write_text(f"#!{sys.executable}\n" + '''import json, os, pathlib, sys
args = sys.argv[1:]
entry = {"args": args, "cwd": os.getcwd()}
if args[:2] == ["repo", "view"]:
    print('{"nameWithOwner": "owner/project"}')
elif args[:2] == ["pr", "create"]:
    entry["body"] = pathlib.Path(args[args.index("--body-file") + 1]).read_text()
    if os.environ.get("FAIL_CREATE"):
        sys.exit("GitHub unavailable")
    print("https://github.com/owner/project/pull/7")
else:
    sys.exit("unexpected GitHub operation: " + repr(args))
with open(os.environ["GH_LOG"], "a") as log:
    log.write(json.dumps(entry) + "\\n")
''')
        gh.chmod(0o755)
        self.gh_log = self.root / "gh.jsonl"
        self.usage_path = self.root / "tokens"
        self.enterContext(patch.dict(os.environ, {
            "PATH": str(bin_dir) + os.pathsep + os.environ["PATH"],
            "FLOW_WORKTREE_ROOT": str(self.worktrees),
            "GH_LOG": str(self.gh_log),
            "MACHINIST_TOKEN_USAGE_PATH": str(self.usage_path),
        }))
        self.enterContext(patch.object(flow.Path, "cwd", return_value=self.repo))
        self.output = self.enterContext(contextlib.redirect_stdout(io.StringIO()))
        self.codex = MagicMock()
        self.thread = self.codex.thread_start.return_value
        self.thread.id = "test-thread"
        self.thread.run.side_effect = self.build
        self.enterContext(patch.object(flow, "Codex", return_value=MagicMock(
            __enter__=MagicMock(return_value=self.codex),
        )))
        self.mode = "approved"

    def git(self, *args, cwd=None):
        return subprocess.check_output(
            ["git", *args], cwd=cwd or self.repo, text=True, stderr=subprocess.PIPE,
        ).strip()

    def build(self, prompt, **kwargs):
        self.worktree = Path(self.codex.thread_start.call_args.kwargs["cwd"])
        self.assertEqual(self.git("rev-parse", "HEAD", cwd=self.worktree), self.base)
        self.assertEqual((self.worktree / "README.md").read_text(), "base\n")
        self.assertFalse((self.worktree / "local.txt").exists())
        self.assertFalse((self.worktree / "draft.txt").exists())
        self.assertIn("Add a --json flag", prompt)
        self.assertEqual(kwargs["output_schema"], flow.RESULT_SCHEMA)
        if self.mode != "no-change":
            (self.worktree / "feature.txt").write_text("implemented\n")
            self.git("add", ".", cwd=self.worktree)
            self.git("commit", "--quiet", "-m", "feat: add flag", cwd=self.worktree)
        head = self.git("rev-parse", "HEAD", cwd=self.worktree)
        if self.mode == "dirty":
            (self.worktree / "feature.txt").write_text("uncommitted repair\n")
        if self.mode == "wrong-branch":
            self.git("checkout", "--quiet", "-b", "unexpected", cwd=self.worktree)
        report = {
            "verdict": "blocked" if self.mode == "blocked" else "approved",
            "reviewed_head": self.base if self.mode == "stale-review" else head,
            "title": "" if self.mode == "missing-title" else "feat: add --json flag",
            "body": "Implement JSON output.\n\nVerification: local checks passed.\nReview: approved.",
        }
        return SimpleNamespace(
            status="failed" if self.mode == "failed-turn" else "completed",
            error=SimpleNamespace(message="agent interrupted"),
            final_response=json.dumps(report),
            usage=SimpleNamespace(total=SimpleNamespace(input_tokens=10, output_tokens=5)),
        )

    def assert_source_unchanged(self):
        self.assertEqual(self.git("rev-parse", "HEAD"), self.original_head)
        self.assertEqual(self.git("branch", "--show-current"), "local-work")
        self.assertEqual(self.git("status", "--porcelain"), self.original_status)
        self.assertEqual((self.repo / "README.md").read_text(), "dirty local edit\n")
        self.assertEqual((self.repo / "draft.txt").read_text(), "untracked work\n")

    def test_opens_pr_from_reviewed_worktree_and_exits(self):
        self.assertEqual(flow.flow("Add a --json flag"), 0)
        branch = self.git("branch", "--show-current", cwd=self.worktree)
        self.assertTrue(branch.startswith("codex/add-a-json-flag-"))
        self.assertEqual(self.worktree.parent, self.worktrees / "project")
        self.assertEqual(self.git("rev-parse", "HEAD", cwd=self.worktree), self.git(
            "rev-parse", branch, cwd=self.remote,
        ))
        calls = [json.loads(line) for line in self.gh_log.read_text().splitlines()]
        self.assertEqual([c["args"][:2] for c in calls], [["repo", "view"], ["pr", "create"]])
        self.assertEqual(calls[0]["args"][2], str(self.remote))
        args = calls[1]["args"]
        self.assertEqual(args[args.index("--repo") + 1], "owner/project")
        self.assertEqual(args[args.index("--base") + 1], "main")
        self.assertEqual(args[args.index("--head") + 1], branch)
        self.assertEqual(calls[1]["cwd"], str(self.worktree))
        self.assertIn("\n\nVerification:", calls[1]["body"])
        self.thread.run.assert_called_once()
        self.assertEqual(self.usage_path.read_text(), "15")
        self.assertIn("opened https://github.com/owner/project/pull/7", self.output.getvalue())
        self.assert_source_unchanged()

    def test_refuses_to_publish_incomplete_or_unreviewed_work(self):
        for mode, message in [
            ("blocked", "agent blocked"),
            ("failed-turn", "agent interrupted"),
            ("dirty", "uncommitted changes"),
            ("no-change", "no changes"),
            ("stale-review", "reviewed commit does not match"),
            ("wrong-branch", "left the supplied branch"),
            ("missing-title", "omitted the PR title"),
        ]:
            with self.subTest(mode=mode):
                self.mode = mode
                with self.assertRaisesRegex(RuntimeError, message):
                    flow.flow("Add a --json flag")
                self.assertTrue(self.worktree.exists())
                self.assertEqual(self.git("for-each-ref", "--format=%(refname)", "refs/heads/codex/", cwd=self.remote), "")
                self.assert_source_unchanged()
        calls = [json.loads(line) for line in self.gh_log.read_text().splitlines()]
        self.assertTrue(all(c["args"][:2] == ["repo", "view"] for c in calls))

    def test_pr_failure_keeps_pushed_branch_and_worktree(self):
        with patch.dict(os.environ, {"FAIL_CREATE": "1"}):
            with self.assertRaisesRegex(RuntimeError, "GitHub unavailable"):
                flow.flow("Add a --json flag")
        self.assertTrue(self.worktree.exists())
        branch = self.git("branch", "--show-current", cwd=self.worktree)
        self.assertEqual(self.git("rev-parse", branch, cwd=self.remote), self.git("rev-parse", "HEAD", cwd=self.worktree))
        self.assert_source_unchanged()

    def test_empty_task_does_not_start_work(self):
        with patch.object(sys, "stdin", io.StringIO(" \n")):
            self.assertEqual(flow.main(), 2)
        self.codex.thread_start.assert_not_called()
        self.assertFalse(self.worktrees.exists())
        self.assertFalse(self.gh_log.exists())


if __name__ == "__main__":
    unittest.main()
