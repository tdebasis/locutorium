#!/usr/bin/env bash
# install.sh — put loc on your PATH and the medium under launchd.
#
# It writes exactly two things and nothing else:
#   1. $PREFIX/loc — a symlink to this tree's bin/loc
#   2. $HOME/Library/LaunchAgents/com.locutorium.nats-server.plist — the server agent
# It never writes under $LOC_HOME (your deployment: creds, config, endpoints, store),
# never runs bootstrap for you, and never restarts a running agent unless asked.
#
# Usage:
#   ./install.sh [--prefix DIR] [--dry-run] [--no-service] [--restart-service]
#   ./install.sh --uninstall [--prefix DIR] [--dry-run]
#
# Exit: 0 done (or nothing to do) · 2 usage · 3 missing dependency · 4 refusal · 5 launchctl failed
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
TARGET="$ROOT/bin/loc"
LOC_HOME="${LOC_HOME:-$HOME/.locutorium}"
PREFIX="" DRY=no UNINSTALL=no NO_SERVICE=no RESTART=no

usage() { sed -n '2,13p' "${BASH_SOURCE[0]}"; exit 2; }
while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix) PREFIX="${2:-}"; [[ -n "$PREFIX" ]] || usage; shift 2 ;;
    --dry-run) DRY=yes; shift ;;
    --uninstall) UNINSTALL=yes; shift ;;
    --no-service) NO_SERVICE=yes; shift ;;
    --restart-service) RESTART=yes; shift ;;
    -h|--help) sed -n '2,13p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "install: unknown argument: $1" >&2; usage ;;
  esac
done

# ── dependencies, always, even under --dry-run ───────────────────────────────
missing=()
if ! (( BASH_VERSINFO[0] > 3 || (BASH_VERSINFO[0] == 3 && BASH_VERSINFO[1] >= 2) )); then
  missing+=("bash >= 3.2 (found $BASH_VERSION)")
fi
command -v nats    >/dev/null 2>&1 || missing+=("nats — brew install nats-io/nats-tools/nats")
command -v python3 >/dev/null 2>&1 || missing+=("python3 — xcode-select --install, or brew install python")
if [[ "$NO_SERVICE" == no ]] && ! command -v nats-server >/dev/null 2>&1; then
  missing+=("nats-server — brew install nats-server (or pass --no-service to talk to a server elsewhere)")
fi
command -v curl    >/dev/null 2>&1 || echo "note: curl not found; 'loc registry' will not work" >&2
command -v openssl >/dev/null 2>&1 || echo "note: openssl not found; bootstrap will not work" >&2
if [[ ${#missing[@]} -gt 0 ]]; then
  echo "install: missing dependencies:" >&2
  printf '  - %s\n' "${missing[@]}" >&2
  exit 3
fi
if [[ "$NO_SERVICE" == no ]] && ! command -v launchctl >/dev/null 2>&1; then
  echo "note: no launchd here; run 'nats-server -c $LOC_HOME/nats-server.conf' under your own supervisor" >&2
  NO_SERVICE=yes
fi

# ── prefix ───────────────────────────────────────────────────────────────────
if [[ -z "$PREFIX" ]]; then
  if command -v brew >/dev/null 2>&1 && [[ -w "$(brew --prefix)/bin" ]]; then PREFIX="$(brew --prefix)/bin"
  else PREFIX="$HOME/.local/bin"; fi
fi
LINK="$PREFIX/loc"
changed=0

# ── uninstall ────────────────────────────────────────────────────────────────
if [[ "$UNINSTALL" == yes ]]; then
  if [[ -L "$LINK" ]]; then
    if [[ "$(readlink "$LINK")" == "$TARGET" ]]; then
      echo "REMOVE     $LINK"; [[ "$DRY" == yes ]] || rm -f "$LINK"; changed=$((changed+1))
    else
      echo "install: $LINK points at $(readlink "$LINK"), not at this tree; leaving it alone" >&2; exit 4
    fi
  elif [[ -e "$LINK" ]]; then
    echo "install: $LINK is a real file, not a link I made; leaving it alone" >&2; exit 4
  else
    echo "UNCHANGED  $LINK (absent)"
  fi
  if [[ "$NO_SERVICE" == no ]]; then
    # shellcheck source=providers/nats/service.sh
    source "$ROOT/providers/nats/service.sh"
    if [[ "$DRY" == yes ]]; then svc_uninstall --dry-run; else svc_uninstall; fi
  fi
  echo "note: $LOC_HOME was not touched — it holds your credentials, config and store."
  echo "      remove it yourself if you mean to:  rm -rf $LOC_HOME"
  exit 0
fi

# ── symlink ──────────────────────────────────────────────────────────────────
if [[ -L "$LINK" ]]; then
  cur="$(readlink "$LINK")"
  if [[ "$cur" == "$TARGET" ]]; then echo "UNCHANGED  $LINK -> $TARGET"
  else
    echo "CHANGED    $LINK: $cur -> $TARGET"; changed=$((changed+1))
    [[ "$DRY" == yes ]] || ln -sfn "$TARGET" "$LINK"
  fi
elif [[ -e "$LINK" ]]; then
  echo "install: $LINK is a real file, not a link; I will not delete something I did not create — move it aside and re-run" >&2
  exit 4
else
  echo "NEW        $LINK -> $TARGET"; changed=$((changed+1))
  if [[ "$DRY" == no ]]; then
    [[ -d "$PREFIX" ]] || mkdir -p "$PREFIX"
    ln -s "$TARGET" "$LINK"
  fi
fi
case ":$PATH:" in *":$PREFIX:"*) ;; *) echo "           $PREFIX is not on your PATH; add:  export PATH=\"$PREFIX:\$PATH\"" ;; esac

# ── service ──────────────────────────────────────────────────────────────────
if [[ "$NO_SERVICE" == no ]]; then
  # shellcheck source=providers/nats/service.sh
  source "$ROOT/providers/nats/service.sh"
  args=(); [[ "$DRY" == yes ]] && args+=(--dry-run); [[ "$RESTART" == yes ]] && args+=(--restart-service)
  out="$(svc_install "${args[@]+"${args[@]}"}")" || exit $?
  printf '%s\n' "$out"
  case "$out" in UNCHANGED*|SKIPPED*) ;; *) changed=$((changed+1)) ;; esac
fi

if [[ "$DRY" == yes ]]; then echo "--dry-run: $changed artifact(s) would change; nothing written."
elif [[ $changed -eq 0 ]]; then echo "nothing to do (every artifact already matches)."
else echo "done: $changed artifact(s) changed."; fi
if [[ ! -e "$LOC_HOME/nats-server.conf" ]]; then
  echo "next: no deployment at $LOC_HOME yet —"
  echo "      $ROOT/providers/nats/bootstrap.sh <endpoint> [<endpoint> ...]   # then re-run ./install.sh"
  echo "      LOC_IDENTITY=admin loc doctor --init                          # create the streams"
  echo "      loc doctor                                                    # verify as an endpoint"
fi
