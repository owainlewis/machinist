#!/bin/sh
# Review the change. A review.json left by an earlier step is removed first,
# so only this review's verdict counts, and the step fails if it writes none.
set -e
rm -f "$MACHINIST_DIR/review.json"
machinist agent review
machinist verdict
