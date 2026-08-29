"""Pure helper tests for the opt-in GitHub-label eval."""

import subprocess
import unittest
from pathlib import Path
from unittest.mock import patch

from evals.github_labels import (
    EvalFailure,
    assert_label_lifecycle,
    assert_run_result,
    cleanup,
    ensure_branch_absent,
    owned_branch,
    pull_request_url,
    worktree_for_branch,
)


def transitions(*labels: str) -> tuple[tuple[str, str], ...]:
    events: list[tuple[str, str]] = []
    for previous, label in zip((None, *labels), labels):
        if previous is not None:
            events.append(("unlabeled", previous))
        events.append(("labeled", label))
    return tuple(events)


class LabelLifecycleTests(unittest.TestCase):
    def test_accepts_expected_lifecycle(self) -> None:
        assert_label_lifecycle(
            ("machinist:ready-for-review",),
            transitions(
                "machinist:planning",
                "machinist:building",
                "machinist:verifying",
                "machinist:verifying",
                "machinist:ready-for-review",
            ),
            0,
        )

    def test_rejects_missing_transition(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "expected planning"):
            assert_label_lifecycle(
                ("machinist:ready-for-review",),
                transitions(
                    "machinist:planning",
                    "machinist:verifying",
                    "machinist:ready-for-review",
                ),
                0,
            )

    def test_rejects_conflicting_final_label(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "expected only"):
            assert_label_lifecycle(
                ("machinist:ready-for-review", "machinist:blocked"),
                transitions(
                    "machinist:planning",
                    "machinist:building",
                    "machinist:verifying",
                    "machinist:ready-for-review",
                ),
                0,
            )

    def test_rejects_nonzero_machinist_exit(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "status 2"):
            assert_label_lifecycle((), (), 2)

    def test_accepts_repair_cycle(self) -> None:
        assert_label_lifecycle(
            ("machinist:ready-for-review",),
            transitions(
                "machinist:planning",
                "machinist:building",
                "machinist:verifying",
                "machinist:building",
                "machinist:verifying",
                "machinist:ready-for-review",
            ),
            0,
        )

    def test_rejects_early_ready_transition(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "expected planning"):
            assert_label_lifecycle(
                ("machinist:ready-for-review",),
                transitions(
                    "machinist:planning",
                    "machinist:ready-for-review",
                    "machinist:building",
                    "machinist:verifying",
                    "machinist:ready-for-review",
                ),
                0,
            )

    def test_rejects_exception_transition(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "expected planning"):
            assert_label_lifecycle(
                ("machinist:ready-for-review",),
                transitions(
                    "machinist:planning",
                    "machinist:blocked",
                    "machinist:building",
                    "machinist:verifying",
                    "machinist:ready-for-review",
                ),
                0,
            )

    def test_rejects_backward_transition(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "expected planning"):
            assert_label_lifecycle(
                ("machinist:ready-for-review",),
                transitions(
                    "machinist:planning",
                    "machinist:building",
                    "machinist:planning",
                    "machinist:verifying",
                    "machinist:ready-for-review",
                ),
                0,
            )

    def test_rejects_success_without_owned_pull_request(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "owned pull request"):
            assert_run_result(
                ("machinist:ready-for-review",),
                transitions(
                    "machinist:planning",
                    "machinist:building",
                    "machinist:verifying",
                    "machinist:ready-for-review",
                ),
                0,
                None,
            )

    def test_rejects_accumulated_machinist_labels(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "one active Machinist label"):
            assert_label_lifecycle(
                ("machinist:ready-for-review",),
                (
                    ("labeled", "machinist:planning"),
                    ("labeled", "machinist:building"),
                    ("labeled", "machinist:verifying"),
                    ("unlabeled", "machinist:planning"),
                    ("unlabeled", "machinist:building"),
                    ("unlabeled", "machinist:verifying"),
                    ("labeled", "machinist:ready-for-review"),
                ),
                0,
            )


class EvidenceTests(unittest.TestCase):
    def test_finds_marked_pull_request(self) -> None:
        self.assertEqual(
            pull_request_url(
                (
                    {
                        "body": "<!-- machinist:foreman-pr -->\n"
                        "https://github.com/acme/machinist-evals/pull/9"
                    },
                ),
                (),
                "acme/machinist-evals",
            ),
            "https://github.com/acme/machinist-evals/pull/9",
        )

    def test_falls_back_to_cross_reference(self) -> None:
        self.assertEqual(
            pull_request_url(
                (),
                (
                    {
                        "event": "cross-referenced",
                        "source": {
                            "issue": {
                                "html_url": "https://github.com/acme/machinist-evals/pull/9",
                                "pull_request": {},
                            }
                        },
                    },
                ),
                "acme/machinist-evals",
            ),
            "https://github.com/acme/machinist-evals/pull/9",
        )

    def test_rejects_pull_request_from_another_repository(self) -> None:
        self.assertIsNone(
            pull_request_url(
                (
                    {
                        "body": "<!-- machinist:foreman-pr -->\n"
                        "https://github.com/acme/production/pull/9"
                    },
                ),
                (),
                "acme/machinist-evals",
            )
        )

    def test_finds_exact_branch_worktree(self) -> None:
        output = """worktree /code/machinist
HEAD abc123
branch refs/heads/main

worktree /code/.worktrees/machinist/eval
HEAD def456
branch refs/heads/codex/eval-marker

"""
        self.assertEqual(
            worktree_for_branch(output, "codex/eval-marker"),
            Path("/code/.worktrees/machinist/eval"),
        )

    def test_accepts_exact_same_repository_eval_branch(self) -> None:
        self.assertEqual(
            owned_branch(
                {
                    "headRefName": "codex/machinist-eval-run-12",
                    "isCrossRepository": False,
                },
                "run-12",
            ),
            "codex/machinist-eval-run-12",
        )

    def test_rejects_unrelated_codex_branch(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "unexpected eval branch"):
            owned_branch(
                {"headRefName": "codex/unrelated", "isCrossRepository": False},
                "run-12",
            )

    def test_rejects_fork_branch(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "unexpected eval branch"):
            owned_branch(
                {
                    "headRefName": "codex/machinist-eval-run-12",
                    "isCrossRepository": True,
                },
                "run-12",
            )


class CleanupTests(unittest.TestCase):
    @patch("evals.github_labels.remove_owned_branch")
    @patch("evals.github_labels.command")
    def test_cleans_owned_branch_without_pull_request(
        self, mocked_command, mocked_remove
    ) -> None:
        mocked_command.return_value = subprocess.CompletedProcess([], 0, "", "")

        errors = cleanup(
            "acme/machinist-evals",
            Path("/code/machinist-evals"),
            "https://github.com/acme/machinist-evals/issues/12",
            None,
            "run-12",
        )

        self.assertEqual(errors, [])
        mocked_remove.assert_called_once_with(
            "acme/machinist-evals",
            Path("/code/machinist-evals"),
            "codex/machinist-eval-run-12",
        )

    @patch("evals.github_labels.command")
    def test_refuses_preexisting_local_branch(self, mocked_command) -> None:
        mocked_command.return_value = subprocess.CompletedProcess([], 0, "", "")

        with self.assertRaisesRegex(EvalFailure, "already exists locally"):
            ensure_branch_absent(
                Path("/code/machinist-evals"), "codex/machinist-eval-run-12"
            )

    @patch("evals.github_labels.command")
    def test_accepts_absent_local_and_remote_branch(self, mocked_command) -> None:
        mocked_command.side_effect = (
            subprocess.CompletedProcess([], 1, "", ""),
            subprocess.CompletedProcess([], 2, "", ""),
        )

        ensure_branch_absent(
            Path("/code/machinist-evals"), "codex/machinist-eval-run-12"
        )


if __name__ == "__main__":
    unittest.main()
