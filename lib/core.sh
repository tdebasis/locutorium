#!/usr/bin/env bash
# core.sh — the Locutorium's semantics layer.
#
# This file knows about endpoints, channels, envelopes, subscriptions, and
# hooks. It does not know what carries them: every operation that touches the
# medium goes through the provider_* interface, loaded from exactly one
# provider module selected by config. It also does not know who deploys it:
# identity and notification are deployment hooks, not code.

set -euo pipefail

LOC_HOME="${LOC_HOME:-$HOME/.locutorium}"
LOC_LIB="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ---------------------------------------------------------------- config ----
# Flat key=value file. No YAML, no parser to own.
loc_config() { # loc_config <key> [default]
  local key="$1" default="${2-}"
  local val=""
  if [[ -f "$LOC_HOME/config" ]]; then
    val="$(sed -n "s/^[[:space:]]*${key}[[:space:]]*=[[:space:]]*//p" "$LOC_HOME/config" | tail -1)"
  fi
  printf '%s' "${val:-$default}"
}

loc_load_provider() {
  local provider
  provider="$(loc_config provider "")"
  [[ -n "$provider" ]] || loc_die "no provider configured (set 'provider = <name>' in $LOC_HOME/config)"
  local module="$LOC_LIB/providers/${provider}.sh"
  [[ -f "$module" ]] || loc_die "unknown provider '$provider' (no module at $module)"
  # shellcheck source=/dev/null
  source "$module"
}

# -------------------------------------------------------------- identity ----
# Resolution order: LOC_IDENTITY env var, then the deployment's identity hook.
# An unattributable caller is refused — never 'unknown'.
loc_identity() {
  if [[ -n "${LOC_IDENTITY:-}" ]]; then printf '%s' "$LOC_IDENTITY"; return; fi
  local hook="$LOC_HOME/hooks/identity"
  if [[ -x "$hook" ]]; then
    local id
    id="$("$hook" 2>/dev/null || true)"
    if [[ -n "$id" ]]; then printf '%s' "$id"; return; fi
  fi
  loc_die "cannot determine sender identity: set LOC_IDENTITY or provide an executable $LOC_HOME/hooks/identity"
}

# -------------------------------------------------------------- envelope ----
loc_envelope() { # loc_envelope <from> <to> <kind> <body>
  python3 - "$1" "$2" "$3" "$4" <<'PY'
import json, sys, uuid, datetime
frm, to, kind, body = sys.argv[1:5]
print(json.dumps({
    "id": str(uuid.uuid4()),
    "ts": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    "from": frm, "to": to, "kind": kind, "body": body,
    "reply_to": None, "refs": []}, separators=(",", ":")))
PY
}

loc_render() { # reads envelope JSON lines on stdin, renders for a reader
  # Script passed via -c, never via stdin: stdin belongs to the data pipe.
  python3 -c '
import json, sys
me = sys.argv[1]
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        e = json.loads(line)
    except ValueError:
        print("  [unparseable] " + line[:120])
        continue
    dest = "you" if e.get("to") == me else e.get("to", "?")
    print("[" + e.get("from", "?").upper() + " -> " + dest + "] "
          + e.get("ts", "?") + "  " + e.get("body", ""))
' "$1"
}

# ----------------------------------------------------------------- hooks ----
loc_nudge() { # loc_nudge <endpoint> <line>  — advisory; failure never loses a message
  local hook="$LOC_HOME/hooks/nudge"
  [[ -x "$hook" ]] && "$hook" "$1" "$2" || true
}

loc_mentions() { # print @mentioned endpoints in a body, one per line
  grep -oE '@[a-z0-9_-]+' <<<"$1" | sed 's/^@//' | sort -u
}

# ----------------------------------------------------------------- verbs ----
loc_send() { # loc_send <endpoint> <body>
  local to="$1" body="$2" from env
  from="$(loc_identity)"
  provider_endpoint_exists "$to" || loc_die "unknown endpoint '$to' (not in this deployment's registry)"
  env="$(loc_envelope "$from" "$to" "msg" "$body")"
  provider_send_queue "$to" "$env"
  loc_nudge "$to" "[LOC] 1 new → loc read"
  echo "sent → queue.$to"
}

loc_publish() { # loc_publish <topic> <body>
  local topic="$1" body="$2" from env m
  [[ "$topic" =~ ^[a-z0-9][a-z0-9._-]*$ ]] || loc_die "invalid topic name '$topic'"
  from="$(loc_identity)"
  env="$(loc_envelope "$from" "#$topic" "msg" "$body")"
  provider_publish_topic "$topic" "$env"
  while IFS= read -r m; do
    [[ -n "$m" ]] && provider_endpoint_exists "$m" && \
      loc_nudge "$m" "[LOC] 1 new in #$topic → loc read"
  done <<<"$(loc_mentions "$body")"
  echo "published → #$topic"
}

loc_read() { # loc_read [--peek]
  local me peek="no"
  [[ "${1:-}" == "--peek" ]] && peek="yes"
  me="$(loc_identity)"
  echo "── queue.$me ──"
  provider_read_queue "$me" 50 "$peek" | loc_render "$me"
  echo "── topics ──"
  provider_read_topics "$me" 100 | loc_render "$me"
}

loc_topics()  { provider_topics; }
loc_status()  { provider_status; }
loc_watch() {
  local id; id="$(loc_identity 2>/dev/null || echo watch)"
  echo "watching (read-only; ^C to stop)…"
  provider_watch "$id" | loc_render "$id"
}
loc_doctor()  { provider_doctor "$@"; }

# ------------------------------------------------------------------ misc ----
loc_die() { echo "loc: $*" >&2; exit 1; }
