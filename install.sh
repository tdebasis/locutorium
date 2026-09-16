#!/usr/bin/env bash
# install.sh — put loc on your PATH.
#
# It writes these two things and nothing else:
#   1. $LIBDIR/loc-<version>-<sha> — the built binary, COPIED out of the build tree
#   2. $PREFIX/loc — a symlink to that copy
# It never writes under $LOC_HOME (your deployment: config, store). The broker is
# embedded in the binary and `loc start` runs it, so this installer supervises
# nothing and starts nothing.
#
# Usage:
#   ./install.sh [--prefix DIR] [--dry-run]             # build the newest release tag
#   ./install.sh --main [--prefix DIR] [--dry-run]      # build this checkout's HEAD
#   ./install.sh --uninstall [--prefix DIR] [--dry-run]
#
# The default reads the newest v* tag from origin and fetches the tags. It
# builds that tag in a temporary worktree, and it removes that worktree
# afterwards. Your checkout is not touched. With no release tag and no
# --main, this script stops and exits 2.
#
# WHY A COPY AND NOT A LINK INTO build/. A link into the build tree makes the
# installed tool whatever was last compiled — `make build` would silently change
# what every caller runs, with no act that looks like an act. The copy is named
# for its version and commit, so what is installed can be read without running it.
#
# Exit: 0 done (or nothing to do) · 2 usage · 3 missing dependency · 4 refusal
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
LOC_HOME="${LOC_HOME:-$HOME/.locutorium}"
PREFIX="" DRY=no UNINSTALL=no MAIN=no

usage() { sed -n '2,19p' "${BASH_SOURCE[0]}"; exit 2; }
while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix) PREFIX="${2:-}"; [[ -n "$PREFIX" ]] || usage; shift 2 ;;
    --dry-run) DRY=yes; shift ;;
    --main) MAIN=yes; shift ;;
    --uninstall) UNINSTALL=yes; shift ;;
    -h|--help) sed -n '2,19p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "install: unknown argument: $1" >&2; usage ;;
  esac
done

# ── dependencies, always, even under --dry-run ───────────────────────────────
missing=()
if ! (( BASH_VERSINFO[0] > 3 || (BASH_VERSINFO[0] == 3 && BASH_VERSINFO[1] >= 2) )); then
  missing+=("bash >= 3.2 (found $BASH_VERSION)")
fi
# The toolchain is needed on the machine: this script never installs anything, so
# an absent `go` is a named missing dependency like the rest.
command -v go   >/dev/null 2>&1 || missing+=("go — loc is built from source; brew install go")
command -v make >/dev/null 2>&1 || missing+=("make — the build is made by the Makefile; xcode-select --install")
command -v curl >/dev/null 2>&1 || echo "note: curl not found; 'loc registry' will not work" >&2
if [[ ${#missing[@]} -gt 0 ]]; then
  echo "install: missing dependencies:" >&2
  printf '  - %s\n' "${missing[@]}" >&2
  exit 3
fi
# ── prefix ───────────────────────────────────────────────────────────────────
if [[ -z "$PREFIX" ]]; then
  if command -v brew >/dev/null 2>&1 && [[ -w "$(brew --prefix)/bin" ]]; then PREFIX="$(brew --prefix)/bin"
  else PREFIX="$HOME/.local/bin"; fi
fi
LINK="$PREFIX/loc"
# Beside the prefix, not inside it: $PREFIX/bin -> $PREFIX/lib/locutorium. The
# installed artifact is named for what it IS, so `readlink` answers "which build
# is this machine running" without executing anything.
LIBDIR="$(dirname "$PREFIX")/lib/locutorium"

# ── which tree gets built ────────────────────────────────────────────────────
# The tag IS the version, so a clone whose tags are behind the remote names an
# older release. The fetch comes BEFORE the name is read, because the name below
# and the stamp `make build` links in both come from the same `git describe`.
# The fetch adds tags and deletes none. The tag list below is read from origin,
# not from this clone, so a deleted-upstream tag is never picked. --prune-tags
# is not used: it deletes a tag the person made and never pushed, with no
# warning that it did.
git -C "$ROOT" fetch --tags --quiet 2>/dev/null || echo "install: could not fetch tags from origin"
TAG=""
if [[ "$MAIN" == no && "$UNINSTALL" == no ]]; then
  REMOTE_TAGS="$(git -C "$ROOT" ls-remote --tags --refs origin 'refs/tags/v[0-9]*')" || {
    echo "install: could not read the tags on origin; run ./install.sh --main to build HEAD" >&2
    exit 3
  }
  # `sort -V` orders by version number, so v0.10.0 comes after v0.9.0. A plain
  # `sort` puts them the other way round and installs the older release.
  TAG="$(printf '%s\n' "$REMOTE_TAGS" | sed 's#.*refs/tags/##' | sort -V | tail -1)"
elif [[ "$MAIN" == no ]]; then
  # The uninstall path must work with no network and on a clone with no tags,
  # so it reads the local tag list here, not the list on origin.
  TAG="$(git -C "$ROOT" tag -l 'v[0-9]*' | sort -V | tail -1)"
fi
# A REFUSAL, NOT A FALLBACK. An empty TAG in the default mode means the
# repository has no release yet. Building HEAD instead would install something
# nobody released, under a name that looks like one, so the script stops and
# names the flag that asks for HEAD. The uninstall path never stops here: it
# builds nothing, and it must work on a clone that has no tags.
if [[ -z "$TAG" && "$MAIN" == no && "$UNINSTALL" == no ]]; then
  echo "install: no release tag found; run ./install.sh --main to build HEAD" >&2
  exit 2
fi
# The tag named above came from origin, not from this clone, so it is verified
# here before any use. A build must not run on a tag the fetch did not bring,
# and it must not run on a local tag whose commit differs from origin's.
if [[ -n "$TAG" && "$UNINSTALL" == no ]]; then
  if ! git -C "$ROOT" rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
    echo "install: $TAG is on origin but not in this clone; the fetch did not bring it" >&2
    exit 4
  fi
  # Both ids are the ref's own object: for an annotated tag that is the tag
  # object, and for a light tag the commit. Peeling one side and not the other
  # would refuse every annotated release.
  REMOTE_SHA="$(printf '%s\n' "$REMOTE_TAGS" | awk -v t="refs/tags/$TAG" '$2==t{print $1}')"
  LOCAL_SHA="$(git -C "$ROOT" rev-parse "refs/tags/$TAG")"
  if [[ "$REMOTE_SHA" != "$LOCAL_SHA" ]]; then
    echo "install: the local tag $TAG differs from origin; I will not build it or move it" >&2
    exit 4
  fi
fi
if [[ -n "$TAG" ]]; then
  VERSION_STR="${TAG#v}"
  SHA="$(git -C "$ROOT" rev-parse --short "$TAG^{commit}" 2>/dev/null || echo nogit)"
else
  VERSION_STR="$(git -C "$ROOT" describe --tags --dirty --always 2>/dev/null | sed 's/^v//')"
  # The `||` of a pipeline reads sed's status, not git's, so a failed describe
  # leaves this empty rather than taking a default. The emptiness is the test.
  [[ -n "$VERSION_STR" ]] || VERSION_STR=dev
  SHA="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo nogit)"
fi
TARGET="$LIBDIR/loc-$VERSION_STR-$SHA"
changed=0

# The worktree is this script's own litter, so this script removes it on every
# exit path. The trap keeps the rc of whatever ended the script: a cleanup that
# reports its own success hides the failure it was cleaning up after.
TMPD="" WT=""
cleanup() {
  local rc=$?
  if [[ -n "$WT" ]]; then
    git -C "$ROOT" worktree remove --force "$WT" >/dev/null 2>&1 || true
    git -C "$ROOT" worktree prune >/dev/null 2>&1 || true
  fi
  if [[ -n "$TMPD" ]]; then rm -rf "$TMPD" || true; fi
  return $rc
}
trap cleanup EXIT

# ── one link, made or reported ───────────────────────────────────────────────
# Both binaries are the same artifact shape — a symlink from the prefix into this
# clone — so they are made by one function and refuse in one voice. A second copy
# of this logic is a second place for the refusals to drift apart.
link_artifact() { # link_artifact <link> <target>
  local link="$1" target="$2" cur
  if [[ -L "$link" ]]; then
    cur="$(readlink "$link")"
    if [[ "$cur" == "$target" ]]; then echo "UNCHANGED  $link -> $target"; return 0; fi
    echo "CHANGED    $link: $cur -> $target"; changed=$((changed+1))
    [[ "$DRY" == yes ]] || ln -sfn "$target" "$link"
  elif [[ -e "$link" ]]; then
    echo "install: $link is a real file, not a link; I will not delete something I did not create — move it aside and re-run" >&2
    exit 4
  else
    echo "NEW        $link -> $target"; changed=$((changed+1))
    if [[ "$DRY" == no ]]; then
      [[ -d "$PREFIX" ]] || mkdir -p "$PREFIX"
      ln -s "$target" "$link"
    fi
  fi
}

# OWNERSHIP IS THE WHOLE TEST. A link is removed only when it points at the
# artifact THIS clone would have made; anything else belongs to somebody, and
# an installer that deletes what it did not create is not an installer.
unlink_artifact() { # unlink_artifact <link> <target>
  local link="$1" target="$2"
  if [[ -L "$link" ]]; then
    if [[ "$(readlink "$link")" == "$target" ]]; then
      echo "REMOVE     $link"; [[ "$DRY" == yes ]] || rm -f "$link"; changed=$((changed+1))
    else
      echo "install: $link points at $(readlink "$link"), not at this tree; leaving it alone" >&2; exit 4
    fi
  elif [[ -e "$link" ]]; then
    echo "install: $link is a real file, not a link I made; leaving it alone" >&2; exit 4
  else
    echo "UNCHANGED  $link (absent)"
  fi
}

# ── uninstall ────────────────────────────────────────────────────────────────
if [[ "$UNINSTALL" == yes ]]; then
  unlink_artifact "$LINK" "$TARGET"
  # The stamped copies this clone made. Removed by NAME PATTERN rather than by
  # sweeping the directory: another clone's build may live here too, and an
  # installer that deletes what it did not create is not an installer.
  if [[ -d "$LIBDIR" ]]; then
    for f in "$LIBDIR"/loc-*-*; do
      [[ -e "$f" ]] || continue
      echo "REMOVE     $f"; [[ "$DRY" == yes ]] || rm -f "$f"; changed=$((changed+1))
    done
    [[ "$DRY" == yes ]] || rmdir "$LIBDIR" 2>/dev/null || true
  fi
  echo "note: $LOC_HOME was not touched — it holds your config and store."
  echo "      remove it yourself if you mean to:  rm -rf $LOC_HOME"
  exit 0
fi

# ── build, copy, link ────────────────────────────────────────────────────────
# Built FIRST: a link to a binary that was never made points at nothing, and the
# failure would surface as a puzzle at the next invocation rather than here,
# where the person is watching.
if [[ "$DRY" == yes ]]; then
  if [[ -n "$TAG" ]]; then
    echo "           --dry-run: would build $TAG in a temporary worktree; $LINK would point at $TARGET"
  else
    echo "           --dry-run: would build HEAD at $VERSION_STR; $LINK would point at $TARGET"
  fi
else
  # THE TAG IS BUILT SOMEWHERE ELSE. A checkout of the tag in place would move
  # the person's working tree, and a release install must not do that. The
  # worktree is detached at the tag, so `git describe` inside it prints the bare
  # number and `make build` stamps that number into the binary.
  if [[ -n "$TAG" ]]; then
    TMPD="$(mktemp -d "${TMPDIR:-/tmp}/loc-install.XXXXXX")"
    WT="$TMPD/src"
    git -C "$ROOT" worktree add --detach --quiet "$WT" "$TAG" || {
      echo "install: could not make a worktree at $TAG; nothing installed" >&2; exit 3; }
    SRC="$WT"
  else
    SRC="$ROOT"
  fi
  BUILT="$SRC/build/bin/loc"
  (cd "$SRC" && make build) || { echo "install: 'make build' failed; nothing installed" >&2; exit 3; }
  [[ -x "$BUILT" ]] || { echo "install: the build produced no $BUILT" >&2; exit 3; }
  mkdir -p "$LIBDIR"
  if [[ -e "$TARGET" ]] && cmp -s "$BUILT" "$TARGET"; then
    echo "UNCHANGED  $TARGET"
  else
    echo "NEW        $TARGET"; changed=$((changed+1))
    cp "$BUILT" "$TARGET" && chmod 755 "$TARGET"
  fi
fi
link_artifact "$LINK" "$TARGET"
case ":$PATH:" in *":$PREFIX:"*) ;; *) echo "           $PREFIX is not on your PATH; add:  export PATH=\"$PREFIX:\$PATH\"" ;; esac

if [[ "$DRY" == yes ]]; then echo "--dry-run: $changed artifact(s) would change; nothing written."
elif [[ $changed -eq 0 ]]; then echo "nothing to do (every artifact already matches)."
else echo "done: $changed artifact(s) changed."; fi
if [[ ! -e "$LOC_HOME/config" ]]; then
  echo "next: no deployment at $LOC_HOME yet —"
  echo "      loc start     # writes $LOC_HOME/config with its defaults, then runs the broker"
fi
