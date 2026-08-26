#!/usr/bin/env bash
# build.sh — re-render the art from its sources. Run after editing a .excalidraw in
# Excalidraw, or the tape. Each PNG is Excalidraw's own renderer at 2x on a white
# background; the GIF is vhs playing the five commands against a scratch house.
#
#   docs/art/build.sh            # PNGs (needs node + playwright-core + a Chrome)
#   docs/art/build.sh --gif      # …and the GIF (needs vhs, nats-server, nats)
#
# Excalidraw saves text with fontFamily 5 (Excalifont), which is what these scenes
# use; older exports wrote 1 (Virgil). Normalise so the look never drifts.
# A re-render can differ from the committed PNG by a few bytes (antialiasing noise,
# PSNR > 100 dB); commit a new PNG only when the scene changed.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
command -v jq >/dev/null || { echo "build: jq is required" >&2; exit 1; }
command -v node >/dev/null || { echo "build: node is required" >&2; exit 1; }
for f in wordmark architecture attendance; do
  tmp="$(mktemp)"
  jq '(.elements[] | select(.type=="text") | .fontFamily) |= 5
      | .appState.viewBackgroundColor = "#ffffff"' "$f.excalidraw" > "$tmp"
  if ! cmp -s "$tmp" "$f.excalidraw"; then
    jq . "$tmp" > "$f.excalidraw"      # canonical layout for small diffs
  fi
  rm -f "$tmp"
  node export.mjs "$f.excalidraw" "$f.png" 2
done
if [[ "${1:-}" == "--gif" ]]; then
  command -v vhs >/dev/null || { echo "build: vhs is required for the GIF" >&2; exit 1; }
  vhs demo.tape
fi
echo "build: done"
