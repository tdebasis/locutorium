#!/usr/bin/env bash
# service.sh — the launchd half of installing the Locutorium: render the
# nats-server agent from its template and load it. Sourced by install.sh;
# runnable on its own with the same flags.
#
#   service.sh [--dry-run] [--uninstall] [--restart-service]
#
# It touches exactly one file, $HOME/Library/LaunchAgents/com.locutorium.nats-server.plist,
# and it never restarts an agent that is already running unless told to with
# --restart-service: a loaded agent whose file changed underneath it is a
# change deferred to the next login, and the operator is told so in prose.
set -euo pipefail

LOC_HOME="${LOC_HOME:-$HOME/.locutorium}"
LABEL="com.locutorium.nats-server"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
SVC_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
TEMPLATE="$SVC_ROOT/providers/nats/launchd/$LABEL.plist.in"
DOMAIN="gui/$(id -u)"

svc_loaded() { launchctl list "$LABEL" >/dev/null 2>&1; }
svc_pid()    { launchctl list "$LABEL" 2>/dev/null | sed -n 's/^[[:space:]]*"PID" = \([0-9]*\);/\1/p'; }
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
  prog="$(printf '%s\n' "$rendered" | sed -n 's|.*<string>\(/[^<]*nats-server[^<]*\)</string>.*|\1|p' | head -1)"
  if [[ -z "$prog" ]]; then
    echo "service: the rendered plist names no nats-server program path" >&2; return 4
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
  local state
  if [[ ! -e "$PLIST" ]]; then state=NEW
  elif cmp -s "$PLIST" <(printf '%s\n' "$rendered"); then state=UNCHANGED
  else state=CHANGED; fi
  local loaded=no; svc_loaded && loaded=yes
  case "$state:$loaded" in
    UNCHANGED:yes) echo "UNCHANGED  $PLIST (loaded, pid $(svc_pid))"; return 0 ;;
    UNCHANGED:no)  echo "UNCHANGED  $PLIST (not loaded)" ;;
    NEW:*)         echo "NEW        $PLIST" ;;
    CHANGED:*)     echo "CHANGED    $PLIST"; diff -u "$PLIST" <(printf '%s\n' "$rendered") | sed 's/^/           /' || true ;;
  esac
  [[ "$dry" == yes ]] && { echo "           --dry-run: nothing written, nothing loaded"; return 0; }
  if [[ "$state" != UNCHANGED ]]; then
    mkdir -p "$(dirname "$PLIST")"; printf '%s\n' "$rendered" > "$PLIST"; echo "           written"
  fi
  if [[ "$loaded" == yes ]]; then
    if [[ "$restart" == yes ]]; then
      launchctl bootout "$DOMAIN/$LABEL" || { echo "service: bootout failed" >&2; return 5; }
      launchctl bootstrap "$DOMAIN" "$PLIST" || { echo "service: bootstrap failed" >&2; return 5; }
      echo "           restarted, pid $(svc_pid)"
    elif [[ "$state" != UNCHANGED ]]; then
      echo "           the running agent (pid $(svc_pid)) still uses the previous definition;"
      echo "           re-run with --restart-service to apply it now, or leave it until next login"
    fi
  else
    launchctl bootstrap "$DOMAIN" "$PLIST" || { echo "service: bootstrap failed" >&2; return 5; }
    echo "           loaded, pid $(svc_pid)"
  fi
}

svc_uninstall() {
  local dry=no; [[ "${1:-}" == "--dry-run" ]] && dry=yes
  if svc_loaded; then
    echo "REMOVE     agent $LABEL (pid $(svc_pid))"
    [[ "$dry" == yes ]] || launchctl bootout "$DOMAIN/$LABEL" || { echo "service: bootout failed" >&2; return 5; }
  fi
  if [[ -e "$PLIST" ]]; then
    echo "REMOVE     $PLIST"
    [[ "$dry" == yes ]] || rm -f "$PLIST"
  fi
  [[ "$dry" == yes ]] && echo "           --dry-run: nothing removed"
  return 0
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  case " $* " in
    *" --uninstall "*) svc_uninstall "${@/--uninstall/}" ;;
    *) svc_install "$@" ;;
  esac
fi
