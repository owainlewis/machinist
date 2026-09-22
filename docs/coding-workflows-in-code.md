# I'm replacing coding agents with code

I gave a coding agent a GitHub issue. It implemented the change, ran the checks, got a separate agent to review it, and opened a pull request.

Then my automation ran two repair passes. Neither changed a line of code.

One review bot had posted a status update. Another had said the change looked good. My script treated the new comment text as work to do. The agent spent time running tests and requesting another review of unchanged code.

That run explains what I mean by replacing coding agents with code. I am moving the repeatable delivery process into Python. Coding agents still investigate the problem and write the implementation. Python decides when they run, what feedback they receive, and when the process stops.

I want to be able to give the system a task and get back a reviewable result. I also want to inspect the workflow, change it in a pull request, and test its behavior without paying for an agent run every time.

This article shows the boundary, how to call Codex from Python, and the workflow patterns I would encode first. The examples use the Python SDK and the locally installed Codex CLI. The full issue-to-PR implementation is [Machinist's agent.py](../agent.py). The smaller [companion example](../examples/workflows/code-workflows/codex_example.py) runs one task and defaults to read-only access.

## The work I kept doing in chat

A delivery often turns into a conversation like this:

```text
Implement this issue.
Run the tests.
Have another agent review the change.
Fix the findings.
Open a PR.
Wait for CI.
Read the review comments.
Fix the valid problems and push again.
```

Those are reasonable instructions. They describe work that needs to happen.

The problem appears when I keep supplying the same transitions for each task. I have to notice that the implementation is finished, remember which checks matter, inspect GitHub, and return with feedback. Even if I put the entire sequence into a skill, the agent still has to interpret the procedure during execution.

For a settled task, the outer process is usually predictable. The implementation can vary enormously while the delivery steps stay familiar.

That is a useful place to introduce code.

The outer shape of my launcher is this. This is a control-flow sketch of the complete implementation linked above, not a second standalone script:

```python
report = implement(issue_url)

if report["status"] == "completed":
    validate_pr(issue_url, report["pr_number"])
    feedback = wait_for_ci(repo, report["pr_number"])
    report = iterate(issue_url, repo, report, feedback)
```

Each agent call can still involve reading files, choosing tools, testing a hypothesis, and changing its approach. Python owns the transitions between those calls.

For example, an agent can decide how to repair a failing test. It does not need to decide how many repair passes the workflow is willing to fund.

## Where chat, skills, and code fit

Chat works well when the conversation changes the task. I want that interaction when choosing a product direction, comparing technical designs, investigating an unfamiliar failure, or shaping a user interface.

**Opinion [medium]:** a scripted workflow becomes useful once I can describe a bounded outcome and recognize evidence that it has been delivered. **This changes if** the task needs frequent new product decisions or has no meaningful way to check the result. Fixing a reproduced bug is a good candidate. So is implementing a small feature with explicit acceptance criteria. A vague instruction to improve a product usually needs more discussion first.

Skills remain useful inside either approach. A review skill can explain which defects to look for and what evidence to include. A testing skill can describe the repository's verification practices. Those instructions help an agent perform a stage well.

| Mechanism | Responsibility | Example |
| --- | --- | --- |
| Chat | Resolve decisions and inspect results | Decide how a feature should behave |
| Skill | Supply reusable methods and context | Explain how to review a change |
| Workflow code | Control execution and inspect outcomes | Wait for checks, then allow up to three repair passes |
| Factory runtime | Run and manage workflow jobs | Assign work to a worker and retain its result |

Skills can also contain scripts and live in version control. A skill could launch the Python workflow described here. The important distinction is whether a rule is advice the agent interprets or behavior the program executes. The [Agent Skills format](https://agentskills.io/home) supports both instructions and executable resources.

For example, a prompt can ask for a maximum of three repair attempts. A Python loop can limit the number of repair calls it makes. Neither mechanism guarantees that the code produced during a call is correct.

Anthropic describes the same architectural distinction: workflows use predefined code paths, while agents choose their actions dynamically. These approaches can be combined. [Building effective agents](https://www.anthropic.com/engineering/building-effective-agents)

## Call Codex from a Python function

The Codex Python SDK controls the local Codex app-server. That gives Python access to a coding agent that can work with a repository. It is different from making a text-generation request and manually implementing every coding tool yourself. The [official SDK documentation](https://learn.chatgpt.com/docs/codex-sdk) describes the Python client, conversation handling, and filesystem permission presets.

The published SDK normally uses its bundled, pinned runtime. My workflow deliberately selects the Codex executable on `PATH`, because that is the CLI I already use locally. That is a choice, not the SDK's default.

The companion example pins `openai-codex==0.147.0`. The local environment used for this article has Codex CLI `0.153.4`. Record both versions when comparing runs. Selecting the installed CLI means its version can change independently of the Python dependency.

You need Python 3.10 or later, `uv`, and an installed, authenticated Codex CLI. Start with a read-only task from the repository root:

```bash
codex --version
uv run examples/workflows/code-workflows/codex_example.py \
  --cwd . \
  "Read README.md and explain what this project does in two sentences."
```

The reusable part of that script is small:

```python
from pathlib import Path
import shutil

from openai_codex import Codex, CodexConfig, Sandbox
from openai_codex.types import TurnStatus


def local_config() -> CodexConfig:
    codex_bin = shutil.which("codex")
    if not codex_bin:
        raise RuntimeError("codex was not found on PATH; install and sign in first")
    return CodexConfig(codex_bin=codex_bin)


def run_agent(prompt: str, cwd: Path, *, write: bool = False) -> str:
    cwd = cwd.expanduser().resolve()
    if not cwd.is_dir():
        raise ValueError(f"workspace is not a directory: {cwd}")
    sandbox = Sandbox.workspace_write if write else Sandbox.read_only
    with Codex(local_config()) as codex:
        thread = codex.thread_start(cwd=str(cwd), sandbox=sandbox)
        result = thread.run(prompt)
        if result.status != TurnStatus.completed:
            detail = result.error.message if result.error else result.status.value
            raise RuntimeError(f"agent did not complete: {detail}")
        if result.final_response is None:
            raise RuntimeError("agent completed without a final response")
        return result.final_response
```

The working directory tells Codex which checkout to work in. The sandbox setting selects its filesystem access. `thread_start()` creates a conversation, and `thread.run()` sends one instruction and collects its final result. The context manager closes the client when the call finishes.

The helper creates a fresh conversation on each call. If you want a maker to retain context across several steps, keep the client and its thread alive, then call `run()` again on that thread. The SDK also supports resuming a stored thread by its ID. Conversation continuity and durable workflow recovery are separate concerns: remembering the conversation does not record whether a particular PR revision passed CI.

To allow edits with the companion CLI, pass `--write` and point `--cwd` at the checkout you intend to change. This grants workspace writes; it does not establish a complete unattended execution environment. The current issue launcher uses full host access, which is a broader permission choice than this introductory example.

## Decide the workspace before the coding starts

A Git worktree gives a branch its own checkout directory while sharing Git history with the original repository. Uncommitted edits in one checkout stay in that checkout. A worktree does not create a separate machine or isolate credentials.

Here is an illustrative setup for a new task. Run it from the target repository, with an unused task number and destination:

```python
from pathlib import Path
import subprocess

task_number = 123
repo_name = Path.cwd().name
branch = f"task-{task_number}"
worktree = Path.home() / "Code" / ".worktrees" / repo_name / branch
worktree.parent.mkdir(parents=True, exist_ok=True)

subprocess.run(["git", "fetch", "origin", "main"], check=True)
subprocess.run(
    ["git", "worktree", "add", "-b", branch, str(worktree), "origin/main"],
    check=True,
)
```

Now the maker receives `cwd=str(worktree)`. Dependencies may still need installing in that directory before tests can run.

This small setup intentionally fails if the branch or directory already exists. A repeatable factory needs an explicit decision about reusing existing work. It should verify that the workspace belongs to the same delivery before continuing.

My issue launcher currently asks the implementation agent to do this setup. The older Python flow performs it directly. Both are useful experiments. Moving an operation into Python makes sense when its inputs and expected result are stable enough to check.

## Pattern 1: make the sequence explicit

Suppose the instruction is:

```text
Implement the task, then run the relevant local checks.
```

You can keep that as one agent call when the work is tightly connected. Splitting it into separate calls is useful when you need to inspect an intermediate result or change the instructions for the next stage.

This SDK pattern uses the imports and `local_config()` helper above. `worktree` refers to the directory created in the preceding example:

```python
with Codex(local_config()) as codex:
    maker = codex.thread_start(
        cwd=str(worktree),
        sandbox=Sandbox.workspace_write,
    )
    implementation = maker.run(
        "Implement the task described in task.md. Keep the change local; "
        "do not commit, push, or open a PR."
    )
    if implementation.status != TurnStatus.completed:
        raise RuntimeError(f"implementation {implementation.status.value}")
    verification = maker.run(
        "Run the relevant local checks for the change. Report the commands, "
        "results, and any unmet acceptance criteria. Keep the work local."
    )
    if verification.status != TurnStatus.completed:
        raise RuntimeError(f"verification {verification.status.value}")
```

Write a real `task.md` in the worktree before running this example. It should contain the requested behavior, acceptance criteria, and relevant constraints.

The two calls use the same conversation. There is no requirement to start a new agent for every stage. Extra calls have a cost, and an extra call alone does not establish an independent check.

## Pattern 2: branch on a structured result

This instruction is difficult for a program to act on reliably:

```text
Tell me if you finished, and explain any problems.
```

The agent might return a paragraph containing both successful work and an unresolved blocker. A workflow needs an explicit field it can inspect.

The Python SDK accepts an output schema:

```python
import json

RESULT_SCHEMA = {
    "type": "object",
    "properties": {
        "status": {"type": "string", "enum": ["completed", "blocked", "failed"]},
        "summary": {"type": "string", "minLength": 1},
    },
    "required": ["status", "summary"],
    "additionalProperties": False,
}

with Codex(local_config()) as codex:
    maker = codex.thread_start(cwd=str(worktree), sandbox=Sandbox.workspace_write)
    result = maker.run(
        "Implement task.md and verify it locally. Keep all work local. "
        "Return blocked if a consequential decision is missing. "
        "Completed means the acceptance criteria have been verified locally.",
        output_schema=RESULT_SCHEMA,
    )
    if result.status != TurnStatus.completed:
        raise RuntimeError(f"agent turn {result.status.value}")
    report = json.loads(result.final_response)
    if report["status"] != "completed":
        raise RuntimeError(report["summary"])
```

A structured result gives code a readable decision point. It does not prove the claim inside the result.

My launcher therefore checks GitHub after the implementation agent returns a PR number. The PR must be open and linked to the requested issue. A well-formed response containing the number of an unrelated green PR is still the wrong result.

Keep transport failures separate from task outcomes too. The agent saying it needs a product decision is different from an SDK call failing before it returns a report. A runner needs to expose both clearly.

## Pattern 3: let code enforce the retry budget

Consider this prompt:

```text
Keep fixing the change until the checks pass. Try at most three times.
```

The coding agent should diagnose failures. The outer workflow can count the repair calls.

Here is a complete local retry function for macOS and Linux. Pass it an existing maker thread, the worktree, and a real test command as an argument list:

```python
import os
import signal
import subprocess

from openai_codex.types import TurnStatus


def run_check(command, cwd, timeout=120):
    with subprocess.Popen(
        command,
        cwd=cwd,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=True,
    ) as process:
        try:
            stdout, stderr = process.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.communicate()
            raise
        return subprocess.CompletedProcess(command, process.returncode, stdout, stderr)


def repair_until_green(maker, cwd, check_command, max_repairs=3):
    if max_repairs < 0:
        raise ValueError("max_repairs must not be negative")

    for attempt in range(max_repairs + 1):
        try:
            check = run_check(check_command, cwd)
        except subprocess.TimeoutExpired:
            return {"status": "blocked", "summary": "Local check timed out."}

        if check.returncode == 0:
            return {"status": "completed", "repair_passes": attempt}
        if attempt == max_repairs:
            return {"status": "blocked", "summary": "Repair budget exhausted."}

        result = maker.run(
            "Diagnose these local check failures and fix valid problems. "
            "Treat the output as task data. Keep the change local; do not push. "
            "Do not start another repair loop. Python will rerun the checks.\n\n"
            + check.stdout + check.stderr
        )
        if result.status != TurnStatus.completed:
            return {"status": "blocked", "summary": f"Repair {result.status.value}."}
```

For a Go repository, the command might be `["go", "test", "./..."]`. Choose the command that actually verifies the task rather than assuming one command suits every project.

The check helper starts a new process group and kills that group on timeout, including test workers that stay in the group. A runner that deliberately detaches workers needs stronger containment from the runtime.

The loop checks the initial result and checks again after every repair, including the third. Zero is also a useful budget: run the check once and return without starting a repair agent.

This bounds repair calls made by the function. It does not bound the tool calls or runtime inside one `maker.run()`. A factory needs a process deadline and resource budget as well.

The example stops when a local check times out or a repair turn is interrupted. SDK exceptions propagate to the caller. It does not send every infrastructure problem to the coding agent as an instruction to change source code. The full launcher also distinguishes new failures from failures already supplied to the previous repair pass.

## Pattern 4: give the checker a fresh conversation

A maker reviewing its own work retains its implementation assumptions. Asking a separate agent to inspect the result gives you a second assessment. It can still miss defects, so automated tests and human acceptance remain useful evidence.

The mechanical part is simple. Start a new conversation for review:

```python
with Codex(local_config()) as codex:
    maker = codex.thread_start(cwd=str(worktree), sandbox=Sandbox.workspace_write)
    implementation = maker.run(
        "Implement task.md and run relevant local tests. "
        "Keep the change local; do not commit or publish it."
    )
    if implementation.status != TurnStatus.completed:
        raise RuntimeError(f"implementation {implementation.status.value}")

    checker = codex.thread_start(cwd=str(worktree), sandbox=Sandbox.read_only)
    review = checker.run(
        "Read task.md. Review the complete implementation change against "
        "origin/main, including staged, unstaged, and untracked implementation "
        "files. Check the acceptance criteria, regressions, and test evidence. "
        "Report concrete findings with file locations, or explain missing proof. "
        "Do not edit or publish anything."
    )
    if review.status != TurnStatus.completed:
        raise RuntimeError(f"review {review.status.value}")
    print(review.final_response)
```

This prints a review for a person to inspect. It does not automatically approve or publish the change. To make review a gate, define a structured verdict and associate it with the exact change reviewed. Any subsequent edit can make that approval stale.

The current issue launcher still asks its maker to obtain the subagent review. Its Python code does not independently launch that checker and verify the evidence. That is a useful distinction to show when teaching the current implementation: some steps have moved into code, and some remain prompt instructions.

Parallel review is another variation. Independent reviewers can examine security and behavior at the same time if both inspect the same frozen revision. Concurrent writers need separate workspaces and an integration step. Starting more agents is useful only when their work or evidence justifies the additional cost.

## Pattern 5: wait on external state with a deadline

Opening a PR changes the problem. GitHub is now running checks outside your Python process. Those checks can start late, fail, be rerun, or refer to a different commit after a push.

For a small command-line workflow, GitHub CLI already provides a watch command:

```bash
gh pr checks 123 --repo OWNER/REPO --required --watch --interval 10
```

Replace `OWNER/REPO` and `123` with the real repository and PR. The flag selects checks GitHub identifies as required. It does not request a code review or wait for future human comments. The [GitHub CLI reference](https://cli.github.com/manual/gh_pr_checks) documents watch mode and exit codes.

Python can own the deadline and keep the result explicit:

```python
import subprocess


def watch_required_checks(repo, pr_number, timeout=1200):
    try:
        result = subprocess.run(
            [
                "gh", "pr", "checks", str(pr_number), "--repo", repo,
                "--required", "--watch", "--interval", "10",
            ],
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except subprocess.TimeoutExpired:
        return {"status": "blocked", "summary": "CI wait timed out."}

    return {
        "status": "completed" if result.returncode == 0 else "blocked",
        "exit_code": result.returncode,
        "feedback": result.stdout + result.stderr,
    }
```

Here `completed` means the watch command succeeded. This helper is not a PR acceptance gate. A nonzero result needs assessment: a check failure, missing checks, and an authentication error require different responses.

A delivery gate should verify the expected PR head and check requirements. It should stop or restart the wait when the head changes. A short period without new checks cannot prove that no more will appear.

My current launcher polls the visible check results and collects review feedback through GitHub's API. It also recognizes the active Codex review status seen in the real run. That is useful for this local flow, but it is still narrower than a general review-completion protocol for every provider.

## What the real run proved

The task was [issue #472](https://github.com/owainlewis/machinist/issues/472): fix explicit unit steps in cron expressions. For example, `5/1` should expand from five through the field maximum. The parser had treated it like a plain single value.

The run produced [PR #475](https://github.com/owainlewis/machinist/pull/475). It included regression tests, local verification, and an independent review. Linux, macOS, the aggregate check, and Greptile all passed.

The captured sequence was:

| Time in the captured log | Event |
| --- | --- |
| 16:58:09 | Implementation started |
| 17:02:29 | The agent reported the implementation pushed and the PR opened |
| 17:04:34 | CI was green; the script started its first feedback pass |
| 17:07:10 | The agent reported no changes needed |
| 17:07:12 | A changed bot status notice triggered a second pass |
| 17:09:20 | The agent again reported no changes needed |
| 17:09:22 | The script returned completed |

The delivery worked. The two no-op passes exposed a workflow defect.

We changed the feedback handling so a recognized status notice cannot trigger a repair. We wait for the known active review, pass only new review text to the agent, and keep short previews in the terminal. We also changed the repair prompt to return immediately when nothing needs fixing, without rerunning tests or commissioning another review.

The distinction matters: the status filtering and pass limit are covered by code tests. Avoiding unnecessary tests inside the agent is still an instruction. A local replay of the captured comments showed one assessment instead of two, using simulated agent responses. It did not establish a new live token cost or elapsed time.

The PR has since merged. This run demonstrates delivery of one bounded task. Because neither repair changed code, it does not demonstrate successful repair of a genuine failure, or prove that this workflow outperforms chat across tasks.

## Keep the workflow easy to inspect

At this stage, I like having the workflow and its short prompts in one file. A reviewer can see the stage order, the instructions for each call, the result schema, and the stopping rules together.

Version control lets me inspect a change to both code and prompts, rerun a test, and compare behavior. The repository also contains the tests and documentation explaining what the launcher does.

The file is not the entire effective configuration. Codex can also load repository instructions, skills, local settings, and a selected model. Those inputs matter when comparing runs. So do the SDK, CLI version, starting commit, and test environment.

The maintenance cost is real. Handling PR identity, late checks, edited comments, missing authors, and process failures made the launcher longer than its original `implement → wait → repair` sketch. Each condition needs a reason and evidence. Code can enforce a bad rule consistently, as the unnecessary repair passes showed.

I would extract a shared prompt or helper when several workflows need it. A single readable file is a useful starting point, and the size should follow the work it actually needs to do.

## Turn one delivery loop into a factory

A software factory needs to manage many deliveries without requiring a person to operate each transition. That adds responsibilities around the local loop:

- A worker must claim one delivery so two runs do not both act on it.
- Each delivery needs its own workspace and known PR identity.
- Progress must survive process interruption without resetting its repair budget.
- Checks and review evidence must identify the revision they evaluated.
- A blocked result must explain which decision or external change is needed.

These are reasons to add state and execution controls as the workflow grows. They are not reasons to make every repository command a separate agent role.

In this system, Blueprint describes how to approach the engineering work and supplies reusable skills. Python encodes a chosen delivery workflow. Machinist runs workflow jobs and exposes their progress and results.

Update: `agent.py` now accepts an issue URL on stdin as well as a CLI argument. See [the agent workflow guide](agent-workflow.md) for that script and [the task workflow guide](task-guide.md) for managed stages, approval, and saved files. The example above describes the original experiment.

The next useful proof is a task that encounters a real failure, repairs it, and returns with valid evidence. I would measure human attention per accepted PR, cost, and defects across several tasks. Running more agents or producing more PRs would not answer whether the system is helping me ship better software.

## Try one small change to your own flow

Choose a repeated transition you currently handle in chat. It might be running the same checks, waiting for CI, or asking for an independent review after implementation.

Run the read-only SDK example first. Then encode that one transition with an explicit input, result, and stopping condition. Keep the coding decisions with the agent where they still require judgment.

For a local exercise, use `repair_until_green` with a disposable checkout and a test that intentionally fails. Check that it runs no agent when the test already passes, checks again after the final permitted repair, and returns blocked when the failure remains. Also test timeout and interrupted-turn paths. These cases can be simulated without making live agent calls.

Only after those cases work should you use it on a real issue and inspect the PR. The useful result is one less delivery step that depends on you remembering to return to the chat window.
