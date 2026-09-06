#!/usr/bin/env bash
# bootstrap.sh — generate a NATS deployment for the Locutorium.
#
#   bootstrap.sh <endpoint> [<endpoint> ...]
#
# Writes to $LOC_HOME (default ~/.locutorium):
#   config             flat key=value (provider, url, window)
#   endpoints          the deployment's registry, one name per line
#   creds/<name>       one credential file per endpoint + admin + watch (0600)
#   nats-server.conf   loopback listener, jetstream, per-user permissions
#
# Idempotence: refuses to overwrite an existing deployment unless --force.
# The server config it writes must then be installed where your service
# manager expects it; the script prints the exact steps.

set -euo pipefail

LOC_HOME="${LOC_HOME:-$HOME/.locutorium}"
FORCE="no"
[[ "${1:-}" == "--force" ]] && { FORCE="yes"; shift; }
[[ $# -ge 1 ]] || { echo "usage: bootstrap.sh [--force] <endpoint> [...]" >&2; exit 1; }
ENDPOINTS=("$@")

if [[ -e "$LOC_HOME/nats-server.conf" && "$FORCE" != "yes" ]]; then
  echo "bootstrap: $LOC_HOME already holds a deployment (use --force to regenerate)" >&2
  exit 1
fi

command -v nats >/dev/null || { echo "bootstrap: nats(1) not found" >&2; exit 1; }

mkdir -p "$LOC_HOME/creds" "$LOC_HOME/store" "$LOC_HOME/hooks"
chmod 700 "$LOC_HOME/creds"

pw_for() { openssl rand -hex 16; }
bcrypt_for() { nats server passwd -p "$1"; }

# --- config + registry -------------------------------------------------------
cat > "$LOC_HOME/config" <<EOF
provider = nats
nats_url = nats://127.0.0.1:4222
monitor_url = http://127.0.0.1:8222
topic_window = 7d
EOF
printf '%s\n' "${ENDPOINTS[@]}" > "$LOC_HOME/endpoints"

# --- users -------------------------------------------------------------------
users_block=""
for name in "${ENDPOINTS[@]}" admin watch; do
  # THE TWO SPELLINGS OF ONE ENDPOINT. A subject may carry a dot; a JetStream
  # object's name may not. So a namespaced endpoint <instance>.<agent> is a
  # subject as written and a stream, consumer and ack subject with the dot
  # substituted — QUEUE_workshop_scribe, not QUEUE_workshop.scribe. Every
  # grant below that names a JetStream object uses $s, and only the subject
  # grants use $name. An un-namespaced name has no dot, so $s is $name and an
  # existing deployment's grants are unchanged.
  s="${name//./_}"
  pw="$(pw_for)"
  printf '%s' "$pw" > "$LOC_HOME/creds/$name"
  chmod 600 "$LOC_HOME/creds/$name"
  hash="$(bcrypt_for "$pw")"
  case "$name" in
    admin)
      users_block+="      { user: admin, password: \"$hash\" }"$'\n' ;;
    watch)
      users_block+="      { user: watch, password: \"$hash\", permissions: {
          subscribe: { allow: [\"queue.>\", \"topic.>\"] },
          publish:   { deny:  [\">\"] } } }"$'\n' ;;
    *)
      # A SEAT MAKES ITS OWN CURSOR. The reader's durable consumer on its own
      # queue is created by the reader, not by the admin: `doctor --init`
      # pre-creates one only for the endpoints in the registry at the time it
      # runs, and a seat that stands its queue up when it arrives has none.
      # Without CONSUMER.CREATE and CONSUMER.DURABLE.CREATE on QUEUE_$s the
      # create is refused, a refusal is answered with SILENCE, and the seat's
      # own queue reads as empty while it is holding mail. Both spellings are
      # granted because which one the client asks on depends on the server it
      # is talking to.
      #
      # AND A SEAT MAKES ITS OWN QUEUE. Under the presence model a queue exists
      # exactly while an agent is subscribed: `subscribe` creates the stream,
      # `unsubscribe` destroys it, and nothing accumulates for an agent that is
      # not running. So the three verbs a seat needs on its OWN backing object
      # — create, inspect, delete — are granted here, on QUEUE_$s and on
      # nothing else. INFO is already reachable through the wildcard above and
      # is named again anyway, because a grant that is only implied is a grant
      # the next edit can remove without noticing.
      #
      # registry.> is the question, not the answer: a seat ASKS who is
      # attending an instance and the supervisor replies on the seat's inbox.
      # Answering is a different job with a different grant, and this is not it.
      users_block+="      { user: $name, password: \"$hash\", permissions: {
          publish: { allow: [
            \"queue.*\", \"queue.*.*\", \"topic.>\", \"registry.>\",
            \"\$JS.ACK.QUEUE_$s.>\", \"\$JS.ACK.TOPICS.$s.>\",
            \"\$JS.API.INFO\",
            \"\$JS.API.STREAM.INFO.*\", \"\$JS.API.STREAM.NAMES\", \"\$JS.API.STREAM.LIST\",
            \"\$JS.API.STREAM.SUBJECTS.TOPICS\",
            \"\$JS.API.STREAM.CREATE.QUEUE_$s\",
            \"\$JS.API.STREAM.INFO.QUEUE_$s\",
            \"\$JS.API.STREAM.DELETE.QUEUE_$s\",
            \"\$JS.API.CONSUMER.INFO.>\",
            \"\$JS.API.CONSUMER.MSG.NEXT.QUEUE_$s.$s\",
            \"\$JS.API.CONSUMER.MSG.NEXT.TOPICS.$s\",
            \"\$JS.API.CONSUMER.CREATE.QUEUE_$s.$s\",
            \"\$JS.API.CONSUMER.DURABLE.CREATE.QUEUE_$s.$s\",
            \"\$JS.API.CONSUMER.DURABLE.CREATE.TOPICS.$s\",
            \"\$JS.API.CONSUMER.CREATE.TOPICS.$s\", \"\$JS.API.CONSUMER.CREATE.TOPICS.$s.>\" ] },
          subscribe: { allow: [\"queue.$name\", \"topic.>\", \"_INBOX.>\"] } } }"$'\n' ;;
  esac
done

# --- server config -----------------------------------------------------------
cat > "$LOC_HOME/nats-server.conf" <<EOF
# Locutorium deployment — generated by bootstrap.sh $(date -u +%Y-%m-%dT%H:%M:%SZ)
# Loopback only: this instance is the inner room. Nothing here listens
# beyond the machine.
listen: 127.0.0.1:4222
# The monitoring port, loopback like everything else. The registry verb
# reads who is attending from the server's own connection table, so
# without this the verb exists, is documented, and cannot work.
#
# NO BACKTICKS ANYWHERE IN THIS HEREDOC. It is unquoted so that LOC_HOME
# expands, which also makes backticks command substitution: a comment
# naming a command in backticks RUNS it and pastes its output into the
# generated config. That happened here, and the config it produced held a
# live listing of the operator's own endpoints.
http: 127.0.0.1:8222
server_name: locutorium

jetstream {
  store_dir: $LOC_HOME/store
}

authorization {
  users [
$(printf '%s' "$users_block" | sed 's/^/    /')
  ]
}
EOF

echo "deployment written to $LOC_HOME for endpoints: ${ENDPOINTS[*]}"
echo
echo "next steps:"
echo "  1. point your nats-server service at $LOC_HOME/nats-server.conf and start it"
echo "  2. LOC_IDENTITY=admin loc doctor --init     # create streams"
echo "  3. loc doctor                               # verify as a real endpoint"
