#!/usr/bin/env bash
# conformance/teardown.sh — the suite's teardown, sourced by run.sh.
#
# It lives in its own file for one reason: a teardown that only ever runs at
# the end of a passing suite is a teardown nobody has tested. Sourced, it can
# be exercised on its own — see conformance/teardown_test.sh — without booting
# a server, which is what a test of "what happens when the run is killed" has
# to be able to do.
#
# Sourced, never executed: it defines the functions and installs the traps in
# the caller's shell, and reads the caller's LOC_IMPL, LOC_HOME, LOC_BIN_DIR,
# SERVER_PID and DEMO_PID at the moment a trap fires.

# THIS SUITE'S OWN LISTENERS, AND ONLY THOSE — identified by a file this run
# wrote, never by which binary a process happens to be running. A machine
# whose real deployment runs from the identical tree (the deployment's own
# `bin/loc`, not a copy) has listeners with the exact same command line and
# the exact same binary path as this suite's; a filter on the path cannot
# tell them apart; a filter on `$LOC_HOME/run/*.listener.pid` — a directory
# that exists ONLY under this scratch deployment — can.
#
# `pgrep -fl 'loc sub' | grep "$LOC_BIN_DIR/loc"` was that path filter, and on
# such a machine it matched every live listener the machine actually serves.
# The cleanup that followed it did not just fail to reap an orphan — it
# killed every one of them.
suite_listeners() { # → one pid per line: this suite's own listeners, plus
  # each one's tap (its `nats subscribe` child, found by PARENT pid — never
  # by matching the tap's own command line, which names no deployment at
  # all). Under LOC_IMPL=go there is no listener in this build to have a
  # pidfile, so this returns nothing; harmless, because the go lane skips
  # every case that would call it.
  [[ "$LOC_IMPL" == go ]] && return 0
  local pf lpid
  for pf in "$LOC_HOME"/run/*.listener.pid; do
    [[ -e "$pf" ]] || continue
    lpid="$(sed -n 1p "$pf" 2>/dev/null)"
    [[ -n "$lpid" ]] || continue
    kill -0 "$lpid" 2>/dev/null || continue   # a stale pidfile names nobody
    printf '%s\n' "$lpid"
    pgrep -P "$lpid" 2>/dev/null
  done
}
# WHAT THE RUN STARTED, AS OPPOSED TO WHAT STILL HAS A PIDFILE. The census
# above reads CURRENT pidfiles, and a pidfile is a licence, not a receipt: it
# is removed by every `unsub` whether or not that unsub's kill landed, and it
# is overwritten by a successor that registers inside a dying listener's
# death window. Either way a listener the suite started can still be running
# with no file on disk naming it — invisible to suite_listeners(), and so
# never reaped. That is how a run that ended `57 passed, 0 failed` left a
# live `loc sub` behind for the next job on the same runner to find.
#
# So the run also keeps its own record: one pid per line, appended by the
# `loc` helper in run.sh as each `loc sub` prints the listener it started.
# The file lives inside the run's own scratch tree, so it can name nothing
# that outlives the run and nothing any other deployment owns.
SUITE_LEDGER_NAME="suite.spawned"
suite_ledger() { # → one pid per line: every listener this run started that is
  # still alive, plus each one's children by PARENT pid — the same tap rule as
  # the census, and for the same reason.
  local lf="$LOC_HOME/run/$SUITE_LEDGER_NAME" lpid
  [[ -r "$lf" ]] || return 0
  while IFS= read -r lpid; do
    [[ "$lpid" =~ ^[0-9]+$ ]] || continue
    [[ "$lpid" == "$$" ]] && continue          # never the run's own shell
    kill -0 "$lpid" 2>/dev/null || continue    # already gone, nothing to reap
    printf '%s\n' "$lpid"
    pgrep -P "$lpid" 2>/dev/null
  done < "$lf"
}
reap_suite_listeners() { # [pid...] — TERM, then KILL after 2s for survivors.
  # With no arguments the target is the UNION of a fresh suite_listeners()
  # census and the spawn ledger — what still has a pidfile, and what this run
  # started. Neither is a superset of the other: the census catches a listener
  # started outside the helper, the ledger catches one whose pidfile is gone.
  # A caller that needs to reap pids a LATER census could not rediscover — the
  # pidfile-removal case below deletes the very file this census reads, on
  # purpose, as its own test — passes that earlier census in explicitly
  # instead, and gets exactly those pids and no others.
  local pids=("$@")
  if [[ "${#pids[@]}" -eq 0 ]]; then
    local _l _seen=" "
    while IFS= read -r _l; do
      [[ -n "$_l" ]] || continue
      [[ "$_seen" == *" $_l "* ]] && continue   # union, not concatenation
      _seen="$_seen$_l "
      pids+=("$_l")
    done < <(suite_listeners; suite_ledger)
  fi
  [[ "${#pids[@]}" -eq 0 ]] && return 0
  local p
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null; done
  sleep 2
  for p in "${pids[@]}"; do kill -0 "$p" 2>/dev/null && kill -9 "$p" 2>/dev/null; done
}

cleanup() {
  # A SECOND CANCEL DURING TEARDOWN IS IGNORED, NOT OBEYED HALFWAY — and this
  # is the first statement here because everything below it is the part that
  # was being abandoned.
  #
  # The rule, and the version it turns on. The manual says a signal arriving
  # while the shell waits for a command is held: "If Bash is waiting for a
  # command to complete and receives a signal for which a trap has been set,
  # it will not execute the trap until the command completes." The reap below
  # waits two seconds between TERM and KILL, so a canceller's second signal
  # lands there and is held exactly that long. What happens when it is finally
  # delivered is what moved: bash 5.0 lists "Bash now allows SIGINT trap
  # handlers to execute recursively" among its new features, and this shell's
  # handlers behave that way for the cancelling signals — the handler runs
  # AGAIN, nested inside itself. The once-guard makes that nested cleanup
  # return immediately, and the trap's own `exit 143` then exits the shell
  # from inside the OUTER handler, at the reap, before the servers are
  # killed and before the scratch tree is removed. bash 3.2 (still the system
  # shell on macOS) runs the handler once and swallows the second signal,
  # which is why this passed there and failed on a hosted bash 5.
  #
  # Ignoring is right rather than merely convenient: teardown is already the
  # thing the canceller is asking for, and there is nothing further to obey.
  # It is not a way to refuse to die — KILL is not trappable and still wins,
  # and the reap's own KILL step still runs on schedule.
  trap '' TERM INT
  # Torn down once. A run that is signalled tears down in the signal's trap and
  # then reaches the EXIT trap on its way out. The guard is what stops that
  # second pass; the line above is what stops a second SIGNAL, which the guard
  # cannot, because the guard only decides what a re-entered handler does, not
  # where the shell resumes when that handler exits.
  [[ -n "${_TORE_DOWN:-}" ]] && return 0
  _TORE_DOWN=1
  # ATTENDANCE ENDS BEFORE THE SERVER DOES. Under the presence model a queue
  # exists exactly while an agent is subscribed, so the seats this suite
  # subscribed are unsubscribed here — while there is still a server to tell.
  # --force because the registration names THIS script's pid, which is by
  # definition still alive: plain unsubscribe refuses a live incumbent on
  # purpose, and refusing here would leave the streams behind.
  if [[ "$LOC_IMPL" == go && -n "$SERVER_PID" ]]; then
    for _s in $(ep alice) $(ep bob) $(ep carol); do
      LOC_IDENTITY=$_s "$LOC_BIN_DIR/loc" unsubscribe "$_s" --force >/dev/null 2>&1
    done
  fi
  # THE LISTENERS GO BEFORE THE SERVER THEY TALK TO, and they go by the
  # pidfiles this run wrote — the census above, and nothing wider. Until this
  # line the teardown killed the server and deleted the scratch tree and left
  # the listeners running: on a machine that goes away with the job that is
  # invisible, and on a runner that does not it is the next job's four
  # failures.
  reap_suite_listeners
  [[ -n "$SERVER_PID" ]] && kill "$SERVER_PID" 2>/dev/null
  # The README demo lays a SECOND scratch house with a second server. It is
  # killed inline where it is used, which covers the run that reaches the end
  # of that case and no other.
  [[ -n "${DEMO_PID:-}" ]] && kill "$DEMO_PID" 2>/dev/null
  rm -rf "$(dirname "$LOC_HOME")"
}

# EXIT IS NOT THE ONLY WAY A RUN ENDS. A cancelled job is signalled, not asked
# to finish, and an untrapped signal does not reliably reach an EXIT trap at
# all: a shell blocked inside a foreground command never runs the teardown on
# INT, and a shell that reaches it as a dying shell has no handler for the
# SECOND signal — the one a canceller sends when the first is not obeyed — so
# it is killed halfway through, having taken some of what it spawned and left
# the rest. Trapped, the signal is handled between commands and a second one
# waits its turn.
#
# Each signal trap ends the run with the conventional status for it. The guard
# is what keeps the EXIT trap that follows from tearing down a second time.
_TORE_DOWN=""
trap cleanup EXIT
trap 'cleanup; exit 143' TERM
trap 'cleanup; exit 130' INT
