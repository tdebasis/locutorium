#!/usr/bin/env bash
# orphan-probe.sh — a diagnostic, not a conformance case.
#
# The suite's "rapid unsub/resub leaves no orphan listener" case has been seen
# to fail intermittently. This script loops that case's death-window sequence
# many times over, in a scratch house of its own, and captures the state of any
# survivor the moment one appears — which is the thing a single flaky suite run
# never gives you. It is deliberately NOT wired into run.sh: it proves nothing
# when it passes, so it would only add a slow case that flakes.
#
#   usage: conformance/orphan-probe.sh <iterations> [load]
#
#   load   spawn two busy loops for the duration, killed on exit. The failure
#          is load-sensitive; an idle machine may never show it.
#
# Per iteration: sub · unsub · sub (registering inside the death window) ·
# sleep 3 · unsub · settle. Anything still alive afterwards that belongs to
# THIS house is a survivor — identified by this tree's own bin/loc path and by
# the queue subject, so a real deployment's listeners are never touched and
# never killed. On the first survivor the script dumps process state (including
# STAT and WCHAN, which say whether it is blocked opening a fifo, reading a
# dead one, or awake), the listener log, the delivery log, and its open pipes.
#
# Exit status: 0 = no survivor in <iterations>, 1 = reproduced (details on
# stdout). Record the iteration count and the load average with any result;
# without both, two runs are not comparable.

set -uo pipefail

ITERS="${1:?usage: orphan-probe.sh <iterations> [load]}"
LOAD="${2:-noload}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT=$(( 20000 + RANDOM % 20000 ))
export LOC_HOME="$(mktemp -d)/house"
PATH="$ROOT/bin:$PATH"
SERVER_PID=""
LOADPIDS=""

finish() {
  for p in $LOADPIDS; do kill "$p" 2>/dev/null; done
  [[ -n "$SERVER_PID" ]] && kill "$SERVER_PID" 2>/dev/null
  rm -rf "$(dirname "$LOC_HOME")"
}
trap finish EXIT

"$ROOT/providers/nats/bootstrap.sh" alice bob carol >/dev/null
sed -i '' "s|127.0.0.1:4222|127.0.0.1:$PORT|" "$LOC_HOME/config" "$LOC_HOME/nats-server.conf"
nats-server -c "$LOC_HOME/nats-server.conf" >"$LOC_HOME/server.log" 2>&1 &
SERVER_PID=$!
sleep 1
LOC_IDENTITY=admin loc doctor --init >/dev/null || { echo "probe: stream init failed" >&2; exit 2; }

for h in nudge wake register; do
  cat > "$LOC_HOME/hooks/$h" <<EOF
#!/bin/sh
echo "\$1 \$2" >> "$LOC_HOME/$h.log"
EOF
  chmod +x "$LOC_HOME/hooks/$h"
done

survivors() { # pids belonging to THIS house only
  { pgrep -f "$ROOT/bin/loc sub"; pgrep -f "subscribe queue.alice"; } 2>/dev/null | sort -u
}

echo "probe: house $LOC_HOME on port $PORT, $ITERS iterations, mode $LOAD"
echo "probe: load at start: $(uptime)"

if [[ "$LOAD" == "load" ]]; then
  yes >/dev/null & LOADPIDS="$LOADPIDS $!"
  yes >/dev/null & LOADPIDS="$LOADPIDS $!"
  sleep 3
fi

pre="$(survivors | tr '\n' ' ')"
[[ -n "$pre" ]] && { echo "probe: refusing to run — pids already match this house: $pre" >&2; exit 2; }

REPRO=0
i=0
for i in $(seq 1 "$ITERS"); do
  LOC_IDENTITY=alice loc sub   >/dev/null 2>&1
  LOC_IDENTITY=alice loc unsub >/dev/null 2>&1
  LOC_IDENTITY=alice loc sub   >/dev/null 2>&1
  sleep 3
  LOC_IDENTITY=alice loc unsub >/dev/null 2>&1
  sleep 4
  s="$(survivors | tr '\n' ' ')"
  if [[ -n "$s" ]]; then
    REPRO=1
    echo "probe: SURVIVOR at iteration $i: $s"
    echo "probe: load now: $(uptime)"
    echo "=== survivors ==="
    # shellcheck disable=SC2086
    ps -o pid,ppid,stat,wchan,command -p $s
    echo "=== their children ==="
    for p in $s; do
      kids="$(pgrep -P "$p" | tr '\n' ' ')"
      # shellcheck disable=SC2086
      [[ -n "$kids" ]] && ps -o pid,ppid,stat,wchan,command -p $kids
    done
    echo "=== run/ ==="
    ls -la "$LOC_HOME/run"
    echo "=== listener log (tail) ==="
    tail -60 "$LOC_HOME/run/alice.listener.log" 2>/dev/null
    echo "=== delivery log (tail) ==="
    tail -60 "$LOC_HOME/run/alice.delivery.log" 2>/dev/null
    echo "=== open pipes ==="
    for p in $s; do lsof -p "$p" 2>/dev/null | grep -iE 'fifo|pipe'; done
    # shellcheck disable=SC2086
    kill -9 $s 2>/dev/null
    break
  fi
  echo "probe: iteration $i clean"
done

echo "probe: reproduced=$REPRO iterations_run=$i"
echo "probe: load at end: $(uptime)"
exit $REPRO
