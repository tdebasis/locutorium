#!/usr/bin/env bash
# conformance/leftovers_test.sh — does the leftovers census see exactly what
# belongs to this runner's earlier jobs, and nothing else on the machine?
#
# Everything here is a plant. Three `sleep`s stand in for what a cancelled job
# leaves running: two whose command line lies inside a fake work tree (a
# listener and its tap), one whose command line names a scratch config inside a
# fake temp directory (the server). A fourth, the control, is the machine's own
# business — same kind of process, unrelated command line — and it is the one
# that matters: the census this replaced matched on binary path, and on a
# machine whose real deployment runs from the same tree it killed every live
# seat that machine was serving.
#
# The plants are `sleep` with argv[0] rewritten, so nothing here is the tool,
# touches a deployment, or opens a socket. RUNNER_WORKSPACE and RUNNER_TEMP are
# pointed at directories this file made, so the scope never includes anything
# real. Every process this file starts, it kills by pid.

set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PASS=0; FAIL=0
ok()  { PASS=$((PASS+1)); printf '  ✓ %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); printf '  ✗ %s\n' "$1"; }
alive() { kill -0 "$1" 2>/dev/null; }

BASE="$(mktemp -d)"
# `_work` is the shape of a runner's tree, and the point of the name here is
# that it is a fake one: nothing under it is the machine's.
WS="$BASE/_work/house"
TMP="$BASE/_work/_temp"
mkdir -p "$WS/house/bin" "$TMP"

plant() { # <argv[0]> → pid, on stdout
  # stdout and stderr go nowhere on purpose: a background job that keeps the
  # command substitution's pipe open holds this function open for the sleep's
  # whole life, and the plant would never return its own pid.
  ARGV0="$1" bash -c 'exec -a "$ARGV0" sleep 1000' >/dev/null 2>&1 &
  printf '%s\n' "$!"
}
listener="$(plant "$WS/house/build/bin/loc mcp")"
tap="$(plant "$WS/house/build/bin/loc watch")"
server="$(plant "nats-server -c $TMP/loc-conformance.aaaa/deployment/nats-server.conf")"
control="$(plant "an unrelated process that this runner did not start")"
sleep 1

named() { # <census output> <pid> → is that pid on a line of its own?
  printf '%s\n' "$1" | awk -v p="$2" '$1 == p { found=1 } END { exit !found }'
}

printf 'leftovers: the census is the runner and nothing else\n'

# 1. With no scope in the environment there is no scope to guess at. This is
#    the guarantee that the script cannot be pointed at a whole machine, so it
#    is asked first and asked of both variables.
out="$(env -u RUNNER_WORKSPACE RUNNER_TEMP="$TMP" bash "$ROOT/conformance/leftovers.sh" census 2>&1)"; rc=$?
if [[ $rc -ne 0 ]]; then ok "refuses to run with no work tree named"; else bad "ran with no work tree named (exit $rc): $out"; fi
out="$(env RUNNER_WORKSPACE="$WS" -u RUNNER_TEMP bash "$ROOT/conformance/leftovers.sh" census 2>&1)"; rc=$?
if [[ $rc -ne 0 ]]; then ok "refuses to run with no temp directory named"; else bad "ran with no temp directory named (exit $rc): $out"; fi

# 2. The census, in scope.
census="$(env RUNNER_WORKSPACE="$WS" RUNNER_TEMP="$TMP" bash "$ROOT/conformance/leftovers.sh" census)"; rc=$?
if [[ $rc -eq 0 ]]; then ok "census exits 0"; else bad "census exited $rc"; fi
if named "$census" "$listener"; then ok "census names the listener";     else bad "census missed the listener"; fi
if named "$census" "$tap";      then ok "census names the tap";          else bad "census missed the tap"; fi
if named "$census" "$server";   then ok "census names the scratch server (its config is under the runner's temp)"; else bad "census missed the scratch server"; fi
if named "$census" "$control";  then bad "census named the control"; else ok "census leaves the control alone"; fi
n="$(printf '%s\n' "$census" | grep -c . )"
if [[ "$n" == "3" ]]; then ok "census names three processes and no others"; else bad "census named $n processes, not 3: $census"; fi

# 3. The reap.
reaped="$(env RUNNER_WORKSPACE="$WS" RUNNER_TEMP="$TMP" bash "$ROOT/conformance/leftovers.sh" reap 2>&1)"; rc=$?
if [[ $rc -eq 0 ]]; then ok "reap exits 0 when it clears the tree"; else bad "reap exited $rc: $reaped"; fi
for pair in "listener:$listener" "tap:$tap" "server:$server"; do
  if alive "${pair#*:}"; then bad "reap left the ${pair%%:*} running"; else ok "reap removed the ${pair%%:*}"; fi
done
if alive "$control"; then ok "the control survived the reap"; else bad "the reap killed the control"; fi

# 4. An empty runner says so, out loud, and does not fail.
out="$(env RUNNER_WORKSPACE="$WS" RUNNER_TEMP="$TMP" bash "$ROOT/conformance/leftovers.sh" census)"; rc=$?
if [[ "$out" == "leftovers: none" && $rc -eq 0 ]]; then ok "a clean runner censuses as 'leftovers: none'"; else bad "a clean runner said '$out' (exit $rc)"; fi

for p in "$listener" "$tap" "$server" "$control"; do kill -9 "$p" 2>/dev/null; done
rm -rf "$BASE"
printf 'leftovers: %s passed, %s failed\n' "$PASS" "$FAIL"
[[ $FAIL -eq 0 ]]
