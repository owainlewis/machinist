#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.10"
# dependencies = ["openai-codex"]
# ///


import argparse
from pathlib import Path
import re
import shutil
from urllib.parse import urlparse

from openai_codex import Codex, CodexConfig, Sandbox
from openai_codex.types import TurnStatus

SANDBOX = Sandbox.full_access

PROMPT = """Implement task {task}.

Read the GitHub issue and its comments with gh, and follow the repository
instructions. Confirm the issue belongs to the current repository before editing.
Create an isolated worktree from the latest origin/main with a codex/ branch under
~/Code/.worktrees/<repo>/<task>. Reuse matching work for this issue if it exists.
Implement the requested change, run the relevant tests and linters, and obtain a
fresh read-only subagent review. Fix valid findings and recheck the final changes.
Make a Conventional Commit without an agent co-author, and open a PR linked to the
issue using gh. Wait for CI on the current PR commit using gh pr checks --watch,
with a 20-minute deadline per push. Diagnose and repair failures for at most three
rounds, rerunning checks and independent review before each push.
Never merge or force-push. Return the PR URL, check results and review outcome,
or a precise blocker if the task cannot be completed.
"""


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


def run_codex(prompt: str) -> str:
    codex_bin = shutil.which("codex")
    if not codex_bin:
        raise RuntimeError(
            "codex was not found on PATH; install the Codex CLI"
        )
    with Codex(CodexConfig(codex_bin=codex_bin)) as codex:
        thread = codex.thread_start(cwd=str(Path.cwd()), sandbox=SANDBOX)
        result = thread.run(prompt)
        if result.status != TurnStatus.completed:
            detail = result.error.message if result.error else result.status.value
            raise RuntimeError(f"coding agent did not complete: {detail}")
        return result.final_response or ""


def main(argv: list[str] | None = None) -> None:
    parser = argparse.ArgumentParser(description="Implement a GitHub issue with Codex.")
    parser.add_argument("task", type=issue_url, metavar="ISSUE_URL")
    args = parser.parse_args(argv)
    try:
        print(run_codex(PROMPT.format(task=args.task)))
    except (RuntimeError, OSError) as error:
        parser.exit(1, f"{parser.prog}: {error}\n")


if __name__ == "__main__":
    main()
