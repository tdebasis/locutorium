# prep.sh — sourced (hidden) by demo.tape: a scratch house on a random port, so every
# line the GIF shows is the tool's real output. Nothing here touches a real deployment.
R="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PATH="$R/bin:$PATH"
export LOC_HOME="$(mktemp -d)/house"
# The port is never 4222: this runs on a machine whose own broker holds it.
P=$((20000 + RANDOM % 20000))
mkdir -p "$LOC_HOME"
printf 'provider = nats\nnats_url = nats://127.0.0.1:%s\n' "$P" > "$LOC_HOME/config"
loc start >/dev/null
export DEMO_SERVER="$(sed -n 1p "$LOC_HOME/run/loc.pid" 2>/dev/null)"
printf '\033[2J\033[3J\033[H'
