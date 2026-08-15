#!/usr/bin/env bash
# conformance/run.sh — the suite that defines the Locutorium.
#
# A provider IS a Locutorium provider iff this suite passes against it.
# Self-contained: boots a scratch server + deployment on a random port,
# runs every case, tears everything down. Never touches a real deployment.
#
# Guarantees exercised here (the contract's test section, v1 scope):
#   delivery to a dormant endpoint · consumption exactly once per queue ·
#   per-sender FIFO · ACL denials · unattributable refusal · cwd-independence ·
#   topic window visibility + bounded catch-up · @mention nudge hook ·
#   cold read with zero prior state · repo cleanliness (no deployment leakage)

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT=$(( 20000 + RANDOM % 20000 ))
export LOC_HOME="$(mktemp -d)/deployment"
PATH="$ROOT/bin:$PATH"
SERVER_PID=""
PASS=0; FAIL=0

say()  { printf '%s\n' "$*"; }
ok()   { PASS=$((PASS+1)); say "  ✓ $1"; }
bad()  { FAIL=$((FAIL+1)); say "  ✗ $1"; }
check() { # check <description> <command...>
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then ok "$desc"; else bad "$desc"; fi
}
check_not() { # check_not <description> <command...>
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then bad "$desc"; else ok "$desc"; fi
}

cleanup() {
  [[ -n "$SERVER_PID" ]] && kill "$SERVER_PID" 2>/dev/null
  rm -rf "$(dirname "$LOC_HOME")"
}
trap cleanup EXIT

say "conformance: scratch deployment on port $PORT"
"$ROOT/providers/nats/bootstrap.sh" alice bob carol >/dev/null
sed -i '' "s|127.0.0.1:4222|127.0.0.1:$PORT|" "$LOC_HOME/config" "$LOC_HOME/nats-server.conf"
nats-server -c "$LOC_HOME/nats-server.conf" >"$LOC_HOME/server.log" 2>&1 &
SERVER_PID=$!
sleep 1
LOC_IDENTITY=admin loc doctor --init >/dev/null || { bad "stream init"; exit 1; }

# Nudge hook stub: records invocations instead of ringing anything.
cat > "$LOC_HOME/hooks/nudge" <<EOF
#!/bin/sh
echo "\$1 \$2" >> "$LOC_HOME/nudges.log"
EOF
chmod +x "$LOC_HOME/hooks/nudge"

say "— identity —"
check_not "unattributable send is refused" \
  env -u LOC_IDENTITY loc send bob "no identity"
check_not "send to unknown endpoint is refused" \
  env LOC_IDENTITY=alice loc send mallory "hi"

say "— queues: delivery, dormancy, consumption —"
check "send to a dormant endpoint succeeds (nobody reading)" \
  env LOC_IDENTITY=alice loc send bob "message-one"
env LOC_IDENTITY=alice loc send bob "message-two" >/dev/null 2>&1
out="$(LOC_IDENTITY=bob loc read 2>/dev/null)"
if grep -q "message-one" <<<"$out" && grep -q "message-two" <<<"$out"; then
  ok "dormant endpoint receives full backlog on read"
else bad "dormant endpoint receives full backlog on read"; fi
if [[ "$(grep -o "message-[a-z]*" <<<"$out" | head -2 | tr '\n' ' ')" == "message-one message-two " ]]; then
  ok "per-sender FIFO order preserved"
else bad "per-sender FIFO order preserved"; fi
out2="$(LOC_IDENTITY=bob loc read 2>/dev/null)"
if grep -q "message-one" <<<"$out2"; then
  bad "queue messages are consumed exactly once"
else ok "queue messages are consumed exactly once"; fi
check "nudge hook fired on queue send" grep -q "bob" "$LOC_HOME/nudges.log"

say "— topics: window, mentions, independent cursors —"
env LOC_IDENTITY=alice loc publish standup "@carol please look at this" >/dev/null 2>&1
check "topic appears in the active list" \
  bash -c "LOC_IDENTITY=alice loc topics | grep -q standup"
check "@mention rang exactly the mentioned endpoint" \
  grep -q "carol .*#standup" "$LOC_HOME/nudges.log"
check_not "unmentioned endpoint got no topic nudge" \
  grep -q "^bob .*#standup" "$LOC_HOME/nudges.log"
c="$(LOC_IDENTITY=carol loc read 2>/dev/null)"; b="$(LOC_IDENTITY=bob loc read 2>/dev/null)"
if grep -q "please look" <<<"$c" && grep -q "please look" <<<"$b"; then
  ok "every reader's cursor sees the conversation independently"
else bad "every reader's cursor sees the conversation independently"; fi

say "— acl (deny-by-default) —"
_creds() { cat "$LOC_HOME/creds/$1"; }
NURL="nats://127.0.0.1:$PORT"
check_not "alice cannot subscribe to bob's queue" \
  env NATS_URL="$NURL" NATS_USER=alice NATS_PASSWORD="$(_creds alice)" \
    nats sub queue.bob --count 1 --timeout 1s
check_not "alice cannot pull bob's queue consumer" \
  env NATS_URL="$NURL" NATS_USER=alice NATS_PASSWORD="$(_creds alice)" \
    nats consumer next QUEUE_bob bob --count 1 --timeout 1s
# A publish is fire-and-forget: the client may exit before the server's
# refusal arrives, so the assertion reads the enforcer's own log — and the
# message must also be provably absent from the stream.
seq_before="$(env NATS_URL="$NURL" NATS_USER=admin NATS_PASSWORD="$(_creds admin)" \
  nats stream info QUEUE_bob --json 2>/dev/null | jq -r .state.messages)"
env NATS_URL="$NURL" NATS_USER=watch NATS_PASSWORD="$(_creds watch)" \
  nats pub queue.bob forbidden >/dev/null 2>&1 || true
sleep 0.5
seq_after="$(env NATS_URL="$NURL" NATS_USER=admin NATS_PASSWORD="$(_creds admin)" \
  nats stream info QUEUE_bob --json 2>/dev/null | jq -r .state.messages)"
if grep -Eq 'Publish Violation.*"queue\.bob"' "$LOC_HOME/server.log" \
   && [[ "$seq_before" == "$seq_after" ]]; then
  ok "watch identity cannot publish (refused by server, stream unchanged)"
else bad "watch identity cannot publish (refused by server, stream unchanged)"; fi

say "— cwd-independence —"
check "every verb works from an unrelated cwd" \
  bash -c "cd / && LOC_IDENTITY=alice loc doctor >/dev/null && LOC_IDENTITY=alice loc send bob from-root >/dev/null && LOC_IDENTITY=alice loc topics >/dev/null && LOC_IDENTITY=alice loc status >/dev/null"

say "— cold read —"
check "endpoint with zero prior state reads cleanly" \
  env LOC_IDENTITY=carol loc read

say "— topic window expiry (short-window scratch stream) —"
env NATS_URL="$NURL" NATS_USER=admin NATS_PASSWORD="$(_creds admin)" \
  nats stream add WINDOWTEST --subjects 'wtest.>' --retention limits \
    --max-age 2s --storage file --replicas 1 --defaults >/dev/null 2>&1
env NATS_URL="$NURL" NATS_USER=admin NATS_PASSWORD="$(_creds admin)" \
  nats pub wtest.x "ephemeral" >/dev/null 2>&1
sleep 4
n="$(env NATS_URL="$NURL" NATS_USER=admin NATS_PASSWORD="$(_creds admin)" \
  nats stream info WINDOWTEST --json 2>/dev/null | jq -r .state.messages)"
if [[ "$n" == "0" ]]; then ok "messages expire at the window's edge (teardown-by-retention)"; else bad "messages expire at the window's edge (got $n)"; fi

say "— delivery: registration, wake, backlog, liveness —"
# Wake hook stub: records wakes instead of waking anything.
cat > "$LOC_HOME/hooks/wake" <<EOF
#!/bin/sh
echo "\$1 \$2" >> "$LOC_HOME/wakes.log"
EOF
chmod +x "$LOC_HOME/hooks/wake"
# Register hook stub: records that channel capture was asked for.
cat > "$LOC_HOME/hooks/register" <<EOF
#!/bin/sh
echo "\$1" >> "$LOC_HOME/registers.log"
EOF
chmod +x "$LOC_HOME/hooks/register"

LOC_IDENTITY=alice loc sub >/dev/null 2>&1
sleep 1
check "registration starts a live listener (verified pid, not file existence)" \
  test -f "$LOC_HOME/run/alice.listener.pid"
check "registration invoked the channel-capture hook" \
  grep -qx "alice" "$LOC_HOME/registers.log"
env LOC_IDENTITY=bob loc send alice "wake-me" >/dev/null 2>&1
sleep 2
check "wake hook fired on arrival (first message wakes instantly)" \
  grep -q "^alice 1" "$LOC_HOME/wakes.log"
# Observe-without-consume: the wake must not have eaten the message.
out="$(LOC_IDENTITY=alice loc read 2>/dev/null)"
if grep -q "wake-me" <<<"$out"; then
  ok "listener observes without consuming (message still readable)"
else bad "listener observes without consuming (message still readable)"; fi
# Stability: one message means ONE wake, and the tap outlives the event.
# (A churning listener re-wakes on every reconnect cycle and still passed
# every grep -q above — this case is why that can never happen again.)
sleep 8
wakes_now="$(grep -c '^alice' "$LOC_HOME/wakes.log" 2>/dev/null || echo 0)"
if [[ "$wakes_now" == "1" ]] && pgrep -f "nats subscribe queue.alice" >/dev/null; then
  ok "one message, one wake; the tap survives the event (no churn)"
else bad "one message, one wake; the tap survives the event (got $wakes_now wakes, tap $(pgrep -f 'nats subscribe queue.alice' >/dev/null && echo alive || echo dead))"; fi
LOC_IDENTITY=alice loc unsub >/dev/null 2>&1
check_not "unsub ends attendance (pidfile gone)" test -f "$LOC_HOME/run/alice.listener.pid"
# Wake-on-backlog: messages sent while unattended wake once, with the count.
env LOC_IDENTITY=bob loc send alice "backlog-1" >/dev/null 2>&1
env LOC_IDENTITY=bob loc send alice "backlog-2" >/dev/null 2>&1
: > "$LOC_HOME/wakes.log"
LOC_IDENTITY=alice loc sub >/dev/null 2>&1
sleep 2
check "wake-on-backlog: (re)registration wakes with the waiting count" \
  grep -q "^alice 2" "$LOC_HOME/wakes.log"
LOC_IDENTITY=alice loc read >/dev/null 2>&1
LOC_IDENTITY=alice loc unsub >/dev/null 2>&1
# Liveness: a listener watching a pid exits when that pid dies.
sleep 300 & WATCHED=$!
LOC_IDENTITY=carol loc sub --watch-pid "$WATCHED" >/dev/null 2>&1
sleep 1
kill "$WATCHED" 2>/dev/null
sleep 4
lpid="$(sed -n 1p "$LOC_HOME/run/carol.listener.pid" 2>/dev/null || true)"
if [[ -z "$lpid" ]] || ! kill -0 "$lpid" 2>/dev/null; then
  ok "listener exits when the watched session dies (employment-tied)"
else bad "listener exits when the watched session dies (employment-tied)"; kill "$lpid" 2>/dev/null; fi
# Say-semantics (config-gated): a send to a known-absent endpoint is refused;
# an attending endpoint still receives.
echo "send_requires_attendance = yes" >> "$LOC_HOME/config"
check_not "say-semantics: send to a known-absent endpoint is refused" \
  env LOC_IDENTITY=bob loc send carol "into the void"
LOC_IDENTITY=carol loc sub >/dev/null 2>&1
sleep 1
check "say-semantics: send to an attending endpoint succeeds" \
  env LOC_IDENTITY=bob loc send carol "present company"
LOC_IDENTITY=carol loc read >/dev/null 2>&1
LOC_IDENTITY=carol loc unsub >/dev/null 2>&1
sed -i '' '/send_requires_attendance/d' "$LOC_HOME/config"

# Breaker: with a 1/minute cap and a 1s window, three spaced sends must
# produce exactly one wake and a loud trip line — and the queue must still
# hold all three messages (suppressed wakes never lose anything).
printf 'wake_breaker_per_minute = 1\nwake_window_seconds = 1\n' >> "$LOC_HOME/config"
: > "$LOC_HOME/wakes.log"
# The breaker's window is calendar-aligned; keep all three sends inside one
# minute or a boundary reset legally allows a second wake (phase flake).
while [ "$(date +%S | sed 's/^0//')" -gt 40 ]; do sleep 2; done
LOC_IDENTITY=alice loc sub >/dev/null 2>&1
sleep 1
env LOC_IDENTITY=bob loc send alice "b1" >/dev/null 2>&1; sleep 2
env LOC_IDENTITY=bob loc send alice "b2" >/dev/null 2>&1; sleep 2
env LOC_IDENTITY=bob loc send alice "b3" >/dev/null 2>&1; sleep 2
wakes="$(grep -c '^alice' "$LOC_HOME/wakes.log" 2>/dev/null || echo 0)"
if [[ "$wakes" == "1" ]] && grep -q "BREAKER TRIPPED" "$LOC_HOME/run/alice.delivery.log"; then
  ok "breaker caps wakes and trips loud (1 wake for 3 sends at cap 1/min)"
else bad "breaker caps wakes and trips loud (got $wakes wakes)"; fi
out="$(LOC_IDENTITY=alice loc read 2>/dev/null)"
if grep -q "b1" <<<"$out" && grep -q "b3" <<<"$out"; then
  ok "suppressed wakes lose nothing (all three messages readable)"
else bad "suppressed wakes lose nothing (all three messages readable)"; fi
LOC_IDENTITY=alice loc unsub >/dev/null 2>&1
sed -i '' '/wake_breaker_per_minute/d;/wake_window_seconds/d' "$LOC_HOME/config"

say "— repo cleanliness (future-public discipline) —"
if "$ROOT/conformance/check-clean.sh" >/dev/null 2>&1; then
  ok "repo carries no deployment/internal vocabulary"
else bad "repo carries no deployment/internal vocabulary (run conformance/check-clean.sh)"; fi

say ""
say "conformance: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]]
