#!/usr/bin/env bash
# check-scratch-only.sh — the suite may only speak to a server it built itself.
#
# The nats command-line tool, given no target, connects to 127.0.0.1:4222. On a
# developer's machine, and on the self-hosted runner that drives the macOS lane,
# that address is not empty: it is the operator's OWN, LIVE deployment. A single
# `nats sub`, `nats pub`, or `nats stream purge` that forgot to say where to go
# would reach in and touch the real thing — subscribe to real traffic, publish
# into a real queue, delete real state — while looking like an ordinary test.
#
# The suite is already careful: every nats invocation is written
# `env NATS_URL="nats://127.0.0.1:$PORT" ... nats ...`, pointing at the scratch
# server it boots on a random port. But care is a habit, and habits lapse across
# edits. This guard turns the habit into a rule: no nats CLI call in the suite
# may connect without naming its target. A future edit that defaults to 4222
# fails here, on the free Linux lane, before it can ever run against a live box.
#
# HOW IT READS
#   - Backslash line-continuations are joined first, because the suite writes the
#     env prefix on one line and the `nats` verb on the next; checked apart, the
#     verb line would look bare and every real call would be a false alarm.
#   - Comments are dropped. A `nats` in prose is not a connection.
#   - A nats CLI call is `nats` standing as its own word with a subcommand after
#     it: preceded by line-start or whitespace, followed by whitespace. This, and
#     nothing fancier, is what separates a command from `nats-server` (the server
#     binary, followed by `-`), from a path like providers/nats/ (flanked by `/`),
#     and from a name inside a string like pgrep -f "nats subscribe ..." (led by
#     a quote, not a space).
#   - Some nats subcommands open no connection and so cannot reach the live box;
#     naming a target for them would be theatre. These are excused:
#       server passwd   local bcrypt of a password; the bootstrap uses it
#       help, --help    usage text
#       --version       version string
#       cheat           built-in cheat-sheet
#       completion      shell-completion script
#       schema          schema listing/inspection of the local tool
#     Every other subcommand is treated as connecting and must carry NATS_URL=.
#   - The guard file itself is skipped (it names the very commands it forbids),
#     as is anything listed in SCRATCH_ONLY_EXCLUDE (space-separated basenames),
#     which is where a deliberately-planted negative fixture would go.
#
# A connecting nats call with no NATS_URL= on its (joined) line is a failure,
# reported as file:line so the offending call can be found and fixed. The suite
# and the release path both gate on the exit status.
#
# SCOPE (v0): this guard catches the bare literal word `nats` standing as a
# command. An invocation through a variable ("$NATS" sub ...) or via
# `command nats ...` escapes it; that is accepted for now because the suite
# uses neither form.

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

# Basenames to skip: this guard itself (it names the very commands it forbids)
# and anything the caller excludes, where a planted negative fixture would go.
excluded=" $(basename "${BASH_SOURCE[0]}") ${SCRATCH_ONLY_EXCLUDE:-} "

# The directory of scripts to scan. Defaults to this one, so an ordinary run
# checks the real suite; the red/green proof points it at a scratch copy.
scan_dir="${1:-.}"

offenders=""
for f in "$scan_dir"/*.sh; do
  [[ -e "$f" ]] || continue
  [[ "$excluded" == *" $(basename "$f") "* ]] && continue

  # Join continuations, drop comments, then flag any connecting nats call that
  # carries no NATS_URL= on its own (now-joined) line. The start line of the
  # logical command is reported, which is where the fix belongs.
  hits="$(awk '
    function flush(   line) {
      line = buf; buf = ""
      # Drop a whole-line comment and any trailing inline comment. No nats call
      # in the suite carries an inline "#", so this cannot eat a NATS_URL=.
      sub(/^[[:space:]]*#.*/, "", line)
      sub(/[[:space:]]#.*/, "", line)
      if (line == "") return

      # Is there a nats CLI command word? (whitespace/BOL) nats (whitespace)
      if (line !~ /(^|[[:space:]])nats[[:space:]]/) return

      # Excuse subcommands that never open a connection.
      if (line ~ /(^|[[:space:]])nats[[:space:]]+server[[:space:]]+passwd([[:space:]]|$)/) return
      if (line ~ /(^|[[:space:]])nats[[:space:]]+(help|--help|--version|cheat|completion|schema)([[:space:]]|$)/) return

      # A connecting call must name its target on the same joined line.
      if (line !~ /NATS_URL=/)
        printf "%d:%s\n", startln, line
    }
    {
      if (buf == "") startln = NR
      # A line ending in a single backslash continues onto the next.
      if ($0 ~ /\\$/) {
        cur = $0; sub(/\\$/, " ", cur)
        buf = buf cur
        next
      }
      buf = buf $0
      flush()
    }
    END { if (buf != "") flush() }
  ' "$f")"

  if [[ -n "$hits" ]]; then
    while IFS= read -r line; do
      offenders+="$f:$line"$'\n'
    done <<<"$hits"
  fi
done

if [[ -n "$offenders" ]]; then
  echo "check-scratch-only: nats CLI call with no explicit NATS_URL= (would default to the live 4222):" >&2
  printf '%s' "$offenders" | sed 's/^/  /' >&2
  exit 1
fi
echo "check-scratch-only: every nats call names its scratch target"
