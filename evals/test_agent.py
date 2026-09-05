"""Test the issue launcher without starting Codex or contacting GitHub."""

import contextlib
import importlib.util
import io
import json
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
        report = {"status": "completed", "pr_number": 467, "summary": "PR opened; verification passed."}
        with patch.object(agent, "run_codex", return_value=report) as run:
            with contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(agent.main([url]), 0)
        self.assertEqual(json.loads(output.getvalue()), report)
        self.assertIn(url, run.call_args.args[0])
        self.assertNotIn("{task}", run.call_args.args[0])

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
        for error in [RuntimeError("agent failed"), ValueError("invalid SDK response")]:
            with self.subTest(error=error), patch.object(agent, "run_codex", side_effect=error):
                with contextlib.redirect_stdout(io.StringIO()) as output:
                    self.assertEqual(agent.main(["https://github.com/owner/repo/issues/123"]), 1)
                self.assertEqual(json.loads(output.getvalue()), {
                    "status": "failed", "pr_number": None, "summary": str(error),
                })

    def test_unsuccessful_outcomes_keep_the_pr_number(self):
        for status in ["blocked", "failed"]:
            for pr_number in [None, 467]:
                report = {"status": status, "pr_number": pr_number, "summary": "Checks could not finish."}
                with self.subTest(report=report), patch.object(agent, "run_codex", return_value=report):
                    with contextlib.redirect_stdout(io.StringIO()) as output:
                        self.assertEqual(agent.main(["https://github.com/owner/repo/issues/123"]), 1)
                    self.assertEqual(json.loads(output.getvalue()), report)

    def test_installed_cli_runs_in_current_repository(self):
        factory = MagicMock()
        client = factory.return_value.__enter__.return_value
        thread = client.thread_start.return_value
        report = {"status": "completed", "pr_number": 467, "summary": "done"}
        thread.run.return_value = SimpleNamespace(status="completed", final_response=json.dumps(report))
        with patch.object(agent, "Codex", factory), patch.object(agent.shutil, "which", return_value="/bin/codex"):
            self.assertEqual(agent.run_codex("implement issue"), report)
        self.assertEqual(factory.call_args.args[0].codex_bin, "/bin/codex")
        self.assertEqual(client.thread_start.call_args.kwargs["cwd"], str(Path.cwd()))
        thread.run.assert_called_once_with("implement issue", output_schema=agent.RESULT_SCHEMA)

    def test_invalid_agent_json_becomes_a_failed_result(self):
        factory = MagicMock()
        client = factory.return_value.__enter__.return_value
        client.thread_start.return_value.run.return_value = SimpleNamespace(
            status="completed", final_response="not JSON",
        )
        with patch.object(agent, "Codex", factory), patch.object(agent.shutil, "which", return_value="/bin/codex"):
            with contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(agent.main(["https://github.com/owner/repo/issues/123"]), 1)
        report = json.loads(output.getvalue())
        self.assertEqual(report["status"], "failed")
        self.assertIsNone(report["pr_number"])
        self.assertTrue(report["summary"])

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
