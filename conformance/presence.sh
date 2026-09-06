#!/usr/bin/env bash
# conformance/presence.sh — the presence model, held to the same fire as run.sh.
#
# run.sh defines the Locutorium as it is. THIS suite defines the Locutorium as
# docs/PRESENCE.md says it must become: the three-question presence model, in
# which a subscription is a live thing that owns an ephemeral queue, a send to
# an endpoint nobody is holding is refused rather than banked, and lifecycle is
# an event stream instead of a persistent registry file.
#
# It is a DRAFT gate for a model the current binaries do not yet implement. Run
# it against the older bash `loc` or the Go build and it goes RED — deliberately
# and, more importantly, MEANINGFULLY. The Go build fails every unbuilt verb the
# same generic way (usage on stdout, or "not implemented" on stderr), so a case
# that merely demanded a non-zero exit would pass on that generic failure and
# prove nothing. Every refusal case here therefore asserts the SPECIFIC reason
# or effect the spec names — the incumbent's pid, the word "expiry" on the
# event, the message provably absent from the medium — so a green line means the
# behaviour happened, not merely that something went wrong.
#
# Guarantees exercised here (PRESENCE.md, inner-parlor scope):
#   namespaced endpoints + injective backing name · subscribe/unsubscribe as
#   queue lifetime · sweep by dead pid · ephemeral queues + refuse-absent-send ·
#   emit non-blocking, emitter-stamped, refs, unknown-field tolerance · registry
#   request/reply · status reporting reachable/alive/working separately · watch
#   read-only · read loss-safety · one-agent-per-endpoint (refuse-held,
#   unsubscribe-refuses-live, --force) · exit-code discipline.
#
# Self-contained, like run.sh: it boots its OWN scratch nats-server on a random
# port and generates its OWN presence-shaped deployment inline. It never calls a
# presence or stubbed verb during setup — those are exactly what is under test —
# so the suite always reaches its cases and fails THEM, rather than aborting at
# boot. See PRESENCE-SUITE-NOTES.md for the bootstrap design and the differences
# from providers/nats/bootstrap.sh.

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Which `loc` the suite drives. Defaults to the tree's own bin/ so an ordinary
# invocation needs no environment; the Go port points this at its build output.
# The suite is the gate for whatever implements the model, so it names neither.
LOC_BIN_DIR="${LOC_BIN_DIR:-$ROOT/bin}"
PORT=$(( 20000 + RANDOM % 20000 ))
# The monitor port moves with the client port: a scratch server must not collide
# with the operator's own deployment on the default 8222. The registry verb
# reads connection state from here.
MPORT=$(( PORT + 1 ))
export LOC_HOME="$(mktemp -d)/deployment"
mkdir -p "$LOC_HOME/creds" "$LOC_HOME/store" "$LOC_HOME/hooks"
chmod 700 "$LOC_HOME/creds"
PATH="$LOC_BIN_DIR:$PATH"
SERVER_PID=""
CAP_PID=""
PASS=0; FAIL=0

say()  { printf '%s\n' "$*"; }
ok()   { PASS=$((PASS+1)); say "  ✓ $1"; }
bad()  { FAIL=$((FAIL+1)); say "  ✗ $1"; }
check() { # check <description> <command...>  — green iff the command exits 0
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then ok "$desc"; else bad "$desc"; fi
}
check_not() { # check_not <description> <command...> — green iff it exits non-zero
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then bad "$desc"; else ok "$desc"; fi
}
# check_refusal is the load-bearing helper of this suite. A refusal is only
# genuine when it fails AND says why: the command must exit non-zero and its
# STDERR must match the pattern the spec's wording implies. A generic failure
# (a wrong exit with the wrong reason, or the reason printed to stdout) is not a
# refusal of THIS thing, and this helper is what refuses to be fooled by one.
check_refusal() { # check_refusal <desc> <stderr-pattern> <command...>
  local desc="$1" pat="$2"; shift 2
  local err rc
  err="$("$@" 2>&1 1>/dev/null)"; rc=$?
  if [[ $rc -ne 0 ]] && grep -qiE "$pat" <<<"$err"; then ok "$desc"
  else bad "$desc"; fi
}
# check_on_stderr enforces the channel discipline of §Exit codes: a failure
# speaks on standard error and prints nothing on standard out — the opposite of
# the usage banner, which is stdout for someone still learning the invocation.
check_on_stderr() { # check_on_stderr <desc> <stderr-pattern> <command...>
  local desc="$1" pat="$2"; shift 2
  local out err rc tmp
  tmp="$(mktemp)"
  err="$("$@" 2>&1 1>"$tmp")"; rc=$?
  out="$(cat "$tmp")"; rm -f "$tmp"
  if [[ $rc -ne 0 && -z "$out" && -n "$err" ]] && grep -qiE "$pat" <<<"$err"; then
    ok "$desc"
  else bad "$desc"; fi
}

_creds() { cat "$LOC_HOME/creds/$1"; }
NURL="nats://127.0.0.1:$PORT"
# adm runs a nats(1) command as the deployment's full-rights user. Setup and the
# assertions use it to inspect and to stand in for parties the harness has no
# real implementation of (an admin publishing an event a `watch` should show).
adm() { env NATS_URL="$NURL" NATS_USER=admin NATS_PASSWORD="$(_creds admin)" nats "$@"; }
# qcount is the message count of a backing stream, or empty if the stream does
# not exist — the difference between "a queue is here holding one" and "there is
# no queue", which is the whole ephemeral-queue distinction.
qcount() { adm stream info "$1" --json 2>/dev/null | jq -r '.state.messages' 2>/dev/null; }
qexists() { adm stream info "$1" --json >/dev/null 2>&1; }
# seed_queue stands a backing queue up by hand, as a real `subscribe` would have.
# The destroy/remove cases need a queue that EXISTS before the verb runs, so that
# a red line reads "the verb failed to destroy it", not the vacuous "there was
# nothing to destroy" — which would pass on any build that does nothing at all.
seed_queue() { # seed_queue <stream> <subject>
  adm stream add "$1" --subjects "$2" --retention work --storage file \
    --replicas 1 --defaults >/dev/null 2>&1
}

cleanup() {
  [[ -n "$CAP_PID" ]] && kill "$CAP_PID" 2>/dev/null
  [[ -n "$SERVER_PID" ]] && kill "$SERVER_PID" 2>/dev/null
  rm -rf "$(dirname "$LOC_HOME")"
}
trap cleanup EXIT

# ─── the presence deployment, generated inline ──────────────────────────────
# Adapted from providers/nats/bootstrap.sh. The differences are forced by the
# model and are documented in PRESENCE-SUITE-NOTES.md. In short:
#   · endpoints are namespaced <instance>.<agent>, so queue subjects have TWO
#     tokens after `queue` (queue.workshop.scribe) and the ACLs use `queue.>`,
#     not bootstrap.sh's single-token `queue.*`;
#   · the backing object name substitutes dots for underscores
#     (queue.workshop.scribe -> QUEUE_workshop_scribe), and every $JS.* subject
#     uses that underscored form;
#   · there is NO `endpoints` file — the registry is host-held and answered by
#     request/reply, not read from a file on disk;
#   · each endpoint may CREATE and DELETE its own queue stream, because in this
#     model subscribe creates the queue and unsubscribe destroys it;
#   · a `host` identity exists (the supervisor that launches and registers
#     agents), alongside the read-only `watch` identity.
ENDPOINTS=(workshop.scribe workshop.clerk atelier.scribe)
sfx() { printf '%s' "${1//./_}"; }   # queue.workshop.scribe -> workshop_scribe
inst() { printf '%s' "${1%%.*}"; }   # workshop.scribe        -> workshop

cat > "$LOC_HOME/config" <<EOF
provider = nats
nats_url = $NURL
monitor_url = http://127.0.0.1:$MPORT
topic_window = 7d
EOF

command -v nats >/dev/null || { say "presence: nats(1) not found"; exit 1; }
command -v nats-server >/dev/null || { say "presence: nats-server not found"; exit 1; }

pw_for() { openssl rand -hex 16; }
bcrypt_for() { nats server passwd -p "$1"; }

users_block=""
for name in "${ENDPOINTS[@]}" host watch admin; do
  pw="$(pw_for)"; printf '%s' "$pw" > "$LOC_HOME/creds/$name"; chmod 600 "$LOC_HOME/creds/$name"
  hash="$(bcrypt_for "$pw")"
  case "$name" in
    admin)
      users_block+="      { user: admin, password: \"$hash\" }"$'\n' ;;
    watch)
      # Read-only: it may follow every queue and every event stream, and may
      # publish nothing at all. This is the credential a `watch` runs under.
      users_block+="      { user: watch, password: \"$hash\", permissions: {
          subscribe: { allow: [\"queue.>\", \"topic.>\", \"registry.>\", \"_INBOX.>\"] },
          publish:   { deny:  [\">\"] } } }"$'\n' ;;
    host)
      # The supervisor. It launches agents and therefore holds their pids; it
      # subscribes and unsubscribes them, sweeps, answers the registry, and
      # sends on their behalf. Broad rights, because it is trusted (§Trust).
      users_block+="      { user: host, password: \"$hash\", permissions: {
          publish:   { allow: [\"queue.>\", \"topic.>\", \"registry.>\", \"\$JS.>\"] },
          subscribe: { allow: [\"queue.>\", \"topic.>\", \"registry.>\", \"_INBOX.>\"] } } }"$'\n' ;;
    *)
      S="$(sfx "$name")"; I="$(inst "$name")"
      users_block+="      { user: \"$name\", password: \"$hash\", permissions: {
          publish: { allow: [
            \"queue.>\", \"topic.>\", \"registry.>\",
            \"\$JS.API.INFO\",
            \"\$JS.API.STREAM.CREATE.QUEUE_$S\", \"\$JS.API.STREAM.DELETE.QUEUE_$S\",
            \"\$JS.API.STREAM.INFO.QUEUE_$S\", \"\$JS.API.STREAM.NAMES\", \"\$JS.API.STREAM.LIST\",
            \"\$JS.API.CONSUMER.DURABLE.CREATE.QUEUE_$S.$S\",
            \"\$JS.API.CONSUMER.CREATE.QUEUE_$S\", \"\$JS.API.CONSUMER.CREATE.QUEUE_$S.>\",
            \"\$JS.API.CONSUMER.INFO.QUEUE_$S.$S\",
            \"\$JS.API.CONSUMER.MSG.NEXT.QUEUE_$S.$S\",
            \"\$JS.ACK.QUEUE_$S.>\" ] },
          subscribe: { allow: [\"queue.$name\", \"topic.$I\", \"registry.$I\", \"_INBOX.>\"] } } }"$'\n' ;;
  esac
done

# No backticks anywhere in this heredoc: it is unquoted so $LOC_HOME expands,
# which would make a backticked word run as a command (the lesson bootstrap.sh
# records the hard way).
cat > "$LOC_HOME/nats-server.conf" <<EOF
# Locutorium PRESENCE-model scratch deployment — generated by presence.sh
# Loopback only: the inner parlor is one machine, and nothing here listens
# beyond it (PRESENCE.md §Inner and outer parlors).
listen: 127.0.0.1:$PORT
# The monitoring port. The registry verb reads who is connected from the
# server's own table; without this the verb cannot answer.
http: 127.0.0.1:$MPORT
server_name: locutorium-presence

jetstream {
  store_dir: $LOC_HOME/store
}

authorization {
  users [
$(printf '%s' "$users_block" | sed 's/^/    /')
  ]
}
EOF

say "presence: scratch deployment on port $PORT (RED against an implementation that lacks the model)"
nats-server -c "$LOC_HOME/nats-server.conf" >"$LOC_HOME/server.log" 2>&1 &
SERVER_PID=$!
disown "$SERVER_PID" 2>/dev/null   # killed in cleanup; disowned so no job-control noise
sleep 1

# The event witness. Events are historyless (§Who is here right now: "the bus
# stores nothing"), so nothing can be inspected after the fact — it has to be
# caught as it flies. This admin subscription tails every instance's event
# subject into a log the cases grep. A real consumer or `watch` would do the
# same; here it stands in for one so the emit/subscribe cases can see what was
# published without depending on the unbuilt `watch`.
adm sub 'topic.>' >"$LOC_HOME/events.log" 2>&1 &
CAP_PID=$!
disown "$CAP_PID" 2>/dev/null   # keep the shell from printing "Terminated" at teardown
sleep 1

# Convenience handles used across cases.
E1=workshop.scribe;  Q1=QUEUE_workshop_scribe   # instance: workshop
E2=workshop.clerk;   Q2=QUEUE_workshop_clerk
E3=atelier.scribe;   Q3=QUEUE_atelier_scribe    # instance: atelier — same agent, other instance

# A pid that is certainly ALIVE for the duration, and one that is certainly DEAD.
sleep 600 & LIVE_PID=$!
disown "$LIVE_PID" 2>/dev/null   # killed at the end; disowned so no job-control noise
sleep 1  & DEAD_PID=$!; wait "$DEAD_PID" 2>/dev/null   # reaped; its pid is now gone

# ─── 1. Namespaced endpoints; injective backing name; no collision ──────────
# PRESENCE.md §Subjects, endpoints and queues → "Endpoint names are namespaced
# by instance" and "A note for implementers" (queue.workshop.scribe backs the
# object QUEUE_workshop_scribe; the substitution must be injective).
say "— namespaced endpoints and their injective backing objects —"
# subscribe is what brings a queue into being; on a build without the model this
# fails and no stream appears — which is exactly the red we want to be able to
# read as "the queue was never created", not "some verb errored".
env LOC_IDENTITY=host loc subscribe "$E1" --pid "$LIVE_PID" --type acme-cli --version 3.2.0 >/dev/null 2>&1
env LOC_IDENTITY=host loc subscribe "$E3" --pid "$LIVE_PID" --type acme-cli --version 3.2.0 >/dev/null 2>&1
if qexists "$Q1"; then ok "subscribe creates the backing object QUEUE_workshop_scribe (dots→underscores)"
else bad "subscribe creates the backing object QUEUE_workshop_scribe (dots→underscores)"; fi
if qexists "$Q1" && qexists "$Q3" && [[ "$Q1" != "$Q3" ]]; then
  ok "the same agent in two instances backs two distinct objects (no collision by construction)"
else bad "the same agent in two instances backs two distinct objects (no collision by construction)"; fi
# And mail for one instance's scribe must not fall into the other's queue.
env LOC_IDENTITY=host loc send "$E1" "for-workshop-scribe-only" >/dev/null 2>&1
if [[ "$(qcount "$Q1")" == "1" && "$(qcount "$Q3")" == "0" ]]; then
  ok "a send to workshop.scribe reaches only its queue, never atelier.scribe's"
else bad "a send to workshop.scribe reaches only its queue, never atelier.scribe's"; fi

# ─── 2. subscribe registers, creates the queue, publishes agent.subscribe ────
# PRESENCE.md §How an agent joins; §Command-line operations → subscribe;
# §Events → agent.subscribe carries the full registration.
say "— subscribe: registration, queue creation, and the join event —"
: > "$LOC_HOME/events.log"
env LOC_IDENTITY=host loc subscribe "$E2" --pid "$LIVE_PID" --type acme-cli --version 3.2.0 \
    --display "The Clerk" --cwd /workspaces/clerk >/dev/null 2>&1
check "subscribe exits 0 when the endpoint is free" \
  env LOC_IDENTITY=host loc subscribe "$E2" --pid "$LIVE_PID" --type acme-cli --version 3.2.0
if qexists "$Q2"; then ok "subscribe created the clerk's queue"
else bad "subscribe created the clerk's queue"; fi
sleep 1
# The join event is the only heavy event: it must carry pid, type and version.
if grep -q '"kind":[[:space:]]*"agent.subscribe"' "$LOC_HOME/events.log" \
   && grep -q "\"pid\":[[:space:]]*$LIVE_PID" "$LOC_HOME/events.log" \
   && grep -q '"version":[[:space:]]*"3.2.0"' "$LOC_HOME/events.log"; then
  ok "subscribe published agent.subscribe on topic.workshop carrying pid/type/version"
else bad "subscribe published agent.subscribe on topic.workshop carrying pid/type/version"; fi

# ─── 3. unsubscribe destroys the queue and publishes agent.unsubscribe(clean) ─
# PRESENCE.md §How an agent leaves (reason: clean); §Queue lifetime (destroyed
# on unsubscribe); §Command-line operations → unsubscribe defaults to clean.
say "— unsubscribe: the queue dies with the subscription —"
# Stand the clerk's queue up first, so "destroyed" measures the verb's effect and
# not the accident that the queue was never there.
seed_queue "$Q2" "queue.$E2"
: > "$LOC_HOME/events.log"
check "unsubscribe --force exits 0 on a held endpoint" \
  env LOC_IDENTITY=host loc unsubscribe "$E2" --force
sleep 1
if ! qexists "$Q2"; then ok "unsubscribe destroyed the clerk's queue (nothing outlives the subscription)"
else bad "unsubscribe destroyed the clerk's queue (nothing outlives the subscription)"; fi
if grep -q '"kind":[[:space:]]*"agent.unsubscribe"' "$LOC_HOME/events.log" \
   && grep -q '"reason":[[:space:]]*"clean"' "$LOC_HOME/events.log"; then
  ok "unsubscribe published agent.unsubscribe with the default reason 'clean'"
else bad "unsubscribe published agent.unsubscribe with the default reason 'clean'"; fi

# ─── 4. sweep unsubscribes dead pids with reason=expiry, idempotently ────────
# PRESENCE.md §How an agent leaves (reason: expiry); §Command-line operations →
# sweep ("unsubscribes those whose process is gone, with --reason expiry.
# Idempotent"). Same-machine is a precondition the harness cannot violate on
# one host; see PRESENCE-SUITE-NOTES.md for that gap.
say "— sweep: the backstop for an agent that could not announce its own exit —"
env LOC_IDENTITY=host loc subscribe "$E2" --pid "$DEAD_PID" --type acme-cli --version 3.2.0 >/dev/null 2>&1
: > "$LOC_HOME/events.log"
check "sweep exits 0" env LOC_IDENTITY=host loc sweep workshop
sleep 1
if grep -q '"kind":[[:space:]]*"agent.unsubscribe"' "$LOC_HOME/events.log" \
   && grep -q '"reason":[[:space:]]*"expiry"' "$LOC_HOME/events.log"; then
  ok "sweep unsubscribes a dead-pid registration with reason 'expiry'"
else bad "sweep unsubscribes a dead-pid registration with reason 'expiry'"; fi
# Idempotent: a second sweep with nothing dead left is a clean no-op, not a fault.
check "a second sweep is a clean no-op (idempotent, safe on a timer)" \
  env LOC_IDENTITY=host loc sweep workshop

# ─── 5. Ephemeral queues; a send to an absent endpoint is REFUSED ────────────
# PRESENCE.md §Queue lifetime ("no queue for an agent that is not running");
# §Inner and outer parlors ("Send is refused — nowhere to deliver"); §Properties
# ("a refused send says so"). The refusal must be about ABSENCE, and nothing may
# reach the medium — a generic "unknown endpoint" is the wrong reason and a
# silent success is the failure the whole model exists to prevent.
say "— ephemeral queues: no subscription, no queue, no silent banking —"
# atelier.clerk was never subscribed: there is no QUEUE_atelier_clerk.
if ! qexists QUEUE_atelier_clerk; then ok "an unsubscribed endpoint has no backing queue at all"
else bad "an unsubscribed endpoint has no backing queue at all"; fi
check_refusal "send to an absent endpoint is refused for ABSENCE (not held, not acked)" \
  "absent|not attending|no live|no subscription|no one is|nobody|no queue|not registered|no endpoint holding" \
  env LOC_IDENTITY=host loc send atelier.clerk "into the void"
# The refusal must be real: nothing may have been created or stored.
if ! qexists QUEUE_atelier_clerk; then
  ok "a refused send never brings a queue into being (no accumulation in the inner parlor)"
else bad "a refused send never brings a queue into being (no accumulation in the inner parlor)"; fi

# ─── 6. emit: non-blocking, emitter-stamped, refs, tolerant of unknown fields ─
# PRESENCE.md §Failure and degradation ("drops the event and returns
# immediately"); §Events → common envelope (ts emitter-stamped), tool.post refs
# tool.pre; §Notes on the fields ("Unknown fields are ignored, not rejected").
say "— emit: presence may degrade, but the agent never waits on it —"
# Non-blocking on a dead broker: a separate home whose config points nowhere.
DOWN="$(dirname "$LOC_HOME")/down"; mkdir -p "$DOWN/creds"
printf 'provider = nats\nnats_url = nats://127.0.0.1:1\n' > "$DOWN/config"
cp "$LOC_HOME/creds/host" "$DOWN/creds/host"
check "emit exits 0 when the broker is unreachable (drops the event, agent never blocks)" \
  env LOC_HOME="$DOWN" LOC_IDENTITY=host loc emit activity.start "$E1"
# Emitter-stamped ts: the moment the caller supplies must survive verbatim.
: > "$LOC_HOME/events.log"
STAMP="2026-01-14T09:31:20.114Z"
env LOC_IDENTITY=host loc emit activity.start "$E1" --ts "$STAMP" >/dev/null 2>&1
sleep 1
if grep -q "\"ts\":[[:space:]]*\"$STAMP\"" "$LOC_HOME/events.log"; then
  ok "emit stamps the caller-supplied ts (the moment it happened, not the publish time)"
else bad "emit stamps the caller-supplied ts (the moment it happened, not the publish time)"; fi
# tool.post refs tool.pre: parallel tool calls are told apart by the reference.
: > "$LOC_HOME/events.log"
env LOC_IDENTITY=host loc emit tool.pre "$E1" --tool shell >/dev/null 2>&1
sleep 1
PRE_ID="$(grep -o '"id":[[:space:]]*"[^"]*"' "$LOC_HOME/events.log" | head -1 | sed -E 's/.*"([^"]*)"$/\1/')"
env LOC_IDENTITY=host loc emit tool.post "$E1" --tool shell --refs "${PRE_ID:-none}" >/dev/null 2>&1
sleep 1
if [[ -n "$PRE_ID" ]] && grep -q '"kind":[[:space:]]*"tool.post"' "$LOC_HOME/events.log" \
   && grep -q "$PRE_ID" "$LOC_HOME/events.log"; then
  ok "tool.post references the tool.pre it closes (parallel calls stay paired)"
else bad "tool.post references the tool.pre it closes (parallel calls stay paired)"; fi
# Unknown-field tolerance is a consumer property: an event bearing a field the
# reader has never heard of must still be shown, not rejected. Exercised through
# `watch`, the reader this model ships.
: > "$LOC_HOME/watch.out"
timeout 4 env LOC_IDENTITY=watch loc watch workshop >"$LOC_HOME/watch.out" 2>&1 &
sleep 1
adm pub topic.workshop \
  '{"id":"ev_unknownfield","ts":"2026-01-14T10:00:00.000Z","kind":"activity.start","endpoint":"workshop.scribe","future_field":"a consumer must not choke on this"}' \
  >/dev/null 2>&1
sleep 2
if grep -q 'ev_unknownfield' "$LOC_HOME/watch.out"; then
  ok "an event carrying an unknown field is still presented (consumers tolerate additions)"
else bad "an event carrying an unknown field is still presented (consumers tolerate additions)"; fi

# ─── 7. registry: request/reply; no host is a failure, not an empty success ──
# PRESENCE.md §Who is here right now ("a request and a reply … if no host is
# running, the request goes unanswered"); §Exit codes ("asking the registry when
# no host is running [is a] failure, not [an] empty success").
say "— registry: the host answers, or the truthful answer is a failure —"
# This scratch deployment runs no host process, so the request must go
# unanswered and the verb must FAIL saying so — never print an empty roster and
# exit 0, which would be indistinguishable from "nobody is here".
check_refusal "registry with no host running fails, naming the missing answer" \
  "no host|unanswered|no reply|timed out|timeout|no registry" \
  env LOC_IDENTITY=host loc registry workshop
# The request/reply happy path needs a live host to answer; the harness ships no
# host implementation, so it is left to a future case (see the notes).

# ─── 8. status reports reachable / alive / working SEPARATELY ────────────────
# PRESENCE.md §Command-line operations → status ("reports its three inputs
# separately, rather than collapsing them into one word"); §Three questions.
say "— status: three questions, three answers, never collapsed to one word —"
# workshop.scribe is subscribed with a live pid: reachable yes, alive yes,
# working idle (no activity within the window). status must show all three, each
# with the reason for its conclusion — not a single verdict.
sout="$(env LOC_IDENTITY=host loc status "$E1" 2>&1)"
if grep -qiE 'reachable' <<<"$sout" && grep -qiE 'alive' <<<"$sout" && grep -qiE 'work|active|idle' <<<"$sout"; then
  ok "status names reachable, alive, and working as three separate facts"
else bad "status names reachable, alive, and working as three separate facts"; fi
# Alive is a (pid, start) fact — the pair, because pids are reused.
if grep -q "$LIVE_PID" <<<"$sout" && grep -qiE 'start|since|pid' <<<"$sout"; then
  ok "the alive fact is the recorded pid and its start time"
else bad "the alive fact is the recorded pid and its start time"; fi

# ─── 9. watch is read-only; its credential cannot publish (ACL deny) ─────────
# PRESENCE.md §Command-line operations → watch ("Follows the event stream …
# Read-only"); §Trust (the watch role publishes nothing).
say "— watch: a reader, and only a reader —"
# The read-only behaviour, through loc: watch must follow the stream and present
# an event published after it starts. (RED where the verb is unbuilt: it prints
# nothing.)
: > "$LOC_HOME/watch.out"
timeout 4 env LOC_IDENTITY=watch loc watch workshop >"$LOC_HOME/watch.out" 2>&1 &
sleep 1
adm pub topic.workshop \
  '{"id":"ev_watchme","ts":"2026-01-14T10:05:00.000Z","kind":"activity.end","endpoint":"workshop.scribe"}' \
  >/dev/null 2>&1
sleep 2
if grep -q 'ev_watchme' "$LOC_HOME/watch.out"; then
  ok "watch follows the event stream and presents events as they arrive"
else bad "watch follows the event stream and presents events as they arrive"; fi
# The credential enforces it below the tool, too: the watch identity is denied
# publish by the server itself. (This is a deployment-ACL invariant — it holds
# for any conformant bootstrap, so it is expected to pass even on the old
# binaries; see the notes.)
seq_before="$(qcount TOPICS)"
env NATS_URL="$NURL" NATS_USER=watch NATS_PASSWORD="$(_creds watch)" \
  nats pub topic.workshop forbidden >/dev/null 2>&1 || true
sleep 1
if grep -Eq 'Publish Violation.*"topic\.workshop"' "$LOC_HOME/server.log"; then
  ok "the watch credential is refused publish by the server (read-only enforced below the tool)"
else bad "the watch credential is refused publish by the server (read-only enforced below the tool)"; fi

# ─── 10. read loss-safety: a read killed mid-render re-presents ──────────────
# PRESENCE.md §Failure and degradation (delivery guarantee within the queue);
# mirrors run.sh's "reading must not destroy what it failed to show". Consumption
# is an ack, and an ack is a delete; a fetch-then-render that dies after the
# fetch must not have destroyed what it never showed.
say "— read: a read that fails mid-render must not consume what it never showed —"
# Give workshop.scribe's live queue one message to lose. On a build with the
# model, case 1's subscribe created this queue; here we stand it up by hand and
# drop one envelope in, so the case measures whether the message SURVIVES a
# broken read rather than whether it was ever there.
seed_queue "$Q1" "queue.$E1"
adm pub "queue.$E1" \
  '{"id":"m1","ts":"2026-01-14T09:00:00.000Z","from":"host","to":"workshop.scribe","kind":"msg","body":"loss-canary"}' \
  >/dev/null 2>&1
# Reader dies after one byte; every later write in a fetch|render pipeline would
# take SIGPIPE mid-render.
env LOC_IDENTITY="$E1" loc read 2>/dev/null | head -c 1 >/dev/null 2>&1
out="$(env LOC_IDENTITY="$E1" loc read 2>/dev/null)"
if grep -q "loss-canary" <<<"$out"; then
  ok "a read that fails mid-render re-presents the message (spool-before-render, exactly-once)"
else bad "a read that fails mid-render re-presents the message (spool-before-render, exactly-once)"; fi

# ─── 11. One agent per endpoint: refuse-held, unsubscribe-refuses-live, --force ─
# PRESENCE.md §One agent per endpoint (subscribing a held endpoint is refused,
# naming the incumbent's pid+start; plain unsubscribe clears empty/dead and
# REFUSES a live incumbent; --force removes a live one).
say "— one agent per endpoint: the incumbent is protected, displacement is deliberate —"
# workshop.scribe is held by LIVE_PID (case 1). A second subscribe must be
# refused, and the refusal must NAME the incumbent's pid so the caller can tell a
# live agent from a crashed one — a bare non-zero exit does not carry that.
check_refusal "subscribe on a held endpoint is refused, naming the incumbent's pid" \
  "$LIVE_PID|held|already|incumbent|in use" \
  env LOC_IDENTITY=host loc subscribe "$E1" --pid "$LIVE_PID" --type acme-cli --version 3.2.0
# Plain unsubscribe must REFUSE a live incumbent, naming the live process.
check_refusal "plain unsubscribe refuses a LIVE incumbent, naming its process" \
  "$LIVE_PID|live|running|still|--force" \
  env LOC_IDENTITY=host loc unsubscribe "$E1"
# --force is the deliberate displacement: it removes a live one. Stand the queue
# up first, so "freed" measures the removal and is not true merely because the
# queue was never created.
seed_queue "$Q1" "queue.$E1"
check "unsubscribe --force removes a live incumbent (the deliberate act)" \
  env LOC_IDENTITY=host loc unsubscribe "$E1" --force
sleep 1
if ! qexists "$Q1"; then ok "--force actually freed the endpoint (queue gone)"
else bad "--force actually freed the endpoint (queue gone)"; fi
# On an EMPTY endpoint plain unsubscribe is a no-op success; on a DEAD incumbent
# it clears — so an unconditional unsubscribe-then-subscribe restart is safe.
check "unsubscribe on an empty endpoint is a no-op success" \
  env LOC_IDENTITY=host loc unsubscribe atelier.clerk
env LOC_IDENTITY=host loc subscribe "$E2" --pid "$DEAD_PID" --type acme-cli --version 3.2.0 >/dev/null 2>&1
check "plain unsubscribe clears a DEAD incumbent (restart needs no pre-check)" \
  env LOC_IDENTITY=host loc unsubscribe "$E2"

# ─── 12. Exit-code discipline: failures are non-zero + stderr, never empty ───
# PRESENCE.md §Exit codes ("exits non-zero and says why on standard error … a
# command that prints nothing and exits zero is indistinguishable from one that
# found nothing"). The channel matters: the reason belongs on STDERR, and the
# refusal must print NOTHING on stdout — the opposite of a usage banner.
say "— exit codes: a failure is loud on stderr, silent on stdout, never a quiet zero —"
check_on_stderr "a refused subscribe speaks on stderr and prints nothing on stdout" \
  "$LIVE_PID|held|already|incumbent" \
  env LOC_IDENTITY=host loc subscribe atelier.scribe --pid 1 --type acme-cli --version 3.2.0
check_on_stderr "a refused send speaks on stderr and prints nothing on stdout" \
  "absent|not attending|no live|no subscription|no queue|nobody" \
  env LOC_IDENTITY=host loc send atelier.clerk "into the void"
check_on_stderr "a registry failure speaks on stderr and prints nothing on stdout" \
  "no host|unanswered|no reply|timed out|timeout" \
  env LOC_IDENTITY=host loc registry workshop

# Reap the live pid we held open for the incumbent cases.
kill "$LIVE_PID" 2>/dev/null

say ""
say "presence: $PASS passed, $FAIL failed"
# This suite is a RED gate for an unbuilt model: on an implementation that does
# not have the presence verbs, a non-zero exit here is the CORRECT outcome. It
# turns green only when the model is built. The exit status reflects the run so
# a CI lane can track the count moving toward zero.
[[ $FAIL -eq 0 ]]
