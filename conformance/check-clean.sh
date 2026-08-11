#!/usr/bin/env bash
# check-clean.sh — the future-public discipline, enforced rather than remembered.
#
# This tree is written for strangers. It must never carry:
#   - the vocabulary of any particular deployment (org names, member names,
#     internal tool names)
#   - personal identifiers or absolute paths from a maintainer's machine
#   - credentials of any kind
# The forbidden list below names this project's own deployment's vocabulary;
# a fork with a different deployment should replace it with its own.

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

FORBIDDEN='conclave|steward|archivist|artificer|convener|moot|cerefox|tmux|tanambamsinha|quantbrik|/Users/'
hits="$(grep -rniE "$FORBIDDEN" --exclude-dir=.git --exclude=check-clean.sh . || true)"

if [[ -n "$hits" ]]; then
  echo "check-clean: FORBIDDEN vocabulary found:" >&2
  echo "$hits" >&2
  exit 1
fi
echo "check-clean: tree is clean"
