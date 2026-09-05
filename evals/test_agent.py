"""Test the issue launcher without starting Codex or contacting GitHub."""

import contextlib
import importlib.util
import io
from pathlib import Path
import sys
from types import ModuleType, SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

sdk = ModuleType("openai_codex")
sdk.Codex = MagicMock()
sdk.CodexConfig = SimpleNamespace
sdk.Sandbox = SimpleNamespace(full_access="full-access")
sdk_types = ModuleType("openai_codex.types")
sdk_types.TurnStatus = SimpleNamespace(completed="completed")
spec = importlib.util.spec_from_file_location("issue_agent", Path(__file__).resolve().parents[1] / "agent.py")
agent = importlib.util.module_from_spec(spec)
with patch.dict(sys.modules, {"openai_codex": sdk, "openai_codex.types": sdk_types}):
    spec.loader.exec_module(agent)


class AgentTests(unittest.TestCase):
    def test_issue_url_becomes_the_task(self):
        url = "https://github.com/owner/repo/issues/123"
        with patch.object(agent, "run_codex", return_value="PR opened") as run:
            with contextlib.redirect_stdout(io.StringIO()) as output:
                agent.main([url])
        self.assertEqual(output.getvalue(), "PR opened\n")
        self.assertTrue(run.call_args.args[0].startswith(f"Implement task {url}."))

    def test_invalid_arguments_never_launch_codex(self):
        for args in [[], ["not-a-url"], ["https://example.com/o/r/issues/1"],
                     ["https://github.com/o/r/pull/1"], ["https://github.com/o/r/issues/0"],
                     ["https://github.com/o/r/issues/1", "extra"]]:
            with self.subTest(args=args), patch.object(agent, "run_codex") as run:
                with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as error:
                    agent.main(args)
                self.assertEqual(error.exception.code, 2)
                run.assert_not_called()

    def test_issue_comment_link_is_accepted(self):
        url = "https://github.com/owner/repo/issues/123#issuecomment-456"
        self.assertEqual(agent.issue_url(url), url)

    def test_cli_reports_agent_failure(self):
        with patch.object(agent, "run_codex", side_effect=RuntimeError("agent failed")):
            with contextlib.redirect_stderr(io.StringIO()) as output:
                with self.assertRaises(SystemExit) as error:
                    agent.main(["https://github.com/owner/repo/issues/123"])
        self.assertEqual(error.exception.code, 1)
        self.assertIn("agent failed", output.getvalue())

    def test_installed_cli_runs_in_current_repository(self):
        factory = MagicMock()
        client = factory.return_value.__enter__.return_value
        thread = client.thread_start.return_value
        thread.run.return_value = SimpleNamespace(status="completed", final_response="done")
        with patch.object(agent, "Codex", factory), patch.object(agent.shutil, "which", return_value="/bin/codex"):
            self.assertEqual(agent.run_codex("implement issue"), "done")
        self.assertEqual(factory.call_args.args[0].codex_bin, "/bin/codex")
        self.assertEqual(client.thread_start.call_args.kwargs["cwd"], str(Path.cwd()))
        thread.run.assert_called_once_with("implement issue")

    def test_failed_turn_is_not_returned_as_success(self):
        factory = MagicMock()
        client = factory.return_value.__enter__.return_value
        client.thread_start.return_value.run.return_value = SimpleNamespace(
            status="failed", error=SimpleNamespace(message="model unavailable"), final_response="partial",
        )
        with patch.object(agent, "Codex", factory), patch.object(agent.shutil, "which", return_value="/bin/codex"):
            with self.assertRaisesRegex(RuntimeError, "model unavailable"):
                agent.run_codex("implement issue")

    def test_missing_cli_never_starts_sdk(self):
        with patch.object(agent, "Codex") as factory, patch.object(agent.shutil, "which", return_value=None):
            with self.assertRaisesRegex(RuntimeError, "codex was not found on PATH"):
                agent.run_codex("implement issue")
        factory.assert_not_called()


if __name__ == "__main__":
    unittest.main()
