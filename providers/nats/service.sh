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
svc_render() {
  local ns; ns="$(command -v nats-server || true)"
  [[ -n "$ns" ]] || { echo "service: nats-server not on PATH (brew install nats-server)" >&2; return 3; }
  sed -e "s|@NATS_SERVER@|$ns|g" -e "s|@LOC_HOME@|$LOC_HOME|g" "$TEMPLATE"
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
