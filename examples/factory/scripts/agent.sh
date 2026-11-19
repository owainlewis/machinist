#!/bin/sh
# Run Claude Code for one step: agent.sh <prompt>. The system prompt is
# prompts/rules.md followed by prompts/<prompt>.md. The job's prompt is the
# user message. machinist agent-log turns Claude's stream-json output into
# readable lines as it works, so the job page shows progress during the step.
set -e
step=${1:?usage: agent.sh <prompt>}
system="You are working in a clone of $MACHINIST_REPO on branch $MACHINIST_BRANCH.
The control directory is $MACHINIST_DIR. Files named below without a path are there.

$(cat "$MACHINIST_DIR/prompts/rules.md")

$(cat "$MACHINIST_DIR/prompts/$step.md")"
exec machinist agent-log -- claude --print --verbose --output-format stream-json \
  --dangerously-skip-permissions --append-system-prompt "$system" < "$MACHINIST_DIR/prompt.md"
