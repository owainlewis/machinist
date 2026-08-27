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


def audit_fields(body: str) -> dict[str, str] | None:
    lines = body.splitlines()
    if AUDIT_MARKER not in (line.strip() for line in lines):
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
) -> None:
    if not isinstance(parent, dict) or not isinstance(child, dict):
        raise EvalFailure("GitHub returned invalid stack transition evidence")
    if (
        parent.get("state") != "MERGED"
        or parent.get("headRefOid") != parent_head
        or parent.get("baseRefName") != parent_base
    ):
        raise EvalFailure(f"stack parent was not merged at its expected comparison: {parent!r}")
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
            "state,isDraft,headRefOid,baseRefName,mergedAt,labels,comments,url",
        )
    )
    if not isinstance(result, dict):
        raise EvalFailure(f"GitHub returned invalid evidence for {url}")
    return result


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
        bootstrap_status = run_shepherd(executable, options, 1)
        label_created = command(
            ("gh", "label", "view", LABEL, "--repo", options.repository)
        ).returncode == 0
        if bootstrap_status != 0:
            raise EvalFailure("label-bootstrap Shepherd run failed")
        label = gh_json(
            (
                "label",
                "view",
                LABEL,
                "--repo",
                options.repository,
                "--json",
                "name,color,description",
            )
        )
        label_created = True
        assert_label(label)
        assert_unlabelled_unchanged(
            pull_request(options.repository, blocked_url),
            head=blocked_head,
            base=base_branch,
            draft=True,
        )
        assert_unlabelled_unchanged(
            pull_request(options.repository, eligible_url),
            head=eligible_head,
            base=base_branch,
            draft=False,
        )
        assert_unlabelled_unchanged(
            pull_request(options.repository, deferred_url),
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
        if run_shepherd(executable, options, 2) != 0:
            raise EvalFailure("queue Shepherd run failed")
        assert_queue_result(
            pull_request(options.repository, blocked_url),
            pull_request(options.repository, eligible_url),
            blocked_head,
            eligible_head,
            require_eligible_audit=False,
        )
        assert_deferred_result(
            pull_request(options.repository, deferred_url),
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

        if run_shepherd(executable, options, 1) != 0:
            raise EvalFailure("stack transition Shepherd run failed")
        if run_shepherd(executable, options, 1) != 0:
            raise EvalFailure("stack parent Shepherd run failed")
        assert_stack_transition(
            pull_request(options.repository, parent_url),
            pull_request(options.repository, child_url),
            parent_url=parent_url,
            parent_head=parent_head,
            parent_base=base_branch,
            child_head=child_head,
            child_base=parent_branch,
            state=PENDING_RETARGET,
        )

        if run_shepherd(executable, options, 1) != 0:
            raise EvalFailure("stack retarget Shepherd run failed")
        if run_shepherd(executable, options, 1) != 0:
            raise EvalFailure("stack transition completion Shepherd run failed")
        assert_stack_transition(
            pull_request(options.repository, parent_url),
            pull_request(options.repository, child_url),
            parent_url=parent_url,
            parent_head=parent_head,
            parent_base=base_branch,
            child_head=child_head,
            child_base=parent_branch,
            state=RETARGETED,
        )

        if run_shepherd(executable, options, 1) != 0:
            raise EvalFailure("stack child review Shepherd run failed")
        if run_shepherd(executable, options, 1) != 0:
            raise EvalFailure("stack child Shepherd run failed")
        assert_queue_result(
            pull_request(options.repository, blocked_url),
            pull_request(options.repository, child_url),
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
