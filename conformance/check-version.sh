#!/usr/bin/env bash
# check-version.sh — one version, stated once, agreed everywhere.
#
# VERSION is the source of truth. `loc version` must print it; every vX.Y.Z
# literal in the docs must equal it; if HEAD sits on a release tag, the tag
# must equal it. A release whose number disagrees with itself is not a release.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
v="$(tr -d '[:space:]' < VERSION)"
[[ "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "check-version: VERSION '$v' is not X.Y.Z" >&2; exit 1; }
got="$(bin/loc version)"
[[ "$got" == "loc $v" ]] || { echo "check-version: 'loc version' printed '$got', VERSION says $v" >&2; exit 1; }
bad="$(grep -rhoE 'v[0-9]+\.[0-9]+\.[0-9]+' README.md RELEASE.md docs/*.md 2>/dev/null | sort -u | grep -v "^v$v$" || true)"
[[ -z "$bad" ]] || { echo "check-version: docs mention other versions: $(echo "$bad" | tr '\n' ' ')" >&2; exit 1; }
if tag="$(git describe --tags --exact-match 2>/dev/null)"; then
  [[ "$tag" == "v$v" ]] || { echo "check-version: HEAD is tagged $tag but VERSION is $v" >&2; exit 1; }
fi
echo "check-version: $v everywhere"
