# `loc` — command reference

Every command needs an identity. `loc` takes it from `LOC_IDENTITY`, else from `hooks/identity`,
else it refuses. There is no anonymous caller.

Errors print `loc: <message>` on stderr and exit **1**. Everything else exits **0**.

| command | does |
|---|---|
| `loc send <endpoint> <body>` | one message into that endpoint's queue |
| `loc publish <topic> <body>` | one message into a topic |
| `loc read [--peek]` | take your messages |
| `loc mcp` | serve this seat to an agent runtime over stdio |
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

`mcp` is how a seat attends: it is this tool's answer to what a background listener used to do — the
same three jobs (register, listen,
wake) done by one process the agent runtime launches instead of by a background listener.

---

## send

```
loc send <endpoint> <body>
```

Delivers one message. The endpoint must be listed in `$LOC_HOME/endpoints`; an unknown name is
refused before anything is sent.

- **Body limit is 4000 characters, not bytes.** Counted in codepoints, so emoji cost one each. An
  over-long body is refused *before* it reaches the medium — nothing is partially sent.
- Prints `sent → queue.<endpoint>`, with a parenthetical when it holds its own bell back (below).
- **The doorbell rings unless the recipient's server is running.** A seat whose server is up is
  watching that queue and will ring for the same arrival — coalesced and breaker-capped — so `send`
  stays quiet rather than putting two lines in one pane for one message, and says so on its own
  stdout: `sent → queue.<endpoint> (the seat's server rings)`. Running means a live process, asked
  of `run/<endpoint>.mcp.pid` and judged on **both** of its lines, the pid and that process's start
  time — a registration is not the question, because a host can register a seat whose server never
  started or has since died, and a pid whose start time no longer matches is a recycled number, not
  the server. Where `send` does ring, the line is the plain `sent → queue.<endpoint>` and the
  doorbell reads `[LOC] 1 new → loc read`, naming the CLI verb, because a seat with no server is a
  seat that reads with the command line.
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
loc read --json
```

Without a flag: presents your queue, then topics — and **consumes** all of it. A message is handed
over exactly once.

Each message is **acknowledged only after it has been shown**, and the acknowledgement is what
deletes it. So if presenting fails part way — a dead terminal, a broken pipe — the message is
**re-presented next time, never dropped.** The failure direction is deliberate: duplicate rather
than lose, and `id` is the key for spotting the duplicate (PROTOCOL.md §5).

A message that has been **fetched but not yet acknowledged is invisible to a following read** for
the length of the consumer's ack-wait — the server's default of **30 s**, which this tool sets
nowhere today. `read` gives such a message back the moment it exits normally: on close it
negatively acknowledges everything it was handed and did not take, so an interrupted read costs a
duplicate and nothing else. A `read` **killed by a signal** (^C, `kill`) never reaches that step,
so its in-flight message comes back only when the window expires — up to that half minute in which
the mailbox reads emptier than it is. Catching the signal and closing through the same path is a
follow-up; this build does not do it yet. A **reader that closes early** — a pipe whose other end
went away, a dead terminal — is not one of those deaths: for this build it is an ordinary **write
error**, so `read` stops without acknowledging and the unshown message is handed back on exit.

Presence events live on their own subject, `presence.<instance>`, so no room stream carries them and a reader is never handed one. `read` still passes over any event it meets — its room cursor advances past each one — because a deployment whose rooms were filled before the split still holds them.

A queue that is empty, or an endpoint that has never had a queue, reads as the two headings and a
zero exit. A **medium that cannot be reached** is the opposite: nothing on standard out, the reason
on standard error, exit 1. Silence and an unreachable broker are different facts and only one of
them is about your mailbox.

There is a third loud answer. If the deployment's access control **refuses the reader a cursor on its own queue** (the consumer-create request on `QUEUE_<instance>_<agent>` is denied), `read` exits non-zero with `cannot read queue.<endpoint>: the medium refused <subject> (permissions) …` on standard error and prints nothing — never an empty mailbox with mail still stored. The fix lives in the deployment: regenerate the access control with `providers/nats/bootstrap.sh`, which grants a seat the cursor on its own queue. An endpoint with no queue at all still reads as the two headings and exit 0; absence is not refusal.

`--peek` is much narrower than it looks, and this is worth knowing before relying on it:

- shows **at most one** queue message, not your backlog
- shows **no topics**, and says so in place of the heading rather than implying an empty room
- consumes nothing

> **Trap, in the shell tool only:** there, any argument that isn't exactly `--peek` is silently
> ignored — so `loc read --pekk` performs a normal, **consuming** read, with no error. The Go build
> **refuses** it: anything but exactly `--peek` prints the verb list and exits 1.

`--json` consumes exactly as a plain read does — the same backlog, the same topics, the same
per-message acknowledgement — but prints each envelope's raw wire bytes, one per line, with **no**
`── queue.x ──` or `── topics ──` headings. The output is JSON Lines and nothing else, for a hook
to parse. `--peek --json` together is refused, same as any other combination that isn't exactly
one recognised flag.

The Go build presents the queue and the topics, and **no spools**. `run/<endpoint>.spool` and
`.wake.spool.raw` belong to the shell tool's listener and to whatever presents what it drained;
this build runs no listener and does not read them.

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

A wake never makes a message unreadable; the shell tool's `read` presents its listener's spools as
well as the queue.

## subscribe

```
loc subscribe <endpoint> --pid <n> --type <t> --version <v>
              [--display <name>] [--role <role>] [--cwd <dir>] [--address <addr>]
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

`--address` is optional and opaque to the bus: it names wherever a delivery mechanism should reach
this instance — a terminal target for one that gets typed into, a runtime-specific session identifier
for one with its own inter-instance push, whatever a given deployment's courier expects. Locutorium
stores and republishes it unexamined; it does not parse, validate, or act on the value. **A
runtime-native push address is only as good as the caller's own guarantee that it stays valid** — for
example, a Claude Code session's display name can drift mid-session if the session was not launched
with an explicit `-n`/`--name` (measured directly: an unnamed session's name changed with nothing
restarted). Satisfying that precondition is the deployment's responsibility; locutorium neither
enforces nor can enforce it.

**Any of these flags is refused if it is given with an empty value** — `--address ""` is an error, and
so is omitting the flag's value entirely. Omit the flag to leave a field unset. This keeps *absent* and
*empty* different facts: every optional field is stored `omitempty`, so a flag passed empty and a flag
never passed produce identical JSON and cannot be told apart afterwards. That costs nothing while a
field is decorative, and costs a great deal once something depends on it — an empty delivery address is
not a missing detail but an endpoint nothing can reach, recorded as though it had been configured, in a
registration that outlives the process which wrote it. Refusing an empty value is not validation of the
value: the string is still never parsed or interpreted.

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

Publishes one lifecycle or activity event on the endpoint's instance subject, `presence.<instance>`.
`<kind>` is one of the taxonomy's kinds (PRESENCE.md §Events → Taxonomy); anything else is a usage
error rather than an event nobody understands.

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
not "zero". The number is what is still **waiting**: once an endpoint has read, it has a cursor, and
a message handed to it and not yet acknowledged is still stored while no longer owed. Where there is
no cursor yet, the queue's own count is the figure — under work-queue retention a stored message is
an untaken one.

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

**Under per-seat servers there is no host process to answer this**, so the question goes unanswered
and the command fails with `no host is answering registry.<instance>`; `status <endpoint>` is the
per-seat reading, and a ledger-backed fallback is tracked separately.

## watch

```
loc watch [<instance>]
```

Follows one instance's event stream — the subject `presence.<instance>` — writing each event out as a
raw line as it arrives, until the connection ends. The stream is live and historyless: a follow shows
what is said from the moment it starts, and nothing that was said before it. Read-only — the credential it uses is denied publish by the server, not merely
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

## mcp

```
loc mcp
```

Serves this seat to whatever agent runtime launched it, over stdin and stdout, speaking the Model
Context Protocol. It does not return: it holds the seat for the life of the runtime's session.

**It is the host, in miniature.** `docs/PRESENCE.md` says whoever launches an agent holds its process
id, which is what makes registration mechanical rather than remembered. A stdio server is launched by
the runtime and dies with it, so it has that shape at seat scale: before it serves anything it
registers this endpoint with its PARENT's pid — the runtime's — the client's name and version from
the initialize handshake, the agent token as the display name and the working directory; when the
runtime lets go (stdin reaches EOF, or a signal arrives) it unsubscribes and exits 0. A seat held by
a registration whose process is DEAD is displaced, and the dead pid is written to the delivery log.
A seat held by a LIVE process that did not launch this server is refused, with the pid named, and the
server exits 1 — the runtime shows it as failed, which is the truth. **The pid registered is whoever
launched this process**, so a shell, a version manager or any other shim placed between the runtime
and the binary becomes that pid and the seat then names a process that is not the agent: a
deployment's config invokes the binary directly, never through a wrapper.

**If the server dies.** The runtime starts this server with the session and does not start it again
if it crashes. A crashed server leaves its registration behind, and the pid that registration
carries is the RUNTIME'S, which is still alive — so the seat reads as present while its bell is
silent and its tools are gone. The failure shows in the runtime's own MCP status, which reports the
server as failed. Recovery is a fresh server: the person restarts the session, or the runtime
reconnects the server where it can do that. The new server displaces the stale registration as it
starts, and writes the displaced pid to the delivery log when that process is gone. `sweep` is the
other route — it clears registrations whose runtime pid is gone, with no restart. Nothing else ever
starts a second server for a seat, and a second server for a live seat is refused by name.

**It runs one generation removed from the runtime.** A runtime may end its MCP child with a HARD
KILL: SIGKILL delivers nothing, runs no handler and closes nothing. A server that was itself the
process the runtime launched has no goodbye to make then, so its registration outlives the session —
the seat reads as `registered: yes` with a process that is gone — until the next server displaces the
dead pid or a sweep removes it. **The periodic sweep is the backstop, and its interval is the bound
on how long a dead seat reads as registered.** What the server can do is not BE the process the
runtime kills. The launched process re-executes this binary with the same stdin, stdout and stderr
and the runtime's pid passed down explicitly, then only waits for it and exits with its status; the
child is the server. Killing the launched process is then not an ending at all: the child still holds
the runtime's descriptors, keeps the seat and keeps ringing, and departs when the pipe finally
closes. **The pipe closing is the goodbye a kill cannot take away**, so in the common case — the
runtime exits and its descriptors close — the seat is given up within the two-second departure bound.
A signal that IS delivered (`TERM`, `INT`, `HUP`) is forwarded down and waited on. The registration
still names the RUNTIME'S pid, not the launched process's and not the server's; which process is
actually serving is recorded separately in `run/<endpoint>.mcp.pid`, written once the listener is up
— a sender reads this file to decide it need not ring, so it must not exist before there is a bell —
and removed at departure. **That file is two lines — the pid, then that process's start time
in the presence model's stamp** — because the pid alone starts naming a stranger the moment the
kernel reuses the number, and a reader asking "is this seat's server running" would then get a
confident yes about somebody else. Both lines are compared, by the same liveness the presence model
uses on the pids it records. **A server ended by SIGKILL leaves the file behind**, since removing it
is part of departing and a kill runs nothing: `send` then reads the two lines, asks the presence
model whether that pid started at that time is alive, finds it is not, and treats the file as stale
— so it rings the pane itself rather than staying quiet for a server that is gone.

**It is also the listener.** It holds a core subscription on this endpoint's own queue subject, which
sees every arrival and consumes nothing, and asks how much is waiting at start and after every
reconnect. Arrivals are coalesced across `wake_window_seconds` and capped by
`wake_breaker_per_minute` and `wake_breaker_per_hour` — the same three keys the shell tool's listener
reads. Each wake calls the deployment's `hooks/nudge` with ONE LINE and no body:

```
🔔 3 new → read
```

and appends `wake <endpoint> count=3` to `run/<endpoint>.delivery.log`. A tripped breaker says so
once. Nothing is lost to a suppressed wake: the queue keeps the truth.

**A bell that could not ring says so.** If the hook is missing, not executable, or exits non-zero,
`bell failed <endpoint>: <reason>` goes to `run/<endpoint>.delivery.log` *and* to stderr, which is
the runtime's own log — stdout is the protocol's. No `wake` line is written for it: a failed bell is
not a wake, and a log saying the pane was woken when it was not is worse than no log at all.

**Four tools, and each is the verb of the same name** — `send {to, body}`, `read {peek?}`,
`status {endpoint?}`, `topics {}`. Each runs this binary's own function and returns exactly what the
command line prints, refusals included (`loc: <what>`). Around every call the server emits
`tool.pre` and `tool.post` for this seat, naming the tool.

**`read` opens with a fixed reminder, written by the server.** The bell carried no body, so nothing
the tool returns has been in front of a person yet; an agent that summarises it instead of printing
it has destroyed the delivery silently. The line is followed by a blank line and then exactly what
`loc read` prints:

```
Nothing below has been shown to anyone yet; the bell only rang. Print it on screen verbatim, then act on it.

── queue.workshop.scribe ──
  `workshop.clerk -> workshop.scribe`  09:00  a question about the ledger
── topics ──
```

Every `read` appends `read <endpoint> handed=<n>` to the delivery log, so a message that was read and
never shown can still be found. **The acknowledgement is made while the result is being rendered,
and the result is returned afterwards** — so a client that drops between the two loses those
messages: they are gone from the queue and they never reached the agent, and the `handed=<n>` line
is all that is left of them. Acknowledging only after delivery is tracked separately, as issue #42.

**Configuring a runtime.** The server needs two things: to be launched, and to be told which endpoint
it is. For Claude Code, `.mcp.json`:

```json
{"mcpServers": {"loc": {"command": "loc", "args": ["mcp"], "env": {"LOC_IDENTITY": "<instance>.<agent>"}}}}
```

For a runtime configured in TOML, `.codex/config.toml`:

```toml
[mcp_servers.loc]
command = "loc"
args = ["mcp"]
env = { LOC_IDENTITY = "<instance>.<agent>" }
```

**The fallback.** A runtime that cannot launch a stdio server uses the command-line verbs directly —
`loc read`, `loc send`, `loc status`, `loc topics`. What is lost is the automatic registration and
the bell; something else must then run `subscribe` and `unsubscribe` around the session.

---

## Configuration

`$LOC_HOME/config`, one `key = value` per line.

| key | default | effect |
|---|---|---|
| `provider` | none | which provider backs this house |
| `nats_url` | `nats://127.0.0.1:4222` | where the medium is |
| `idle_window` | `10m` | how long after an event `status <endpoint>` still reads *active* |
| `send_requires_attendance` | `no` | refuse sends to endpoints that are not attending. With the NATS provider the key is satisfied by the live queue: a subscribed peer attends, and an unsubscribed one has no queue, so its send is refused for absence. |
| `monitor_url` | none | written by `bootstrap.sh`; no verb reads it — `registry` asks the instance's host over the bus |
| `topic_window` | `7d` | how long topic messages live |
| `wake_window_seconds` | `5` | wakes are coalesced across this window (`loc mcp`) |
| `wake_breaker_per_minute` | `6` | cap on wakes per minute (both listeners) |
| `wake_breaker_per_hour` | `60` | cap on wakes per hour (both listeners) |

Every key above except `monitor_url` and `topic_window` is read by the Go build; the three wake keys
are read by BOTH listeners, so a deployment tunes one set of numbers whichever one it runs.
`registry`'s wait for a host is fixed in the code, not a key.

When a breaker trips it says so in `run/<endpoint>.delivery.log` and **suppresses only the wake**.
No message is lost; the next read still finds everything.
