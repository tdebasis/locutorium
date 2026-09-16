#!/usr/bin/env bash
# check-clean.sh — the future-public discipline, enforced rather than remembered.
#
# This tree is written for strangers. It must never carry:
#   - the vocabulary of any particular deployment (org names, member names,
#     internal tool names)
#   - personal identifiers or absolute paths from a maintainer's machine
#   - credentials of any kind
#
# The vocabulary itself is DEPLOYMENT DATA, not source. Keeping the list in the
# tree meant the tree carried, in permanent history, the very words it exists to
# keep out — and no rewrite short of re-rooting the repository could take them
# back. So the list lives where every other piece of deployment data lives:
# under $LOC_HOME, outside any repository, one extended-regex alternative per
# line, blank lines and #-comments ignored. A fork writes its own file and
# changes nothing here.
#
#   LOC_FORBIDDEN_FILE   explicit path to the list (wins)
#   $LOC_HOME/forbidden  the default; $HOME/.locutorium/forbidden if LOC_HOME
#                        is unset
#
# With no list at all the check still runs, against a built-in generic list that
# holds only universally-wrong content — a maintainer's absolute home path — and
# says on stderr that it is doing so. A missing list is a weaker check, never a
# silent pass and never a hard failure: the suite and the release path both gate
# on the exit status.

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

FORBIDDEN_FILE="${LOC_FORBIDDEN_FILE:-${LOC_HOME:-$HOME/.locutorium}/forbidden}"

# The generic list is written with a bracket so that this script does not itself
# contain the literal absolute-path prefix it is looking for. The expression
# matches exactly what the plain spelling would.
GENERIC='/[Uu]sers/[a-z]|/home/[a-z]'

if [[ -r "$FORBIDDEN_FILE" ]]; then
  pattern="$(sed -e 's/#.*//' -e 's/[[:space:]]*$//' "$FORBIDDEN_FILE" \
    | grep -v '^[[:space:]]*$' | paste -sd '|' -)"
  n="$(sed -e 's/#.*//' -e 's/[[:space:]]*$//' "$FORBIDDEN_FILE" \
    | grep -cv '^[[:space:]]*$')"
  source_note="deployment list: $FORBIDDEN_FILE, $n patterns"
else
  pattern="$GENERIC"
  source_note="generic list only"
  echo "check-clean: no deployment list at $FORBIDDEN_FILE; generic list only" >&2
fi

if [[ -z "$pattern" ]]; then
  echo "check-clean: the list at $FORBIDDEN_FILE is empty — nothing to check against" >&2
  exit 1
fi

# .idea and .vscode are excluded BY NAME. This is not honouring .gitignore, which
# the check must never do: it names two folders that hold IDE state and nothing
# else, so a maintainer's hand run is not red every day for a harmless reason.
# A guard that is red every day is a guard nobody reads, and then a real hit is
# waved through with the noise (#54).
# A worktree's .git is a FILE, not a directory: it holds one line, an absolute
# path to the real .git. --exclude-dir=.git does not match a file, so that line
# was read and flagged. The file is never tracked, so excluding it loses nothing.
hits="$(grep -rniIE "$pattern" --exclude-dir=.git --exclude=.git --exclude-dir=.idea --exclude-dir=.vscode \
  --exclude=check-clean.sh . || true)"

if [[ -n "$hits" ]]; then
  echo "check-clean: forbidden vocabulary found ($source_note):" >&2
  echo "$hits" >&2
  exit 1
fi
echo "check-clean: tree is clean ($source_note)"
