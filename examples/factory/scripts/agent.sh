#!/bin/sh
# Run Claude Code for one step: agent.sh <prompt>. The system prompt is
# prompts/rules.md followed by prompts/<prompt>.md. The job's prompt is the
# user message.
set -e
step=${1:?usage: agent.sh <prompt>}
system="You are working in a clone of $MACHINIST_REPO on branch $MACHINIST_BRANCH.
The control directory is $MACHINIST_DIR. Files named below without a path are there.

$(cat "$MACHINIST_DIR/prompts/rules.md")

$(cat "$MACHINIST_DIR/prompts/$step.md")"
claude --print --dangerously-skip-permissions --append-system-prompt "$system" < "$MACHINIST_DIR/prompt.md"
