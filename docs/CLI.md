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
| `loc subscribe <endpoint> ...` | register an agent instance and create its queue |
| `loc unsubscribe <endpoint> [--force]` | free the endpoint and destroy its queue |
| `loc sweep [<instance>]` | unsubscribe registrations whose process is gone |
| `loc emit <kind> <endpoint> ...` | publish one lifecycle or activity event |
| `loc status` | unread count per endpoint |
| `loc status <endpoint>` | one agent's three facts, each with its reason |
| `loc topics` | topics with traffic in the window |
| `loc registry [<instance>] [--json]` | who is registered in an instance, asked of that instance's host; fails when no host answers |
| `loc watch [<instance>]` | follow an instance's event stream, read-only |
| `loc doctor [--init]` | health checks; `--init` creates the streams |
| `loc version` | version string |

`read`, `sub`, `unsub` and `doctor` are the shell tool's verbs. The Go build names them and answers
`not implemented in this build`; every other row it answers itself.

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

## subscribe

```
loc subscribe <endpoint> --pid <n> --type <t> --version <v>
              [--display <name>] [--role <role>] [--cwd <dir>]
```

Registers one agent instance, creates its queue, and publishes `agent.subscribe` — the one heavy
event, carrying the whole registration so that nothing later has to repeat it.

The endpoint is fully qualified, `<instance>.<agent>`, each segment `[a-z0-9-]+`; the underscore is
barred, because the backing object's name is the endpoint with its dots substituted for underscores
and the substitution has to stay injective (PRESENCE.md §Subjects, endpoints and queues). `--pid` is
the process id of the agent that was launched, and `--type` and `--version` say what program it is.
`--display` and `--role` are what a person or a display should call it and what it is there to do;
both are optional, and a role that was not given is absent from the event rather than empty. `--cwd`
records where it is working.

An endpoint holds **one instance at a time, and a held one is refused** (non-zero), naming the
incumbent's process — displacing it is a deliberate act by whoever knows the old process is
finished, not a side effect of somebody else subscribing (PRESENCE.md §One agent per endpoint).

A pid that is already gone is registered anyway, with no start time. The caller is reporting what it
launched; noticing that it did not survive is `sweep`'s job.

## unsubscribe

```
loc unsubscribe <endpoint> [--reason clean|expiry] [--force]
```

Removes the registration, destroys the queue, and publishes `agent.unsubscribe`. Leaving cleanly and
leaving by expiry are the same occurrence with different causes, so they are one event with a
reason rather than two kinds; `--reason` is `clean` by default and takes nothing else.

Plain `unsubscribe` is safe reconciliation: an empty endpoint is a no-op that announces nothing, a
dead incumbent is cleared, and a **live one is refused** (non-zero, naming its process). That is
what makes an unconditional unsubscribe-then-subscribe restart safe — a crashed predecessor is
cleared, a working agent is never displaced by accident. `--force` is the deliberate displacement,
and it is a separate word because it is a separate decision (PRESENCE.md §How an agent leaves).

## sweep

```
loc sweep [<instance>]
```

Checks every registration in the namespace against its recorded process and unsubscribes the ones
whose process is gone, with reason `expiry`. An agent that crashes cannot announce its own
departure, so something has to do it on the agent's behalf.

**It must run on the machine holding the processes** — a process id means nothing anywhere else.
With `<instance>` omitted it means the caller's own, taken from its identity; an identity that is
not of the form `<instance>.<agent>` is refused, because there is nothing to take one from. A sweep
with nothing to reap publishes nothing, and that silence is what makes it safe to run on a timer.

## emit

```
loc emit <kind> <endpoint> [--ts <t>] [--tool <name>] [--refs <id,id>]
```

Publishes one lifecycle or activity event. `<kind>` is one of the taxonomy's kinds (PRESENCE.md
§Events → Taxonomy); anything else is a usage error rather than an event nobody understands.

`--ts` is the moment the thing happened, carried verbatim — an adapter should always pass it, since
the publish time is only correct for something reporting itself immediately. `--refs` is a
comma-separated list of event ids.

**This is the one verb that does not report an unreachable medium.** It runs inside the agent's own
lifecycle hook, so anything it waits on the agent waits on: the event is dropped and the exit is
clean. The local activity record is updated either way, because an event that could not be published
still happened, and a reader asking later deserves the truth about it.

## status

```
loc status
loc status <endpoint>
```

Bare, it is the message plane's report: the unread count for each endpoint in `$LOC_HOME/endpoints`.
If the medium is unreachable this prints `?` rather than failing, so a `?` means "could not ask",
not "zero". A namespaced endpoint's queue has no consumer on it, so the number is what that queue is
holding — under work-queue retention a stored message is an untaken one.

With an endpoint it is the presence report: three facts, **each with the reason for it**. *Away*
because there is no process and *idle* because no event arrived inside the window are different
situations, and a report that printed only the conclusion would hide which one it is looking at
(PRESENCE.md §Command-line operations).

```
workshop.scribe
  registered: no
  process:    unknown (no registration)
  activity:   unknown (no registration)
```

It touches no medium — every answer it gives is held locally, which is what lets it answer at all
when the broker is the thing that is wrong. An unregistered endpoint is a truthful answer to a fair
question rather than a failure, so it exits **0**; a name that is not of the form
`<instance>.<agent>`, or an `idle_window` this deployment cannot parse, exits **1**. An event counts
as current for `idle_window` after it arrived.

## topics

```
loc topics
```

Topics with traffic inside the window, and their counts. Prints `(no active topics)` when there are
none — note that an unreachable medium looks the same as an empty one here.

## registry

```
loc registry [<instance>] [--json]
```

Who is registered in an instance, asked of that instance's host. Events describe changes and the bus
keeps no history, so a consumer that started after an agent joined has missed the only event that
carried its details; this asks for the current picture instead of replaying a history that does not
exist (PRESENCE.md §Who is here right now).

The request goes out on `registry.<instance>` and the host answers, because the host holds the
registrations — it created them. **Nobody answering is a failure, not an empty roster**: the command
exits non-zero and names the subject that went unanswered. A host that answers with an empty roster
prints `(no agents registered)` and exits **0**; the two are different, and telling them apart is
the point.

With `<instance>` omitted it means the caller's own, taken from its identity. `--json` hands back
what the host said, unreshaped — a consumer wants the reply, and a reply this tool had rewritten
would be a second format to learn. The wait for an answer is a fixed two seconds.

## watch

```
loc watch [<instance>]
```

Follows one instance's event stream, writing each event out as a raw line as it arrives, until the
connection ends. Read-only — the credential it uses is denied publish by the server, not merely
discouraged. `^C` to stop. With `<instance>` omitted it means the caller's own, taken from its
identity. A follow that simply ends is not an error; a follow that ends badly is reported.

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
| `provider` | none | which provider backs this house |
| `nats_url` | `nats://127.0.0.1:4222` | where the medium is |
| `idle_window` | `10m` | how long after an event `status <endpoint>` still reads *active* |
| `send_requires_attendance` | `no` | refuse sends to endpoints with no listener |
| `monitor_url` | none | read by the shell tool's own `registry`, and written by its bootstrap; no Go verb reads it |
| `topic_window` | `7d` | how long topic messages live (the shell tool) |
| `wake_window_seconds` | `5` | wakes are coalesced across this window (the shell tool) |
| `wake_breaker_per_minute` | `6` | cap on wakes per minute (the shell tool) |
| `wake_breaker_per_hour` | `60` | cap on wakes per hour (the shell tool) |

The first four are every key the Go build reads; the rest belong to the shell tool. `registry`'s
wait for a host is fixed in the code, not a key.

When a breaker trips it says so in `run/<endpoint>.delivery.log` and **suppresses only the wake**.
No message is lost; the next read still finds everything.
