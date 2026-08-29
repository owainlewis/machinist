"""Run Machinist's foreman and verify its GitHub issue-label lifecycle."""

from __future__ import annotations

import argparse
import json
import re
import shutil
import subprocess
import sys
import time
import uuid
from pathlib import Path
from typing import Any, Mapping, Sequence


EXPECTED_LABELS = (
    "machinist:planning",
    "machinist:building",
    "machinist:verifying",
    "machinist:ready-for-review",
)
MACHINIST_STATE_LABELS = frozenset(
    (*EXPECTED_LABELS, "machinist:needs-human", "machinist:blocked")
)
PR_MARKER = "<!-- machinist:foreman-pr -->"
REPOSITORY_NAME = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")
PR_URL = re.compile(r"https://github\.com/[^/\s]+/[^/\s]+/pull/\d+")


class EvalFailure(RuntimeError):
    pass


def command(
    arguments: Sequence[str],
    *,
    cwd: Path | None = None,
    input_text: str | None = None,
    capture: bool = True,
    env: Mapping[str, str] | None = None,
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        list(arguments),
        cwd=cwd,
        input=input_text,
        text=True,
        capture_output=capture,
        check=False,
        env=env,
    )


def checked(result: subprocess.CompletedProcess[str], action: str) -> str:
    if result.returncode == 0:
        return result.stdout or ""
    detail = (result.stderr or result.stdout or "no output").strip()
    raise EvalFailure(f"{action} failed with status {result.returncode}: {detail}")


def gh_json(
    arguments: Sequence[str],
    *,
    cwd: Path | None = None,
    env: Mapping[str, str] | None = None,
) -> Any:
    output = checked(
        command(("gh", *arguments), cwd=cwd, env=env), " ".join(arguments)
    )
    try:
        return json.loads(output)
    except json.JSONDecodeError as error:
        raise EvalFailure(f"GitHub returned invalid JSON: {error}") from error


def assert_label_lifecycle(
    final_labels: Sequence[str],
    label_events: Sequence[tuple[str, str]],
    machinist_exit_code: int,
) -> None:
    if machinist_exit_code != 0:
        raise EvalFailure(f"Machinist exited with status {machinist_exit_code}")
    states: list[str] = []
    active: set[str] = set()
    for action, label in label_events:
        if action == "unlabeled":
            active.discard(label)
            continue
        active.add(label)
        if len(active) != 1:
            raise EvalFailure(
                f"expected one active Machinist label, observed {sorted(active)}"
            )
        if not states or states[-1] != label:
            states.append(label)
    expected_start = list(EXPECTED_LABELS[:3])
    valid = states[:3] == expected_start
    position = 3
    while valid and states[position : position + 2] == [
        "machinist:building",
        "machinist:verifying",
    ]:
        position += 2
    valid = valid and states[position:] == ["machinist:ready-for-review"]
    if not valid:
        raise EvalFailure(
            "expected planning -> building -> verifying, optional repair cycles, "
            f"then ready-for-review; observed {states}"
        )
    observed_final = MACHINIST_STATE_LABELS.intersection(final_labels)
    if observed_final != {"machinist:ready-for-review"}:
        raise EvalFailure(
            "expected only machinist:ready-for-review at completion, "
            f"observed {sorted(observed_final)}"
        )
    if active != observed_final:
        raise EvalFailure(
            f"label events ended at {sorted(active)}, final issue has {sorted(observed_final)}"
        )


def assert_run_result(
    final_labels: Sequence[str],
    label_events: Sequence[tuple[str, str]],
    machinist_exit_code: int,
    pr_url: str | None,
) -> None:
    if pr_url is None:
        raise EvalFailure("Machinist did not leave an owned pull request for cleanup")
    assert_label_lifecycle(final_labels, label_events, machinist_exit_code)


def issue_number(issue_url: str) -> int:
    match = re.search(r"/issues/(\d+)$", issue_url)
    if match is None:
        raise EvalFailure(f"invalid issue URL returned by GitHub: {issue_url!r}")
    return int(match.group(1))


def pull_request_url(
    comments: Sequence[dict[str, Any]],
    events: Sequence[dict[str, Any]],
    repository: str,
) -> str | None:
    prefix = f"https://github.com/{repository}/pull/"
    for comment in reversed(comments):
        body = comment.get("body")
        match = PR_URL.search(body) if isinstance(body, str) and PR_MARKER in body else None
        if match is not None and match.group(0).startswith(prefix):
            return match.group(0)
    for event in reversed(events):
        source = event.get("source") if event.get("event") == "cross-referenced" else None
        source_issue = source.get("issue") if isinstance(source, dict) else None
        url = source_issue.get("html_url") if isinstance(source_issue, dict) else None
        if (
            isinstance(url, str)
            and isinstance(source_issue.get("pull_request"), dict)
            and url.startswith(prefix)
        ):
            return url
    return None


def worktree_for_branch(output: str, branch: str) -> Path | None:
    current: Path | None = None
    for line in output.splitlines():
        if line.startswith("worktree "):
            current = Path(line.removeprefix("worktree "))
        elif line == f"branch refs/heads/{branch}":
            return current
        elif not line:
            current = None
    return None


def owned_branch(pr: Any, run_id: str) -> str:
    branch = pr.get("headRefName") if isinstance(pr, dict) else None
    expected = f"codex/machinist-eval-{run_id}"
    if branch != expected or pr.get("isCrossRepository") is not False:
        raise EvalFailure(f"refused to remove unexpected eval branch {branch!r}")
    return branch


def ensure_branch_absent(repository_path: Path, branch: str) -> None:
    local = command(
        ("git", "show-ref", "--verify", "--quiet", f"refs/heads/{branch}"),
        cwd=repository_path,
    )
    if local.returncode == 0:
        raise EvalFailure(f"eval branch already exists locally: {branch}")
    if local.returncode != 1:
        checked(local, f"check local branch {branch}")
    remote = command(
        ("git", "ls-remote", "--exit-code", "--heads", "origin", branch),
        cwd=repository_path,
    )
    if remote.returncode == 0:
        raise EvalFailure(f"eval branch already exists remotely: {branch}")
    if remote.returncode != 2:
        checked(remote, f"check remote branch {branch}")


def create_issue(repository: str, run_id: str) -> str:
    filename = f"machinist-eval-{run_id}.md"
    branch = f"codex/machinist-eval-{run_id}"
    body = f"""## Outcome

Add `{filename}` at the repository root with exactly one line: `Machinist eval {run_id}`.

## Scope

Create only that file. Do not modify any existing file.

## Acceptance criteria

- `{filename}` exists at the repository root.
- Its only line is `Machinist eval {run_id}`.
- No other file changes.

## Verification

Read the file and inspect the complete diff.

## Implementation context

Use the exact branch name `{branch}`.

This disposable issue was created by Machinist eval `{run_id}`.
"""
    result = command(
        (
            "gh",
            "issue",
            "create",
            "--repo",
            repository,
            "--title",
            f"[machinist-eval:{run_id}] Add a temporary eval marker",
            "--body-file",
            "-",
        ),
        input_text=body,
    )
    url = checked(result, "create eval issue").strip()
    issue_number(url)
    return url


def capture_evidence(
    repository: str, issue_url: str
) -> tuple[tuple[str, ...], tuple[tuple[str, str], ...], str | None]:
    issue = gh_json(("issue", "view", issue_url, "--json", "labels,comments"))
    events = gh_json(
        (
            "api",
            f"repos/{repository}/issues/{issue_number(issue_url)}/events?per_page=100",
        )
    )
    if not isinstance(issue, dict) or not isinstance(events, list):
        raise EvalFailure("GitHub returned unexpected issue evidence")
    labels = tuple(
        label["name"]
        for label in issue.get("labels", ())
        if isinstance(label, dict) and isinstance(label.get("name"), str)
    )
    label_events = tuple(
        (event["event"], event["label"]["name"])
        for event in events
        if event.get("event") in ("labeled", "unlabeled")
        and isinstance(event.get("label"), dict)
        and isinstance(event["label"].get("name"), str)
        and event["label"]["name"] in MACHINIST_STATE_LABELS
    )
    comments = issue.get("comments")
    pr_url = pull_request_url(
        comments if isinstance(comments, list) else (), events, repository
    )
    return labels, label_events, pr_url


def cleanup(
    repository: str,
    repository_path: Path,
    issue_url: str,
    pr_url: str | None,
    run_id: str,
) -> list[str]:
    errors: list[str] = []
    branch = f"codex/machinist-eval-{run_id}"
    if pr_url is not None:
        try:
            pr = gh_json(
                ("pr", "view", pr_url, "--json", "headRefName,isCrossRepository")
            )
            owned_branch(pr, run_id)
            checked(command(("gh", "pr", "close", pr_url)), f"close {pr_url}")
        except EvalFailure as error:
            errors.append(str(error))
    try:
        remove_owned_branch(repository, repository_path, branch)
    except EvalFailure as error:
        errors.append(str(error))
    result = command(
        (
            "gh",
            "issue",
            "close",
            issue_url,
            "--comment",
            f"Machinist eval `{run_id}` finished and cleanup was attempted.",
        )
    )
    if result.returncode != 0:
        errors.append((result.stderr or f"could not close {issue_url}").strip())
    return errors


def remove_owned_branch(repository: str, repository_path: Path, branch: str) -> None:
    remote = command(
        ("git", "ls-remote", "--exit-code", "--heads", "origin", branch),
        cwd=repository_path,
    )
    if remote.returncode == 0:
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
            f"delete remote branch {branch}",
        )
    elif remote.returncode != 2:
        checked(remote, f"inspect remote branch {branch}")
    remove_local_worktree(repository_path, branch)


def remove_local_worktree(repository_path: Path, branch: str) -> None:
    worktrees = checked(
        command(("git", "worktree", "list", "--porcelain"), cwd=repository_path),
        "list Git worktrees",
    )
    path = worktree_for_branch(worktrees, branch)
    if path is not None:
        status = checked(
            command(("git", "status", "--porcelain"), cwd=path),
            f"inspect {path}",
        )
        if status.strip():
            raise EvalFailure(f"refused to remove dirty eval worktree {path}")
        checked(
            command(("git", "worktree", "remove", str(path)), cwd=repository_path),
            f"remove {path}",
        )
    result = command(("git", "branch", "-D", branch), cwd=repository_path)
    if result.returncode != 0 and "not found" not in (result.stderr or "").lower():
        raise EvalFailure((result.stderr or f"could not delete {branch}").strip())


def validate(repository: str, repository_path: Path, machinist: str) -> str:
    if REPOSITORY_NAME.fullmatch(repository) is None:
        raise EvalFailure("--repository must use owner/name format")
    if not repository_path.is_absolute() or not repository_path.is_dir():
        raise EvalFailure("--repo-path must be an existing absolute directory")
    executable = shutil.which(machinist)
    if executable is None:
        raise EvalFailure(f"Machinist executable was not found: {machinist}")
    checked(command(("gh", "auth", "status")), "GitHub authentication")
    local = gh_json(("repo", "view", "--json", "nameWithOwner"), cwd=repository_path)
    if not isinstance(local, dict) or local.get("nameWithOwner") != repository:
        raise EvalFailure(f"--repo-path is not a checkout of {repository}")
    status = checked(
        command(("git", "status", "--porcelain"), cwd=repository_path),
        "inspect repository status",
    )
    if status.strip():
        raise EvalFailure("--repo-path must be a clean checkout")
    return str(Path(executable).resolve())


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(
        description="Run Machinist's foreman and verify its issue-label lifecycle."
    )
    result.add_argument("--repository", required=True, help="scratch repo as owner/name")
    result.add_argument("--repo-path", required=True, type=Path)
    result.add_argument("--machinist", default="machinist")
    result.add_argument("--worker-config", type=Path)
    result.add_argument("--machinist-config", type=Path)
    result.add_argument("--model")
    return result


def main(arguments: Sequence[str] | None = None) -> int:
    options = parser().parse_args(sys.argv[1:] if arguments is None else arguments)
    issue_url: str | None = None
    pr_url: str | None = None
    run_id = f"{time.strftime('%Y%m%d%H%M%S', time.gmtime())}-{uuid.uuid4().hex[:8]}"
    failure: BaseException | None = None
    try:
        executable = validate(options.repository, options.repo_path, options.machinist)
        ensure_branch_absent(
            options.repo_path, f"codex/machinist-eval-{run_id}"
        )
        issue_url = create_issue(options.repository, run_id)
        machinist_command = [executable]
        if options.worker_config is not None:
            machinist_command.append(f"--config={options.worker_config.resolve()}")
        machinist_command.extend(
            (
                "run",
                "--command=foreman",
                f"--prompt=Complete {issue_url}",
                f"--repo={options.repo_path}",
            )
        )
        if options.machinist_config is not None:
            machinist_command.append(f"--machinist-config={options.machinist_config.resolve()}")
        if options.model is not None:
            machinist_command.append(f"--model={options.model}")
        machinist_result = command(machinist_command, cwd=options.repo_path, capture=False)
        final_labels, label_events, pr_url = capture_evidence(
            options.repository, issue_url
        )
        assert_run_result(
            final_labels, label_events, machinist_result.returncode, pr_url
        )
    except BaseException as error:
        failure = error
        if issue_url is not None and pr_url is None:
            try:
                _, _, pr_url = capture_evidence(options.repository, issue_url)
            except Exception:
                pass
    cleanup_errors = (
        cleanup(options.repository, options.repo_path, issue_url, pr_url, run_id)
        if issue_url is not None
        else []
    )
    if failure is not None or cleanup_errors:
        details = [str(failure)] if failure is not None else []
        details.extend(cleanup_errors)
        print(f"FAIL github-labels: {'; '.join(details)}", file=sys.stderr)
        return 1
    print(f"PASS github-labels: observed {' -> '.join(EXPECTED_LABELS)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
