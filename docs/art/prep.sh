# prep.sh — sourced (hidden) by demo.tape: a scratch house on a random port, so every
# line the GIF shows is the tool's real output. Nothing here touches a real deployment.
R="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# build/bin, not bin: `make build` writes there. $R/bin does not exist, so this line
# used to fall through to whatever `loc` was installed on the machine — which is how a
# recording drifts away from the build it claims to show.
PATH="$R/build/bin:$PATH"
# DEMO_ROOT is remembered so the teardown can prove it is removing the folder THIS script
# made and no other.
DEMO_ROOT="$(mktemp -d)"
export LOC_HOME="$DEMO_ROOT/house"
# The port is never 4222: this runs on a machine whose own broker holds it.
P=$((20000 + RANDOM % 20000))
mkdir -p "$LOC_HOME"
printf 'provider = nats\nnats_url = nats://127.0.0.1:%s\n' "$P" > "$LOC_HOME/config"
loc start >/dev/null
# THE TEARDOWN REFUSES ANY HOME BUT THE ONE MADE ABOVE. `loc stop` reads LOC_HOME from
# the environment, and an unset LOC_HOME means the default home: on a machine that runs
# the product, that is the live deployment, and `loc stop` would end its daemon and lose
# every unread message. So the check comes first and an ambient value is never trusted.
# The daemon is stopped BEFORE the home is removed, and the home is removed only when
# the stop worked: the home holds the config that keeps a survivor on its own port.
demo_teardown() {
  if [ -z "${DEMO_ROOT:-}" ] || [ "${LOC_HOME:-}" != "$DEMO_ROOT/house" ]; then
    echo "demo teardown: LOC_HOME is not the folder this demo made; nothing stopped, nothing removed" >&2
    return 1
  fi
  loc stop && rm -rf "$DEMO_ROOT"
}
# Both seats attend before the demo speaks. A queue exists because something
# subscribed; with nobody attending, `send` has nowhere to deliver and refuses.
# The listener type is `none`: this house rings no bell, it only holds mail.
# The pid is this shell, which outlives the recording.
for ep in house.ada house.bob; do
  loc subscribe "$ep" --pid $$ --type none --version 1 >/dev/null 2>&1
done
printf '\033[2J\033[3J\033[H'
