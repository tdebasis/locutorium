#!/usr/bin/env bash
# service.sh — the launchd half of installing the Locutorium: render the two
# agents from their templates and load them. Sourced by install.sh; runnable on
# its own with the same flags.
#
#   service.sh [--dry-run] [--uninstall] [--restart-service]
#
# It touches exactly two files:
#   $HOME/Library/LaunchAgents/com.locutorium.nats-server.plist   the medium
#   $HOME/Library/LaunchAgents/com.locutorium.sweep.plist         the timer
#
# It never restarts an agent that is already running unless told to with
# --restart-service: a loaded agent whose file changed underneath it is a
# change deferred to the next login, and the operator is told so in prose.
#
# THE TIMER ANSWERS ONE QUESTION: HOW OFTEN. It passes no instance. `loc sweep`
# finds every instance itself, so the plist carries a period and nothing else
# that could go stale as the house changes.
set -euo pipefail

LOC_HOME="${LOC_HOME:-$HOME/.locutorium}"
LABEL="com.locutorium.nats-server"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
SWEEP_LABEL="com.locutorium.sweep"
SWEEP_PLIST="$HOME/Library/LaunchAgents/$SWEEP_LABEL.plist"
SVC_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
TEMPLATE="$SVC_ROOT/providers/nats/launchd/$LABEL.plist.in"
SWEEP_TEMPLATE="$SVC_ROOT/providers/nats/launchd/$SWEEP_LABEL.plist.in"
DOMAIN="gui/$(id -u)"

# The label is an argument because there are two agents. It defaults to the
# medium's, so a caller written before the timer existed still asks the same
# question of the same agent.
svc_loaded() { launchctl list "${1:-$LABEL}" >/dev/null 2>&1; }
svc_pid()    { launchctl list "${1:-$LABEL}" 2>/dev/null | sed -n 's/^[[:space:]]*"PID" = \([0-9]*\);/\1/p'; }
# THE PLIST NAMES A PINNED COPY, NEVER A PACKAGE MANAGER'S PATH.
#
# It used to render `command -v nats-server`, which resolves to Homebrew's
# floating `opt` symlink. On 2026-09-06 a CI job on the deployment machine ran
# `brew install nats-server`; that upgraded the binary and repointed the symlink.
# The running process kept the old one in memory, so nobody noticed — until the
# machine rebooted overnight and launchd started the NEW binary at 07:55. Six
# hours of measurements were taken against a version the records said was not
# running. Nothing was misconfigured and nobody made a mistake: an ordinary
# reboot was enough, because A VERSION HELD IN MEMORY IS NOT HELD.
#
# So the binary is COPIED to a versioned path this installer owns, and the plist
# names that file. A Cellar path is not the answer either — `brew cleanup`
# deletes old versions, and the medium would then fail to start at all, trading
# a silent upgrade for a silent absence.
svc_pinned_path() { # → the path the plist should name, printed
  local ns ver
  ns="$(command -v nats-server || true)"
  [[ -n "$ns" ]] || { echo "service: nats-server not on PATH (brew install nats-server)" >&2; return 3; }
  ver="$("$ns" --version 2>/dev/null | sed -n 's/.*v\([0-9][0-9.]*\).*/\1/p')"
  [[ -n "$ver" ]] || { echo "service: could not read a version from '$ns --version'" >&2; return 3; }
  [[ -n "${LIBDIR:-}" ]] || { echo "service: LIBDIR is not set; run this through install.sh" >&2; return 3; }
  printf '%s/nats-server-%s\n' "$LIBDIR" "$ver"
}

svc_render() {
  local ns pinned
  ns="$(command -v nats-server || true)"
  [[ -n "$ns" ]] || { echo "service: nats-server not on PATH (brew install nats-server)" >&2; return 3; }
  pinned="$(svc_pinned_path)" || return $?
  if [[ ! -e "$pinned" ]] || ! cmp -s "$ns" "$pinned"; then
    mkdir -p "$(dirname "$pinned")"
    cp "$ns" "$pinned" && chmod 755 "$pinned" || {
      echo "service: could not place the pinned medium at $pinned" >&2; return 3; }
  fi
  sed -e "s|@NATS_SERVER@|$pinned|g" -e "s|@LOC_HOME@|$LOC_HOME|g" "$TEMPLATE" | svc_assert_pinned
}

# THE CHECK, NOT THE CORRECTED LINE. A renderer that happens to be right today
# is a fact; this is the property held. It reads the plist that is about to be
# written and REFUSES it unless the program path is a real file we own — so a
# future edit that reintroduces `command -v`, a symlink, or a package path fails
# here rather than at the next reboot, months later, silently.
svc_assert_pinned() { # stdin: a rendered plist → stdout unchanged, or refuse
  local rendered prog
  rendered="$(cat)"
  # STRUCTURALLY, from ProgramArguments[0] — never by searching for a substring.
  #
  # This first matched the first <string> containing "nats-server", which is a
  # FALSE PASS waiting to happen: the CONFIG path contains that substring too
  # (nats-server.conf). Let a string carrying the config ever precede the
  # program — an EnvironmentVariables block, a hoisted -c, a reordered
  # ProgramArguments — and the check would inspect the CONF path, which is a
  # real file, not a symlink and not under a package prefix, and PASS, while the
  # program path floated free. A check that validates the wrong string is worse
  # than no check: it reports on something nobody asked about.
  prog="$(printf '%s\n' "$rendered" | plutil -extract ProgramArguments.0 raw -o - - 2>/dev/null)"
  if [[ -z "$prog" ]]; then
    echo "service: could not read ProgramArguments[0] from the rendered plist" >&2; return 4
  fi
  case "$prog" in
    */Cellar/*|*/opt/*|*/bin/nats-server)
      echo "service: REFUSING a plist that names '$prog' — that path is a package manager's and can be" >&2
      echo "         upgraded or deleted underneath the machine. The plist must name a versioned copy" >&2
      echo "         this installer placed. See providers/nats/service.sh (svc_pinned_path)." >&2
      return 4 ;;
  esac
  if [[ -L "$prog" ]]; then
    echo "service: REFUSING a plist that names the symlink '$prog' — what boots must not be repointable" >&2
    echo "         without editing the boot configuration. That is the failure this check exists for." >&2
    return 4
  fi
  [[ -f "$prog" ]] || { echo "service: REFUSING a plist naming '$prog', which is not a file" >&2; return 4; }
  printf '%s\n' "$rendered"
}

# THE PERIOD, READ FROM THE DEPLOYMENT AT RENDER TIME.
#
# `sweep_interval` is a whole number of seconds and it defaults to 60. It is
# read with the same sed the rest of this tree reads the config file with: the
# whitespace around the equals sign is of any width, and the LAST matching line
# wins rather than the first.
#
# A value that is not a positive whole number is REFUSED, never corrected.
# launchd would take a corrected one and run happily, and the operator would
# never learn that the line they wrote was ignored.
svc_sweep_interval() { # → the period in seconds, printed
  local v
  v="$(sed -n 's/^[[:space:]]*sweep_interval[[:space:]]*=[[:space:]]*//p' "$LOC_HOME/config" 2>/dev/null | tail -1)"
  v="${v%"${v##*[![:space:]]}"}"
  [[ -n "$v" ]] || { echo 60; return 0; }
  case "$v" in
    *[!0-9]*)
      echo "service: sweep_interval in $LOC_HOME/config is '$v'; it must be a whole number of seconds" >&2
      return 4 ;;
  esac
  if [[ "$v" -le 0 ]]; then
    echo "service: sweep_interval in $LOC_HOME/config is '$v'; it must be greater than zero" >&2
    return 4
  fi
  echo "$v"
}

# svc_sweep_render prints the timer's plist.
#
# It names the STAMPED COPY of loc, not the $PREFIX/loc symlink, for the reason
# svc_assert_pinned gives above: what boots must not be repointable without
# editing the boot configuration. An upgrade of loc changes this path, so the
# next install reports the plist as CHANGED and says how to apply it.
svc_sweep_render() {
  local interval
  interval="$(svc_sweep_interval)" || return $?
  [[ -n "${LOC_BIN:-}" ]] || { echo "service: LOC_BIN is not set; run this through install.sh" >&2; return 3; }
  [[ -e "$SWEEP_TEMPLATE" ]] || { echo "service: no template at $SWEEP_TEMPLATE" >&2; return 3; }
  sed -e "s|@LOC_BIN@|$LOC_BIN|g" -e "s|@LOC_HOME@|$LOC_HOME|g" -e "s|@SWEEP_INTERVAL@|$interval|g" "$SWEEP_TEMPLATE"
}

# svc_apply <label> <plist> <dry> <restart> <rendered>
#
# ONE FUNCTION FOR BOTH AGENTS. A second copy of this would be a second place
# for the refusals and the deferred-restart wording to drift apart, and the two
# agents differ only in what they run.
svc_apply() {
  local label="$1" plist="$2" dry="$3" restart="$4" rendered="$5"
  local state
  if [[ ! -e "$plist" ]]; then state=NEW
  elif cmp -s "$plist" <(printf '%s\n' "$rendered"); then state=UNCHANGED
  else state=CHANGED; fi
  local loaded=no; svc_loaded "$label" && loaded=yes
  case "$state:$loaded" in
    UNCHANGED:yes) echo "UNCHANGED  $plist (loaded, pid $(svc_pid "$label"))"; return 0 ;;
    UNCHANGED:no)  echo "UNCHANGED  $plist (not loaded)" ;;
    NEW:*)         echo "NEW        $plist" ;;
    CHANGED:*)     echo "CHANGED    $plist"; diff -u "$plist" <(printf '%s\n' "$rendered") | sed 's/^/           /' || true ;;
  esac
  [[ "$dry" == yes ]] && { echo "           --dry-run: nothing written, nothing loaded"; return 0; }
  if [[ "$state" != UNCHANGED ]]; then
    mkdir -p "$(dirname "$plist")"; printf '%s\n' "$rendered" > "$plist"; echo "           written"
  fi
  if [[ "$loaded" == yes ]]; then
    if [[ "$restart" == yes ]]; then
      launchctl bootout "$DOMAIN/$label" || { echo "service: bootout failed" >&2; return 5; }
      launchctl bootstrap "$DOMAIN" "$plist" || { echo "service: bootstrap failed" >&2; return 5; }
      echo "           restarted, pid $(svc_pid "$label")"
    elif [[ "$state" != UNCHANGED ]]; then
      echo "           the running agent (pid $(svc_pid "$label")) still uses the previous definition;"
      echo "           re-run with --restart-service to apply it now, or leave it until next login"
    fi
  else
    launchctl bootstrap "$DOMAIN" "$plist" || { echo "service: bootstrap failed" >&2; return 5; }
    echo "           loaded, pid $(svc_pid "$label")"
  fi
}

# svc_install [--dry-run] [--restart-service]  → prints one status line per artifact
svc_install() {
  local dry=no restart=no
  for a in "$@"; do case "$a" in --dry-run) dry=yes;; --restart-service) restart=yes;; esac; done
  if [[ ! -e "$LOC_HOME/nats-server.conf" ]]; then
    echo "SKIPPED    service: no deployment at $LOC_HOME yet"
    echo "           next: $SVC_ROOT/providers/nats/bootstrap.sh <endpoint> [<endpoint> ...], then re-run install"
    return 0
  fi
  local rendered; rendered="$(svc_render)" || return $?
  svc_apply "$LABEL" "$PLIST" "$dry" "$restart" "$rendered"
}

# svc_sweep_install [--dry-run] [--restart-service]  → one status line
#
# The timer needs a deployment for the same reason the medium does: it
# authenticates as the supervisor credential bootstrap wrote, and there is none
# before bootstrap has run.
svc_sweep_install() {
  local dry=no restart=no
  for a in "$@"; do case "$a" in --dry-run) dry=yes;; --restart-service) restart=yes;; esac; done
  if [[ ! -e "$LOC_HOME/nats-server.conf" ]]; then
    echo "SKIPPED    sweep: no deployment at $LOC_HOME yet"
    return 0
  fi
  local rendered; rendered="$(svc_sweep_render)" || return $?
  svc_apply "$SWEEP_LABEL" "$SWEEP_PLIST" "$dry" "$restart" "$rendered"
}

# svc_remove <label> <plist> <dry>
svc_remove() {
  local label="$1" plist="$2" dry="$3"
  if svc_loaded "$label"; then
    echo "REMOVE     agent $label (pid $(svc_pid "$label"))"
    [[ "$dry" == yes ]] || launchctl bootout "$DOMAIN/$label" || { echo "service: bootout failed" >&2; return 5; }
  fi
  if [[ -e "$plist" ]]; then
    echo "REMOVE     $plist"
    [[ "$dry" == yes ]] || rm -f "$plist"
  fi
  return 0
}

# svc_uninstall removes BOTH agents. An uninstall that left the timer behind
# would leave launchd running a binary the uninstall had just deleted.
svc_uninstall() {
  local dry=no; [[ "${1:-}" == "--dry-run" ]] && dry=yes
  svc_remove "$SWEEP_LABEL" "$SWEEP_PLIST" "$dry" || return $?
  svc_remove "$LABEL" "$PLIST" "$dry" || return $?
  [[ "$dry" == yes ]] && echo "           --dry-run: nothing removed"
  return 0
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  case " $* " in
    *" --uninstall "*) svc_uninstall "${@/--uninstall/}" ;;
    *) svc_install "$@"; svc_sweep_install "$@" ;;
  esac
fi
