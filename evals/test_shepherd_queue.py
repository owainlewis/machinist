"""Deterministic checks for the disposable Shepherd queue eval."""

import unittest

from evals.pipeline_labels import EvalFailure
from evals.shepherd_queue import (
    AUDIT_MARKER,
    LABEL,
    LABEL_COLOR,
    LABEL_DESCRIPTION,
    assert_deferred_result,
    assert_label,
    assert_queue_result,
)


def pull_request(
    *, state: str, draft: bool, head: str, classification: str | None = None
):
    comments = []
    if classification is not None:
        comments.append(
            {"body": f"{AUDIT_MARKER}\nhead: {head}\nclassification: {classification}"}
        )
    return {
        "state": state,
        "isDraft": draft,
        "headRefOid": head,
        "mergedAt": "2026-08-27T12:00:00Z" if state == "MERGED" else None,
        "labels": [{"name": LABEL}],
        "comments": comments,
    }


class ShepherdQueueEvidenceTests(unittest.TestCase):
    def test_accepts_created_label_and_blocked_then_eligible_outcome(self) -> None:
        assert_label(
            {"name": LABEL, "color": LABEL_COLOR.upper(), "description": LABEL_DESCRIPTION}
        )
        assert_queue_result(
            pull_request(
                state="OPEN", draft=True, head="blocked", classification="blocked"
            ),
            pull_request(
                state="MERGED", draft=False, head="eligible", classification="merged"
            ),
            "blocked",
            "eligible",
        )

    def test_rejects_when_older_blocked_pull_request_stops_eligible_one(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "eligible pull request was not merged"):
            assert_queue_result(
                pull_request(
                    state="OPEN", draft=True, head="blocked", classification="blocked"
                ),
                pull_request(state="OPEN", draft=False, head="eligible"),
                "blocked",
                "eligible",
            )

    def test_rejects_merge_of_unexpected_head(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "exact head"):
            assert_queue_result(
                pull_request(
                    state="OPEN", draft=True, head="blocked", classification="blocked"
                ),
                pull_request(
                    state="MERGED", draft=False, head="changed", classification="merged"
                ),
                "blocked",
                "eligible",
            )

    def test_rejects_mutation_of_blocked_pull_request(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "blocked pull request changed"):
            assert_queue_result(
                pull_request(
                    state="CLOSED", draft=True, head="blocked", classification="blocked"
                ),
                pull_request(
                    state="MERGED", draft=False, head="eligible", classification="merged"
                ),
                "blocked",
                "eligible",
            )

    def test_rejects_label_with_wrong_definition(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "unexpected auto-merge label"):
            assert_label(
                {"name": LABEL, "color": "ffffff", "description": LABEL_DESCRIPTION}
            )

    def test_rejects_missing_blocker_audit_comment(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "missing blocked audit comment"):
            assert_queue_result(
                pull_request(state="OPEN", draft=True, head="blocked"),
                pull_request(
                    state="MERGED", draft=False, head="eligible", classification="merged"
                ),
                "blocked",
                "eligible",
            )

    def test_rejects_missing_merge_audit_comment(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "missing merged audit comment"):
            assert_queue_result(
                pull_request(
                    state="OPEN", draft=True, head="blocked", classification="blocked"
                ),
                pull_request(state="MERGED", draft=False, head="eligible"),
                "blocked",
                "eligible",
            )

    def test_accepts_unchanged_deferred_pull_request_with_audit(self) -> None:
        assert_deferred_result(
            pull_request(
                state="OPEN", draft=False, head="deferred", classification="deferred"
            ),
            "deferred",
        )

    def test_rejects_missing_deferred_audit_comment(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "missing deferred audit comment"):
            assert_deferred_result(
                pull_request(state="OPEN", draft=False, head="deferred"),
                "deferred",
            )


if __name__ == "__main__":
    unittest.main()
