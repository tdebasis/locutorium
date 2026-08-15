#!/usr/bin/env bash
# make-contexts.sh — generate a nats(1) CLI context per credential.
#
#   make-contexts.sh
#
# Reads $LOC_HOME (default ~/.locutorium): config (nats_url) and creds/*.
# Writes one CLI context per credential, named loc-<name>, so operators can
# verify stream state as any identity without hand-reading credential files:
#
#   nats --context loc-<name> stream info QUEUE_<name>
#
# Deliberately leaves NO context selected: a selected default would make a
# bare `nats` invocation silently act as one identity from any shell. Bare
# `nats` must keep failing loud (authorization violation), so every call
# names its context explicitly.
#
# Idempotence: re-running regenerates every context from the current creds;
# safe after a credential rotation.

set -euo pipefail

LOC_HOME="${LOC_HOME:-$HOME/.locutorium}"

command -v nats >/dev/null || { echo "make-contexts: nats(1) not found" >&2; exit 1; }
[[ -d "$LOC_HOME/creds" ]] || { echo "make-contexts: $LOC_HOME/creds not found (run bootstrap.sh first)" >&2; exit 1; }

url="$(sed -n 's/^nats_url = //p' "$LOC_HOME/config")"
[[ -n "$url" ]] || { echo "make-contexts: no nats_url in $LOC_HOME/config" >&2; exit 1; }

made=0
for credfile in "$LOC_HOME"/creds/*; do
  name="$(basename "$credfile")"
  pw="$(cat "$credfile")"
  nats context save "loc-$name" \
    --server "$url" --user "$name" --password "$pw" \
    --description "locutorium identity: $name" >/dev/null
  made=$((made + 1))
done

# Context files hold the password; match the creds files' permissions.
ctx_dir="${XDG_CONFIG_HOME:-$HOME/.config}/nats/context"
chmod 600 "$ctx_dir"/loc-*.json 2>/dev/null || true

# Unselect any default (nats auto-selects the first context ever saved).
rm -f "${XDG_CONFIG_HOME:-$HOME/.config}/nats/context.txt"

echo "wrote $made contexts (loc-*) for $url; none selected — always pass --context"
