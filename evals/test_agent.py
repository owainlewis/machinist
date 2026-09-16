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
spec = importlib.util.spec_from_file_location(
    "issue_agent", Path(__file__).resolve().parents[1] / "agent.py"
)
agent = importlib.util.module_from_spec(spec)
with patch.dict(sys.modules, {"openai_codex": sdk, "openai_codex.types": sdk_types}):
    spec.loader.exec_module(agent)


def item_event(method, kind, **fields):
    return SimpleNamespace(
        method=method,
        payload=SimpleNamespace(
            item=SimpleNamespace(root=SimpleNamespace(type=kind, **fields))
        ),
    )


def message_event(text, phase="final_answer"):
    return item_event(
        "item/completed",
        "agentMessage",
        text=text,
        phase=SimpleNamespace(value=phase) if phase is not None else None,
    )


def completed_event(status="completed", error=None):
    return SimpleNamespace(
        method="turn/completed",
        payload=SimpleNamespace(turn=SimpleNamespace(status=status, error=error)),
    )


def streamed_codex(events):
    factory = MagicMock()
    thread = factory.return_value.__enter__.return_value.thread_start.return_value
    thread.turn.return_value.stream.return_value = (event for event in events)
    return factory, thread


def thread_page(*threads):
    return {
        "data": {
            "repository": {"pullRequest": {"reviewThreads": {"nodes": list(threads)}}}
        }
    }


def codex_review_status(state="Running", head="abcdef0"):
    """The status-only comment format observed in the issue #472 run."""
    return {
        "id": 10,
        "user": {"login": "chatgpt-codex-connector[bot]"},
        "body": (
            "<!-- codex-pull-request-review-summary -->\n"
            "## Codex Review Summary\n"
            "| Review | Status | Commit | Review trigger |\n"
            "| --- | --- | --- | --- |\n"
            f"| 📝 **Code Review** | **{state}** | `{head}` | PR opened |\n"
        ),
    }


class AgentTests(unittest.TestCase):
    def setUp(self):
        repository = patch.object(agent, "validate_repository")
        repository.start()
        self.addCleanup(repository.stop)
        login = patch.object(
            agent,
            "gh",
            return_value={
                "login": "builder",
                "state": "OPEN",
                "closingIssuesReferences": [
                    {"url": "https://github.com/owner/repo/issues/123"}
                ],
            },
        )
        login.start()
        self.addCleanup(login.stop)
        poll = patch.object(agent, "wait_for_ci", return_value={"ci_status": "passed"})
        self.poll = poll.start()
        self.addCleanup(poll.stop)
        self.progress = io.StringIO()
        stderr = contextlib.redirect_stderr(self.progress)
        stderr.__enter__()
        self.addCleanup(stderr.__exit__, None, None, None)

    def test_flow_implements_waits_and_iterates(self):
        report = {"status": "completed", "pr_number": 467, "summary": "done"}
        flow = MagicMock()
        flow.implement.return_value = report
        flow.wait.return_value = {"ci_status": "passed"}
        flow.iterate.return_value = report
        with (
            patch.object(agent, "implement", flow.implement),
            patch.object(agent, "wait_for_ci", flow.wait),
            patch.object(agent, "iterate", flow.iterate),
        ):
            with contextlib.redirect_stdout(io.StringIO()) as out:
                self.assertEqual(
                    agent.main(["https://github.com/owner/repo/issues/123"]), 0
                )
        self.assertEqual(
            [call[0] for call in flow.mock_calls], ["implement", "wait", "iterate"]
        )
        flow.wait.assert_called_once_with("owner/repo", 467)
        self.assertEqual(json.loads(out.getvalue()), report)

    def test_polling_failure_keeps_pr(self):
        self.poll.side_effect = RuntimeError("GitHub unavailable")
        with patch.object(
            agent,
            "run_codex",
            return_value={"status": "completed", "pr_number": 467, "summary": "done"},
        ):
            with contextlib.redirect_stdout(io.StringIO()) as out:
                self.assertEqual(
                    agent.main(["https://github.com/owner/repo/issues/123"]), 1
                )
        self.assertEqual(
            json.loads(out.getvalue()),
            {"status": "failed", "pr_number": 467, "summary": "GitHub unavailable"},
        )

    def test_ci_timeout_blocks_completion(self):
        self.poll.return_value = {"ci_status": "timed_out"}
        report = {"status": "completed", "pr_number": 467, "summary": "done"}
        with patch.object(agent, "run_codex", return_value=report):
            with contextlib.redirect_stdout(io.StringIO()) as out:
                self.assertEqual(
                    agent.main(["https://github.com/owner/repo/issues/123"]), 1
                )
        self.assertEqual(json.loads(out.getvalue())["status"], "blocked")

    def test_issue_url_becomes_the_task(self):
        url = "https://github.com/owner/repo/issues/123"
        report = {
            "status": "completed",
            "pr_number": 467,
            "summary": "PR opened; verification passed.",
        }
        with patch.object(agent, "run_codex", return_value=report) as run:
            with contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(agent.main([url]), 0)
        self.assertEqual(json.loads(output.getvalue()), report)
        self.assertIn("Starting AI agent to implement", self.progress.getvalue())
        self.assertIn("Finished: completed", self.progress.getvalue())
        self.assertIn(url, run.call_args.args[0])
        self.assertNotIn("{task}", run.call_args.args[0])

    def test_invalid_arguments_never_launch_codex(self):
        for args in [
            [],
            ["not-a-url"],
            ["https://example.com/o/r/issues/1"],
            ["https://github.com/o/r/pull/1"],
            ["https://github.com/o/r/issues/0"],
            ["https://github.com/o/r/issues/1", "extra"],
        ]:
            with self.subTest(args=args), patch.object(agent, "run_codex") as run:
                with (
                    contextlib.redirect_stderr(io.StringIO()),
                    self.assertRaises(SystemExit) as error,
                ):
                    agent.main(args)
                self.assertEqual(error.exception.code, 2)
                run.assert_not_called()

    def test_issue_comment_link_is_accepted(self):
        url = "https://github.com/owner/repo/issues/123#issuecomment-456"
        self.assertEqual(agent.issue_url(url), url)

    def test_cli_reports_agent_failure(self):
        for error in [RuntimeError("agent failed"), ValueError("invalid SDK response")]:
            with (
                self.subTest(error=error),
                patch.object(agent, "run_codex", side_effect=error),
            ):
                with contextlib.redirect_stdout(io.StringIO()) as output:
                    self.assertEqual(
                        agent.main(["https://github.com/owner/repo/issues/123"]), 1
                    )
                self.assertEqual(
                    json.loads(output.getvalue()),
                    {
                        "status": "failed",
                        "pr_number": None,
                        "summary": str(error),
                    },
                )

    def test_unsuccessful_outcomes_keep_the_pr_number(self):
        for status in ["blocked", "failed"]:
            for pr_number in [None, 467]:
                report = {
                    "status": status,
                    "pr_number": pr_number,
                    "summary": "Checks could not finish.",
                }
                with (
                    self.subTest(report=report),
                    patch.object(agent, "run_codex", return_value=report),
                ):
                    with contextlib.redirect_stdout(io.StringIO()) as output:
                        self.assertEqual(
                            agent.main(["https://github.com/owner/repo/issues/123"]), 1
                        )
                    self.assertEqual(json.loads(output.getvalue()), report)

        self.poll.assert_not_called()

    def test_completion_without_a_pr_is_not_success(self):
        report = {"status": "completed", "pr_number": None, "summary": "done"}
        with patch.object(agent, "run_codex", return_value=report):
            with contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(
                    agent.main(["https://github.com/owner/repo/issues/123"]), 1
                )
        result = json.loads(output.getvalue())
        self.assertEqual(result["status"], "failed")
        self.assertIn("without a PR number", result["summary"])

    def test_installed_cli_runs_in_current_repository(self):
        report = {"status": "completed", "pr_number": 467, "summary": "done"}
        factory, thread = streamed_codex(
            [message_event(json.dumps(report)), completed_event()]
        )
        client = factory.return_value.__enter__.return_value
        with (
            patch.object(agent, "Codex", factory),
            patch.object(agent.shutil, "which", return_value="/bin/codex"),
        ):
            self.assertEqual(agent.run_codex("implement issue"), report)
        self.assertEqual(factory.call_args.args[0].codex_bin, "/bin/codex")
        self.assertEqual(client.thread_start.call_args.kwargs["cwd"], str(Path.cwd()))
        thread.turn.assert_called_once_with(
            "implement issue", output_schema=agent.RESULT_SCHEMA
        )
        thread.run.assert_not_called()

    def test_invalid_agent_json_becomes_a_failed_result(self):
        factory, _ = streamed_codex([message_event("not JSON"), completed_event()])
        with (
            patch.object(agent, "Codex", factory),
            patch.object(agent.shutil, "which", return_value="/bin/codex"),
        ):
            with contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(
                    agent.main(["https://github.com/owner/repo/issues/123"]), 1
                )
        report = json.loads(output.getvalue())
        self.assertEqual(report["status"], "failed")
        self.assertIsNone(report["pr_number"])
        self.assertTrue(report["summary"])

    def test_failed_turn_is_not_returned_as_success(self):
        factory, _ = streamed_codex(
            [
                message_event("partial"),
                completed_event(
                    status=SimpleNamespace(value="failed"),
                    error=SimpleNamespace(message="model unavailable"),
                ),
            ]
        )
        with (
            patch.object(agent, "Codex", factory),
            patch.object(agent.shutil, "which", return_value="/bin/codex"),
        ):
            with self.assertRaisesRegex(RuntimeError, "model unavailable"):
                agent.run_codex("implement issue")

    def test_progress_arrives_before_completion_and_stdout_stays_json(self):
        report = {"status": "completed", "pr_number": 467, "summary": "done"}
        output = io.StringIO()

        def events():
            yield message_event("Checking the implementation.", phase="commentary")
            self.assertIn(
                "Agent: Checking the implementation.", self.progress.getvalue()
            )
            self.assertEqual(output.getvalue(), "")
            yield item_event(
                "item/started", "commandExecution", command="go test ./..."
            )
            self.assertIn("Running: go test ./...", self.progress.getvalue())
            yield item_event(
                "item/completed",
                "commandExecution",
                exit_code=1,
                aggregated_output="FAIL: secret-token-123\x1b[2J",
                status=SimpleNamespace(value="completed"),
            )
            self.assertIn("Command finished: exit 1", self.progress.getvalue())
            self.assertNotIn("secret-token-123", self.progress.getvalue())
            self.assertIn("raw output is not logged", self.progress.getvalue())
            self.assertNotIn("\x1b", self.progress.getvalue())
            yield item_event(
                "item/completed",
                "fileChange",
                status=SimpleNamespace(value="completed"),
                changes=[SimpleNamespace(path="internal/triggers/cron.go")],
            )
            self.assertIn(
                "File changes (completed): internal/triggers/cron.go",
                self.progress.getvalue(),
            )
            yield message_event(json.dumps(report))
            self.assertNotIn(json.dumps(report), self.progress.getvalue())
            self.assertEqual(output.getvalue(), "")
            yield completed_event()

        factory, _ = streamed_codex(events())
        with (
            patch.object(agent, "Codex", factory),
            patch.object(agent.shutil, "which", return_value="/bin/codex"),
            contextlib.redirect_stdout(output),
        ):
            self.assertEqual(
                agent.main(["https://github.com/owner/repo/issues/123"]), 0
            )
        self.assertEqual(json.loads(output.getvalue()), report)
        self.assertEqual(len(output.getvalue().splitlines()), 1)

    def test_final_response_takes_precedence_over_other_messages(self):
        report = {"status": "completed", "pr_number": 467, "summary": "done"}
        factory, _ = streamed_codex(
            [
                message_event("Starting work.", phase="commentary"),
                message_event(json.dumps(report)),
                message_event("Unphased message.", phase=None),
                message_event("Later progress.", phase="commentary"),
                completed_event(),
            ]
        )
        with (
            patch.object(agent, "Codex", factory),
            patch.object(agent.shutil, "which", return_value="/bin/codex"),
        ):
            self.assertEqual(agent.run_codex("implement issue"), report)

    def test_unphased_response_is_supported(self):
        report = {"status": "completed", "pr_number": 467, "summary": "done"}
        factory, _ = streamed_codex(
            [
                message_event("Earlier message.", phase=None),
                message_event(json.dumps(report), phase=None),
                completed_event(),
            ]
        )
        with (
            patch.object(agent, "Codex", factory),
            patch.object(agent.shutil, "which", return_value="/bin/codex"),
        ):
            self.assertEqual(agent.run_codex("implement issue"), report)

    def test_unknown_item_payloads_do_not_abort_the_turn(self):
        report = {"status": "completed", "pr_number": 467, "summary": "done"}
        unknown = SimpleNamespace(params={"item": {"type": "futureItem"}})
        factory, _ = streamed_codex(
            [
                SimpleNamespace(method="item/started", payload=unknown),
                SimpleNamespace(method="item/completed", payload=unknown),
                message_event(json.dumps(report)),
                completed_event(),
            ]
        )
        with (
            patch.object(agent, "Codex", factory),
            patch.object(agent.shutil, "which", return_value="/bin/codex"),
        ):
            self.assertEqual(agent.run_codex("implement issue"), report)

    def test_missing_completion_does_not_accept_partial_result(self):
        report = {"status": "completed", "pr_number": 467, "summary": "done"}
        factory, _ = streamed_codex([message_event(json.dumps(report))])
        with (
            patch.object(agent, "Codex", factory),
            patch.object(agent.shutil, "which", return_value="/bin/codex"),
        ):
            with self.assertRaisesRegex(RuntimeError, "without a completed turn"):
                agent.run_codex("implement issue")

    def test_interrupted_turn_reports_status_without_error(self):
        factory, _ = streamed_codex(
            [completed_event(status=SimpleNamespace(value="interrupted"))]
        )
        with (
            patch.object(agent, "Codex", factory),
            patch.object(agent.shutil, "which", return_value="/bin/codex"),
        ):
            with self.assertRaisesRegex(RuntimeError, "did not complete: interrupted"):
                agent.run_codex("implement issue")

    def test_stream_is_closed_if_progress_logging_fails(self):
        factory, thread = streamed_codex(
            [message_event("Reading.", phase="commentary")]
        )
        stream = MagicMock(wraps=thread.turn.return_value.stream.return_value)
        stream.__iter__.return_value = iter(
            [message_event("Reading.", phase="commentary")]
        )
        thread.turn.return_value.stream.return_value = stream
        with (
            patch.object(agent, "Codex", factory),
            patch.object(agent.shutil, "which", return_value="/bin/codex"),
            patch.object(
                agent, "log_agent_event", side_effect=OSError("closed stderr")
            ),
        ):
            with self.assertRaisesRegex(OSError, "closed stderr"):
                agent.run_codex("implement issue")
        stream.close.assert_called_once()

    def test_progress_ignores_unrelated_events_and_handles_missing_exit_code(self):
        agent.log_agent_event(SimpleNamespace(method="item/agentMessage/delta"))
        self.assertEqual(self.progress.getvalue(), "")
        agent.log_agent_event(
            item_event(
                "item/completed",
                "commandExecution",
                exit_code=None,
                status=SimpleNamespace(value="declined"),
                aggregated_output=None,
            )
        )
        self.assertIn("Command finished: declined", self.progress.getvalue())
        agent.log_agent_event(
            item_event(
                "item/started",
                "collabAgentToolCall",
                tool=SimpleNamespace(value="spawnAgent"),
                status=SimpleNamespace(value="inProgress"),
            )
        )
        self.assertIn("Subagent: spawnAgent (inProgress)", self.progress.getvalue())

    def test_missing_cli_never_starts_sdk(self):
        with (
            patch.object(agent, "Codex") as factory,
            patch.object(agent.shutil, "which", return_value=None),
        ):
            with self.assertRaisesRegex(RuntimeError, "codex was not found on PATH"):
                agent.run_codex("implement issue")
        factory.assert_not_called()

    def test_closed_or_unrelated_pr_is_rejected_and_number_is_preserved(self):
        report = {"status": "completed", "pr_number": 467, "summary": "done"}
        for state, issues in [
            ("CLOSED", [{"url": "https://github.com/owner/repo/issues/123"}]),
            ("MERGED", [{"url": "https://github.com/owner/repo/issues/123"}]),
            ("OPEN", [{"url": "https://github.com/other/repo/issues/123"}]),
            ("OPEN", []),
        ]:
            with (
                self.subTest(state=state, issues=issues),
                patch.object(agent, "run_codex", return_value=report),
                patch.object(
                    agent,
                    "gh",
                    return_value={"state": state, "closingIssuesReferences": issues},
                ),
                contextlib.redirect_stdout(io.StringIO()) as output,
            ):
                self.assertEqual(
                    agent.main(["https://github.com/owner/repo/issues/123"]), 1
                )
            result = json.loads(output.getvalue())
            self.assertEqual(result["pr_number"], 467)
            self.assertEqual(result["status"], "failed")
        self.poll.assert_not_called()

    def test_issue_comment_url_validates_against_canonical_issue(self):
        agent.validate_pr(
            "https://github.com/owner/repo/issues/123/#issuecomment-42", 467
        )
        agent.validate_pr("https://github.com/OWNER/REPO/issues/123", 467)


class FeedbackTests(unittest.TestCase):
    def setUp(self):
        self.output = io.StringIO()
        stderr = contextlib.redirect_stderr(self.output)
        stderr.__enter__()
        self.addCleanup(stderr.__exit__, None, None, None)

    def test_logs_escape_terminal_controls_and_bound_display(self):
        agent.log("Feedback: \x1b[2J\r" + "x" * 5000)
        output = self.output.getvalue()
        self.assertNotIn("\x1b", output)
        self.assertNotIn("\r", output)
        self.assertIn(r"\x1b[2J\r", output)
        self.assertIn("truncated", output)
        self.assertLess(len(output), 4200)

    def poll(self, snapshots, *, clock=None, reviews=None, head="abc"):
        replies = [
            *[{"state": "OPEN", **snapshot} for snapshot in snapshots],
            [[{"body": "bot summary"}]],
            reviews or [[]],
            [[{"body": "fix this", "path": "agent.py"}]],
            [thread_page()],
            {"headRefOid": head, "state": "OPEN"},
        ]

        def respond(*args, **kwargs):
            return SimpleNamespace(
                returncode=0, stdout=json.dumps(replies.pop(0)), stderr=""
            )

        with (
            patch.object(agent.subprocess, "run", side_effect=respond) as run,
            patch.object(agent.time, "sleep") as sleep,
        ):
            with patch.object(agent.time, "monotonic", side_effect=clock or [0, 1, 2]):
                result = agent.wait_for_ci("owner/repo", 467, timeout=10, interval=2)
        return result, run, sleep

    def test_waits_for_pending_checks_and_collects_all_review_pages(self):
        result, run, sleep = self.poll(
            [
                {"headRefOid": "abc", "statusCheckRollup": [{"status": "IN_PROGRESS"}]},
                {
                    "headRefOid": "abc",
                    "statusCheckRollup": [
                        {"status": "COMPLETED", "conclusion": "SUCCESS"}
                    ],
                },
            ],
            reviews=[
                [{"body": "first", "commit_id": "old"}],
                [{"body": "second", "commit_id": "abc"}],
            ],
        )
        self.assertEqual(result["ci_status"], "passed")
        progress = self.output.getvalue()
        self.assertIn("Waiting for feedback on owner/repo#467", progress)
        self.assertIn("Waiting for check; checking again in 2s", progress)
        self.assertIn("CI passed: 1 checks", progress)
        # Comments are displayed once when assessed, not at every CI snapshot.
        self.assertNotIn("fix this", progress)
        self.assertNotIn("bot summary", progress)
        self.assertEqual(len(result["reviews"]), 2)
        self.assertEqual(result["review_comments"][0]["path"], "agent.py")
        self.assertEqual(result["comments"][0]["body"], "bot summary")
        sleep.assert_called_once_with(2)
        self.assertIn("--paginate", run.call_args_list[2].args[0])

    def test_missing_and_pending_checks_time_out(self):
        for checks in [[], [{"status": "IN_PROGRESS"}], [{"state": "PENDING"}]]:
            result, _, _ = self.poll(
                [{"headRefOid": "abc", "statusCheckRollup": checks}], clock=[0, 10]
            )
            self.assertEqual(result["ci_status"], "timed_out")
            self.assertTrue(result["review_comments"])

    def test_failed_and_legacy_checks(self):
        for check, expected in [
            ({"state": "SUCCESS"}, "passed"),
            ({"state": "ERROR"}, "failed"),
            ({"status": "COMPLETED", "conclusion": "FAILURE"}, "failed"),
        ]:
            result, _, _ = self.poll(
                [{"headRefOid": "abc", "statusCheckRollup": [check]}]
            )
            self.assertEqual(result["ci_status"], expected)

    def test_changed_head_rejects_stale_snapshot(self):
        with self.assertRaisesRegex(RuntimeError, "head changed"):
            self.poll(
                [{"headRefOid": "abc", "statusCheckRollup": [{"state": "SUCCESS"}]}],
                head="new",
            )

    def test_github_errors_are_not_empty_feedback(self):
        with patch.object(
            agent.subprocess,
            "run",
            return_value=SimpleNamespace(returncode=1, stderr="authentication failed"),
        ):
            with self.assertRaisesRegex(RuntimeError, "authentication failed"):
                agent.wait_for_ci("owner/repo", 467)

    def poll_codex_review(self, statuses, *, clock=None, review_head="abcdef0"):
        statuses = iter(statuses)
        calls = []
        pr = {
            "state": "OPEN",
            "headRefOid": "abcdef0123456789",
            "statusCheckRollup": [{"state": "SUCCESS", "context": "check"}],
        }

        def respond(*args):
            calls.append(args)
            if args[0] == "pr":
                return pr
            if args[1] == "graphql":
                return [thread_page()]
            if args[1].endswith("issues/467/comments"):
                return [[codex_review_status(next(statuses), review_head)]]
            if args[1].endswith("pulls/467/comments"):
                return [[{"id": 20, "body": "A real finding", "user": None}]]
            return [[]]

        with (
            patch.object(agent, "gh", side_effect=respond),
            patch.object(agent.time, "monotonic", side_effect=clock or [0, 1, 2, 3]),
            patch.object(agent.time, "sleep") as sleep,
        ):
            result = agent.wait_for_ci("owner/repo", 467, timeout=10, interval=2)
        return result, calls, sleep

    def test_green_ci_waits_for_active_codex_review_then_collects_findings(self):
        result, calls, sleep = self.poll_codex_review(["Running", "Completed"])
        self.assertEqual(result["ci_status"], "passed")
        self.assertEqual(result["pending_reviewers"], [])
        self.assertEqual(result["review_comments"][0]["body"], "A real finding")
        sleep.assert_called_once_with(2)
        self.assertIn("CI passed; waiting for Codex review", self.output.getvalue())
        # Observe completion before fetching the findings it promises are ready.
        endpoints = [call[1] for call in calls if call[0] == "api"]
        self.assertEqual(
            endpoints[-4:],
            [
                "repos/owner/repo/issues/467/comments",
                "repos/owner/repo/pulls/467/reviews",
                "repos/owner/repo/pulls/467/comments",
                "graphql",
            ],
        )

    def test_active_review_uses_existing_deadline(self):
        result, _, sleep = self.poll_codex_review(["Running"], clock=[0, 9, 11])
        self.assertEqual(result["ci_status"], "passed")
        self.assertEqual(result["pending_reviewers"], ["Codex"])
        sleep.assert_not_called()

    def test_stale_or_completed_review_does_not_keep_waiting(self):
        for state, head in [("Running", "aaaaaaa"), ("Completed", "abcdef0")]:
            with self.subTest(state=state, head=head):
                result, _, sleep = self.poll_codex_review([state], review_head=head)
            self.assertEqual(result["pending_reviewers"], [])
            sleep.assert_not_called()

    def test_comment_preview_is_short_but_full_feedback_is_retained(self):
        body = "<h3>Finding</h3>\n" + "More detail &amp; evidence. " * 100
        feedback = {
            "comments": [
                {
                    "body": body,
                    "html_url": "https://github.com/o/r/pull/1#issuecomment-2",
                }
            ]
        }
        items = agent.review_items(feedback)
        agent.log_feedback(items)
        output = self.output.getvalue()
        self.assertNotIn("<h3>", output)
        self.assertIn("Finding More detail & evidence.", output)
        self.assertIn("https://github.com/o/r/pull/1#issuecomment-2", output)
        self.assertLess(len(output), 500)
        self.assertEqual(next(iter(items.values()))["body"], body)

    def test_comment_preview_preserves_comparisons_and_code(self):
        body = "<h3>Finding</h3> Check `if lower < value and value > upper` and `<p>` output."
        agent.log_feedback(agent.review_items({"comments": [{"body": body}]}))
        self.assertIn(
            "Finding Check `if lower < value and value > upper` and `<p>` output.",
            self.output.getvalue(),
        )

    def test_closed_pr_stops_polling(self):
        with patch.object(agent, "gh", return_value={"state": "CLOSED"}):
            with self.assertRaisesRegex(RuntimeError, "no longer open"):
                agent.wait_for_ci("owner/repo", 467)


class CollectedFeedbackTests(unittest.TestCase):
    def test_thread_pages_use_full_comment_ids_and_preserve_unresolved_outdated_state(
        self,
    ):
        resolved = {
            "id": "resolved",
            "isResolved": True,
            "isOutdated": False,
            "comments": {"nodes": [{"fullDatabaseId": "3952481309"}]},
        }
        outdated = {
            "id": "outdated",
            "isResolved": False,
            "isOutdated": True,
            "comments": {"nodes": [{"fullDatabaseId": "3952481310"}]},
        }
        with patch.object(
            agent, "gh", return_value=[thread_page(resolved), thread_page(outdated)]
        ) as api:
            states = agent.review_thread_status("owner/repo", 467)
        self.assertTrue(states[3952481309]["is_resolved"])
        self.assertEqual(
            states[3952481310],
            {
                "thread_id": "outdated",
                "is_resolved": False,
                "is_outdated": True,
            },
        )
        self.assertIn("--paginate", api.call_args.args)

    def test_collection_filters_resolved_roots_and_replies_but_keeps_unknown_findings(
        self,
    ):
        comments = [
            {"id": 1, "body": "Resolved root"},
            {"id": 2, "in_reply_to_id": 1, "body": "Resolved reply"},
            {"id": 3, "body": "Outdated but unresolved"},
            {"id": 4, "body": "Unknown thread"},
        ]
        pr = {
            "state": "OPEN",
            "headRefOid": "abc",
            "statusCheckRollup": [{"state": "SUCCESS"}],
        }
        with (
            patch.object(agent, "gh", side_effect=[pr, [[]], [[]], [comments], pr]),
            patch.object(
                agent,
                "review_thread_status",
                return_value={
                    1: {"thread_id": "T1", "is_resolved": True, "is_outdated": False},
                    3: {"thread_id": "T3", "is_resolved": False, "is_outdated": True},
                },
            ),
            contextlib.redirect_stderr(io.StringIO()),
        ):
            feedback = agent.wait_for_ci("owner/repo", 467)
        self.assertEqual(
            [item["id"] for item in agent.review_items(feedback).values()], [3, 4]
        )
        self.assertEqual(feedback["review_comments"][1]["thread_id"], "T1")

    def test_failed_job_logs_are_included_before_the_head_is_rechecked(self):
        url = "https://github.com/owner/repo/actions/runs/10/job/20"
        check = {
            "status": "COMPLETED",
            "conclusion": "FAILURE",
            "name": "tests",
            "detailsUrl": url,
        }
        pr = {"state": "OPEN", "headRefOid": "abc", "statusCheckRollup": [check]}
        calls = []

        def api(*args):
            calls.append(args)
            return pr if args[0] == "pr" else [[]]

        def logs(*args, **kwargs):
            calls.append(("logs",))
            return "test failure detail"

        with (
            patch.object(agent, "gh", side_effect=api),
            patch.object(agent, "review_thread_status", return_value={}),
            patch.object(agent, "gh_text", side_effect=logs),
            contextlib.redirect_stderr(io.StringIO()) as output,
        ):
            feedback = agent.wait_for_ci("owner/repo", 467)
        self.assertEqual(feedback["failed_job_logs"][0]["text"], "test failure detail")
        self.assertEqual(calls[-2], ("logs",))
        self.assertEqual(calls[-1][0], "pr")
        self.assertNotIn("test failure detail", output.getvalue())

    def test_log_collection_is_bounded_and_only_reads_this_repositorys_failed_jobs(
        self,
    ):
        def job(number, state="FAILURE", repo="owner/repo"):
            return {
                "name": str(number),
                "state": state,
                "targetUrl": f"https://github.com/{repo}/actions/runs/10/job/{number}",
            }

        checks = [
            job(1, "SUCCESS"),
            job(2),
            job(2),
            job(3, repo="other/repo"),
            job(4),
            job(5),
            job(6),
            job(7),
        ]
        with patch.object(
            agent, "gh_text", return_value="x" * 15000 + "FAILURE"
        ) as cli:
            logs = agent.failed_job_logs("owner/repo", checks)
        self.assertEqual(cli.call_count, 4)
        excerpts = [item for item in logs if "text" in item]
        self.assertEqual(sum(len(item["text"]) for item in excerpts), 48000)
        self.assertTrue(
            all(
                item["truncated"] and item["text"].endswith("FAILURE")
                for item in excerpts
            )
        )
        self.assertIn("already included", logs[1]["unavailable"])
        self.assertIn("provider", logs[2]["unavailable"])
        self.assertIn("limit", logs[-1]["unavailable"])

    def test_log_collection_uses_the_existing_deadline(self):
        checks = [
            {
                "state": "FAILURE",
                "targetUrl": f"https://github.com/owner/repo/actions/runs/10/job/{number}",
            }
            for number in (20, 21)
        ]
        with (
            patch.object(agent.time, "monotonic", side_effect=[9, 11]),
            patch.object(agent, "gh_text", return_value="failure") as cli,
        ):
            logs = agent.failed_job_logs("owner/repo", checks, deadline=10)
        cli.assert_called_once()
        self.assertEqual(cli.call_args.kwargs["timeout"], 1)
        self.assertIn("timed out", logs[1]["unavailable"])

    def test_missing_logs_keep_failure_context_without_exposing_command_errors(self):
        check = {
            "name": "tests",
            "state": "FAILURE",
            "targetUrl": "https://github.com/owner/repo/actions/runs/10/job/20",
        }
        for error in (
            RuntimeError("private error detail"),
            agent.subprocess.TimeoutExpired("gh", 30),
        ):
            with (
                self.subTest(error=error),
                patch.object(agent, "gh_text", side_effect=error),
            ):
                logs = agent.failed_job_logs("owner/repo", [check])
            self.assertEqual(logs[0]["url"], check["targetUrl"])
            self.assertIn("unavailable", logs[0])
            self.assertNotIn("private error detail", json.dumps(logs))
        with patch.object(agent, "gh_text", return_value=""):
            self.assertIn(
                "unavailable", agent.failed_job_logs("owner/repo", [check])[0]
            )

    def test_resolved_threads_do_not_start_an_agent_but_logs_reach_repairs(self):
        report = {"status": "completed", "pr_number": 467, "summary": "done"}
        feedback = {
            "ci_status": "passed",
            "head_sha": "abc",
            "review_comments": [{"id": 1, "body": "fixed", "is_resolved": True}],
        }
        with (
            patch.object(agent, "run_codex") as run,
            contextlib.redirect_stderr(io.StringIO()),
        ):
            self.assertEqual(
                agent.iterate("task", "owner/repo", report, feedback), report
            )
        run.assert_not_called()
        failed = {
            **feedback,
            "ci_status": "failed",
            "failed_job_logs": [{"text": "actual failed test"}],
        }
        with (
            patch.object(agent, "gh", return_value={"login": "builder"}),
            patch.object(agent, "run_codex", return_value=report) as run,
            patch.object(agent, "wait_for_ci", return_value=feedback),
            contextlib.redirect_stderr(io.StringIO()) as output,
        ):
            agent.iterate("task", "owner/repo", report, failed)
        self.assertIn("actual failed test", run.call_args.args[0])
        self.assertNotIn("actual failed test", output.getvalue())


class RepositoryTests(unittest.TestCase):
    task = "https://github.com/owner/repo/issues/123"

    def test_matching_origin_supports_ssh_and_https_and_case_insensitive_names(self):
        for remote in (
            "git@github.com:Owner/Repo.git",
            "https://github.com/owner/repo.git",
            "ssh://git@github.com/owner/repo.git",
            "https://user:password@github.com/owner/repo.git",
        ):
            with (
                self.subTest(remote=remote),
                patch.object(
                    agent.subprocess, "run", return_value=SimpleNamespace(stdout=remote)
                ),
            ):
                agent.validate_repository(self.task)

    def test_wrong_repository_is_rejected_before_the_agent_starts(self):
        for remote in (
            "git@github.com:other/repo.git",
            "https://other.example/owner/repo.git",
            "/local/repo",
        ):
            with (
                self.subTest(remote=remote),
                patch.object(
                    agent.subprocess, "run", return_value=SimpleNamespace(stdout=remote)
                ),
                patch.object(agent, "run_codex") as run,
            ):
                with self.assertRaisesRegex(ValueError, "does not match"):
                    agent.implement(self.task)
                run.assert_not_called()


class IterationTests(unittest.TestCase):
    task = "https://github.com/owner/repo/issues/123"
    report = {"status": "completed", "pr_number": 467, "summary": "Implemented."}

    def setUp(self):
        login = patch.object(agent, "gh", return_value={"login": "builder"})
        login.start()
        self.addCleanup(login.stop)
        self.progress = io.StringIO()
        stderr = contextlib.redirect_stderr(self.progress)
        stderr.__enter__()
        self.addCleanup(stderr.__exit__, None, None, None)

    def feedback(self, status="passed", head="abc", body=None):
        return {
            "ci_status": status,
            "head_sha": head,
            "reviews": [{"id": 1, "body": body}] if body else [],
        }

    def test_clean_ci_without_reviews_needs_no_repair(self):
        with patch.object(agent, "run_codex") as run:
            result = agent.iterate(
                self.task, "owner/repo", self.report, self.feedback()
            )
        run.assert_not_called()
        self.assertEqual(result["status"], "completed")

    def test_status_update_does_not_repeat_assessment_of_positive_review(self):
        # The real run spent two passes rechecking unchanged code because this
        # status notice changed from Running to Completed beside a positive review.
        positive_review = {
            "id": 11,
            "user": {"login": "greptile-apps[bot]"},
            "body": (
                "<h3>Greptile Summary</h3>\n"
                "The PR appears safe to merge with no actionable correctness, "
                "security, or quality issues identified."
            ),
        }
        initial = {
            **self.feedback(),
            "comments": [codex_review_status(), positive_review],
        }
        final = {
            **self.feedback(),
            "comments": [codex_review_status("Completed"), positive_review],
        }
        with (
            patch.object(agent, "run_codex", return_value=self.report) as run,
            patch.object(agent, "wait_for_ci", return_value=final),
        ):
            result = agent.iterate(self.task, "owner/repo", self.report, initial)
        self.assertEqual(result["status"], "completed")
        self.assertEqual(run.call_count, 1)
        self.assertNotIn("codex-pull-request-review-summary", run.call_args.args[0])
        self.assertNotIn("<h3>", self.progress.getvalue())

    def test_status_notice_alone_needs_no_agent(self):
        feedback = {**self.feedback(), "comments": [codex_review_status("Completed")]}
        with patch.object(agent, "run_codex") as run:
            result = agent.iterate(self.task, "owner/repo", self.report, feedback)
        self.assertEqual(result["status"], "completed")
        run.assert_not_called()

    def test_status_marker_does_not_hide_human_or_inline_feedback(self):
        human = {**codex_review_status(), "user": {"login": "reviewer"}}
        inline = {**codex_review_status(), "path": "agent.py", "line": 12}
        for kind, item in [("comments", human), ("review_comments", inline)]:
            with self.subTest(kind=kind):
                self.assertTrue(agent.review_items({kind: [item]}))

    def test_only_new_feedback_is_logged_and_sent_on_later_passes(self):
        initial = self.feedback(body="First finding")
        updated = self.feedback(head="fixed", body="First finding")
        updated["comments"] = [{"id": 2, "body": "Second finding", "user": None}]
        with (
            patch.object(agent, "run_codex", return_value=self.report) as run,
            patch.object(agent, "wait_for_ci", side_effect=[updated, updated]),
        ):
            result = agent.iterate(self.task, "owner/repo", self.report, initial)
        self.assertEqual(result["status"], "completed")
        self.assertEqual(run.call_count, 2)
        self.assertNotIn("First finding", run.call_args.args[0])
        self.assertIn("Second finding", run.call_args.args[0])
        self.assertEqual(self.progress.getvalue().count("First finding"), 1)

    def test_pending_review_timeout_blocks_even_with_green_ci(self):
        feedback = {**self.feedback(), "pending_reviewers": ["Codex"]}
        with patch.object(agent, "run_codex") as run:
            result = agent.iterate(self.task, "owner/repo", self.report, feedback)
        self.assertEqual(result["status"], "blocked")
        self.assertIn("Codex", result["summary"])
        run.assert_not_called()

    def test_review_is_assessed_even_when_ci_passes(self):
        feedback = self.feedback(body="Fix a bug")
        with (
            patch.object(agent, "run_codex", return_value=self.report) as run,
            patch.object(
                agent,
                "wait_for_ci",
                return_value=self.feedback(head="fixed", body="Fix a bug"),
            ) as wait,
        ):
            result = agent.iterate(self.task, "owner/repo", self.report, feedback)
        self.assertEqual(result["status"], "completed")
        self.assertEqual(run.call_count, 1)
        self.assertIn("Fix a bug", run.call_args.args[0])
        self.assertIn("PR #467", run.call_args.args[0])
        self.assertIn(
            "Starting AI agent to assess feedback and fix valid findings (pass 1/3",
            self.progress.getvalue(),
        )
        wait.assert_called_once_with("owner/repo", 467)

    def test_approved_and_dismissed_reviews_do_not_trigger_repairs(self):
        feedback = self.feedback()
        feedback["reviews"] = [
            {"id": 1, "body": "Looks good", "state": "APPROVED"},
            {"id": 2, "body": "Old finding", "state": "DISMISSED"},
        ]
        with patch.object(agent, "run_codex") as run:
            result = agent.iterate(self.task, "owner/repo", self.report, feedback)
        self.assertEqual(result["status"], "completed")
        run.assert_not_called()

    def test_clean_or_timed_out_ci_does_not_need_author_lookup(self):
        for status, expected in [("passed", "completed"), ("timed_out", "blocked")]:
            with patch.object(
                agent, "gh", side_effect=RuntimeError("unavailable")
            ) as lookup:
                result = agent.iterate(
                    self.task, "owner/repo", self.report, self.feedback(status)
                )
            self.assertEqual(result["status"], expected)
            lookup.assert_not_called()

    def test_previous_run_replies_do_not_trigger_repairs(self):
        feedback = self.feedback()
        feedback["comments"] = [
            {
                "id": 2,
                "body": "[agent.py repair] Already fixed.",
                "user": {"login": "builder"},
            }
        ]
        with patch.object(agent, "run_codex") as run:
            result = agent.iterate(self.task, "owner/repo", self.report, feedback)
        self.assertEqual(result["status"], "completed")
        run.assert_not_called()

    def test_own_repair_replies_do_not_trigger_another_pass(self):
        initial = self.feedback(body="Fix this")
        final = self.feedback(head="fixed", body="Fix this")
        final["review_comments"] = [
            {
                "id": 2,
                "body": "[agent.py repair] Fixed and tested.",
                "user": {"login": "builder"},
                "in_reply_to_id": 1,
            }
        ]
        with (
            patch.object(agent, "run_codex", return_value=self.report) as run,
            patch.object(agent, "wait_for_ci", return_value=final),
        ):
            result = agent.iterate(self.task, "owner/repo", self.report, initial)
        self.assertEqual(result["status"], "completed")
        self.assertEqual(run.call_count, 1)
        self.assertTrue(final["review_comments"])

    def test_reply_marker_does_not_hide_another_reviewers_feedback(self):
        feedback = self.feedback()
        feedback["review_comments"] = [
            {
                "id": 2,
                "body": "[agent.py repair] Still broken.",
                "user": {"login": "reviewer"},
            }
        ]
        self.assertTrue(agent.review_items(feedback, "builder"))

    def test_ci_failure_is_fixed_and_rechecked(self):
        with (
            patch.object(agent, "run_codex", return_value=self.report) as run,
            patch.object(
                agent, "wait_for_ci", return_value=self.feedback(head="fixed")
            ),
        ):
            result = agent.iterate(
                self.task, "owner/repo", self.report, self.feedback("failed")
            )
        self.assertEqual(result["status"], "completed")
        self.assertEqual(run.call_count, 1)

    def test_three_repair_passes_are_the_limit(self):
        checks = [self.feedback("failed", head=str(i)) for i in range(3)]
        with (
            patch.object(agent, "run_codex", return_value=self.report) as run,
            patch.object(agent, "wait_for_ci", side_effect=checks) as wait,
        ):
            result = agent.iterate(
                self.task, "owner/repo", self.report, self.feedback("failed")
            )
        self.assertEqual(run.call_count, 3)
        self.assertEqual(wait.call_count, 3)
        self.assertEqual(result["status"], "blocked")
        self.assertEqual(result["pr_number"], 467)

    def test_success_on_third_pass_counts_as_completed(self):
        checks = [
            self.feedback("failed", "one"),
            self.feedback("failed", "two"),
            self.feedback(head="three"),
        ]
        with (
            patch.object(agent, "run_codex", return_value=self.report) as run,
            patch.object(agent, "wait_for_ci", side_effect=checks),
        ):
            result = agent.iterate(
                self.task, "owner/repo", self.report, self.feedback("failed")
            )
        self.assertEqual(result["status"], "completed")
        self.assertEqual(run.call_count, 3)

    def test_new_or_edited_feedback_gets_another_pass(self):
        checks = [
            self.feedback(head="one", body="Updated finding"),
            self.feedback(head="two", body="Updated finding"),
        ]
        with (
            patch.object(agent, "run_codex", return_value=self.report) as run,
            patch.object(agent, "wait_for_ci", side_effect=checks),
        ):
            result = agent.iterate(
                self.task,
                "owner/repo",
                self.report,
                self.feedback(body="Initial finding"),
            )
        self.assertEqual(result["status"], "completed")
        self.assertEqual(run.call_count, 2)

    def test_blocked_repair_stops_without_waiting(self):
        blocked = {**self.report, "status": "blocked", "summary": "Need credentials."}
        with (
            patch.object(agent, "run_codex", return_value=blocked),
            patch.object(agent, "wait_for_ci") as wait,
        ):
            result = agent.iterate(
                self.task, "owner/repo", self.report, self.feedback("failed")
            )
        self.assertEqual(result, blocked)
        wait.assert_not_called()

    def test_ci_timeout_does_not_start_repairs(self):
        with patch.object(agent, "run_codex") as run:
            result = agent.iterate(
                self.task, "owner/repo", self.report, self.feedback("timed_out")
            )
        self.assertEqual(result["status"], "blocked")
        run.assert_not_called()

    def test_failed_ci_without_a_push_stops(self):
        feedback = self.feedback("failed")
        with (
            patch.object(agent, "run_codex", return_value=self.report) as run,
            patch.object(agent, "wait_for_ci", return_value=feedback),
        ):
            result = agent.iterate(self.task, "owner/repo", self.report, feedback)
        self.assertEqual(result["status"], "blocked")
        self.assertEqual(run.call_count, 1)

    def test_new_ci_failure_without_a_push_gets_a_repair_pass(self):
        failed = self.feedback("failed", body="Looks good")
        for initial_status in ("passed", "failed"):
            initial = self.feedback(initial_status, body="Looks good")
            initial["checks"] = [
                {
                    "name": "first",
                    "state": "FAILURE" if initial_status == "failed" else "SUCCESS",
                }
            ]
            failed["checks"] = [{"name": "second", "state": "FAILURE"}]
            with (
                self.subTest(initial_status=initial_status),
                patch.object(agent, "run_codex", return_value=self.report) as run,
                patch.object(
                    agent,
                    "wait_for_ci",
                    side_effect=[
                        failed,
                        self.feedback(head="fixed", body="Looks good"),
                    ],
                ),
            ):
                result = agent.iterate(self.task, "owner/repo", self.report, initial)
            self.assertEqual(result["status"], "completed")
            self.assertEqual(run.call_count, 2)

    def test_late_repair_exception_retains_pr_in_cli_result(self):
        with (
            patch.object(agent, "validate_pr"),
            patch.object(agent, "implement", return_value=self.report),
            patch.object(agent, "wait_for_ci", return_value=self.feedback("failed")),
            patch.object(
                agent, "run_codex", side_effect=RuntimeError("repair crashed")
            ),
        ):
            with contextlib.redirect_stdout(io.StringIO()) as out:
                self.assertEqual(agent.main([self.task]), 1)
        self.assertEqual(
            json.loads(out.getvalue()),
            {"status": "failed", "pr_number": 467, "summary": "repair crashed"},
        )

    def test_repair_cannot_switch_pr(self):
        with patch.object(
            agent, "run_codex", return_value={**self.report, "pr_number": 999}
        ):
            with self.assertRaisesRegex(ValueError, "original PR"):
                agent.iterate(
                    self.task, "owner/repo", self.report, self.feedback("failed")
                )


if __name__ == "__main__":
    unittest.main()
