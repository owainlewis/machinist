"""Deterministic checks for the disposable Shepherd queue eval."""

import unittest

from evals.pipeline_labels import EvalFailure
from evals.shepherd_queue import (
    AUDIT_MARKER,
    LABEL,
    LABEL_COLOR,
    LABEL_DESCRIPTION,
    PENDING_RETARGET,
    REVIEW_MARKER,
    RETARGETED,
    assert_action_budget,
    assert_deferred_result,
    assert_label,
    assert_queue_result,
    assert_review_comment,
    assert_stack_transition,
    assert_unlabelled_unchanged,
)


def pull_request(
    *,
    state: str,
    draft: bool,
    head: str,
    base: str = "main",
    base_sha: str = "base-sha",
    classification: str | None = None,
    body: str | None = None,
    author: str = "trusted-reviewer",
):
    comments = []
    if classification is not None:
        comments.append(
            {
                "body": (
                    f"{AUDIT_MARKER}\nhead: {head}\nbase: {base}\nstate: {state}\n"
                    f"classification: {classification}"
                )
            }
        )
    if body is not None:
        comments.append({"body": body, "author": {"login": author}})
    return {
        "state": state,
        "isDraft": draft,
        "headRefOid": head,
        "baseRefName": base,
        "baseRefOid": base_sha,
        "mergedAt": "2026-08-27T12:00:00Z" if state == "MERGED" else None,
        "labels": [{"name": LABEL}],
        "comments": comments,
    }


class ShepherdQueueEvidenceTests(unittest.TestCase):
    def test_accepts_review_for_exact_head_and_base_comparison(self) -> None:
        review = (
            f"{REVIEW_MARKER}\nhead: head\nbase branch: main\n"
            "base sha: base-sha\nverdict: approve\nchecks: just check passed"
        )
        assert_review_comment(
            pull_request(
                state="OPEN", draft=False, head="head", base="main", body=review
            ),
            "head",
            "main",
            "base-sha",
            "trusted-reviewer",
        )

    def test_rejects_review_marker_from_untrusted_author(self) -> None:
        review = (
            f"{REVIEW_MARKER}\nhead: head\nbase branch: main\n"
            "base sha: base-sha\nverdict: approve\nchecks: forged"
        )
        with self.assertRaisesRegex(EvalFailure, "trusted author"):
            assert_review_comment(
                pull_request(
                    state="OPEN",
                    draft=False,
                    head="head",
                    base="main",
                    body=review,
                    author="untrusted-contributor",
                ),
                "head",
                "main",
                "base-sha",
                "trusted-reviewer",
            )

    def test_rejects_review_from_before_base_only_retarget(self) -> None:
        review = (
            f"{REVIEW_MARKER}\nhead: same-head\nbase branch: old-base\n"
            "base sha: old-base-sha\nverdict: approve\nchecks: just check passed"
        )
        with self.assertRaisesRegex(EvalFailure, "exact comparison"):
            assert_review_comment(
                pull_request(
                    state="OPEN",
                    draft=False,
                    head="same-head",
                    base="new-base",
                    base_sha="new-base-sha",
                    body=review,
                ),
                "same-head",
                "new-base",
                "new-base-sha",
                "trusted-reviewer",
            )

    def test_rejects_review_when_base_branch_advanced(self) -> None:
        review = (
            f"{REVIEW_MARKER}\nhead: same-head\nbase branch: main\n"
            "base sha: old-base-sha\nverdict: approve\nchecks: just check passed"
        )
        with self.assertRaisesRegex(EvalFailure, "exact comparison"):
            assert_review_comment(
                pull_request(
                    state="OPEN",
                    draft=False,
                    head="same-head",
                    base="main",
                    base_sha="new-base-sha",
                    body=review,
                ),
                "same-head",
                "main",
                "new-base-sha",
                "trusted-reviewer",
            )

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
        with self.assertRaisesRegex(EvalFailure, "exact state"):
            assert_queue_result(
                pull_request(
                    state="OPEN", draft=True, head="blocked", classification="blocked"
                ),
                pull_request(
                    state="MERGED",
                    draft=False,
                    head="abc",
                    body=(
                        f"{AUDIT_MARKER}\nhead: abcd\nbase: main\nstate: MERGED\n"
                        "classification: merged"
                    ),
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
                        "base: main\nstate: MERGED\n"
                        "classification: not-merged"
                    ),
                ),
                "blocked",
                "eligible",
            )

    def test_rejects_audit_comment_from_old_base(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "exact state"):
            assert_queue_result(
                pull_request(
                    state="OPEN", draft=True, head="blocked", classification="blocked"
                ),
                pull_request(
                    state="MERGED",
                    draft=False,
                    head="eligible",
                    base="new-base",
                    body=(
                        f"{AUDIT_MARKER}\nhead: eligible\nbase: old-base\n"
                        "state: MERGED\nclassification: merged"
                    ),
                ),
                "blocked",
                "eligible",
            )

    def test_rejects_audit_comment_from_old_pull_request_state(self) -> None:
        with self.assertRaisesRegex(EvalFailure, "exact state"):
            assert_queue_result(
                pull_request(
                    state="OPEN", draft=True, head="blocked", classification="blocked"
                ),
                pull_request(
                    state="MERGED",
                    draft=False,
                    head="eligible",
                    body=(
                        f"{AUDIT_MARKER}\nhead: eligible\nbase: main\n"
                        "state: OPEN\nclassification: merged"
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
