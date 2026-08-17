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

# Maximum message body, in CHARACTERS. Deliberately not configurable: that a limit
# exists and that breaking it is a loud refusal is the product's opinion, and it is what
# makes the guarantee mean anything. A deployment wanting different conversational norms
# changes this constant.
#
# WHY A LIMIT: this carries conversation, not documents. A message is a text, not an
# essay. Long content belongs in files or an archive.
#
# WHY CHARACTERS AND NOT BYTES: both are deterministic, bytes are unfair. An emoji is
# four bytes, so two messages of visibly equal length would fail differently depending
# on how many status markers they carry.
#
# 🔴 WHAT KIND OF NUMBER THIS IS, because getting that wrong is the whole story below:
# 4000 is MESSAGE DISCIPLINE, not a technical bound. It is a choice about what this
# channel is for, and raising it is a policy conversation, not a physics one.
#
# The transport carries far more, and THAT is what was measured (2026-08-16,
# receiving-side verified with a token at the END of the body so truncation was
# distinguishable from non-delivery): bodies delivered intact at 700, 1500, 3000, 8000,
# 12000 and 16000 characters, and multi-line at 4026. The only observed failure is the
# terminal multiplexer's own argument limit near 20000, and it is LOUD (rc=1, reproduced
# twice). So the CEILING is measured, the LIMIT is chosen, and 4000 sits at a 4x margin
# under a failure that cannot be silent.
#
# This replaced a believed 700 ceiling that did not reproduce at any size or newline
# count. That number was treated as physics, was never tested, and every design decision
# touching message length bent around it for nine days. Do not let 4000 become the same
# thing: it is an opinion with a margin, and anyone changing the delivery mechanism owes
# a fresh measurement rather than trust in this constant.
LOC_MAX_BODY_CHARS=4000

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

# ------------------------------------------------------------ body size ----
# Counting is decided in ONE place, and it always means codepoints. bash's ${#var}
# counts bytes or characters depending on the locale, and `wc -c` and `wc -m` disagree
# on the same text; a limit that means different things in different scripts is not a
# limit. Body arrives on stdin rather than as an argument so an absurd payload hits this
# check rather than the OS argument limit, and is decoded explicitly so a C locale
# cannot turn an emoji into a different answer.
_loc_body_chars() { # <body>
  printf '%s' "$1" | python3 -c \
    'import sys; print(len(sys.stdin.buffer.read().decode("utf-8", "replace")))'
}

# Refuse before the message reaches the medium. Names BOTH numbers: a refusal that only
# says "too long" makes the sender guess how much to cut.
_loc_check_body() { # <body>
  local n; n="$(_loc_body_chars "$1")"
  [[ "$n" -le "$LOC_MAX_BODY_CHARS" ]] || loc_die \
    "message too long: $n characters, limit $LOC_MAX_BODY_CHARS. This channel carries conversation, not documents. Put the content in a file or the archive and send a pointer to it."
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
# argv[1] is the reading endpoint. It was used to collapse the recipient to
# "you"; both ends are named now, so it is accepted and ignored rather than
# removed, because every caller passes it.
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        e = json.loads(line)
    except ValueError:
        print("  [unparseable] " + line[:120])
        continue
    # Both ends NAMED, always. "you" was written for someone reading their own
    # queue at a terminal, where the recipient is obvious because they typed the
    # command. Delivery now puts this line in front of whoever is watching the
    # pane, and "FORGE -> you" tells that reader nothing about who "you" is.
    # Backticks are the ONLY colour lever available. Measured 2026-08-16 against
    # a real pane: markdown renders (bold, italic, fences), inline code renders
    # COLOURED, and ANSI escapes are stripped in transit and arrive as literal
    # "[34m" junk. So the endpoints are marked as inline code to colour them,
    # and the timestamp is left plain so it recedes behind the names.
    # Bold AROUND the code span, not inside it: markdown does not parse emphasis
    # within a span, so **text** inside backticks arrives as literal asterisks.
    # Wrapping the other way gives a bold code span, which is the only way to
    # differentiate the header now that the body is also blue.
    #
    # There is no colour PALETTE here to reach for. Inline code is the single
    # colour lever the renderer offers, and ANSI escapes are stripped in transit
    # (measured 2026-08-16, they arrive as literal "[34m"). So "a different
    # colour" is really bold-blue against plain-blue, plus emoji if a sender
    # wants an accent.
    print("**`" + e.get("from", "?") + " -> " + e.get("to", "?") + "`**   " + e.get("ts", "?"))
    print()
    # Each line is its own INLINE CODE SPAN, which is what renders blue. Chosen by
    # the Convener from three measured against a real pane on 2026-08-16:
    #
    #   blockquote  REJECTED — this renderer paints a background behind every WORD
    #               and leaves the gaps dark, so the text arrives striped and is
    #               genuinely hard to read.
    #   plain       readable, but no colour.
    #   code span   blue and legible. Chosen.
    #
    # ⚠️ THE COST, because it must not be rediscovered as a bug: markdown does NOT
    # render inside a code span. Bold, headings, lists and tables arrive as literal
    # characters. Blue body and rendered formatting are mutually exclusive, and
    # this picks blue deliberately.
    #
    # Per LINE, not one span for the whole body: a code span does not cross a line
    # break, so a single pair of backticks would colour the first line and leak raw
    # backticks into the rest.
    for b in e.get("body", "").split("\n"):
        if not b:
            print()
        elif "`" in b:
            # A body containing a backtick would close the span early and spill the
            # remainder unstyled. Markdown allows a longer fence, and the padding
            # spaces are required so a leading or trailing backtick in the text is
            # not read as part of the delimiter.
            print("`` " + b + " ``")
        else:
            print("`" + b + "`")
    # Between envelopes, so a batch does not run together.
    print()
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

# -------------------------------------------------------------- delivery ----
# Registration is attendance. `loc sub` starts a listener that observes the
# endpoint's own queue WITHOUT consuming it and wakes the registered channel
# through the deployment's wake hook. Employment-tied: the listener watches
# what it was told to watch and exits when that dies. Every start or reconnect
# begins with a depth check (wake-on-backlog), so a missed event can never
# become a missed message. The listener never sends through this tool's own
# verbs: a delivery layer that speaks on the bus can recurse into it.

_loc_rundir() { mkdir -p "$LOC_HOME/run"; chmod 700 "$LOC_HOME/run"; printf '%s' "$LOC_HOME/run"; }
_loc_pid_start() { ps -o lstart= -p "$1" 2>/dev/null | sed 's/^[[:space:]]*//'; }

_loc_listener_alive() { # <endpoint> — a RUNNING pid with a MATCHING start time.
  # File existence alone must never stand in for liveness: a crash leaves a
  # stale pidfile, and pid recycling can make a stale pid look alive.
  local pidfile="$LOC_HOME/run/$1.listener.pid" pid start
  [[ -f "$pidfile" ]] || return 1
  pid="$(sed -n '1p' "$pidfile")"; start="$(sed -n '2p' "$pidfile")"
  [[ -n "$pid" ]] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  [[ "$(_loc_pid_start "$pid")" == "$start" ]]
}

_loc_wake() { # <endpoint> <count> — the deployment's last inch. Its failure
  # is logged (the statusline reads stream truth, so a stall becomes visible);
  # it is never allowed to lose a message, which lives safely in the queue.
  local hook="$LOC_HOME/hooks/wake"
  [[ -x "$hook" ]] || return 0
  "$hook" "$1" "$2" \
    || echo "$(date -u +%FT%TZ) wake FAILED for $1 (count $2)" >> "$LOC_HOME/run/$1.delivery.log"
}

_loc_session_dead() { # <watch_pid> <endpoint> — generic liveness. A pid if we
  # were given one; else the deployment's alive hook; else live until unsub.
  local wpid="$1" me="$2" hook="$LOC_HOME/hooks/alive"
  if [[ -n "$wpid" ]]; then
    kill -0 "$wpid" 2>/dev/null && return 1 || return 0
  fi
  if [[ -x "$hook" ]]; then
    "$hook" "$me" >/dev/null 2>&1 && return 1 || return 0
  fi
  return 1
}

_loc_listener() { # <endpoint> <watch_pid> — the background body of `loc sub`.
  set +e  # a transient error must not kill attendance
  local me="$1" wpid="$2"
  local window perm perh
  window="$(loc_config wake_window_seconds 5)"
  perm="$(loc_config wake_breaker_per_minute 6)"
  perh="$(loc_config wake_breaker_per_hour 60)"
  local dlog="$LOC_HOME/run/$me.delivery.log"
  local pending=0 last_wake=0 now m_e=0 m_n=0 h_e=0 h_n=0 tripped=""
  # Cleanup state lives in subshell globals, NOT locals: the EXIT trap fires
  # after this function returns, when its locals no longer exist (under set -u
  # that error aborted cleanup and left a stale pidfile behind). Own pid via
  # a child's PPID: BASHPID does not exist in bash 3.2.
  _L_ME="$me"; _L_TAP=""
  _L_SELF="$(exec sh -c 'echo "$PPID"')"

  _cleanup() {
    [[ -n "${_L_TAP:-}" ]] && kill "$_L_TAP" 2>/dev/null
    [[ -n "${_L_SELF:-}" ]] && pkill -P "$_L_SELF" 2>/dev/null
    rm -f "$LOC_HOME/run/${_L_ME:-nobody}.listener.pid" \
          "$LOC_HOME/run/${_L_ME:-nobody}.listen.fifo"
  }
  trap '_cleanup; exit 0' TERM INT
  trap '_cleanup' EXIT

  _wake_guarded() { # <count> — the breaker sits between policy and the hook
    # Fire-time depth check: the reader may have drained the queue between the
    # arrival event and now (readers and listeners share no lock, by design).
    # One query kills the common spurious wake; the residual race is accepted
    # because it only over-delivers — an empty read, never a lost message.
    local d; d="$(provider_depth "$me" 2>/dev/null)"
    [[ "$d" =~ ^[0-9]+$ ]] || d=0
    if (( d == 0 )); then
      echo "$(date -u +%FT%TZ) wake $me skipped: queue already drained" >> "$dlog"
      return 0
    fi
    now="$(date +%s)"
    (( now/60  != m_e )) && { m_e=$((now/60));  m_n=0; }
    (( now/3600 != h_e )) && { h_e=$((now/3600)); h_n=0; tripped=""; }
    if (( m_n >= perm || h_n >= perh )); then
      [[ -z "$tripped" ]] && { echo "$(date -u +%FT%TZ) BREAKER TRIPPED for $me (min $m_n/$perm, hr $h_n/$perh): wakes suppressed, the queue keeps the truth" >> "$dlog"; tripped=yes; }
      return 0
    fi
    m_n=$((m_n+1)); h_n=$((h_n+1)); last_wake="$now"
    echo "$(date -u +%FT%TZ) wake $me count=$1" >> "$dlog"
    # Take the bodies BEFORE handing off. The deployment's wake hook presents
    # them, and presenting creates a turn boundary, which runs whatever else the
    # deployment attaches to boundaries; if the messages were still on the queue
    # at that instant, two readers would race for them. Consuming first is what
    # removes the race, and the spool is what makes consuming-first survivable.
    #
    # STREAMED, never captured into a variable first: the fetch acks, and on a
    # work queue an ack is a delete, so between the acked fetch and a durable
    # write the message would exist only in memory. That gap is exactly the
    # defect this file spent 2026-08-16 removing one layer up.
    #
    # Append, because a previous hand-off may have failed to present and its
    # bodies are still owed. Order is fetch order, which is queue order, so a
    # retry cannot silently reorder a conversation.
    provider_read_queue "$me" 50 no 2>/dev/null | loc_render "$me" \
      >> "$(_loc_rundir)/$me.spool" || true
    _loc_wake "$me" "$1"
  }

  while :; do
    _loc_session_dead "$wpid" "$me" && break
    # wake-on-backlog: every start and every reconnect begins at the queue
    local depth; depth="$(provider_depth "$me" 2>/dev/null)"
    [[ "$depth" =~ ^[0-9]+$ ]] || depth=0
    (( depth > 0 )) && _wake_guarded "$depth"
    pending=0
    # The event tap runs as an explicitly tracked child writing to a fifo. A
    # process substitution would leak it: a subscriber on a quiet queue never
    # writes, so it never takes SIGPIPE when its reader vanishes, and every
    # reconnect cycle would orphan another immortal one.
    local fifo="$LOC_HOME/run/$me.listen.fifo"
    rm -f "$fifo"; mkfifo "$fifo"
    provider_listen "$me" > "$fifo" 2>/dev/null &
    _L_TAP=$!
    exec 3< "$fifo"
    while :; do
      local line="" rcv=0
      IFS= read -t 2 -r -u 3 line || rcv=$?
      if _loc_session_dead "$wpid" "$me"; then break 2; fi
      now="$(date +%s)"
      if (( rcv == 0 )) && [[ -z "$line" ]]; then
        continue                       # record separator: --raw prints a blank
                                       # line after each message; not EOF
      elif (( rcv == 0 )); then
        if (( now - last_wake >= window )); then
          _wake_guarded 1              # the first message wakes instantly
        else
          pending=$((pending+1))       # followers coalesce into one wake
        fi
      elif kill -0 "$_L_TAP" 2>/dev/null; then
        # Timeout tick. Decided by the TAP'S LIVENESS, not the read status:
        # bash 3.2 returns 1 for both timeout and EOF (>128 is bash 4+), so
        # the status alone cannot tell a quiet queue from a dead pipe.
        if (( pending > 0 && now - last_wake >= window )); then
          _wake_guarded "$pending"; pending=0
        fi
      else
        break                          # tap died: reconnect with backoff
      fi
    done
    exec 3<&-
    kill "$_L_TAP" 2>/dev/null; wait "$_L_TAP" 2>/dev/null
    _L_TAP=""
    rm -f "$fifo"
    sleep 2
  done
}

loc_sub() { # loc_sub [--watch-pid <pid>] — register attendance
  local me wpid=""
  while [[ $# -gt 0 ]]; do case "$1" in
    --watch-pid) wpid="${2:?--watch-pid needs a value}"; shift 2 ;;
    *) loc_die "usage: loc sub [--watch-pid <pid>]" ;;
  esac; done
  me="$(loc_identity)"
  provider_endpoint_exists "$me" || loc_die "unknown endpoint '$me' (not in this deployment's registry)"
  local rundir; rundir="$(_loc_rundir)"
  if _loc_listener_alive "$me"; then
    echo "already attending → queue.$me (listener $(sed -n 1p "$rundir/$me.listener.pid"))"
    return 0
  fi
  # Channel capture is the deployment's business; the token is opaque here.
  local reg="$LOC_HOME/hooks/register"
  [[ -x "$reg" ]] && "$reg" "$me" || true
  ( _loc_listener "$me" "$wpid" ) >> "$rundir/$me.listener.log" 2>&1 &
  local pid=$!
  disown "$pid" 2>/dev/null || true
  { echo "$pid"; _loc_pid_start "$pid"; } > "$rundir/$me.listener.pid"
  chmod 600 "$rundir/$me.listener.pid"
  echo "attending → queue.$me (listener $pid)"
}

loc_unsub() { # end attendance; the queue keeps holding messages regardless
  local me rundir pid
  me="$(loc_identity)"
  rundir="$(_loc_rundir)"
  if _loc_listener_alive "$me"; then
    pid="$(sed -n 1p "$rundir/$me.listener.pid")"
    kill "$pid" 2>/dev/null || true
  fi
  rm -f "$rundir/$me.listener.pid"
  echo "not attending → queue.$me"
}

loc_registry() { provider_registry; }

# ----------------------------------------------------------------- verbs ----
loc_send() { # loc_send <endpoint> <body>
  local to="$1" body="$2" from env
  from="$(loc_identity)"
  _loc_check_body "$body"
  provider_endpoint_exists "$to" || loc_die "unknown endpoint '$to' (not in this deployment's registry)"
  # Say-semantics (config-gated; enable only once every endpoint registers at
  # session-up): a send expects an attending peer. When the deployment KNOWS
  # nobody is attending, accepting the message would manufacture a false
  # belief in the sender. A crash inside the liveness window still stores —
  # bounded, and repaired by wake-on-backlog. Durable store-and-forward
  # semantics belong to a different channel, not to send.
  if [[ "$(loc_config send_requires_attendance no)" == "yes" ]]; then
    _loc_listener_alive "$to" \
      || loc_die "not attending: '$to' has no live listener (say-semantics: a send expects an attending peer; use a durable channel for messages meant to wait)"
  fi
  env="$(loc_envelope "$from" "$to" "msg" "$body")"
  provider_send_queue "$to" "$env"
  loc_nudge "$to" "[LOC] 1 new → loc read"
  echo "sent → queue.$to"
}

loc_publish() { # loc_publish <topic> <body>
  local topic="$1" body="$2" from env m
  [[ "$topic" =~ ^[a-z0-9][a-z0-9._-]*$ ]] || loc_die "invalid topic name '$topic'"
  # Same medium, same limit: a room is not a document store either.
  _loc_check_body "$body"
  from="$(loc_identity)"
  env="$(loc_envelope "$from" "#$topic" "msg" "$body")"
  provider_publish_topic "$topic" "$env"
  while IFS= read -r m; do
    [[ -n "$m" ]] && provider_endpoint_exists "$m" && \
      loc_nudge "$m" "[LOC] 1 new in #$topic → loc read"
  done <<<"$(loc_mentions "$body")"
  echo "published → #$topic"
}

# Reading is the one verb that DESTROYS. Queues are workqueue-retention, so the
# provider's ack is a delete with no recovery — and the ack is atomic with the
# fetch (nats CLI has no ack-after-the-fact; see providers/nats.sh). So the only
# available safety is to get the bytes onto disk before anything fallible runs,
# and to forget them only once rendering has actually succeeded.
#
# This used to be `provider_read_queue ... | loc_render`, which acked INSIDE the
# pipeline: `loc read | head`, a dead terminal, or a renderer error destroyed
# whatever had been fetched. On 2026-08-16 the same shape, one layer up in the
# prompt-ingest hook, destroyed five messages.
#
# Failure now costs a duplicate, never a deletion: an uncleared spool is
# re-rendered by the next read.
loc_read() { # loc_read [--peek]
  local me peek="no" spool
  [[ "${1:-}" == "--peek" ]] && peek="yes"
  me="$(loc_identity)"

  if [[ "$peek" == "yes" ]]; then
    # --peek must not consume anything. It previously still drained TOPICS with
    # --ack, so "peeking" destroyed topic messages; topics are simply not read
    # here now, and the output says so rather than implying an empty room.
    echo "── queue.$me ──"
    provider_read_queue "$me" 50 yes | loc_render "$me"
    echo "── topics ── (not shown: --peek never consumes, and topic reads cannot yet be non-consuming)"
    return 0
  fi

  # One spool per section. A single spool with an in-band separator would break
  # exactly when it matters: after two failed renders it holds two separators,
  # and every split rule then mis-sorts the carried-over lines.
  local qspool tspool rundir
  rundir="$(_loc_rundir)"
  qspool="$rundir/$me.read.spool"
  tspool="$rundir/$me.topics.spool"
  for spool in "$qspool" "$tspool"; do
    [[ -f "$spool" ]] || : > "$spool"
    chmod 600 "$spool" 2>/dev/null || true
  done

  # Append, never overwrite: a spool may already hold envelopes a previous read
  # fetched and failed to render.
  provider_read_queue  "$me" 50 no >> "$qspool"
  provider_read_topics "$me" 100   >> "$tspool"

  # Everything below is fallible and reads the files, not the socket.
  echo "── queue.$me ──"
  loc_render "$me" < "$qspool" || return 1
  echo "── topics ──"
  loc_render "$me" < "$tspool" || return 1

  # Forgotten only now, once rendering has actually succeeded.
  : > "$qspool"; : > "$tspool"
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
