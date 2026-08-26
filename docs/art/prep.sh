# prep.sh — sourced (hidden) by demo.tape: a scratch house on a random port, so every
# line the GIF shows is the tool's real output. Nothing here touches a real deployment.
R="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PATH="$R/bin:$PATH"
export LOC_HOME="$(mktemp -d)/house"
P=$((20000 + RANDOM % 20000))
"$R/providers/nats/bootstrap.sh" ada bob carol >/dev/null
sed -i '' "s|127.0.0.1:4222|127.0.0.1:$P|" "$LOC_HOME/config" "$LOC_HOME/nats-server.conf"
nats-server -c "$LOC_HOME/nats-server.conf" >"$LOC_HOME/server.log" 2>&1 &
export DEMO_SERVER=$!
sleep 1
LOC_IDENTITY=admin loc doctor --init >/dev/null
printf '\033[2J\033[3J\033[H'
