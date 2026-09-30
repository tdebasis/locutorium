#!/usr/bin/env python3
"""A stand-in for the loc binary. Its state is files in $LOC_HOME.

mcp            a wrapper that starts a server and waits, as `loc mcp` does
mcp-serve      the server: answers the handshake, holds the seat until its input ends
status <ep>    says whether the seat is registered
read --json    prints queue.jsonl and empties it
send <to> <b>  appends to sent.jsonl
"""
import json
import os
import subprocess
import sys
import time
from datetime import datetime, timezone

home = os.environ["LOC_HOME"]
args = sys.argv[1:]


def path(name):
    return os.path.join(home, name)


if args[:1] == ["mcp"]:
    if os.path.exists(path("refuse")):
        sys.stderr.write("loc: endpoint is held by a LIVE process\n")
        sys.exit(1)
    child = subprocess.Popen([sys.executable, os.path.abspath(__file__), "mcp-serve"])
    sys.exit(child.wait())
elif args[:1] == ["mcp-serve"]:
    with open(path("env.json"), "w") as f:
        json.dump({k: os.environ.get(k) for k in ("LOC_IDENTITY", "LOC_LISTENER_TYPE", "LOC_LISTENER_ADDRESS")}, f)
    sys.stdin.readline()
    sys.stdout.write('{"jsonrpc":"2.0","id":1,"result":{}}\n')
    sys.stdout.flush()
    with open(path("registered"), "w") as f:
        f.write(str(os.getpid()))
    while sys.stdin.readline():
        pass
    os.remove(path("registered"))
elif args[:1] == ["status"]:
    print(args[1])
    if os.path.exists(path("registered")):
        since = datetime.fromtimestamp(os.path.getmtime(path("registered")), tz=timezone.utc)
        stamp = since.strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"
        print("  registered: yes (since %s; webhook 0)" % stamp)
    else:
        print("  registered: no")
elif args == ["read", "--json"]:
    if os.path.exists(path("queue.jsonl")):
        with open(path("queue.jsonl")) as f:
            sys.stdout.write(f.read())
        sys.stdout.flush()
        os.remove(path("queue.jsonl"))
    if os.path.exists(path("slow_read")):
        time.sleep(60)
elif args[:1] == ["send"]:
    with open(path("sent.jsonl"), "a") as f:
        f.write(json.dumps({"from": os.environ.get("LOC_IDENTITY"), "to": args[1], "body": args[2]}) + "\n")
    print("sent → queue.%s uid=00000000-0000-0000-0000-000000000001" % args[1])
else:
    sys.exit(2)
