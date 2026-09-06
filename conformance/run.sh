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
# Which `loc` the suite drives. Defaults to the tree's own bin/, so an ordinary
# run is unchanged. The Go port points this at its build output and runs THIS
# IDENTICAL FILE against the new binary — the suite is the gate for both
# implementations, so it must not name one of them.
LOC_BIN_DIR="${LOC_BIN_DIR:-$ROOT/bin}"
PORT=$(( 20000 + RANDOM % 20000 ))
# The monitor port moves with the client port: a scratch server must not
# collide with the operator's own deployment on the default 8222.
MPORT=$(( PORT + 1 ))
# The operator's real house, remembered BEFORE the scratch one replaces it: the
# cleanliness check reads its vocabulary list from there, so the suite must
# still check the tree against the deployment the machine actually runs.
REAL_LOC_HOME="${LOC_HOME:-$HOME/.locutorium}"
export LOC_HOME="$(mktemp -d)/deployment"
PATH="$LOC_BIN_DIR:$PATH"
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
sed -i '' -e "s|127.0.0.1:4222|127.0.0.1:$PORT|" -e "s|127.0.0.1:8222|127.0.0.1:$MPORT|" \
  "$LOC_HOME/config" "$LOC_HOME/nats-server.conf"
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

say "— reading must not destroy what it failed to show —"
# Consumption is irreversible (workqueue retention: the ack is a delete), so a
# read that fetches but fails to present must not be a deletion. Regression
# cover for a defect that destroyed five real messages: the ack happened inside
# a `fetch | render` pipeline, so a broken pipe or a renderer error consumed the
# message and showed nobody anything.
env LOC_IDENTITY=alice loc send bob "loss-canary" >/dev/null 2>&1
# Reader dies after one byte; every later write gets SIGPIPE mid-render.
LOC_IDENTITY=bob loc read 2>/dev/null | head -c 1 >/dev/null 2>&1
out3="$(LOC_IDENTITY=bob loc read 2>/dev/null)"
if grep -q "loss-canary" <<<"$out3"; then
  ok "a read that fails mid-render re-presents the message instead of losing it"
else bad "a read that fails mid-render re-presents the message instead of losing it"; fi

# --peek is the non-destructive read. It previously drained TOPICS with --ack,
# so peeking silently destroyed topic messages.
env LOC_IDENTITY=alice loc send bob "peek-canary" >/dev/null 2>&1
env LOC_IDENTITY=alice loc publish standup "peek-topic-canary" >/dev/null 2>&1
LOC_IDENTITY=bob loc read --peek >/dev/null 2>&1
# The guarantee is that peeking never DESTROYS. Note the sharp edge this poll
# exposes: a peeked message goes in-flight for the consumer's ack_wait, so an
# immediate real read shows an EMPTY queue and an agent reasonably concludes the
# message was lost. It returns on redelivery. Non-destructive, but the window is
# indistinguishable from loss at the moment it matters, and it is a live
# suspect for the "nudge says 1 new, read shows nothing" reports.
# ACCUMULATE across polls. Assigning each iteration would discard the topic
# line, which only ever appears in the first read (topics are consumed there),
# and the topic assertion below would then fail for a reason that has nothing
# to do with peeking.
out4=""
for _i in $(seq 1 40); do
  out4="$out4
$(LOC_IDENTITY=bob loc read 2>/dev/null)"
  grep -q "peek-canary" <<<"$out4" && break
  sleep 1
done
if grep -q "peek-canary" <<<"$out4"; then
  ok "--peek never destroys the queue message (returns on redelivery)"
else bad "--peek never destroys the queue message (returns on redelivery)"; fi
if grep -q "peek-topic-canary" <<<"$out4"; then
  ok "--peek does not consume topic messages"
else bad "--peek does not consume topic messages"; fi

say "— message size is a guarantee, not a suggestion —"
# This carries conversation, not documents. The limit is in CHARACTERS: an emoji is
# four bytes, so a byte limit would refuse messages that look perfectly ordinary to
# whoever wrote them. Refusal happens at send, before the medium sees anything, and a
# warning-then-send-anyway is not a limit.
AT_LIMIT="$(python3 -c 'print("x"*4000)')"
check "a body at the limit is accepted" \
  env LOC_IDENTITY=alice loc send bob "$AT_LIMIT"
OVER="OVERSIZE-CANARY$(python3 -c 'print("x"*4001)')"
check_not "a body over the limit is refused at send" \
  env LOC_IDENTITY=alice loc send bob "$OVER"
# The refusal must be real: nothing may have reached the medium.
out_sz="$(LOC_IDENTITY=bob loc read 2>/dev/null)"
if grep -q "OVERSIZE-CANARY" <<<"$out_sz"; then
  bad "a refused body never reaches the queue"
else ok "a refused body never reaches the queue"; fi
# 3999 emoji is ~16000 bytes. Accepted on characters, refused on bytes: this case is
# the entire reason the unit was chosen, so it fails loudly if someone "optimises" the
# counter into ${#var} or wc -c.
EMOJI_BODY="$(python3 -c 'print("\U0001F534"*3999)')"
check "an emoji body under the character limit is accepted (not judged by bytes)" \
  env LOC_IDENTITY=alice loc send bob "$EMOJI_BODY"
LOC_IDENTITY=bob loc read >/dev/null 2>&1

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

say "— the CLI finds its own house —"
# loc is reached through a link on PATH. Whatever shape that link takes, loc
# must find the tree it belongs to; a bare copy has no tree and must say so.
L="$(dirname "$LOC_HOME")/links"; mkdir -p "$L/bin"
# Relative links are resolved by the kernel against PHYSICAL paths; a relpath computed
# from a logical path under a symlinked directory dangles before loc ever runs.
rel="$(python3 -c 'import os,sys;print(os.path.relpath(os.path.realpath(sys.argv[1]),os.path.realpath(sys.argv[2])))' "$LOC_BIN_DIR/loc" "$L")"
ln -s "$rel" "$L/loc-rel"
check "a relative symlink to loc finds its house" env LOC_IDENTITY=alice "$L/loc-rel" topics
ln -s "$LOC_BIN_DIR/loc" "$L/hop1"; ln -s hop1 "$L/hop2"
check "a two-hop symlink to loc finds its house" env LOC_IDENTITY=alice "$L/hop2" topics
cp "$LOC_BIN_DIR/loc" "$L/loc-copy"
check_not "a copied loc refuses to run" env LOC_IDENTITY=alice "$L/loc-copy" topics
copy_out="$(env LOC_IDENTITY=alice "$L/loc-copy" topics 2>&1 || true)"
if grep -q "copy, not a link" <<<"$copy_out"; then ok "…and says why (copy, not a link)"; else bad "…and says why (copy, not a link)"; fi

say "— the installer links loc and leaves when told —"
check "install.sh links loc into a prefix (no service)" \
  "$ROOT/install.sh" --prefix "$L/bin" --no-service
check "the installed link runs loc" env LOC_IDENTITY=alice "$L/bin/loc" topics
inst2="$("$ROOT/install.sh" --prefix "$L/bin" --no-service 2>&1 || true)"
if grep -q "nothing to do" <<<"$inst2"; then ok "a second install has nothing to do"; else bad "a second install has nothing to do"; fi
"$ROOT/install.sh" --uninstall --prefix "$L/bin" --no-service >/dev/null 2>&1 || true
check_not "uninstall removes the link it made" test -e "$L/bin/loc"

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
# The verb is documented in four places and, until the deployment gained a
# monitor port, could not work on anything bootstrap.sh produced — it worked
# only where somebody had hand-edited the server config. Nothing tested it, so
# nothing said so. Alice is attending by now, so registry must name her.
reg_out="$(env LOC_IDENTITY=alice loc registry 2>&1 || true)"
if grep -q 'alice' <<<"$reg_out"; then
  ok "registry names an attending endpoint on a freshly bootstrapped deployment"
else
  bad "registry names an attending endpoint on a freshly bootstrapped deployment"
  say "    registry said: $reg_out"
fi
env LOC_IDENTITY=bob loc send alice "wake-me" >/dev/null 2>&1
sleep 2
check "wake hook fired on arrival (first message wakes instantly)" \
  grep -q "^alice 1" "$LOC_HOME/wakes.log"
# A wake must never make a message unreadable. The listener drains the queue
# into a spool for the deployment hook to present (consuming first is what
# stops the hook racing a reader), and `loc read` presents that spool too —
# so wherever the body sits at this instant (queue, raw spool, rendered
# spool), the read must show it. This replaced "listener observes without
# consuming", which asserted the pre-drain design.
out="$(LOC_IDENTITY=alice loc read 2>/dev/null)"
if grep -q "wake-me" <<<"$out"; then
  ok "a wake never makes a message unreadable (read presents queue and spool)"
else bad "a wake never makes a message unreadable (read presents queue and spool)"; fi
# Stability: one message means ONE wake, and the tap outlives the event.
# (A churning listener re-wakes on every reconnect cycle and still passed
# every grep -q above — this case is why that can never happen again.)
sleep 8
wakes_now="$(grep -c '^alice' "$LOC_HOME/wakes.log" 2>/dev/null || true)"; wakes_now="${wakes_now:-0}"
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
wakes="$(grep -c '^alice' "$LOC_HOME/wakes.log" 2>/dev/null || true)"; wakes="${wakes:-0}"
if [[ "$wakes" == "1" ]] && grep -q "BREAKER TRIPPED" "$LOC_HOME/run/alice.delivery.log"; then
  ok "breaker caps wakes and trips loud (1 wake for 3 sends at cap 1/min)"
else bad "breaker caps wakes and trips loud (got $wakes wakes)"; fi
out="$(LOC_IDENTITY=alice loc read 2>/dev/null)"
if grep -q "b1" <<<"$out" && grep -q "b3" <<<"$out"; then
  ok "suppressed wakes lose nothing (all three messages readable)"
else bad "suppressed wakes lose nothing (all three messages readable)"; fi
LOC_IDENTITY=alice loc unsub >/dev/null 2>&1
sed -i '' '/wake_breaker_per_minute/d;/wake_window_seconds/d' "$LOC_HOME/config"

say "— rapid unsub/resub leaves no orphan listener —"
# A dying listener's cleanup runs up to a read-tick (~2s) after the kill. If a
# successor registers inside that window, cleanup must not remove the
# successor's pidfile — an orphaned listener is invisible to the liveness
# check, unkillable by unsub, and wakes forever beside the next registration.
LOC_IDENTITY=alice loc sub >/dev/null 2>&1
LOC_IDENTITY=alice loc unsub >/dev/null 2>&1
LOC_IDENTITY=alice loc sub >/dev/null 2>&1   # registers inside the death window
sleep 3                                       # let the first listener finish dying
LOC_IDENTITY=alice loc unsub >/dev/null 2>&1  # must actually kill the successor
: > "$LOC_HOME/wakes.log"
env LOC_IDENTITY=bob loc send alice "orphan-bait" >/dev/null 2>&1
sleep 8
if ! grep -q '^alice' "$LOC_HOME/wakes.log"; then
  ok "rapid unsub/resub leaves no orphan listener (no wake after final unsub)"
else bad "rapid unsub/resub leaves no orphan listener (no wake after final unsub)"; fi
LOC_IDENTITY=alice loc read >/dev/null 2>&1   # drain the bait

say "— the pidfile is the listener's licence to live —"
# The pidfile is not a RECORD of the listener, it is its MANDATE: remove the
# entry and the listener must go, with no signal sent to it at all. That is the
# property `unsub` needs, because its kill can miss — the listener may be
# seconds deep in a drain, or a generation the pidfile never named — and before
# this, the rm that followed simply deleted the last handle to it. The captured
# survivor (docs/NOTES-orphan-listener.md) held an unlinked fifo, a tap whose
# server was dead, and no file on disk naming it: nothing could reach it again.
#
# No signal is sent here on purpose. A case that unsubs would pass on the kill
# alone and prove nothing about the mandate.
LOC_IDENTITY=alice loc sub >/dev/null 2>&1
sleep 1
rm -f "$LOC_HOME/run/alice.listener.pid"
sleep 5
# macOS pgrep has no -c. Count only listeners from THIS tree: the operator's own
# deployment may be attending on the same machine and must not be counted, and
# must certainly not be killed.
survivors="$(pgrep -fl 'loc sub' 2>/dev/null | grep -c "$LOC_BIN_DIR/loc" || true)"
survivors="${survivors:-0}"
# Leaving is not enough: it must leave nothing behind. The fifo is named for the
# generation that made it, so one left here is one left forever — nothing will
# ever reuse that name. Counted by glob, not by ls|grep, so "no match" is simply
# a path that does not exist.
stale_fifos=0
for f in "$LOC_HOME/run/"alice*.listen.fifo; do [[ -e "$f" ]] && stale_fifos=$((stale_fifos+1)); done
if [[ "$survivors" == "0" ]] && ! pgrep -f "nats subscribe queue.alice" >/dev/null 2>&1 \
   && [[ "$stale_fifos" == "0" ]]; then
  ok "listener exits within 5 s when its pidfile is removed"
else
  bad "listener exits within 5 s when its pidfile is removed ($survivors listener(s) from this tree, tap $(pgrep -f 'nats subscribe queue.alice' >/dev/null && echo alive || echo dead), $stale_fifos stale fifo(s))"
  rm -f "$LOC_HOME/run/"alice*.listen.fifo
  # Do not leave the orphan behind for the next run to trip over. Again: only
  # processes whose command line names this tree. Its tap goes with it — the
  # listener's TERM trap reaps its own children.
  pgrep -fl 'loc sub' 2>/dev/null | grep "$LOC_BIN_DIR/loc" | while IFS= read -r _p; do
    kill "${_p%% *}" 2>/dev/null
  done
fi

say "— attendance leaves nothing behind on disk —"
# The case above covers the eviction path. This one covers the path the machine
# actually walks a dozen times a day: plain sub, plain unsub. Both end in the
# same cleanup, but they reach it differently, and a future change could easily
# tidy the fifo on one path only — so the everyday path gets its own case.
#
# THREE CYCLES, because one proves nothing here. A single stale fifo is a
# harmless leftover that the next generation would once have reclaimed; what
# makes it a defect is that per-generation names never repeat, so the count
# GROWS. Guarded, this counted 1, then 2, then 3.
#
# The sub count is asserted too: with no listeners ever started there would be
# no fifos to leak, and this case would pass by doing nothing.
subs_ok=0
for _n in 1 2 3; do
  LOC_IDENTITY=alice loc sub >/dev/null 2>&1 && subs_ok=$((subs_ok+1))
  sleep 2
  LOC_IDENTITY=alice loc unsub >/dev/null 2>&1
  sleep 3
done
stale_fifos=0
for f in "$LOC_HOME/run/"alice*.listen.fifo; do [[ -e "$f" ]] && stale_fifos=$((stale_fifos+1)); done
if [[ "$stale_fifos" == "0" ]] && [[ "$subs_ok" == "3" ]]; then
  ok "repeated sub/unsub leaves no stale fifo behind"
else
  bad "repeated sub/unsub leaves no stale fifo behind ($stale_fifos after 3 cycles, $subs_ok/3 subs registered)"
  rm -f "$LOC_HOME/run/"alice*.listen.fifo
fi

say "— a stranded wake spool is recovered by the next read —"
# A listener that died between fetching (acked = deleted from the queue) and
# rendering leaves raw envelopes in its wake spool. Those bodies exist nowhere
# else; the next read must present them rather than show an empty mailbox.
printf '%s\n' '{"id":"x","ts":"2026-08-19T00:00:00Z","from":"bob","to":"alice","kind":"msg","body":"stranded-in-wake-spool"}' \
  >> "$LOC_HOME/run/alice.wake.spool.raw"
out="$(LOC_IDENTITY=alice loc read 2>/dev/null)"
if grep -q "stranded-in-wake-spool" <<<"$out"; then
  ok "stranded wake-spool bodies are presented by the next read"
else bad "stranded wake-spool bodies are presented by the next read"; fi
# And presenting forgot it: claimed and removed, so a second read is clean.
out="$(LOC_IDENTITY=alice loc read 2>/dev/null)"
if ! grep -q "stranded-in-wake-spool" <<<"$out"; then
  ok "a presented wake-spool body is forgotten, not re-presented"
else bad "a presented wake-spool body is forgotten, not re-presented"; fi

say "— the front page shows what the tool prints —"
# README's "Two agents, one conversation" block is captured, not typed. This case
# replays its five commands in a fresh house (ada/bob/carol, own port, own server)
# and diffs the output against the block with timestamps masked. If the tool's
# output ever moves, the page moves with it or this fails.
DEMO_HOME="$(mktemp -d)/house"; DEMO_PORT=$(( 20000 + RANDOM % 20000 )); DEMO_MPORT=$(( DEMO_PORT + 1 ))
LOC_HOME="$DEMO_HOME" "$ROOT/providers/nats/bootstrap.sh" ada bob carol >/dev/null
sed -i '' -e "s|127.0.0.1:4222|127.0.0.1:$DEMO_PORT|" -e "s|127.0.0.1:8222|127.0.0.1:$DEMO_MPORT|" \
  "$DEMO_HOME/config" "$DEMO_HOME/nats-server.conf"
nats-server -c "$DEMO_HOME/nats-server.conf" >"$DEMO_HOME/server.log" 2>&1 &
DEMO_PID=$!; sleep 1
LOC_HOME="$DEMO_HOME" LOC_IDENTITY=admin loc doctor --init >/dev/null 2>&1
mask() { sed -E 's/[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z/<ts>/g'; }
expected="$(awk '/^```console$/{f=1;next} f&&/^```$/{exit} f' "$ROOT/README.md" | mask)"
actual="$(
  while IFS= read -r c; do
    printf '$ %s\n' "$c"; LOC_HOME="$DEMO_HOME" bash -c "$c" 2>&1
  done <<<"$(awk '/^```console$/{f=1;next} f&&/^```$/{exit} f&&/^\$ /{sub(/^\$ /,"");print}' "$ROOT/README.md")" | mask)"
if [[ -n "$expected" && "$expected" == "$actual" ]]; then
  ok "README transcript equals a fresh run (timestamps masked)"
else
  bad "README transcript equals a fresh run (timestamps masked)"
  diff <(printf '%s\n' "$expected") <(printf '%s\n' "$actual") | head -20 | sed 's/^/      /'
fi
kill "$DEMO_PID" 2>/dev/null; rm -rf "$(dirname "$DEMO_HOME")"

say "— one version, stated once —"
if "$ROOT/conformance/check-version.sh" >/dev/null 2>&1; then
  ok "VERSION, loc version, docs and tag agree"
else bad "VERSION, loc version, docs and tag agree (run conformance/check-version.sh)"; fi

say "— repo cleanliness (future-public discipline) —"
say "  (cleanliness list read from: $REAL_LOC_HOME/forbidden)"
if LOC_FORBIDDEN_FILE="$REAL_LOC_HOME/forbidden" "$ROOT/conformance/check-clean.sh" >/dev/null 2>&1; then
  ok "repo carries no deployment/internal vocabulary"
else bad "repo carries no deployment/internal vocabulary (run conformance/check-clean.sh)"; fi
# With no list on the machine at all the check must still run and still pass on
# a clean tree — a missing list is a weaker check, never a hard failure.
check "cleanliness check runs on its generic list when no list is installed" \
  env LOC_FORBIDDEN_FILE=/nonexistent "$ROOT/conformance/check-clean.sh"
# And it must actually FAIL on a dirty tree. Asserting pass-only proves nothing:
# a checker that always exits 0 passes that. A scratch tree, one file holding a
# nonce word, a list naming that nonce — the check has to find it. The copy of
# the script sits at <tree>/conformance/ because the check walks up one level
# from itself to decide what tree it is checking.
CLEAN_T="$(mktemp -d)"; mkdir -p "$CLEAN_T/conformance"
cp "$ROOT/conformance/check-clean.sh" "$CLEAN_T/conformance/check-clean.sh"
CLEAN_NONCE="zzq$(( RANDOM ))vocab"
printf 'a line that says %s and should not survive review\n' "$CLEAN_NONCE" > "$CLEAN_T/leaky.md"
printf '# scratch list\n%s\n' "$CLEAN_NONCE" > "$CLEAN_T/list"
check_not "cleanliness check fails on a tree that carries a listed word" \
  env LOC_FORBIDDEN_FILE="$CLEAN_T/list" "$CLEAN_T/conformance/check-clean.sh"
rm -rf "$CLEAN_T"

say ""
say "conformance: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]]
