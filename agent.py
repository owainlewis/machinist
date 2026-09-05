#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.10"
# dependencies = ["openai-codex"]
# ///


import argparse
import json
from pathlib import Path
import re
import shutil
import subprocess
import sys
import time
from urllib.parse import urlparse

from openai_codex import Codex, CodexConfig, Sandbox
from openai_codex.types import TurnStatus

PROMPT = """Implement this GitHub issue: {task}.

1. Read the issue and comments with gh. Confirm it belongs to the current
repository and read the applicable repository instructions before editing.

2. Create an isolated worktree from the latest origin/main. Name the branch
task-<issue-number> (for example task-123 for issue #123), and put the worktree at
~/Code/.worktrees/<repo>/task-<issue-number>. Reuse matching work if it exists.

3. Implement the requested change, run the relevant tests and linters, and obtain a
fresh read-only subagent review. Fix valid findings, rerun affected checks, and
obtain independent approval of the final changes.

4. Make a Conventional Commit without an agent co-author, push the branch, and
create or update the PR linked to the issue using gh.

5. Wait for CI on the current PR commit using gh pr checks --watch, with a
20-minute deadline per push. Diagnose and repair failures for at most three
rounds, rerunning checks and independent review before each push. Missing or
pending checks are not a pass. On timeout or exhausted repairs, report blocked.

Never merge or force-push. Return status (completed, blocked, or failed), pr_number
(null if no PR exists), and a concise summary with the PR URL, checks and review
outcome, or the error and what remains. Report completed only when the PR exists
and verification passed. Retain the PR number if a later step fails.
"""

RESULT_SCHEMA = {
    "type": "object",
    "properties": {
        "status": {"type": "string", "enum": ["completed", "blocked", "failed"]},
        "pr_number": {"type": ["integer", "null"], "minimum": 1},
        "summary": {"type": "string", "minLength": 1},
    },
    "required": ["status", "pr_number", "summary"],
    "additionalProperties": False,
}


def issue_url(value: str) -> str:
    url = urlparse(value)
    if (
        url.scheme != "https"
        or url.netloc != "github.com"
        or not re.fullmatch(r"/[^/]+/[^/]+/issues/[1-9][0-9]*/?", url.path)
    ):
        raise argparse.ArgumentTypeError(
            "expected a GitHub issue URL: https://github.com/owner/repo/issues/123"
        )
    return value


def run_codex(prompt: str) -> dict:
    codex_bin = shutil.which("codex")
    if not codex_bin:
        raise RuntimeError("codex was not found on PATH; install the Codex CLI")
    with Codex(CodexConfig(codex_bin=codex_bin)) as codex:
        thread = codex.thread_start(cwd=str(Path.cwd()), sandbox=Sandbox.full_access)
        result = thread.run(prompt, output_schema=RESULT_SCHEMA)
        if result.status != TurnStatus.completed:
            detail = result.error.message if result.error else result.status.value
            raise RuntimeError(f"coding agent did not complete: {detail}")
        return json.loads(result.final_response or "")


def wait_for_ci_feedback(repo: str, pr_number: int, *, timeout: float = 1200,
                         interval: float = 30) -> dict:
    """Wait for visible CI checks, then collect available reviews (including history).

    An empty check list keeps waiting. Human reviews may still arrive after return.
    This only collects feedback; it does not run an agent or repair failures.
    """
    if timeout <= 0 or interval <= 0:
        raise ValueError("timeout and interval must be positive")
    deadline = time.monotonic() + timeout

    def gh(*args: str):
        result = subprocess.run(
            ["gh", *args], capture_output=True, text=True, timeout=60,
        )
        if result.returncode:
            raise RuntimeError(result.stderr.strip() or "gh command failed")
        return json.loads(result.stdout)

    while True:
        pr = gh("pr", "view", str(pr_number), "--repo", repo,
                "--json", "headRefOid,statusCheckRollup")
        checks = pr["statusCheckRollup"] or []
        finished = bool(checks) and all(
            c.get("status") == "COMPLETED" if "status" in c
            else c.get("state") in {"SUCCESS", "FAILURE", "ERROR"}
            for c in checks
        )
        remaining = deadline - time.monotonic()
        if finished or remaining <= 0:
            break
        time.sleep(min(interval, remaining))

    passed = finished and all(
        c.get("conclusion", c.get("state")) in {"SUCCESS", "NEUTRAL", "SKIPPED"}
        for c in checks
    )
    feedback = {
        "pr_number": pr_number,
        "head_sha": pr["headRefOid"],
        "ci_status": "passed" if passed else "failed" if finished else "timed_out",
        "checks": checks,
    }
    # Review bodies and inline comments are separate GitHub endpoints. Include all
    # pages and retain commit IDs so earlier feedback is not mistaken for new review.
    for key, endpoint in {
        "reviews": f"pulls/{pr_number}/reviews",
        "review_comments": f"pulls/{pr_number}/comments",
        "comments": f"issues/{pr_number}/comments",
    }.items():
        pages = gh("api", f"repos/{repo}/{endpoint}", "--paginate", "--slurp")
        feedback[key] = [item for page in pages for item in page]
    current = gh("pr", "view", str(pr_number), "--repo", repo, "--json", "headRefOid")
    if current["headRefOid"] != feedback["head_sha"]:
        raise RuntimeError("PR head changed while collecting feedback; run again")
    return feedback


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Implement a GitHub issue with Codex.")
    parser.add_argument("task", type=issue_url, metavar="ISSUE_URL")
    args = parser.parse_args(argv)
    report = {"status": "failed", "pr_number": None, "summary": ""}
    try:
        report = run_codex(PROMPT.format(task=args.task))
        if report["status"] == "completed" and report["pr_number"] is None:
            raise ValueError("agent reported completion without a PR number")
        if report["pr_number"] is not None:
            repo = "/".join(urlparse(args.task).path.split("/")[1:3])
            feedback = wait_for_ci_feedback(repo, report["pr_number"])
            print(json.dumps(feedback), file=sys.stderr)
            if feedback["ci_status"] != "passed" and report["status"] == "completed":
                report["status"] = "blocked"
                report["summary"] += f" CI feedback: {feedback['ci_status']}."
        exit_code = 0 if report["status"] == "completed" else 1
    except Exception as error:
        # SDK failures can happen before the agent returns a structured result.
        report = {"status": "failed", "pr_number": report["pr_number"],
                  "summary": str(error) or type(error).__name__}
        exit_code = 1
    print(json.dumps(report))
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
