"""Exercise Shepherd against two pull requests in a disposable repository."""

from __future__ import annotations

import argparse
import sys
import tempfile
import time
import uuid
from pathlib import Path
from typing import Any, Sequence

from evals.pipeline_labels import EvalFailure, checked, command, gh_json, validate


LABEL = "machinist:auto-merge"
LABEL_COLOR = "0e8a16"
LABEL_DESCRIPTION = "Allow Shepherd to verify, update, repair, and merge this pull request"
AUDIT_MARKER = "<!-- machinist:shepherd-audit -->"
REVIEW_MARKER = "<!-- machinist:shepherd-review -->"
STACK_CLASSIFICATION = "stack-transition"
PENDING_RETARGET = "pending-retarget"
RETARGETED = "retargeted"


def label_names(pull_request: dict[str, Any]) -> set[str]:
    return {
        label["name"]
        for label in pull_request.get("labels", ())
        if isinstance(label, dict) and isinstance(label.get("name"), str)
    }


def assert_label(label: Any) -> None:
    if not isinstance(label, dict):
        raise EvalFailure("Shepherd did not create the auto-merge label")
    observed = (
        label.get("name"),
        str(label.get("color", "")).lower(),
        label.get("description"),
    )
    expected = (LABEL, LABEL_COLOR, LABEL_DESCRIPTION)
    if observed != expected:
        raise EvalFailure(f"unexpected auto-merge label definition: {observed!r}")


def marker_fields(body: str, marker: str) -> dict[str, str] | None:
    lines = body.splitlines()
    if marker not in (line.strip() for line in lines):
        return None
    fields: dict[str, str] = {}
    for line in lines:
        key, separator, value = line.partition(":")
        if not separator:
            continue
        key = key.strip().lower()
        if not key or key in fields:
            return None
        fields[key] = value.strip()
    return fields


def audit_fields(body: str) -> dict[str, str] | None:
    return marker_fields(body, AUDIT_MARKER)


def assert_audit_comment(
    pull_request: dict[str, Any], head: str, classification: str
) -> None:
    comments = pull_request.get("comments")
    if not isinstance(comments, list):
        raise EvalFailure("GitHub returned invalid pull request comments")
    for comment in comments:
        if not isinstance(comment, dict) or not isinstance(comment.get("body"), str):
            continue
        fields = audit_fields(comment["body"])
        if (
            fields is not None
            and fields.get("head") == head
            and fields.get("classification") == classification
        ):
            return
    raise EvalFailure(
        f"missing {classification} audit comment for exact head {head}"
    )


def assert_queue_result(
    blocked: Any,
    eligible: Any,
    blocked_head: str,
    eligible_head: str,
    *,
    require_eligible_audit: bool = True,
) -> None:
    if not isinstance(blocked, dict) or not isinstance(eligible, dict):
        raise EvalFailure("GitHub returned invalid pull request evidence")
    if (
        blocked.get("state") != "OPEN"
        or blocked.get("isDraft") is not True
        or blocked.get("headRefOid") != blocked_head
        or LABEL not in label_names(blocked)
    ):
        raise EvalFailure(f"older blocked pull request changed unexpectedly: {blocked!r}")
    if (
        eligible.get("state") != "MERGED"
        or not eligible.get("mergedAt")
        or eligible.get("headRefOid") != eligible_head
        or LABEL not in label_names(eligible)
    ):
        raise EvalFailure(f"eligible pull request was not merged at its exact head: {eligible!r}")
    assert_audit_comment(blocked, blocked_head, "blocked")
    if require_eligible_audit:
        assert_audit_comment(eligible, eligible_head, "merged")


def assert_deferred_result(
    deferred: Any, deferred_head: str, *, require_audit: bool = True
) -> None:
    if not isinstance(deferred, dict):
        raise EvalFailure("GitHub returned invalid deferred pull request evidence")
    if (
        deferred.get("state") != "OPEN"
        or deferred.get("isDraft") is not False
        or deferred.get("headRefOid") != deferred_head
        or LABEL not in label_names(deferred)
    ):
        raise EvalFailure(f"deferred pull request changed unexpectedly: {deferred!r}")
    if require_audit:
        assert_audit_comment(deferred, deferred_head, "deferred")


def assert_unlabelled_unchanged(
    pull_request: Any, *, head: str, base: str, draft: bool
) -> None:
    if not isinstance(pull_request, dict):
        raise EvalFailure("GitHub returned invalid unlabelled pull request evidence")
    if (
        pull_request.get("state") != "OPEN"
        or pull_request.get("isDraft") is not draft
        or pull_request.get("headRefOid") != head
        or pull_request.get("baseRefName") != base
        or LABEL in label_names(pull_request)
        or pull_request.get("comments") not in ([], None)
    ):
        raise EvalFailure(
            f"Shepherd changed an unlabelled pull request: {pull_request!r}"
        )


def assert_stack_transition(
    parent: Any,
    child: Any,
    *,
    parent_url: str,
    parent_head: str,
    parent_base: str,
    child_head: str,
    child_base: str,
    state: str,
    parent_state: str = "MERGED",
    child_current_base: str | None = None,
) -> None:
    if not isinstance(parent, dict) or not isinstance(child, dict):
        raise EvalFailure("GitHub returned invalid stack transition evidence")
    if (
        parent.get("state") != parent_state
        or parent.get("headRefOid") != parent_head
        or parent.get("baseRefName") != parent_base
        or (parent_state == "MERGED" and not parent.get("mergedAt"))
        or (parent_state == "OPEN" and parent.get("mergedAt") is not None)
    ):
        raise EvalFailure(
            f"stack parent was not {parent_state.lower()} at its expected comparison: "
            f"{parent!r}"
        )
    expected_child_base = child_current_base
    if expected_child_base is None:
        expected_child_base = child_base if state == PENDING_RETARGET else parent_base
    if (
        child.get("state") != "OPEN"
        or child.get("headRefOid") != child_head
        or child.get("baseRefName") != expected_child_base
        or LABEL not in label_names(child)
    ):
        raise EvalFailure(f"stack child does not match {state} state: {child!r}")
    comments = child.get("comments")
    if not isinstance(comments, list):
        raise EvalFailure("GitHub returned invalid stack child comments")
    records = [
        fields
        for comment in comments
        if isinstance(comment, dict) and isinstance(comment.get("body"), str)
        if (fields := audit_fields(comment["body"])) is not None
    ]
    expected = {
        "classification": STACK_CLASSIFICATION,
        "state": state,
        "parent": parent_url,
        "parent head": parent_head,
        "parent base": parent_base,
        "dependent head": child_head,
        "dependent base": child_base,
    }
    if state == RETARGETED and any(
        record.get("classification") == STACK_CLASSIFICATION
        and record.get("state") == PENDING_RETARGET
        for record in records
    ):
        raise EvalFailure("active pending-retarget transition remains after retarget")
    if any(
        all(record.get(key) == value for key, value in expected.items())
        for record in records
    ):
        return
    raise EvalFailure(f"missing durable {state} stack transition evidence")


def assert_review_comment(pull_request: Any, head: str) -> None:
    if not isinstance(pull_request, dict):
        raise EvalFailure("GitHub returned invalid review evidence")
    comments = pull_request.get("comments")
    if not isinstance(comments, list):
        raise EvalFailure("GitHub returned invalid review comments")
    for comment in comments:
        if not isinstance(comment, dict) or not isinstance(comment.get("body"), str):
            continue
        fields = marker_fields(comment["body"], REVIEW_MARKER)
        if fields is None:
            continue
        recorded_head = fields.get("head") or fields.get("head sha")
        if (
            recorded_head == head
            and fields.get("verdict", "").lower() == "approve"
            and fields.get("checks")
        ):
            return
    raise EvalFailure(f"missing approved review audit comment for exact head {head}")


def ensure_label_absent(repository: str) -> None:
    labels = gh_json(
        ("label", "list", "--repo", repository, "--limit", "1000", "--json", "name")
    )
    if not isinstance(labels, list):
        raise EvalFailure("GitHub returned invalid repository label evidence")
    if any(isinstance(label, dict) and label.get("name") == LABEL for label in labels):
        raise EvalFailure(
            f"{LABEL} already exists; use a scratch repository where Shepherd can create it"
        )


def pull_request(repository: str, url: str) -> dict[str, Any]:
    result = gh_json(
        (
            "pr",
            "view",
            url,
            "--repo",
            repository,
            "--json",
            "state,isDraft,headRefOid,baseRefName,mergedAt,labels,comments,reviews,"
            "title,body,url",
        )
    )
    if not isinstance(result, dict):
        raise EvalFailure(f"GitHub returned invalid evidence for {url}")
    return result


def repository_label(repository: str) -> dict[str, Any] | None:
    labels = gh_json(
        (
            "label",
            "list",
            "--repo",
            repository,
            "--limit",
            "1000",
            "--json",
            "name,color,description",
        )
    )
    if not isinstance(labels, list):
        raise EvalFailure("GitHub returned invalid repository label evidence")
    matches = [
        label
        for label in labels
        if isinstance(label, dict) and label.get("name") == LABEL
    ]
    if len(matches) > 1:
        raise EvalFailure(f"GitHub returned duplicate {LABEL} definitions")
    return matches[0] if matches else None


def queue_snapshot(repository: str, pull_request_urls: Sequence[str]) -> dict[str, Any]:
    return {
        "label": repository_label(repository),
        "pull_requests": {
            url: pull_request(repository, url) for url in pull_request_urls
        },
    }


def record_mutations(
    before: Any, after: Any, *, pull_request_url: str, kind: str
) -> list[str]:
    if not isinstance(before, list) or not isinstance(after, list):
        raise EvalFailure(f"GitHub returned invalid {kind} evidence for {pull_request_url}")

    def records(items: list[Any]) -> dict[str, Any]:
        result: dict[str, Any] = {}
        for index, item in enumerate(items):
            if not isinstance(item, dict):
                raise EvalFailure(
                    f"GitHub returned invalid {kind} evidence for {pull_request_url}"
                )
            identity = item.get("id") or item.get("url") or f"position:{index}"
            identity = str(identity)
            if identity in result:
                raise EvalFailure(
                    f"GitHub returned duplicate {kind} evidence for {pull_request_url}"
                )
            result[identity] = item
        return result

    before_records = records(before)
    after_records = records(after)
    return [
        f"{pull_request_url} {kind} {identity} changed"
        for identity in sorted(before_records.keys() | after_records.keys())
        if before_records.get(identity) != after_records.get(identity)
    ]


def pull_request_mutations(
    before: Any, after: Any, pull_request_url: str
) -> list[str]:
    if not isinstance(before, dict) or not isinstance(after, dict):
        raise EvalFailure(f"GitHub returned invalid snapshot for {pull_request_url}")
    mutations: list[str] = []
    for field in ("state", "headRefOid", "baseRefName", "isDraft", "title", "body"):
        if before.get(field) != after.get(field):
            mutations.append(f"{pull_request_url} {field} changed")
    if before.get("state") == after.get("state") and before.get("mergedAt") != after.get(
        "mergedAt"
    ):
        mutations.append(f"{pull_request_url} mergedAt changed")
    if label_names(before) != label_names(after):
        mutations.append(f"{pull_request_url} labels changed")
    mutations.extend(
        record_mutations(
            before.get("comments", []),
            after.get("comments", []),
            pull_request_url=pull_request_url,
            kind="comment",
        )
    )
    mutations.extend(
        record_mutations(
            before.get("reviews", []),
            after.get("reviews", []),
            pull_request_url=pull_request_url,
            kind="review",
        )
    )
    return mutations


def assert_action_budget(
    before: Any, after: Any, max_actions: int
) -> list[str]:
    if max_actions <= 0:
        raise EvalFailure("action limit must be positive")
    if not isinstance(before, dict) or not isinstance(after, dict):
        raise EvalFailure("invalid GitHub mutation snapshots")
    before_pulls = before.get("pull_requests")
    after_pulls = after.get("pull_requests")
    if not isinstance(before_pulls, dict) or not isinstance(after_pulls, dict):
        raise EvalFailure("invalid pull request mutation snapshots")
    if before_pulls.keys() != after_pulls.keys():
        raise EvalFailure("pull request mutation snapshots cover different queues")
    mutations: list[str] = []
    if before.get("label") != after.get("label"):
        mutations.append(f"repository label {LABEL} changed")
    for url in before_pulls:
        mutations.extend(pull_request_mutations(before_pulls[url], after_pulls[url], url))
    if len(mutations) > max_actions:
        raise EvalFailure(
            f"Shepherd used {len(mutations)} actions, limit {max_actions}: "
            f"{'; '.join(mutations)}"
        )
    return mutations


def create_remote_branch(repository_path: Path, sha: str, branch: str) -> None:
    checked(
        command(
            ("git", "push", "origin", f"{sha}:refs/heads/{branch}"),
            cwd=repository_path,
        ),
        f"create {branch}",
    )


def create_pull_request(
    repository: str,
    repository_path: Path,
    base_sha: str,
    base_branch: str,
    branch: str,
    run_id: str,
    kind: str,
    *,
    draft: bool,
) -> tuple[str, str]:
    with tempfile.TemporaryDirectory(prefix="machinist-shepherd-eval-") as directory:
        worktree = Path(directory) / "checkout"
        checked(
            command(
                ("git", "worktree", "add", "--detach", str(worktree), base_sha),
                cwd=repository_path,
            ),
            f"create worktree for {branch}",
        )
        try:
            checked(
                command(("git", "switch", "-c", branch), cwd=worktree),
                f"create {branch}",
            )
            marker = f"machinist-shepherd-eval-{run_id}-{kind}.txt"
            (worktree / marker).write_text(
                f"Shepherd eval {run_id} {kind}\n", encoding="utf-8"
            )
            checked(command(("git", "add", marker), cwd=worktree), f"stage {marker}")
            checked(
                command(
                    (
                        "git",
                        "-c",
                        "user.name=Machinist Eval",
                        "-c",
                        "user.email=machinist-eval@example.invalid",
                        "commit",
                        "-m",
                        f"test: add Shepherd {kind} marker",
                    ),
                    cwd=worktree,
                ),
                f"commit {branch}",
            )
            head = checked(
                command(("git", "rev-parse", "HEAD"), cwd=worktree), "read head"
            ).strip()
            checked(
                command(
                    ("git", "push", "origin", f"HEAD:refs/heads/{branch}"),
                    cwd=worktree,
                ),
                f"push {branch}",
            )
        finally:
            checked(
                command(
                    ("git", "worktree", "remove", "--force", str(worktree)),
                    cwd=repository_path,
                ),
                f"remove worktree for {branch}",
            )
    checked(
        command(("git", "branch", "-D", branch), cwd=repository_path),
        f"remove local {branch}",
    )
    arguments = [
        "gh",
        "pr",
        "create",
        "--repo",
        repository,
        "--base",
        base_branch,
        "--head",
        branch,
        "--title",
        f"[shepherd-eval:{run_id}] {kind}",
        "--body",
        f"Disposable Shepherd {kind} candidate for {run_id}.",
    ]
    if draft:
        arguments.append("--draft")
    url = checked(command(arguments), f"create {kind} pull request").strip()
    return url, head


def run_shepherd(executable: str, options: argparse.Namespace, max_actions: int) -> int:
    arguments = [executable]
    if options.worker_config is not None:
        arguments.append(f"--config={options.worker_config.resolve()}")
    arguments.extend(
        (
            "run",
            "--agent=shepherd",
            f'--prompt=Run the scheduled Shepherd queue for repository "{options.repository}" '
            f"with max_actions={max_actions}. Perform at most {max_actions} mutating actions in this run.",
            f"--repo={options.repo_path}",
        )
    )
    if options.machinist_config is not None:
        arguments.append(f"--machinist-config={options.machinist_config.resolve()}")
    if options.model is not None:
        arguments.append(f"--model={options.model}")
    return command(arguments, cwd=options.repo_path, capture=False).returncode


def run_shepherd_with_budget(
    executable: str,
    options: argparse.Namespace,
    max_actions: int,
    pull_request_urls: Sequence[str],
) -> tuple[int, dict[str, Any], list[str]]:
    before = queue_snapshot(options.repository, pull_request_urls)
    status = run_shepherd(executable, options, max_actions)
    after = queue_snapshot(options.repository, pull_request_urls)
    mutations = assert_action_budget(before, after, max_actions)
    return status, after, mutations


def assert_actions_used(mutations: Sequence[str], expected: int, stage: str) -> None:
    if len(mutations) != expected:
        raise EvalFailure(
            f"{stage} used {len(mutations)} actions, expected {expected}: "
            f"{'; '.join(mutations) if mutations else 'none'}"
        )


def delete_remote_branch(
    repository: str, repository_path: Path, branch: str, run_id: str
) -> None:
    if not branch.startswith(f"codex/shepherd-eval-{run_id}-"):
        raise EvalFailure(f"refused to delete unexpected branch {branch!r}")
    result = command(
        ("git", "ls-remote", "--exit-code", "--heads", "origin", branch),
        cwd=repository_path,
    )
    if result.returncode == 2:
        return
    checked(result, f"inspect {branch}")
    checked(
        command(
            (
                "gh",
                "api",
                "--method",
                "DELETE",
                f"repos/{repository}/git/refs/heads/{branch}",
            )
        ),
        f"delete {branch}",
    )


def cleanup(
    repository: str,
    repository_path: Path,
    pull_requests: Sequence[str],
    branches: Sequence[str],
    run_id: str,
    label_created: bool,
) -> list[str]:
    errors: list[str] = []
    for url in pull_requests:
        result = command(("gh", "pr", "close", url, "--repo", repository))
        detail = ((result.stderr or "") + (result.stdout or "")).lower()
        already_terminal = "already closed" in detail or "already merged" in detail
        if result.returncode != 0 and not already_terminal:
            errors.append((result.stderr or f"could not close {url}").strip())
    for branch in branches:
        try:
            delete_remote_branch(repository, repository_path, branch, run_id)
        except EvalFailure as error:
            errors.append(str(error))
        local = command(
            ("git", "show-ref", "--verify", "--quiet", f"refs/heads/{branch}"),
            cwd=repository_path,
        )
        if local.returncode == 0:
            removed = command(("git", "branch", "-D", branch), cwd=repository_path)
            if removed.returncode != 0:
                errors.append((removed.stderr or f"could not delete local {branch}").strip())
        elif local.returncode != 1:
            errors.append((local.stderr or f"could not inspect local {branch}").strip())
    if label_created:
        result = command(("gh", "label", "delete", LABEL, "--yes", "--repo", repository))
        if result.returncode != 0:
            errors.append((result.stderr or f"could not delete {LABEL}").strip())
    return errors


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(
        description="Run Shepherd against an older blocked PR and an eligible PR."
    )
    result.add_argument("--repository", required=True, help="scratch repo as owner/name")
    result.add_argument("--confirm-disposable", required=True, help="repeat owner/name")
    result.add_argument("--repo-path", required=True, type=Path)
    result.add_argument("--machinist", default="machinist")
    result.add_argument("--worker-config", type=Path)
    result.add_argument("--machinist-config", type=Path)
    result.add_argument("--model")
    return result


def main(arguments: Sequence[str] | None = None) -> int:
    options = parser().parse_args(sys.argv[1:] if arguments is None else arguments)
    if options.confirm_disposable != options.repository:
        print(
            "FAIL shepherd-queue: --confirm-disposable must exactly match --repository",
            file=sys.stderr,
        )
        return 1
    run_id = f"{time.strftime('%Y%m%d%H%M%S', time.gmtime())}-{uuid.uuid4().hex[:8]}"
    prefix = f"codex/shepherd-eval-{run_id}"
    (
        base_branch,
        blocked_branch,
        eligible_branch,
        deferred_branch,
        parent_branch,
        child_branch,
    ) = (
        f"{prefix}-base",
        f"{prefix}-blocked",
        f"{prefix}-eligible",
        f"{prefix}-deferred",
        f"{prefix}-parent",
        f"{prefix}-child",
    )
    branches = (
        child_branch,
        parent_branch,
        blocked_branch,
        eligible_branch,
        deferred_branch,
        base_branch,
    )
    pull_requests: list[str] = []
    label_created = False
    failure: BaseException | None = None
    try:
        executable = validate(options.repository, options.repo_path, options.machinist)
        ensure_label_absent(options.repository)
        checked(
            command(("git", "fetch", "origin", "--prune"), cwd=options.repo_path),
            "fetch origin",
        )
        repository_data = gh_json(
            ("repo", "view", options.repository, "--json", "defaultBranchRef")
        )
        default_branch = repository_data.get("defaultBranchRef", {}).get("name")
        if not isinstance(default_branch, str) or not default_branch:
            raise EvalFailure("GitHub did not return the default branch")
        base_sha = checked(
            command(("git", "rev-parse", f"origin/{default_branch}"), cwd=options.repo_path),
            "resolve default branch",
        ).strip()
        for branch in branches:
            remote = command(
                ("git", "ls-remote", "--exit-code", "--heads", "origin", branch),
                cwd=options.repo_path,
            )
            if remote.returncode == 0:
                raise EvalFailure(f"eval branch already exists: {branch}")
            if remote.returncode != 2:
                checked(remote, f"inspect {branch}")
        create_remote_branch(options.repo_path, base_sha, base_branch)
        blocked_url, blocked_head = create_pull_request(
            options.repository,
            options.repo_path,
            base_sha,
            base_branch,
            blocked_branch,
            run_id,
            "blocked",
            draft=True,
        )
        pull_requests.append(blocked_url)
        eligible_url, eligible_head = create_pull_request(
            options.repository,
            options.repo_path,
            base_sha,
            base_branch,
            eligible_branch,
            run_id,
            "eligible",
            draft=False,
        )
        pull_requests.append(eligible_url)
        deferred_url, deferred_head = create_pull_request(
            options.repository,
            options.repo_path,
            base_sha,
            base_branch,
            deferred_branch,
            run_id,
            "deferred",
            draft=False,
        )
        pull_requests.append(deferred_url)
        bootstrap_status, bootstrap_snapshot, bootstrap_mutations = (
            run_shepherd_with_budget(executable, options, 1, pull_requests)
        )
        label_created = bootstrap_snapshot["label"] is not None
        if bootstrap_status != 0:
            raise EvalFailure("label-bootstrap Shepherd run failed")
        assert_actions_used(bootstrap_mutations, 1, "label-bootstrap Shepherd run")
        assert_label(bootstrap_snapshot["label"])
        bootstrap_pulls = bootstrap_snapshot["pull_requests"]
        assert_unlabelled_unchanged(
            bootstrap_pulls[blocked_url],
            head=blocked_head,
            base=base_branch,
            draft=True,
        )
        assert_unlabelled_unchanged(
            bootstrap_pulls[eligible_url],
            head=eligible_head,
            base=base_branch,
            draft=False,
        )
        assert_unlabelled_unchanged(
            bootstrap_pulls[deferred_url],
            head=deferred_head,
            base=base_branch,
            draft=False,
        )
        for url in pull_requests:
            checked(
                command(
                    (
                        "gh",
                        "pr",
                        "edit",
                        url,
                        "--repo",
                        options.repository,
                        "--add-label",
                        LABEL,
                    )
                ),
                f"opt in {url}",
            )
        queue_status, queue_snapshot_result, queue_mutations = run_shepherd_with_budget(
            executable, options, 2, pull_requests
        )
        if queue_status != 0:
            raise EvalFailure("queue Shepherd run failed")
        assert_actions_used(queue_mutations, 2, "queue Shepherd run")
        queue_pulls = queue_snapshot_result["pull_requests"]
        assert_queue_result(
            queue_pulls[blocked_url],
            queue_pulls[eligible_url],
            blocked_head,
            eligible_head,
            require_eligible_audit=False,
        )
        assert_deferred_result(
            queue_pulls[deferred_url],
            deferred_head,
            require_audit=False,
        )
        checked(
            command(("gh", "pr", "close", deferred_url, "--repo", options.repository)),
            "remove the independent deferred candidate from the stack scenario",
        )

        parent_url, parent_head = create_pull_request(
            options.repository,
            options.repo_path,
            base_sha,
            base_branch,
            parent_branch,
            run_id,
            "parent",
            draft=False,
        )
        pull_requests.append(parent_url)
        child_url, child_head = create_pull_request(
            options.repository,
            options.repo_path,
            parent_head,
            parent_branch,
            child_branch,
            run_id,
            "child",
            draft=False,
        )
        pull_requests.append(child_url)
        for url in (parent_url, child_url):
            checked(
                command(
                    (
                        "gh",
                        "pr",
                        "edit",
                        url,
                        "--repo",
                        options.repository,
                        "--add-label",
                        LABEL,
                    )
                ),
                f"opt in stacked pull request {url}",
            )

        transition_status, transition_snapshot, transition_mutations = (
            run_shepherd_with_budget(executable, options, 1, pull_requests)
        )
        if transition_status != 0:
            raise EvalFailure("stack transition Shepherd run failed")
        assert_actions_used(
            transition_mutations, 1, "stack transition Shepherd run"
        )
        transition_pulls = transition_snapshot["pull_requests"]
        assert_stack_transition(
            transition_pulls[parent_url],
            transition_pulls[child_url],
            parent_url=parent_url,
            parent_head=parent_head,
            parent_base=base_branch,
            child_head=child_head,
            child_base=parent_branch,
            state=PENDING_RETARGET,
            parent_state="OPEN",
        )

        parent_status, parent_snapshot, parent_mutations = run_shepherd_with_budget(
            executable, options, 1, pull_requests
        )
        if parent_status != 0:
            raise EvalFailure("stack parent Shepherd run failed")
        assert_actions_used(parent_mutations, 1, "stack parent Shepherd run")
        parent_pulls = parent_snapshot["pull_requests"]
        assert_stack_transition(
            parent_pulls[parent_url],
            parent_pulls[child_url],
            parent_url=parent_url,
            parent_head=parent_head,
            parent_base=base_branch,
            child_head=child_head,
            child_base=parent_branch,
            state=PENDING_RETARGET,
        )

        retarget_status, retarget_snapshot, retarget_mutations = (
            run_shepherd_with_budget(executable, options, 1, pull_requests)
        )
        if retarget_status != 0:
            raise EvalFailure("stack retarget Shepherd run failed")
        assert_actions_used(retarget_mutations, 1, "stack retarget Shepherd run")
        retarget_pulls = retarget_snapshot["pull_requests"]
        assert_stack_transition(
            retarget_pulls[parent_url],
            retarget_pulls[child_url],
            parent_url=parent_url,
            parent_head=parent_head,
            parent_base=base_branch,
            child_head=child_head,
            child_base=parent_branch,
            state=PENDING_RETARGET,
            child_current_base=base_branch,
        )

        completion_status, completion_snapshot, completion_mutations = (
            run_shepherd_with_budget(executable, options, 1, pull_requests)
        )
        if completion_status != 0:
            raise EvalFailure("stack transition completion Shepherd run failed")
        assert_actions_used(
            completion_mutations, 1, "stack transition completion Shepherd run"
        )
        completion_pulls = completion_snapshot["pull_requests"]
        assert_stack_transition(
            completion_pulls[parent_url],
            completion_pulls[child_url],
            parent_url=parent_url,
            parent_head=parent_head,
            parent_base=base_branch,
            child_head=child_head,
            child_base=parent_branch,
            state=RETARGETED,
        )

        review_status, review_snapshot, review_mutations = run_shepherd_with_budget(
            executable, options, 1, pull_requests
        )
        if review_status != 0:
            raise EvalFailure("stack child review Shepherd run failed")
        assert_actions_used(review_mutations, 1, "stack child review Shepherd run")
        review_pulls = review_snapshot["pull_requests"]
        assert_stack_transition(
            review_pulls[parent_url],
            review_pulls[child_url],
            parent_url=parent_url,
            parent_head=parent_head,
            parent_base=base_branch,
            child_head=child_head,
            child_base=parent_branch,
            state=RETARGETED,
        )
        assert_review_comment(review_pulls[child_url], child_head)

        child_status, child_snapshot, child_mutations = run_shepherd_with_budget(
            executable, options, 1, pull_requests
        )
        if child_status != 0:
            raise EvalFailure("stack child Shepherd run failed")
        assert_actions_used(child_mutations, 1, "stack child Shepherd run")
        child_pulls = child_snapshot["pull_requests"]
        assert_queue_result(
            child_pulls[blocked_url],
            child_pulls[child_url],
            blocked_head,
            child_head,
            require_eligible_audit=False,
        )
    except BaseException as error:
        failure = error
    cleanup_errors = cleanup(
        options.repository,
        options.repo_path,
        pull_requests,
        branches,
        run_id,
        label_created,
    )
    if failure is not None or cleanup_errors:
        details = [str(failure)] if failure is not None else []
        details.extend(cleanup_errors)
        print(f"FAIL shepherd-queue: {'; '.join(details)}", file=sys.stderr)
        return 1
    print(
        "PASS shepherd-queue: created label, audited older draft blocker, "
        "merged the eligible exact head within the full mutation budget, deferred the "
        "remaining candidate, and resumed a max_actions=1 stack through recorded parent "
        "merge, safe child retarget, transition completion, review, and merge"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
