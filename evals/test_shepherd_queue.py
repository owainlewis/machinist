"""Deterministic checks for the disposable Shepherd queue eval."""

import unittest

from evals.pipeline_labels import EvalFailure
from evals.shepherd_queue import (
    AUDIT_MARKER,
    LABEL,
    LABEL_COLOR,
    LABEL_DESCRIPTION,
    PENDING_RETARGET,
    RETARGETED,
    assert_action_budget,
    assert_deferred_result,
    assert_label,
    assert_queue_result,
    assert_stack_transition,
    assert_unlabelled_unchanged,
)


def pull_request(
    *,
    state: str,
    draft: bool,
    head: str,
    base: str = "main",
    classification: str | None = None,
    body: str | None = None,
):
    comments = []
    if classification is not None:
        comments.append(
            {"body": f"{AUDIT_MARKER}\nhead: {head}\nclassification: {classification}"}
        )
    if body is not None:
        comments.append({"body": body})
    return {
        "state": state,
        "isDraft": draft,
        "headRefOid": head,
        "baseRefName": base,
        "mergedAt": "2026-08-27T12:00:00Z" if state == "MERGED" else None,
        "labels": [{"name": LABEL}],
        "comments": comments,
    }


class ShepherdQueueEvidenceTests(unittest.TestCase):
    def test_rejects_audit_mutations_beyond_action_budget(self) -> None:
        before = {
            "label": {"name": LABEL},
            "pull_requests": {
                "blocked": pull_request(state="OPEN", draft=True, head="blocked"),
                "eligible": pull_request(state="OPEN", draft=False, head="eligible"),
                "deferred": pull_request(state="OPEN", draft=False, head="deferred"),
            },
        }
        after = {
            "label": {"name": LABEL},
            "pull_requests": {
                "blocked": pull_request(
                    state="OPEN", draft=True, head="blocked", classification="blocked"
                ),
                "eligible": pull_request(
                    state="MERGED",
                    draft=False,
                    head="eligible",
                    classification="merged",
                ),
                "deferred": pull_request(
                    state="OPEN",
                    draft=False,
                    head="deferred",
                    classification="deferred",
                ),
            },
        }

        with self.assertRaisesRegex(EvalFailure, "used 4 actions, limit 2"):
            assert_action_budget(before, after, 2)

    def test_rejects_two_stack_mutations_in_max_actions_one_run(self) -> None:
        pending = (
            f"{AUDIT_MARKER}\nclassification: stack-transition\n"
            f"state: {PENDING_RETARGET}\nparent: https://example.test/pull/1\n"
            "parent head: parent-head\nparent base: main\n"
            "dependent head: child-head\ndependent base: parent-branch"
        )
        before = {
            "label": {"name": LABEL},
            "pull_requests": {
                "parent": pull_request(
                    state="OPEN", draft=False, head="parent-head", base="main"
                ),
                "child": pull_request(
                    state="OPEN",
                    draft=False,
                    head="child-head",
                    base="parent-branch",
                ),
            },
        }
        after = {
            "label": {"name": LABEL},
            "pull_requests": {
                "parent": pull_request(
                    state="MERGED", draft=False, head="parent-head", base="main"
                ),
                "child": pull_request(
                    state="OPEN",
                    draft=False,
                    head="child-head",
                    base="parent-branch",
                    body=pending,
                ),
            },
        }

        with self.assertRaisesRegex(EvalFailure, "used 2 actions, limit 1"):
            assert_action_budget(before, after, 1)

    def test_accepts_unlabelled_pull_request_with_no_mutation(self) -> None:
        pull = pull_request(state="OPEN", draft=False, head="head", base="main")
        pull["labels"] = []
        assert_unlabelled_unchanged(pull, head="head", base="main", draft=False)

    def test_rejects_audit_comment_on_unlabelled_pull_request(self) -> None:
        pull = pull_request(
            state="OPEN",
            draft=False,
            head="head",
            base="main",
            classification="blocked",
        )
        pull["labels"] = []
        with self.assertRaisesRegex(EvalFailure, "changed an unlabelled"):
            assert_unlabelled_unchanged(pull, head="head", base="main", draft=False)

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

    def test_accepts_merge_without_audit_when_action_budget_is_consumed(self) -> None:
        assert_queue_result(
            pull_request(
                state="OPEN", draft=True, head="blocked", classification="blocked"
            ),
            pull_request(state="MERGED", draft=False, head="eligible"),
            "blocked",
            "eligible",
            require_eligible_audit=False,
        )

    def test_rejects_audit_comment_with_prefix_matched_head(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "exact head"):
            assert_queue_result(
                pull_request(
                    state="OPEN", draft=True, head="blocked", classification="blocked"
                ),
                pull_request(
                    state="MERGED",
                    draft=False,
                    head="abc",
                    body=f"{AUDIT_MARKER}\nhead: abcd\nclassification: merged",
                ),
                "blocked",
                "abc",
            )

    def test_rejects_audit_comment_with_substring_classification(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "missing merged audit comment"):
            assert_queue_result(
                pull_request(
                    state="OPEN", draft=True, head="blocked", classification="blocked"
                ),
                pull_request(
                    state="MERGED",
                    draft=False,
                    head="eligible",
                    body=(
                        f"{AUDIT_MARKER}\nhead: eligible\n"
                        "classification: not-merged"
                    ),
                ),
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

    def test_accepts_unchanged_deferred_without_unbudgeted_comment(self) -> None:
        assert_deferred_result(
            pull_request(state="OPEN", draft=False, head="deferred"),
            "deferred",
            require_audit=False,
        )

    def test_accepts_persisted_transition_after_parent_uses_final_action(self) -> None:
        transition = (
            f"{AUDIT_MARKER}\nclassification: stack-transition\n"
            f"state: {PENDING_RETARGET}\nparent: https://example.test/pull/1\n"
            "parent head: parent-head\nparent base: main\n"
            "dependent head: child-head\ndependent base: parent-branch"
        )
        assert_stack_transition(
            pull_request(
                state="MERGED", draft=False, head="parent-head", base="main"
            ),
            pull_request(
                state="OPEN",
                draft=False,
                head="child-head",
                base="parent-branch",
                body=transition,
            ),
            parent_url="https://example.test/pull/1",
            parent_head="parent-head",
            parent_base="main",
            child_head="child-head",
            child_base="parent-branch",
            state=PENDING_RETARGET,
        )

    def test_accepts_retargeted_child_on_later_max_actions_one_run(self) -> None:
        transition = (
            f"{AUDIT_MARKER}\nclassification: stack-transition\n"
            f"state: {RETARGETED}\nparent: https://example.test/pull/1\n"
            "parent head: parent-head\nparent base: main\n"
            "dependent head: child-head\ndependent base: parent-branch"
        )
        assert_stack_transition(
            pull_request(
                state="MERGED", draft=False, head="parent-head", base="main"
            ),
            pull_request(
                state="OPEN", draft=False, head="child-head", base="main", body=transition
            ),
            parent_url="https://example.test/pull/1",
            parent_head="parent-head",
            parent_base="main",
            child_head="child-head",
            child_base="parent-branch",
            state=RETARGETED,
        )

    def test_rejects_child_treated_as_independent_without_retarget(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "durable pending-retarget"):
            assert_stack_transition(
                pull_request(
                    state="MERGED", draft=False, head="parent-head", base="main"
                ),
                pull_request(
                    state="OPEN", draft=False, head="child-head", base="parent-branch"
                ),
                parent_url="https://example.test/pull/1",
                parent_head="parent-head",
                parent_base="main",
                child_head="child-head",
                child_base="parent-branch",
                state=PENDING_RETARGET,
            )

    def test_rejects_retarget_when_parent_merge_evidence_changed(self) -> None:
        transition = (
            f"{AUDIT_MARKER}\nclassification: stack-transition\n"
            f"state: {PENDING_RETARGET}\nparent: https://example.test/pull/1\n"
            "parent head: parent-head\nparent base: main\n"
            "dependent head: child-head\ndependent base: parent-branch"
        )
        with self.assertRaisesRegex(EvalFailure, "stack parent"):
            assert_stack_transition(
                pull_request(
                    state="MERGED", draft=False, head="different-head", base="main"
                ),
                pull_request(
                    state="OPEN",
                    draft=False,
                    head="child-head",
                    base="parent-branch",
                    body=transition,
                ),
                parent_url="https://example.test/pull/1",
                parent_head="parent-head",
                parent_base="main",
                child_head="child-head",
                child_base="parent-branch",
                state=PENDING_RETARGET,
            )

    def test_rejects_stack_transition_with_prefix_matched_parent_head(self) -> None:
        transition = (
            f"{AUDIT_MARKER}\nclassification: stack-transition\n"
            f"state: {PENDING_RETARGET}\nparent: https://example.test/pull/1\n"
            "parent head: parent-head-extra\nparent base: main\n"
            "dependent head: child-head\ndependent base: parent-branch"
        )
        with self.assertRaisesRegex(EvalFailure, "durable pending-retarget"):
            assert_stack_transition(
                pull_request(
                    state="MERGED", draft=False, head="parent-head", base="main"
                ),
                pull_request(
                    state="OPEN",
                    draft=False,
                    head="child-head",
                    base="parent-branch",
                    body=transition,
                ),
                parent_url="https://example.test/pull/1",
                parent_head="parent-head",
                parent_base="main",
                child_head="child-head",
                child_base="parent-branch",
                state=PENDING_RETARGET,
            )

    def test_rejects_retargeted_transition_with_active_pending_marker(self) -> None:
        pending = (
            f"{AUDIT_MARKER}\nclassification: stack-transition\n"
            f"state: {PENDING_RETARGET}\nparent: https://example.test/pull/1\n"
            "parent head: parent-head\nparent base: main\n"
            "dependent head: child-head\ndependent base: parent-branch"
        )
        retargeted = pending.replace(
            f"state: {PENDING_RETARGET}", f"state: {RETARGETED}"
        )
        child = pull_request(
            state="OPEN", draft=False, head="child-head", base="main", body=retargeted
        )
        child["comments"].append({"body": pending})
        with self.assertRaisesRegex(EvalFailure, "active pending-retarget"):
            assert_stack_transition(
                pull_request(
                    state="MERGED", draft=False, head="parent-head", base="main"
                ),
                child,
                parent_url="https://example.test/pull/1",
                parent_head="parent-head",
                parent_base="main",
                child_head="child-head",
                child_base="parent-branch",
                state=RETARGETED,
            )


if __name__ == "__main__":
    unittest.main()
