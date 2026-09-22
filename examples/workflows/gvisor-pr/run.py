#!/usr/bin/env python3
"""Read a prompt from stdin and run one disposable gVisor PR worker."""

import argparse
import json
import os
import re
import signal
import subprocess
import sys
from uuid import uuid4


def launch(repo, task, image, timeout, model=None):
    for key in ("GH_TOKEN", "CODEX_API_KEY"):
        if not os.environ.get(key):
            raise ValueError(f"{key} must be set")
    runtimes = subprocess.check_output(
        ["docker", "info", "--format", "{{json .Runtimes}}"], text=True, timeout=20
    )
    if "runsc" not in json.loads(runtimes):
        raise ValueError("Docker runtime runsc is required; no fallback is allowed")

    name = "machinist-pr-" + uuid4().hex
    branch = "codex/task-" + uuid4().hex
    print(f"gvisor-pr: container={name} repo={repo} branch={branch}", flush=True)
    command = [
        "docker", "run", "--rm", "-i", "--init", "--runtime=runsc",
        "--name", name, "--label", "machinist.workflow=gvisor-pr",
        "--user", "1000:1000", "--cap-drop=ALL",
        "--security-opt=no-new-privileges:true", "--read-only",
        "--tmpfs", "/work:rw,exec,nosuid,nodev,size=4g,mode=1777",
        "--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=1g,mode=1777",
        "--cpus=2", "--memory=6g", "--pids-limit=512",
        "--env", "GH_TOKEN", "--env", "CODEX_API_KEY", image,
        "--repo", repo, "--branch", branch,
    ]
    if model:
        command.extend(["--model", model])

    def interrupted(signum, _frame):
        raise SystemExit(128 + signum)

    previous = {sig: signal.signal(sig, interrupted) for sig in (signal.SIGINT, signal.SIGTERM)}
    child = None
    try:
        # Own the Docker client separately so process-group cancellation reaches
        # this supervisor first and cleanup can explicitly remove the container.
        child = subprocess.Popen(command, stdin=subprocess.PIPE, text=True, start_new_session=True)
        child.communicate(task, timeout=timeout)
        return child.returncode
    except subprocess.TimeoutExpired:
        print(f"gvisor-pr: exceeded {timeout}s deadline", file=sys.stderr)
        return 124
    finally:
        for sig in previous:
            signal.signal(sig, signal.SIG_IGN)
        try:
            if child is not None and child.poll() is None:
                child.kill()
                child.wait(timeout=10)
            # --rm handles normal exit. This covers interrupted Docker clients.
            removed = subprocess.run(
                ["docker", "rm", "--force", name], capture_output=True, text=True, timeout=20
            )
            if removed.returncode and "No such container" not in removed.stderr:
                raise RuntimeError(f"cleanup failed for {name}: {removed.stderr.strip()}")
        finally:
            for sig, handler in previous.items():
                signal.signal(sig, handler)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", required=True, help="GitHub OWNER/REPO; main is the base")
    parser.add_argument("--image", default="machinist-gvisor-pr:local")
    parser.add_argument("--timeout", type=int, default=2700, help="whole container deadline in seconds")
    parser.add_argument("--model", help="Codex model ID")
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+", args.repo):
        parser.error("--repo must be a GitHub OWNER/REPO")
    if args.timeout <= 0:
        parser.error("--timeout must be positive")
    task = sys.stdin.read().strip()
    if not task:
        parser.error("a non-empty prompt is required on stdin")
    return launch(args.repo, task, args.image, args.timeout, args.model)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"gvisor-pr: {error}", file=sys.stderr)
        sys.exit(1)
