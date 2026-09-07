#!/usr/bin/env bash
# conformance/leftovers.sh — what an earlier job on this runner left behind.
#
# A cancelled job's step shell is killed; what the suite detached is not. On a
# hosted image that does not matter, because the machine goes away with the
# job. On a persistent runner the seats' listener loops and the scratch server
# survive into the next job, whose own census then counts them and whose
# registry and wake cases see a second, stale attendance. Run before the suite,
# this says what is there and takes it away.
#
# WHAT IT WILL TOUCH, AND WHY THAT IS ALL IT CAN TOUCH. A process qualifies
# only if its command line contains the runner's own work tree or the runner's
# own temp directory, and both are read from the environment with `:?` — with
# neither set there is no scope at all and the script refuses to run, so it can
# never be pointed at a whole machine. Its own shell, everything that started
# it, and everything it starts are excluded by pid: the step that runs this
# runs from inside the work tree, so without that it would name itself.
#
# WHERE EACH ONE IS CAUGHT (verified 2026-09-06 against this runner):
#
#   the listeners — a listener is a forked subshell of `loc`, so its command
#   line is the interpreter and the script's full path: `bash <tree>/bin/loc
#   sub`. The tree is the checkout, which lives under the work tree. Caught by
#   the work-tree filter.
#
#   the scratch server — `nats-server -c <scratch LOC_HOME>/nats-server.conf`,
#   and the scratch home is a `mktemp -d`. The work-tree filter does NOT reach
#   it: `mktemp` follows TMPDIR, and TMPDIR on this runner is not the runner's
#   `_work/_temp` but the per-user temp directory the service manager hands
#   every process in the session. The runner sets RUNNER_TEMP and does not set
#   TMPDIR — the job's environment carries both, and the service definition
#   sets neither. So run.sh makes its scratch trees under
#   `${RUNNER_TEMP:-${TMPDIR:-/tmp}}` instead, which puts the path that appears
#   on the server's command line inside the runner's own temp directory, and
#   the temp filter reaches it.
#
# Also learned the hard way: a listener's environment is not readable from
# outside its own process, so a census by environment variable cannot be
# written at all. The command line is the only evidence there is.

set -uo pipefail

MODE="${1:-census}"
case "$MODE" in
  census|reap) ;;
  *) echo "leftovers: usage: leftovers.sh [census|reap]" >&2; exit 2 ;;
esac

# An apostrophe inside a ${...:?} message is a quote to the parser and would
# swallow the rest of the file, so these two say it without one.
WORKSPACE="${RUNNER_WORKSPACE:?leftovers: RUNNER_WORKSPACE is unset — this runs on a runner, against the work tree of that runner, and refuses to guess a scope}"
TEMP="${RUNNER_TEMP:?leftovers: RUNNER_TEMP is unset — this runs on a runner, against the temp directory of that runner, and refuses to guess a scope}"

# One snapshot, so the tree and the command lines agree with each other. The
# command line is everything after the two numeric columns; `ps` is asked for
# exactly those two so that reassembling the rest is unambiguous.
census() { # → "pid  command", one per line
  ps -A -o pid=,ppid=,command= 2>/dev/null | awk -v self="$$" -v ws="$WORKSPACE" -v tmp="$TEMP" '
    { pid=$1; par[pid]=$2; $1=""; $2=""; sub(/^[[:space:]]+/, ""); cmd[pid]=$0; order[++n]=pid }
    END {
      # MINE: this shell, everything it started, and everything that started
      # it. The step that invokes this file runs from a path inside the work
      # tree and its wrapper from a path inside the temp directory, so both
      # would otherwise match and a reap would kill the job it is protecting.
      #
      # DESCENDANTS ARE TAKEN FROM THIS SHELL ONLY, NEVER FROM AN ANCESTOR.
      # An orphan is reparented to the service manager, which is also an
      # ancestor of this shell — so a closure that walked down from every
      # ancestor would call the entire machine its own and reap nothing.
      mine[self]=1
      changed=1
      while (changed) {
        changed=0
        for (i=1; i<=n; i++) { p=order[i]
          if (!(p in mine) && (par[p] in mine)) { mine[p]=1; changed=1 }
        }
      }
      for (p=self; p in par && par[p] != ""; p=par[p]) mine[par[p]]=1
      for (i=1; i<=n; i++) { p=order[i]
        if (p in mine) continue
        if (index(cmd[p], ws) || index(cmd[p], tmp)) printf "%s  %s\n", p, cmd[p]
      }
    }'
}

found="$(census)"

if [[ "$MODE" == census ]]; then
  if [[ -z "$found" ]]; then echo "leftovers: none"; else printf '%s\n' "$found"; fi
  exit 0
fi

if [[ -z "$found" ]]; then echo "leftovers: none"; exit 0; fi

echo "leftovers: reaping"
printf '%s\n' "$found"
pids="$(printf '%s\n' "$found" | awk '{print $1}')"
for p in $pids; do kill "$p" 2>/dev/null; done
sleep 2
for p in $pids; do kill -0 "$p" 2>/dev/null && kill -9 "$p" 2>/dev/null; done
sleep 1

# THE SECOND CENSUS IS THE POINT. A job that starts with somebody else's
# listeners still attending is a job whose result means nothing, so anything
# still standing here fails the step rather than being reported and stepped
# over.
still="$(census)"
if [[ -n "$still" ]]; then
  echo "leftovers: still running after TERM and KILL — the suite would run against them:" >&2
  printf '%s\n' "$still" >&2
  exit 1
fi
echo "leftovers: none survive"
