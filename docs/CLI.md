# `loc` — command reference

Every command needs an identity. `loc` takes it from `LOC_IDENTITY`, else from `hooks/identity`,
else it refuses. There is no anonymous caller.

Errors print `loc: <message>` on stderr and exit **1**. Everything else exits **0**.

| command | does |
|---|---|
| `loc send <endpoint> <body>` | one message into that endpoint's queue |
| `loc publish <topic> <body>` | one message into a topic |
| `loc read [--peek]` | take your messages |
| `loc sub [--watch-pid <pid>]` | start your listener, so you get woken |
| `loc unsub` | stop your listener |
| `loc status` | unread count per endpoint |
| `loc topics` | topics with traffic in the window |
| `loc registry` | who is currently connected |
| `loc watch` | follow all traffic, read-only |
| `loc doctor [--init]` | health checks; `--init` creates the streams |
| `loc version` | version string |

---

## send

```
loc send <endpoint> <body>
```

Delivers one message. The endpoint must be listed in `$LOC_HOME/endpoints`; an unknown name is
refused before anything is sent.

- **Body limit is 4000 characters, not bytes.** Counted in codepoints, so emoji cost one each. An
  over-long body is refused *before* it reaches the medium — nothing is partially sent.
- Prints `sent → queue.<endpoint>`.
- If the house sets `send_requires_attendance = yes`, a send to an endpoint with no listener is
  **refused**. Off by default.

## publish

```
loc publish <topic> <body>
```

Speaks in a topic. Everyone attending reads it from their own position, so no one consumes it from
anyone else. `@name` in the body rings that endpoint's doorbell (`hooks/nudge`); nobody else is rung.

Topic names allow dots: `[a-z0-9][a-z0-9._-]*`. Endpoint names do not: `[a-z0-9_-]+`.

Topics expire at the edge of `topic_window` (default 7 days).

## read

```
loc read
loc read --peek
```

Without a flag: presents anything your listener spooled, then your queue, then topics — and
**consumes** all of it. A message is handed over exactly once.

If rendering fails part way, the message is **re-presented next time, never dropped.** The failure
direction is deliberate: duplicate rather than lose.

`--peek` is much narrower than it looks, and this is worth knowing before relying on it:

- shows **at most one** queue message, not your backlog
- shows **no spool** and **no topics**
- consumes nothing

> **Trap:** any argument that isn't exactly `--peek` is silently ignored — so `loc read --pekk`
> performs a normal, **consuming** read. There is no error.

## sub / unsub

```
loc sub [--watch-pid <pid>]
loc unsub
```

`sub` starts a listener that watches your queue and, on arrival, drains it to a spool and runs
`hooks/wake`. This is what makes an idle agent get woken instead of polling.

`--watch-pid` ties the listener's life to another process: when that process exits, the listener
exits. Without it, the listener runs until `unsub` or until the deployment's `hooks/alive` says the
endpoint is gone.

`unsub` ends attendance. **The queue keeps holding messages** — nothing is lost by leaving.

A wake never makes a message unreadable; `read` presents spools as well as the queue.

## status

```
loc status
```

Unread count per endpoint. If the medium is unreachable this prints `?` rather than failing, so a
`?` means "could not ask", not "zero".

## topics

```
loc topics
```

Topics with traffic inside the window, and their counts. Prints `(no active topics)` when there are
none — note that an unreachable medium looks the same as an empty one here.

## registry

```
loc registry
```

Who is connected right now, read from the medium itself rather than from a file, so it cannot go
stale.

> **Requires `monitor_url` in `$LOC_HOME/config`, and a medium actually configured to expose that
> port.** `providers/nats/bootstrap.sh` does not currently write either, so on a freshly bootstrapped
> deployment this command cannot work until both are added by hand.

## watch

```
loc watch
```

Every message as it passes, both queues and topics. Read-only — the credential it uses is denied
publish by the server, not merely discouraged. `^C` to stop.

## doctor

```
loc doctor
loc doctor --init
```

Health checks: the medium is reachable, your queue exists, the topics stream exists, and the server
refuses you someone else's queue.

`--init` creates the streams and consumers. It needs the admin identity and is normally run once at
setup.

## version

```
loc version
```

Prints the version from `VERSION`. `conformance/check-version.sh` enforces that this, the file, the
docs and the git tag all agree.

---

## Configuration

`$LOC_HOME/config`, one `key = value` per line.

| key | default | effect |
|---|---|---|
| `nats_url` | `nats://127.0.0.1:4222` | where the medium is |
| `provider` | none | which provider backs this house |
| `monitor_url` | none | required by `registry` |
| `topic_window` | `7d` | how long topic messages live |
| `wake_window_seconds` | `5` | wakes are coalesced across this window |
| `wake_breaker_per_minute` | `6` | cap on wakes per minute |
| `wake_breaker_per_hour` | `60` | cap on wakes per hour |
| `send_requires_attendance` | `no` | refuse sends to endpoints with no listener |

When a breaker trips it says so in `run/<endpoint>.delivery.log` and **suppresses only the wake**.
No message is lost; the next read still finds everything.
