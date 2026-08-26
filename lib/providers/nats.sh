#!/usr/bin/env bash
# providers/nats.sh — the NATS + JetStream adapter.
#
# The one module that knows the medium is NATS. It translates the semantics
# (queues, topics, cursors) into nats(1) invocations:
#   queue.<endpoint>  -> stream QUEUE_<endpoint>, work-queue retention
#                        (retained until the endpoint consumes = delivery
#                        guarantee), one durable pull consumer per endpoint
#   topic.<name>      -> one TOPICS stream, max_age = the availability window
#                        (idle teardown falls out of retention), per-reader
#                        durable pull consumers as cursors
#   watch             -> plain subscriptions, nothing stored
#
# Credentials: $LOC_HOME/creds/<identity> (mode 0600). Endpoint registry:
# $LOC_HOME/endpoints, one name per line — written by the deployment, not us.

_nats_env() { # _nats_env <identity> — exports connection env for nats(1)
  local id="$1" credfile
  credfile="$LOC_HOME/creds/$id"
  [[ -f "$credfile" ]] || loc_die "no credentials for '$id' at $credfile"
  export NATS_URL="$(loc_config nats_url "nats://127.0.0.1:4222")"
  export NATS_USER="$id"
  export NATS_PASSWORD="$(cat "$credfile")"
}

# Resolve identity in an assignment, not an argument: loc_die inside "$( )"
# exits only the subshell, and in argument position that failure is discarded,
# so the parent would carry on with an empty identity and print a second,
# wrong-layer error ("no credentials for ''"). The assignment form honors the
# failure; the subshell has already printed the right error.
_nats_me() {
  local id
  id="$(loc_identity)" || exit 1
  _nats_env "$id"
}

provider_endpoint_exists() { # <name>
  [[ -f "$LOC_HOME/endpoints" ]] && grep -qxF "$1" "$LOC_HOME/endpoints"
}

_nats_endpoints() { cat "$LOC_HOME/endpoints" 2>/dev/null || true; }

provider_send_queue() { # <endpoint> <envelope>
  local to="$1" env="$2" ack
  _nats_me
  ack="$(nats request "queue.$to" "$env" --timeout 3s --raw 2>/dev/null)" \
    || loc_die "send failed: no acknowledgement from the store (is the server up? try: loc doctor)"
  grep -q '"seq"' <<<"$ack" || loc_die "send failed: unexpected acknowledgement: $ack"
}

provider_publish_topic() { # <topic> <envelope>
  local topic="$1" env="$2" ack
  _nats_me
  ack="$(nats request "topic.$topic" "$env" --timeout 3s --raw 2>/dev/null)" \
    || loc_die "publish failed: no acknowledgement from the store (is the server up? try: loc doctor)"
  grep -q '"seq"' <<<"$ack" || loc_die "publish failed: unexpected acknowledgement: $ack"
}

# Drain a pull consumer one message at a time: over-asking (--count N) makes
# the client wait out its full timeout after the channel is dry, and a short
# timeout can expire mid-delivery, stranding a delivered-but-unprinted message
# in its ack-wait window. One-at-a-time with a generous timeout has neither
# failure mode; the loop ends on the first miss.
_nats_drain() { # <stream> <consumer> <max> <ackflag>
  local stream="$1" consumer="$2" max="$3" ackflag="$4" i msg
  for ((i = 0; i < max; i++)); do
    msg="$(nats consumer next "$stream" "$consumer" --count 1 --timeout 3s $ackflag --raw 2>/dev/null)" || break
    [[ -n "$msg" ]] && printf '%s\n' "$msg"
  done
  return 0
}

provider_read_queue() { # <endpoint> <max> <peek: yes|no>
  local me="$1" max="$2" peek="$3" ackflag="--ack"
  [[ "$peek" == "yes" ]] && { ackflag="--no-ack"; max=1; }
  _nats_env "$me"
  _nats_drain "QUEUE_$me" "$me" "$max" "$ackflag"
}

provider_read_topics() { # <reader> <max>
  local me="$1" max="$2"
  _nats_env "$me"
  # The reader's cursor: a durable pull consumer, created lazily on first read.
  if ! nats consumer info TOPICS "$me" >/dev/null 2>&1; then
    nats consumer add TOPICS "$me" --pull --deliver all --filter 'topic.>' \
      --ack explicit --defaults >/dev/null 2>&1 || true
  fi
  _nats_drain TOPICS "$me" "$max" "--ack"
}

provider_topics() {
  _nats_me
  nats stream subjects TOPICS --json 2>/dev/null | python3 -c '
import json, sys
try:
    subjects = json.load(sys.stdin) or {}
except ValueError:
    subjects = {}
if not subjects:
    print("(no active topics)")
for s, n in sorted(subjects.items()):
    name = s[len("topic."):] if s.startswith("topic.") else s
    print("#" + name + "  (" + str(n) + " in window)")
'
}

provider_status() {
  local e pending
  _nats_me
  for e in $(_nats_endpoints); do
    pending="$(nats consumer info "QUEUE_$e" "$e" --json 2>/dev/null \
      | python3 -c 'import json,sys;print(json.load(sys.stdin).get("num_pending","?"))' 2>/dev/null || echo "?")"
    printf '%-12s unread: %s\n' "$e" "$pending"
  done
}

provider_watch() { # <identity>
  _nats_env "$1"
  trap 'kill 0' INT TERM
  nats sub 'queue.>' --raw 2>/dev/null &
  nats sub 'topic.>' --raw 2>/dev/null &
  wait
}

provider_listen() { # <endpoint> — event stream: one raw envelope line per
  # arrival on the endpoint's own queue. A plain subscription: it receives a
  # copy as the message is stored; the stored message is untouched. Exits when
  # the connection drops — the caller owns reconnect and wake-on-backlog.
  _nats_env "$1"
  exec nats subscribe "queue.$1" --raw 2>/dev/null
}

provider_depth() { # <endpoint> — stored (unconsumed) messages in own queue
  _nats_env "$1"
  nats stream info "QUEUE_$1" --json 2>/dev/null | python3 -c '
import json, sys
try: print(json.load(sys.stdin)["state"]["messages"])
except Exception: print(0)' 2>/dev/null || echo 0
}

provider_registry() { # render live listeners from the medium's own state
  local url; url="$(loc_config monitor_url "")"
  [[ -n "$url" ]] || loc_die "registry unavailable: no monitor_url configured (the server exposes no introspection surface)"
  curl -s -m 3 "$url/connz?subs=1&auth=1" | python3 -c '
import json, sys
try: conns = json.load(sys.stdin).get("connections", [])
except Exception: sys.exit("registry: cannot read the monitor endpoint")
rows = []
for c in conns:
    user = c.get("authorized_user", "?")
    for s in c.get("subscriptions_list", []) or []:
        if s == "queue." + user:
            rows.append((user, c.get("start", "?"), c.get("cid", "?")))
if not rows:
    print("(nobody attending)")
for user, start, cid in sorted(rows):
    print("attending: " + user + "  since " + str(start) + "  (conn " + str(cid) + ")")'
}

provider_doctor() { # [--init]
  local init="no" e ok=0
  [[ "${1:-}" == "--init" ]] && init="yes"

  if [[ "$init" == "yes" ]]; then
    _nats_env admin
    local window
    window="$(loc_config topic_window "7d")"
    for e in $(_nats_endpoints); do
      nats stream info "QUEUE_$e" >/dev/null 2>&1 || \
        nats stream add "QUEUE_$e" --subjects "queue.$e" --retention work \
          --storage file --replicas 1 --defaults >/dev/null
      nats consumer info "QUEUE_$e" "$e" >/dev/null 2>&1 || \
        nats consumer add "QUEUE_$e" "$e" --pull --deliver all \
          --ack explicit --defaults >/dev/null
      echo "✓ queue.$e"
    done
    nats stream info TOPICS >/dev/null 2>&1 || \
      nats stream add TOPICS --subjects 'topic.>' --retention limits \
        --max-age "$window" --storage file --replicas 1 --defaults >/dev/null
    echo "✓ topics (window: $window)"
    return 0
  fi

  local id; id="$(loc_identity)"
  _nats_env "$id"
  if nats account info >/dev/null 2>&1; then
    echo "✓ server reachable, credentials for '$id' accepted"
  else
    echo "✗ cannot reach the server as '$id' at ${NATS_URL}"
    echo "  if it is not running:  launchctl bootstrap gui/\$(id -u) \$HOME/Library/LaunchAgents/com.locutorium.nats-server.plist"
    echo "  (or ./install.sh from your clone, which renders and loads that agent)"
    ok=1
  fi
  if provider_endpoint_exists "$id"; then
    if nats stream info "QUEUE_$id" >/dev/null 2>&1; then
      echo "✓ own queue stream exists"
    else
      echo "✗ QUEUE_$id missing — run: loc doctor --init (as admin)"; ok=1
    fi
  fi
  nats stream info TOPICS >/dev/null 2>&1 && echo "✓ topics stream exists" \
    || { echo "✗ TOPICS stream missing — run: loc doctor --init (as admin)"; ok=1; }
  # ACL self-check: reading another endpoint's queue must be refused.
  local other
  other="$(_nats_endpoints | grep -vxF "$id" | head -1 || true)"
  if [[ -n "$other" ]]; then
    if nats consumer next "QUEUE_$other" "$other" --count 1 --timeout 1s --no-ack --raw >/dev/null 2>&1; then
      echo "✗ ACL HOLE: '$id' can read queue.$other — permissions are wrong"; ok=1
    else
      echo "✓ acl: reading queue.$other as '$id' is refused"
    fi
  fi
  return $ok
}
