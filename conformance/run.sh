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

say "— repo cleanliness (future-public discipline) —"
if "$ROOT/conformance/check-clean.sh" >/dev/null 2>&1; then
  ok "repo carries no deployment/internal vocabulary"
else bad "repo carries no deployment/internal vocabulary (run conformance/check-clean.sh)"; fi

say ""
say "conformance: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]]
