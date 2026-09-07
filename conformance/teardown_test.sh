#!/usr/bin/env bash
# conformance/teardown_test.sh — does the suite's teardown actually fire when
# the run is killed, and does it take only what the suite spawned?
#
# The suite itself cannot be started here: a run boots a server and a
# deployment, and on a persistent runner a second one collides with the job
# already in progress. So the thing under test is conformance/teardown.sh on
# its own. A child shell sources it, plants stand-ins for the processes a real
# run leaves running, blocks the way a run blocks — inside a foreground
# command — and is then signalled.
#
# The stand-ins are `sleep`, not the tool: the question is whether a trap fires
# and whether the pids it was handed die, which is a question about the
# teardown and about nothing the tool does.
#
#   spawned by the run  → the scratch server, the demo's server, and a listener
#                         named by a pidfile under the run's own scratch
#                         LOC_HOME. All three must be gone.
#   not spawned by it   → a listener whose pidfile lives under a DIFFERENT
#                         home. It must be untouched: the census this replaced
#                         matched on binary path, and on a machine whose real
#                         deployment runs from the same tree it killed every
#                         live seat that machine was serving.
#
# The third case signals twice. A cancelled job is not asked once — it is asked
# and then insisted upon — and a teardown reached only as a dying shell's exit
# trap is abandoned where it stands by the second signal.
#
# Every process this file starts, it kills by pid, whatever the verdict.

set -uo pipefail
# JOB CONTROL, SO THAT THE CHILD IS THE RUN AND NOT A BACKGROUND JOB. A shell
# with job control off gives every asynchronous child SIG_IGN for INT, and a
# signal that was ignored on entry cannot be trapped afterwards — so without
# this the INT case would be measuring how this file starts its child rather
# than what the teardown does, and would report a failure that CI, which runs
# the suite in the foreground, could never have.
set -m
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PASS=0; FAIL=0
ok()  { PASS=$((PASS+1)); printf '  ✓ %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); printf '  ✗ %s\n' "$1"; }

alive() { kill -0 "$1" 2>/dev/null; }
# A pid may take a moment to go: TERM, then KILL two seconds later, is the
# teardown's own schedule. Ten seconds is that with room, and it returns the
# instant the process is gone rather than waiting out the clock.
gone_within() { # <pid> <seconds>
  local i=0
  while [[ $i -lt $(( $2 * 2 )) ]]; do
    alive "$1" || return 0
    sleep 0.5; i=$((i+1))
  done
  return 1
}

WORK="$(mktemp -d)"
CHILD="$WORK/child.sh"
cat > "$CHILD" <<'CHILDEOF'
#!/usr/bin/env bash
set -uo pipefail
ROOT="$1"; SCRATCH="$2"; PIDS="$3"; OTHER="$4"; IDLE="$5"
LOC_IMPL=shell
LOC_BIN_DIR="$ROOT/bin"
export LOC_HOME="$SCRATCH/deployment"
mkdir -p "$LOC_HOME/run" "$OTHER/run"
sleep 900 & SERVER_PID=$!
sleep 900 & DEMO_PID=$!
sleep 900 & listener=$!
sleep 900 & control=$!
# The pidfile is what marks a listener as THIS run's — the same file the
# teardown's census reads. The control's pidfile names the same kind of
# process under a home this run does not own.
printf '%s\n' "$listener" > "$LOC_HOME/run/probe.listener.pid"
printf '%s\n' "$control"  > "$OTHER/run/probe.listener.pid"
printf 'server=%s\ndemo=%s\nlistener=%s\ncontrol=%s\n' \
  "$SERVER_PID" "$DEMO_PID" "$listener" "$control" > "$PIDS"
. "$ROOT/conformance/teardown.sh"
printf 'ready\n' >> "$PIDS"
# Where a run spends its time: inside a foreground command, not at a prompt.
read -r _ < "$IDLE"
CHILDEOF

run_case() { # <signal> [second signal, sent while the teardown is running]
  local sig="$1" again="${2:-}"
  local label="$sig"; [[ -n "$again" ]] && label="$sig then $again"
  local scratch other pids idle child_pid i
  scratch="$(mktemp -d)"; other="$(mktemp -d)"; pids="$(mktemp)"
  idle="$WORK/idle.$$.$RANDOM"; mkfifo "$idle"
  bash "$CHILD" "$ROOT" "$scratch" "$pids" "$other" "$idle" &
  child_pid=$!
  # Signal only once the child says the traps are installed; signalling before
  # that would be a test of the plant, not of the teardown.
  i=0
  while ! grep -q '^ready$' "$pids" 2>/dev/null; do
    sleep 0.2; i=$((i+1))
    if [[ $i -gt 50 ]]; then bad "$label: the child never came up"; kill -9 "$child_pid" 2>/dev/null; return; fi
  done
  local server demo listener control
  server="$(sed -n 's/^server=//p' "$pids")"
  demo="$(sed -n 's/^demo=//p' "$pids")"
  listener="$(sed -n 's/^listener=//p' "$pids")"
  control="$(sed -n 's/^control=//p' "$pids")"

  kill -"$sig" "$child_pid" 2>/dev/null
  # Half a second in is inside the teardown's own wait for the listeners it
  # has just asked to leave.
  if [[ -n "$again" ]]; then sleep 0.5; kill -"$again" "$child_pid" 2>/dev/null; fi
  # Bounded, because a shell that ignores the signal is one of the results
  # this file is here to catch — and a test that hangs reports nothing.
  i=0
  while alive "$child_pid" && [[ $i -lt 40 ]]; do sleep 0.5; i=$((i+1)); done
  if alive "$child_pid"; then
    bad "$label: the run's own shell ignored the signal and never tore down"
    kill -9 "$child_pid" 2>/dev/null
  fi
  wait "$child_pid" 2>/dev/null

  if gone_within "$server" 10;   then ok "$label: the scratch server is gone";       else bad "$label: the scratch server survived"; fi
  if gone_within "$demo" 10;     then ok "$label: the demo server is gone";          else bad "$label: the demo server survived"; fi
  if gone_within "$listener" 10; then ok "$label: this run's listener is gone";      else bad "$label: this run's listener survived"; fi
  if alive "$control";           then ok "$label: another home's listener survived"; else bad "$label: another home's listener was killed"; fi

  local p
  for p in "$server" "$demo" "$listener" "$control"; do
    [[ -n "$p" ]] && kill -9 "$p" 2>/dev/null
  done
  rm -f "$idle"; rm -rf "$scratch" "$other" "$pids"
}

printf 'teardown: the suite tears down when the run is killed\n'
run_case TERM
run_case INT
run_case TERM TERM
rm -rf "$WORK"
printf 'teardown: %s passed, %s failed\n' "$PASS" "$FAIL"
[[ $FAIL -eq 0 ]]
