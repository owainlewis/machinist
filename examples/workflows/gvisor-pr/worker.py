#!/usr/bin/env python3
"""Container-only worker: clone main, implement, commit, push, open a PR."""

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys


SCHEMA = {
    "type": "object",
    "properties": {
        "status": {"type": "string", "enum": ["complete", "blocked"]},
        "title": {"type": "string"},
        "body": {"type": "string"},
    },
    "required": ["status", "title", "body"],
    "additionalProperties": False,
}


def run(args, cwd=None, **kwargs):
    return subprocess.run(args, cwd=cwd, check=True, text=True, **kwargs)


def output(args, cwd):
    return run(args, cwd, stdout=subprocess.PIPE).stdout.strip()


def work(repo, branch, task, root=Path("/work"), model=None):
    checkout = root / "repo"
    Path(os.environ["HOME"]).mkdir(parents=True, exist_ok=True)
    Path(os.environ.get("CODEX_HOME", str(Path(os.environ["HOME"]) / ".codex"))).mkdir(parents=True, exist_ok=True)
    run(["gh", "auth", "setup-git", "--hostname", "github.com"])
    remote = f"https://github.com/{repo}.git"
    run(["git", "clone", "--single-branch", "--branch", "main", "--", remote, str(checkout)])
    run(["git", "switch", "--create", branch], checkout)
    run(["git", "config", "user.name", "Machinist"], checkout)
    run(["git", "config", "user.email", "machinist@users.noreply.github.com"], checkout)
    base = output(["git", "rev-parse", "HEAD"], checkout)
    print(f"gvisor-pr: base={base} branch={branch}", flush=True)

    schema = root / "schema.json"
    report_file = root / "report.json"
    schema.write_text(json.dumps(SCHEMA))
    prompt = f"""Implement this task in the current checkout on branch {branch}.

{task}

Read the repository instructions. Make the smallest complete change and run the
relevant tests and linters. Fix failures caused by the change. If work is incomplete,
checks fail, or a required decision is missing, report status blocked and explain.
Leave the changes uncommitted on the supplied branch. The outer script handles
publishing: do not commit, push, open PRs, merge, or change GitHub settings.
Return status complete only when ready for human review. Include a Conventional
Commit title and a PR body explaining the problem, the change, exact validation
commands/results, and any limitations. Link the source issue when provided.
"""
    command = [
        "codex", "exec", "--json", "--sandbox", "danger-full-access",
        "--output-schema", str(schema), "--output-last-message", str(report_file),
    ]
    if model:
        command.extend(["--model", model])
    run(command + ["-"], checkout, input=prompt)
    report = json.loads(report_file.read_text())
    if report.get("status") != "complete":
        raise RuntimeError(f"agent blocked: {report.get('body', 'no completion report')}")
    title, body = report.get("title"), report.get("body")
    if not isinstance(title, str) or not title.strip() or "\n" in title or len(title) > 200:
        raise RuntimeError("invalid PR title")
    if not isinstance(body, str) or not body.strip():
        raise RuntimeError("missing PR body")
    if output(["git", "branch", "--show-current"], checkout) != branch:
        raise RuntimeError("agent left the supplied branch")
    if output(["git", "rev-parse", "HEAD"], checkout) != base:
        raise RuntimeError("agent changed HEAD; expected uncommitted changes")
    if output(["git", "remote", "get-url", "origin"], checkout) != remote:
        raise RuntimeError("agent changed origin")
    run(["git", "add", "--all"], checkout)
    if not output(["git", "diff", "--cached", "--name-only"], checkout):
        raise RuntimeError("agent produced no changes; no PR created")
    run(["git", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false",
         "commit", "--message", title], checkout)
    head = output(["git", "rev-parse", "HEAD"], checkout)
    # Explicit URL/refspec prevents an altered push URL or push.default from
    # redirecting publication. No force push and no changes to main.
    run(["git", "-c", "core.hooksPath=/dev/null", "push", remote, f"{head}:refs/heads/{branch}"], checkout)
    print(f"gvisor-pr: pushed {branch}; creating PR", flush=True)
    body_file = root / "pr-body.md"
    body_file.write_text(body)
    url = output([
        "gh", "pr", "create", "--repo", repo, "--base", "main", "--head", branch,
        "--title", title, "--body-file", str(body_file),
    ], checkout)
    if not url.startswith(f"https://github.com/{repo}/pull/"):
        raise RuntimeError(f"unexpected PR result: {url}")
    print(f"gvisor-pr: ready for human review: {url}", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--branch", required=True)
    parser.add_argument("--model")
    args = parser.parse_args()
    task = sys.stdin.read().strip()
    if not task:
        parser.error("prompt is required")
    work(args.repo, args.branch, task, model=args.model)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"gvisor-pr: {error}", file=sys.stderr)
        sys.exit(1)
