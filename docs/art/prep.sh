# prep.sh — sourced (hidden) by demo.tape: a scratch house on a random port, so every
# line the GIF shows is the tool's real output. Nothing here touches a real deployment.
R="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# build/bin, not bin: `make build` writes there. $R/bin does not exist, so this line
# used to fall through to whatever `loc` was installed on the machine — which is how a
# recording drifts away from the build it claims to show.
PATH="$R/build/bin:$PATH"
export LOC_HOME="$(mktemp -d)/house"
# The port is never 4222: this runs on a machine whose own broker holds it.
P=$((20000 + RANDOM % 20000))
mkdir -p "$LOC_HOME"
printf 'provider = nats\nnats_url = nats://127.0.0.1:%s\n' "$P" > "$LOC_HOME/config"
loc start >/dev/null
export DEMO_SERVER="$(sed -n 1p "$LOC_HOME/run/loc.pid" 2>/dev/null)"
# Both seats attend before the demo speaks. A queue exists because something
# subscribed; with nobody attending, `send` has nowhere to deliver and refuses.
# The listener type is `none`: this house rings no bell, it only holds mail.
# The pid is this shell, which outlives the recording.
for ep in house.ada house.bob; do
  loc subscribe "$ep" --pid $$ --type none --version 1 >/dev/null 2>&1
done
printf '\033[2J\033[3J\033[H'
