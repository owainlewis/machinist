#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.10"
# dependencies = ["openai-codex==0.147.0"]
# ///
"""Create a worktree from main, implement and review a task, then open a PR.

Read the task from stdin. Run from the target repository with authenticated git,
gh and Codex. Keep the worktree for human review; do not wait for GitHub checks.
"""

from __future__ import annotations

import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
from uuid import uuid4

from openai_codex import Codex, CodexConfig, Sandbox
from openai_codex.types import TurnStatus


PROMPT = """Implement the following task in this worktree on branch {branch}.
The starting commit is {base_head}.

Task:
{task}

Read the repository instructions. Implement the smallest complete change, run
the relevant local tests and linters, and make a Conventional Commit without an
agent co-author. Ask a fresh read-only subagent to review the entire change from
the starting commit to HEAD against the task and check evidence. Fix valid
findings, rerun affected checks, commit, and get a fresh review of the final HEAD.
Allow at most three repair rounds, then report blocked if defects remain.

Stay on the supplied branch in this worktree. Python owns publishing: do not push,
open a PR, change GitHub, merge, or wait for remote checks, even if repository
delivery instructions normally include those steps.

Return JSON with verdict (approved or blocked), reviewed_head (the exact commit
approved by the subagent), a Conventional Commit PR title, and a short PR body
explaining what changed and why, exact local checks and results, and the review
outcome. Link the source issue when provided. If checks fail, a subagent is
unavailable, or work is incomplete, return blocked and explain why in body.
"""

RESULT_SCHEMA = {
    "type": "object",
    "properties": {
        "verdict": {"type": "string", "enum": ["approved", "blocked"]},
        **{key: {"type": "string"} for key in ("reviewed_head", "title", "body")},
    },
    "required": ["verdict", "reviewed_head", "title", "body"],
    "additionalProperties": False,
}


def log(message: str) -> None:
    print(f"flow: {message}", flush=True)


def run(args: list[str], cwd: Path) -> str:
    result = subprocess.run(args, cwd=cwd, text=True, capture_output=True)
    if result.returncode != 0:
        raise RuntimeError(f"{' '.join(args)} failed: {result.stderr.strip()}")
    return result.stdout.strip()


def flow(task: str) -> int:
    codex_bin = os.environ.get("FLOW_CODEX_BIN") or shutil.which("codex")
    if not codex_bin:
        raise RuntimeError("codex was not found on PATH; install Codex or set FLOW_CODEX_BIN")
    source = Path.cwd()
    remote = run(["git", "remote", "get-url", "origin"], source)
    repo = json.loads(run(["gh", "repo", "view", remote, "--json", "nameWithOwner"], source))["nameWithOwner"]
    run(["git", "fetch", "-q", "origin", "refs/heads/main"], source)
    base_head = run(["git", "rev-parse", "FETCH_HEAD"], source)
    slug = re.sub(r"[^a-z0-9]+", "-", task.lower()).strip("-")[:40] or "task"
    name = f"{slug}-{uuid4().hex[:8]}"
    branch = f"codex/{name}"
    root = Path(os.environ.get("FLOW_WORKTREE_ROOT", "~/Code/.worktrees")).expanduser().resolve()
    worktree = root / repo.split("/")[-1] / name
    worktree.parent.mkdir(parents=True, exist_ok=True)
    run(["git", "worktree", "add", "-b", branch, str(worktree), base_head], source)
    log(f"worktree: {worktree}")
    log(f"branch: {branch}; base: {base_head}")

    sandbox = Sandbox(os.environ.get("FLOW_SANDBOX", "full-access"))
    with Codex(CodexConfig(codex_bin=codex_bin)) as codex:
        thread = codex.thread_start(cwd=str(worktree), sandbox=sandbox)
        log(f"implement and review (thread {thread.id})")
        result = thread.run(
            PROMPT.format(branch=branch, base_head=base_head, task=task),
            sandbox=sandbox,
            output_schema=RESULT_SCHEMA,
        )
        if result.usage and (usage_path := os.environ.get("MACHINIST_TOKEN_USAGE_PATH")):
            total = result.usage.total
            Path(usage_path).write_text(str(total.input_tokens + total.output_tokens))
        if result.status != TurnStatus.completed:
            detail = result.error.message if result.error else result.status.value
            raise RuntimeError(f"coding agent did not complete: {detail}")
        report = json.loads(result.final_response or "{}")

    if report.get("verdict") != "approved":
        raise RuntimeError(f"agent blocked: {report.get('body', 'missing review result')}")
    head = run(["git", "rev-parse", "HEAD"], worktree)
    if run(["git", "branch", "--show-current"], worktree) != branch:
        raise RuntimeError("agent left the supplied branch")
    if run(["git", "status", "--porcelain"], worktree):
        raise RuntimeError("worktree has uncommitted changes")
    run(["git", "merge-base", "--is-ancestor", base_head, head], worktree)
    if not run(["git", "diff", "--name-only", base_head, head], worktree):
        raise RuntimeError("agent produced no changes")
    if report.get("reviewed_head") != head:
        raise RuntimeError("reviewed commit does not match HEAD")
    if not report.get("title", "").strip() or not report.get("body", "").strip():
        raise RuntimeError("agent omitted the PR title or body")

    run(["git", "push", "-q", "origin", f"{head}:refs/heads/{branch}"], worktree)
    # Pass the body as a file so multiline text reaches gh unchanged.
    with tempfile.NamedTemporaryFile(mode="w", suffix=".md") as body:
        body.write(report["body"])
        body.flush()
        url = run([
            "gh", "pr", "create", "--repo", repo, "--base", "main", "--head", branch,
            "--title", report["title"], "--body-file", body.name,
        ], worktree)
    if not url.startswith("https://"):
        raise RuntimeError(f"gh did not return a PR URL: {url}")
    log(f"opened {url}")
    return 0


def main() -> int:
    task = sys.stdin.read().strip()
    if not task:
        log("task is required on standard input")
        return 2
    return flow(task)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (RuntimeError, OSError, ValueError) as error:
        log(str(error))
        sys.exit(1)
