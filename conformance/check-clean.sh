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
# The two lists add up. A built-in generic list holds only universally-wrong
# content — a maintainer's absolute home path — and it applies on every run,
# because a home path is wrong for everybody and no deployment list is obliged to
# repeat it. When a deployment list is readable, its patterns are checked as
# well. If the deployment list replaced the generic one, the same tree could pass
# on a machine whose list happens to omit one home-path form and fail on a
# machine with no list at all (#137).
#
# With no list at all the check still runs, against the generic list alone, and
# says on stderr that it is doing so. A missing list is a weaker check, never a
# silent pass and never a hard failure: the suite and the release path both gate
# on the exit status.
#
# The script has two lanes and one pattern. The first lane reads the working
# tree. The second lane reads the lines that the commits in origin/main..HEAD
# add, because a word removed before the tip stays in the history. The comment
# above that lane says why the range stops where it does.

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

FORBIDDEN_FILE="${LOC_FORBIDDEN_FILE:-${LOC_HOME:-$HOME/.locutorium}/forbidden}"

# The generic list is written with a bracket so that this script does not itself
# contain the literal absolute-path prefix it is looking for. The expression
# matches exactly what the plain spelling would.
GENERIC='/[Uu]sers/[a-z]|/home/[a-z]'

pattern="$GENERIC"
if [[ -r "$FORBIDDEN_FILE" ]]; then
  deployment="$(sed -e 's/#.*//' -e 's/[[:space:]]*$//' "$FORBIDDEN_FILE" \
    | grep -v '^[[:space:]]*$' | paste -sd '|' - || true)"
  n="$(sed -e 's/#.*//' -e 's/[[:space:]]*$//' "$FORBIDDEN_FILE" \
    | grep -cv '^[[:space:]]*$' || true)"
  # An installed list that holds no patterns is still refused. The generic list
  # would make the run look healthy, and the maintainer believes the deployment
  # words are being checked when none are. The `|| true` above lets the run reach
  # this line: under pipefail a grep that selects nothing ends the script with no
  # message.
  if [[ -z "$deployment" ]]; then
    echo "check-clean: the list at $FORBIDDEN_FILE is empty — nothing to check against" >&2
    exit 1
  fi
  pattern="$pattern|$deployment"
  source_note="generic list + deployment list: $FORBIDDEN_FILE, $n patterns"
else
  source_note="generic list only"
  echo "check-clean: no deployment list at $FORBIDDEN_FILE; generic list only" >&2
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

# --- the commit lane -------------------------------------------------------
#
# The tree lane above reads only what survives to the tip. A forbidden word
# added in one commit of a pull request and removed in a later commit of the
# same pull request passes it, and the word stays in the history for good
# (#178). The lane below reads the content the pull request ADDS, against the
# same $pattern the tree lane built. One pattern. Two would drift.
#
# The range is origin/main..HEAD and nothing wider. A lane over all history can
# never pass: the words are already back there, in commits that predate the
# list moving out of the tree, and the header above says why that cannot be
# undone. A guard that is red every day is a guard nobody reads, and then a
# real hit is waved through with the noise (#54).
#
# In a pull-request run the checkout is the merge commit of the head branch
# into the base, so HEAD is that merge. origin/main..HEAD then holds the pull
# request's own commits and the merge commit. Git prints no patch for a merge
# commit, so the merge adds no lines and the range still means "what this pull
# request adds". The hygiene job checks out with fetch-depth 0, which fetches
# every branch into refs/remotes/origin, so origin/main resolves there.
#
# Four cases the lane must not get wrong:
#
#   - origin/main does not resolve, or the clone is shallow. A tarball, a clone
#     with no remote and a depth-limited fetch all give a range that is either
#     impossible or untrue. The lane says so on stderr and skips. That is the
#     answer the header already gives for a missing deployment list: a weaker
#     check, never a silent pass and never a hard failure.
#   - The range is empty. Every push to main is this case. The lane passes and
#     reports that it scanned 0 commits.
#   - This script is excluded from the tree scan by name, so the commit lane
#     excludes it too. It carries the patterns it hunts for, and every change to
#     it would otherwise flag itself.
#   - A commit is named in the output. The tree is clean at that point, so the
#     reader needs the commit to find the word.

if ! git rev-parse --git-dir >/dev/null 2>&1; then
  echo "check-clean: not a git work tree; commit lane skipped" >&2
  exit 0
fi

if [[ "$(git rev-parse --is-shallow-repository 2>/dev/null || echo unknown)" == "true" ]]; then
  echo "check-clean: shallow clone; commit lane skipped" >&2
  exit 0
fi

if ! git rev-parse --verify --quiet origin/main >/dev/null 2>&1; then
  echo "check-clean: no origin/main here; commit lane skipped" >&2
  exit 0
fi

# Added lines carry a file and a line number so the report points somewhere.
# The hunk header gives the first line number on the new side; each added line
# advances it. Context lines are counted for the same reason, although
# --unified=0 emits none.
added_lines='
  /^\+\+\+ / { path = substr($0, 5); sub(/^b\//, "", path); next }
  /^@@ /     { plus = index($0, "+")
               split(substr($0, plus + 1), f, /[, ]/)
               line = f[1] + 0
               next }
  /^\+/      { print path ":" line ":" substr($0, 2); line++; next }
  /^ /       { line++; next }
'

commit_hits=""
commit_count=0
while IFS= read -r sha; do
  [[ -n "$sha" ]] || continue
  commit_count=$((commit_count + 1))
  hits="$(git show --format='' --unified=0 --no-color "$sha" \
      -- . ':(exclude,glob)**/check-clean.sh' \
    | awk "$added_lines" \
    | grep -iE "$pattern" || true)"
  if [[ -n "$hits" ]]; then
    commit_hits="${commit_hits}$(git log -1 --format='%h %s' "$sha")
$(echo "$hits" | sed 's/^/    /')
"
  fi
done <<EOF
$(git rev-list origin/main..HEAD)
EOF

if [[ -n "$commit_hits" ]]; then
  echo "check-clean: forbidden vocabulary added by a commit in origin/main..HEAD ($source_note):" >&2
  printf '%s' "$commit_hits" >&2
  echo "check-clean: the tree is clean, so this word is only in the history. Amend or rebase the commit above." >&2
  exit 1
fi

echo "check-clean: commits are clean ($commit_count commits in origin/main..HEAD)"
