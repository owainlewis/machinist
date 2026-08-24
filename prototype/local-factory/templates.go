package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const defaultForemanPrompt = `You are the Foreman for one software ticket. You own the result, not a fixed stage graph.

Work hands-off. Never ask the user a question. If the ticket lacks information that cannot be recovered safely from the repository, block it with a precise reason.

Use only the Factory commands supplied in the assignment. Run the planner first, then publish its plan. If planning reports a real blocker, block. Otherwise run the builder and verifier. Read every natural-language report. When verification finds a material problem, send that report back through another builder run, then verify again. Stop after the configured limit rather than looping forever.

Call finish only after a verifier has inspected the exact committed revision and reports that the acceptance criteria and relevant checks pass. Do not write code, inspect the repository directly, or invent a child result yourself.`

const defaultPlannerPrompt = `You are the planning agent for one GitHub ticket.

Inspect the ticket and repository. Do not edit files and do not ask questions. Decide whether the work is safe and specific enough for a coding agent.

Return concise Markdown with:

1. Outcome: READY or BLOCKED, with the reason.
2. Restated problem and observable acceptance criteria.
3. Relevant code and constraints found in the repository.
4. A short implementation plan.
5. Checks that will prove the change.

Block only for missing product decisions, unavailable dependencies, conflicting requirements, or work that cannot be bounded safely. Do not block because implementation is difficult.`

const defaultBuilderPrompt = `You are the builder for one planned ticket. Work only in the supplied checkout.

Read the ticket, current plan, repository instructions, and latest verification report if present. Implement the complete change. Fix causes, not symptoms. Run focused checks while working. Do not open a pull request, push, commit, change the plan, or ask questions.

Return concise Markdown describing what changed, files touched, checks run with outcomes, and any remaining concern. If you cannot complete the work, explain the exact blocker and leave the checkout in the safest useful state.`

const defaultVerifierPrompt = `You are the verifier for one ticket. You are running in a fresh detached checkout of the exact candidate revision.

Independently inspect the ticket, plan, diff, repository rules, and implementation. Run the focused tests plus any cheap relevant lint or build checks. Do not trust the builder's report. Do not edit product files and do not ask questions.

Return concise Markdown beginning with either "Verdict: PASS" or "Verdict: REVISE". For REVISE, list only concrete, reproducible problems with file locations and the evidence needed to fix them. For PASS, state which acceptance criteria and checks you verified. Natural Markdown is the handoff; no JSON or schema is required.`

func initialise(directory, repository, repositoryPath, requestedBaseRef string) error {
	abs, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	if !validRepositoryName(repository) {
		return fmt.Errorf("--repo must use owner/repository format")
	}
	if repositoryPath == "" {
		return fmt.Errorf("--repo-path is required")
	}
	repositoryPath, err = filepath.Abs(repositoryPath)
	if err != nil {
		return err
	}
	baseRef := requestedBaseRef
	if baseRef == "" {
		baseRef, err = detectBaseRef(repositoryPath, repository)
		if err != nil {
			return err
		}
	}
	if entries, readErr := os.ReadDir(abs); readErr == nil && len(entries) != 0 {
		return fmt.Errorf("directory %q is not empty", abs)
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	if err := os.MkdirAll(filepath.Join(abs, "agents"), 0o755); err != nil {
		return err
	}
	configBody := fmt.Sprintf(`version = 1
state_directory = ".state"
max_revisions = 2

[server]
listen = "127.0.0.1:7338"
poll_every = "60s"
trigger_label = "factory"
max_concurrent = 1

[[repositories]]
github = %s
path = %s
base_ref = %s

[roles]
foreman = "foreman"
plan = "planner"
build = "builder"
verify = "verifier"

[agents.foreman]
runtime = "claude"
prompt = "agents/foreman.md"
timeout = "2h"

[agents.planner]
runtime = "codex"
prompt = "agents/planner.md"
timeout = "30m"

[agents.builder]
runtime = "codex"
prompt = "agents/builder.md"
timeout = "45m"

[agents.verifier]
runtime = "codex"
prompt = "agents/verifier.md"
timeout = "45m"
`, strconv.Quote(repository), strconv.Quote(repositoryPath), strconv.Quote(baseRef))
	files := map[string]string{
		"factory.toml":       configBody,
		".gitignore":         ".state/\n",
		"agents/foreman.md":  defaultForemanPrompt + "\n",
		"agents/planner.md":  defaultPlannerPrompt + "\n",
		"agents/builder.md":  defaultBuilderPrompt + "\n",
		"agents/verifier.md": defaultVerifierPrompt + "\n",
	}
	for name, body := range files {
		if err := atomicWrite(filepath.Join(abs, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func detectBaseRef(repositoryPath, repository string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	body, err := commandOutput(ctx, repositoryPath, nil, "git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	cancel()
	if err == nil {
		if value := strings.TrimSpace(string(body)); value != "" {
			return value, nil
		}
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	body, err = commandOutput(ctx, repositoryPath, nil, "gh", "repo", "view", repository, "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name")
	cancel()
	if err == nil {
		if value := strings.TrimSpace(string(body)); value != "" {
			return "origin/" + value, nil
		}
	}
	return "", errors.New("cannot determine the repository default branch; pass --base-ref explicitly")
}
