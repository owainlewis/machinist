#!/usr/bin/env python3
"""Implement a task, open a pull request, then address review feedback in bounded rounds.

Machinist writes the task on standard input. The script owns git and GitHub
mechanics; the coding agent owns the code. Each round it waits for feedback on
the pull request, hands that feedback to the agent, and pushes the result.

Feedback is anything newer than the last push: unresolved review threads with a
new comment, reviews that request changes, or failing checks on the current head.
That makes the last push the only cursor, so the script keeps no state on disk.

Environment:
  MACHINIST_AGENT_COMMAND   agent command that reads the prompt on stdin
                            (default: "codex exec --json -")
  MACHINIST_MAX_ROUNDS      feedback rounds before giving up (default: 3)
  MACHINIST_FEEDBACK_WAIT   seconds to wait for feedback per round (default: 1800)
  MACHINIST_POLL_INTERVAL   seconds between GitHub polls (default: 60)
  MACHINIST_BASE_BRANCH     pull request base (default: repository default branch)
"""

from __future__ import annotations

import json
import os
import re
import shlex
import subprocess
import sys
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone

AGENT_COMMAND = shlex.split(os.environ.get("MACHINIST_AGENT_COMMAND", "codex exec --json -"))
MAX_ROUNDS = int(os.environ.get("MACHINIST_MAX_ROUNDS", "3"))
FEEDBACK_WAIT = int(os.environ.get("MACHINIST_FEEDBACK_WAIT", "1800"))
POLL_INTERVAL = int(os.environ.get("MACHINIST_POLL_INTERVAL", "60"))

IMPLEMENT_PROMPT = """You are working in a Git repository on branch {branch}.

Task:
{task}

Do this in order:
1. Implement the task. Keep the change focused.
2. Ask a fresh subagent to review the diff against the task for correctness,
   missing tests, and anything a careful reviewer would flag. Fix what it finds.
3. Run the repository's tests and linters. Fix failures.
4. Commit with a clear message. Do not push and do not open a pull request.

If the task cannot be completed, explain why and exit non-zero.
"""

FEEDBACK_PROMPT = """You are working in a Git repository on branch {branch}, which has an open
pull request: {url}

Original task:
{task}

Reviewers left feedback since your last push. Address every item below.
For each review thread, reply in the thread with what you changed, or explain
why no change is needed. Use `gh api` for replies; do not resolve threads.

{feedback}

Then run the tests, commit, and stop. Do not push.
"""


@dataclass
class Feedback:
    threads: list[dict] = field(default_factory=list)
    changes_requested: list[str] = field(default_factory=list)
    failing_checks: list[dict] = field(default_factory=list)
    approved: bool = False
    checks_pending: bool = False

    def actionable(self) -> bool:
        return bool(self.threads or self.changes_requested or self.failing_checks)


def log(message: str) -> None:
    print(f"pr-feedback-loop: {message}", flush=True)


def run(args: list[str], stdin: str | None = None, check: bool = True) -> str:
    result = subprocess.run(args, input=stdin, text=True, capture_output=True)
    if check and result.returncode != 0:
        raise RuntimeError(f"{' '.join(args)} failed: {result.stderr.strip()}")
    return result.stdout


def gh_json(args: list[str]) -> dict | list:
    return json.loads(run(["gh", *args]))


def agent(prompt: str) -> None:
    # The agent's own output streams straight to Machinist's event log.
    completed = subprocess.run(AGENT_COMMAND, input=prompt, text=True)
    if completed.returncode != 0:
        raise RuntimeError(f"agent exited with status {completed.returncode}")


def slugify(text: str) -> str:
    slug = re.sub(r"[^a-z0-9]+", "-", text.lower()).strip("-")
    return slug[:40] or "task"


def parse_time(value: str) -> datetime:
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def repository() -> str:
    return gh_json(["repo", "view", "--json", "nameWithOwner"])["nameWithOwner"]


def base_branch() -> str:
    return os.environ.get("MACHINIST_BASE_BRANCH") or gh_json(
        ["repo", "view", "--json", "defaultBranchRef"]
    )["defaultBranchRef"]["name"]


def current_login() -> str:
    return gh_json(["api", "user"])["login"]


def create_branch(task: str) -> str:
    branch = f"machinist/{slugify(task)}-{int(time.time())}"
    run(["git", "checkout", "-q", "-b", branch])
    return branch


def head_sha() -> str:
    return run(["git", "rev-parse", "HEAD"]).strip()


def push(branch: str) -> datetime:
    run(["git", "push", "-q", "-u", "origin", branch])
    return datetime.now(timezone.utc)


def open_pull_request(branch: str, base: str) -> str:
    output = run(["gh", "pr", "create", "--fill", "--base", base, "--head", branch])
    url = output.strip().splitlines()[-1]
    if not url.startswith("https://"):
        raise RuntimeError(f"could not find pull request URL in: {output}")
    return url


def has_new_commits(base: str) -> bool:
    return run(["git", "rev-list", "--count", f"origin/{base}..HEAD"]).strip() != "0"


def collect_feedback(repo: str, number: int, since: datetime, me: str) -> Feedback:
    owner, name = repo.split("/")
    query = """
    query($owner: String!, $name: String!, $number: Int!) {
      repository(owner: $owner, name: $name) {
        pullRequest(number: $number) {
          reviewThreads(last: 100) {
            nodes {
              isResolved isOutdated path line
              comments(first: 50) { nodes { author { login } body createdAt url } }
            }
          }
          reviews(last: 50) { nodes { author { login } state submittedAt } }
        }
      }
    }
    """
    data = gh_json([
        "api", "graphql", "-f", f"query={query}",
        "-F", f"owner={owner}", "-F", f"name={name}", "-F", f"number={number}",
    ])
    pull = data["data"]["repository"]["pullRequest"]
    feedback = Feedback()

    for thread in pull["reviewThreads"]["nodes"]:
        if thread["isResolved"]:
            continue
        comments = [
            c for c in thread["comments"]["nodes"]
            if c["author"]["login"] != me and parse_time(c["createdAt"]) > since
        ]
        if comments:
            feedback.threads.append({"path": thread["path"], "line": thread["line"], "comments": comments})

    latest: dict[str, dict] = {}
    for review in pull["reviews"]["nodes"]:
        login = review["author"]["login"]
        if login == me or review["state"] not in ("APPROVED", "CHANGES_REQUESTED"):
            continue
        if login not in latest or review["submittedAt"] > latest[login]["submittedAt"]:
            latest[login] = review
    for login, review in latest.items():
        if review["state"] == "CHANGES_REQUESTED" and parse_time(review["submittedAt"]) > since:
            feedback.changes_requested.append(login)
    feedback.approved = any(r["state"] == "APPROVED" for r in latest.values()) and not any(
        r["state"] == "CHANGES_REQUESTED" for r in latest.values()
    )

    for check in gh_json(["pr", "checks", str(number), "--json", "name,bucket,link"]):
        if check["bucket"] == "fail":
            feedback.failing_checks.append(check)
        elif check["bucket"] == "pending":
            feedback.checks_pending = True
    return feedback


def wait_for_feedback(repo: str, number: int, since: datetime, me: str) -> Feedback:
    deadline = time.monotonic() + FEEDBACK_WAIT
    while True:
        feedback = collect_feedback(repo, number, since, me)
        if feedback.actionable():
            return feedback
        if feedback.approved and not feedback.checks_pending:
            return feedback
        if time.monotonic() >= deadline:
            return feedback
        time.sleep(POLL_INTERVAL)


def describe(feedback: Feedback) -> str:
    lines: list[str] = []
    for check in feedback.failing_checks:
        lines.append(f"- Failing check: {check['name']} ({check['link']})")
    for login in feedback.changes_requested:
        lines.append(f"- {login} requested changes")
    for thread in feedback.threads:
        location = f"{thread['path']}:{thread['line']}" if thread["line"] else thread["path"]
        lines.append(f"- Review thread at {location} ({thread['comments'][0]['url']})")
        for comment in thread["comments"]:
            body = comment["body"].strip().replace("\n", "\n    ")
            lines.append(f"  {comment['author']['login']} wrote:\n    {body}")
    return "\n".join(lines)


def main() -> int:
    task = sys.stdin.read().strip()
    if not task:
        log("task is required on standard input")
        return 2

    repo = repository()
    base = base_branch()
    me = current_login()
    run(["git", "fetch", "-q", "origin", base])
    branch = create_branch(task)

    log(f"implement on {branch}")
    agent(IMPLEMENT_PROMPT.format(branch=branch, task=task))
    if not has_new_commits(base):
        log("agent produced no commits")
        return 1
    since = push(branch)
    url = open_pull_request(branch, base)
    number = int(url.rstrip("/").rsplit("/", 1)[-1])
    log(f"opened {url}")

    for round_number in range(1, MAX_ROUNDS + 1):
        log(f"wait for feedback: round {round_number}/{MAX_ROUNDS}")
        feedback = wait_for_feedback(repo, number, since, me)
        if not feedback.actionable():
            if feedback.approved:
                log("pull request approved with green checks")
            else:
                log("no new feedback; leaving the pull request for people")
            return 0
        log(f"address feedback: round {round_number}/{MAX_ROUNDS}")
        agent(FEEDBACK_PROMPT.format(branch=branch, url=url, task=task, feedback=describe(feedback)))
        if head_sha() == run(["git", "rev-parse", f"origin/{branch}"]).strip():
            log("agent made no changes; leaving the pull request for people")
            return 0
        since = push(branch)

    log(f"feedback still open after {MAX_ROUNDS} rounds")
    return 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except RuntimeError as error:
        log(str(error))
        sys.exit(1)
