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


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Implement a GitHub issue with Codex.")
    parser.add_argument("task", type=issue_url, metavar="ISSUE_URL")
    args = parser.parse_args(argv)
    try:
        report = run_codex(PROMPT.format(task=args.task))
        exit_code = 0 if report["status"] == "completed" else 1
    except Exception as error:
        # SDK failures can happen before the agent returns a structured result.
        report = {"status": "failed", "pr_number": None, "summary": str(error) or type(error).__name__}
        exit_code = 1
    print(json.dumps(report))
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
