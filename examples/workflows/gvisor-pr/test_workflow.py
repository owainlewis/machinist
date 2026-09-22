"""Lifecycle tests and real-Git publication tests; no API calls or Docker needed."""

import contextlib
import io
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

import run as supervisor
import worker


class SupervisorTests(unittest.TestCase):
    def launch(self, child, cleanup=None):
        with patch.dict(os.environ, {"GH_TOKEN": "github-secret", "CODEX_API_KEY": "api-secret"}), \
             patch.object(supervisor.subprocess, "check_output", return_value='{"runsc": {}}'), \
             patch.object(supervisor.subprocess, "Popen", return_value=child) as popen, \
             patch.object(supervisor.subprocess, "run", return_value=cleanup or subprocess.CompletedProcess([], 0, "", "")) as remove, \
             contextlib.redirect_stdout(io.StringIO()):
            try:
                result = supervisor.launch("acme/repo", "fix it\nwithout shell interpolation", "image", 30)
            finally:
                self.last_command = popen.call_args.args[0]
                self.removal = remove.call_args.args[0]
        return result

    def test_success_has_no_host_mounts_and_cleans_up(self):
        child = Mock(returncode=0)
        child.poll.return_value = 0
        self.assertEqual(self.launch(child), 0)
        self.assertIn("--runtime=runsc", self.last_command)
        self.assertNotIn("--mount", self.last_command)
        self.assertNotIn("-v", self.last_command)
        self.assertNotIn("github-secret", " ".join(self.last_command))
        child.communicate.assert_called_once_with("fix it\nwithout shell interpolation", timeout=30)
        self.assertEqual(self.removal[:3], ["docker", "rm", "--force"])
        self.assertEqual(self.removal[-1], self.last_command[self.last_command.index("--name") + 1])

    def test_failure_preserves_exit_code(self):
        child = Mock(returncode=7)
        child.poll.return_value = 7
        self.assertEqual(self.launch(child), 7)

    def test_timeout_kills_client_and_removes_container(self):
        child = Mock()
        child.poll.return_value = None
        child.communicate.side_effect = subprocess.TimeoutExpired("docker", 30)
        self.assertEqual(self.launch(child), 124)
        child.kill.assert_called_once()

    def test_sigterm_removes_container(self):
        child = Mock()
        child.poll.return_value = None
        child.communicate.side_effect = lambda *a, **k: signal.getsignal(signal.SIGTERM)(signal.SIGTERM, None)
        with self.assertRaises(SystemExit) as error:
            self.launch(child)
        self.assertEqual(error.exception.code, 143)
        child.kill.assert_called_once()
        self.assertEqual(self.removal[:3], ["docker", "rm", "--force"])

    def test_cleanup_failure_is_reported(self):
        child = Mock(returncode=0)
        child.poll.return_value = 0
        with self.assertRaisesRegex(RuntimeError, "cleanup failed"):
            self.launch(child, subprocess.CompletedProcess([], 1, "", "daemon unavailable"))

    def test_missing_runtime_fails_before_launch(self):
        with patch.dict(os.environ, {"GH_TOKEN": "x", "CODEX_API_KEY": "y"}), \
             patch.object(supervisor.subprocess, "check_output", return_value='{"runc": {}}'), \
             patch.object(supervisor.subprocess, "Popen") as popen:
            with self.assertRaisesRegex(ValueError, "no fallback"):
                supervisor.launch("acme/repo", "task", "image", 30)
            popen.assert_not_called()


class WorkerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.remote = self.root / "remote.git"
        self.workspace = self.root / "work"
        self.workspace.mkdir()
        self.real_run = worker.run
        self.calls = []
        self.status = "complete"
        self.change = True
        self.codex_failure = False
        self.pr_failure = False
        self.title = "fix: handle empty input"
        self.body = "Handles empty input.\n\nValidation: fixture tests passed.\nLiteral: `x` $(false)"
        self.git("init", "--bare", "--initial-branch=main", str(self.remote))
        seed = self.root / "seed"
        self.git("clone", str(self.remote), str(seed))
        (seed / "README.md").write_text("initial\n")
        self.git("-C", str(seed), "add", ".")
        self.git("-C", str(seed), "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
        self.git("-C", str(seed), "push", "origin", "main")
        self.base = self.git("--git-dir", str(self.remote), "rev-parse", "main")

    def git(self, *args):
        return subprocess.check_output(["git", *args], text=True, stderr=subprocess.DEVNULL).strip()

    def intercept(self, args, cwd=None, **kwargs):
        self.calls.append(args)
        if args[0] == "codex":
            if self.codex_failure:
                raise subprocess.CalledProcessError(1, args)
            if self.change:
                (Path(cwd) / "new.txt").write_text("implementation\n")
            report = Path(args[args.index("--output-last-message") + 1])
            report.write_text(json.dumps({"status": self.status, "title": self.title, "body": self.body}))
            return subprocess.CompletedProcess(args, 0)
        if args[:3] == ["gh", "pr", "create"]:
            if self.pr_failure:
                raise subprocess.CalledProcessError(1, args)
            self.assertEqual(Path(args[args.index("--body-file") + 1]).read_text(), self.body)
            return subprocess.CompletedProcess(args, 0, "https://github.com/acme/repo/pull/42\n")
        if args[0] == "gh":
            return subprocess.CompletedProcess(args, 0)
        if args[:2] == ["git", "clone"]:
            result = self.real_run([*args[:-2], str(self.remote), args[-1]], cwd, **kwargs)
            self.real_run(["git", "remote", "set-url", "origin", "https://github.com/acme/repo.git"], Path(args[-1]))
            return result
        if args[0] == "git" and "push" in args:
            args = [str(self.remote) if a == "https://github.com/acme/repo.git" else a for a in args]
        return self.real_run(args, cwd, **kwargs)

    def work(self):
        with patch.dict(os.environ, {"HOME": str(self.root / "home")}), \
             patch.object(worker, "run", side_effect=self.intercept), \
             contextlib.redirect_stdout(io.StringIO()):
            worker.work("acme/repo", "codex/task-test", "implement it", self.workspace)

    def test_fresh_branch_pushed_and_pr_created_without_changing_main(self):
        self.work()
        self.assertEqual(self.git("--git-dir", str(self.remote), "rev-parse", "main"), self.base)
        self.assertEqual(self.git("--git-dir", str(self.remote), "show", "codex/task-test:new.txt"), "implementation")
        self.assertTrue(any(c[:3] == ["gh", "pr", "create"] for c in self.calls))

    def test_blocked_agent_does_not_publish(self):
        self.status = "blocked"
        with self.assertRaisesRegex(RuntimeError, "agent blocked"):
            self.work()
        self.assertFalse(any("push" in c or c[:3] == ["gh", "pr", "create"] for c in self.calls))

    def test_no_changes_does_not_publish(self):
        self.change = False
        with self.assertRaisesRegex(RuntimeError, "no changes"):
            self.work()
        self.assertFalse(any("push" in c for c in self.calls))

    def test_agent_failure_does_not_publish(self):
        self.codex_failure = True
        with self.assertRaises(subprocess.CalledProcessError):
            self.work()
        self.assertFalse(any("push" in c for c in self.calls))

    def test_pr_failure_leaves_pushed_branch_for_recovery(self):
        self.pr_failure = True
        with self.assertRaises(subprocess.CalledProcessError):
            self.work()
        self.assertEqual(self.git("--git-dir", str(self.remote), "show", "codex/task-test:new.txt"), "implementation")


if __name__ == "__main__":
    unittest.main()
