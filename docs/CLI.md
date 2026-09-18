# `loc` — command reference

Every command needs an identity. `loc` takes it from `LOC_IDENTITY`, and refuses when that variable
is empty or unset. There is no anonymous caller and no second place to look.

Errors print `loc: <message>` on stderr and exit **1**. Everything else exits **0**.

| command | does |
|---|---|
| `loc start` | boot the broker and the heartbeat, detached |
| `loc stop [--force]` | stop the daemon, and the broker it runs |
| `loc send <endpoint> <body>` | one message into that endpoint's queue |
| `loc publish <topic> <body>` | one message into a topic |
| `loc read [--peek]` | take your messages |
| `loc mcp` | serve this seat to an agent runtime over stdio |
| `loc subscribe <endpoint> ...` | register an agent instance and create its queue |
| `loc unsubscribe <endpoint> [--force]` | free the endpoint and destroy its queue |
| `loc sweep` | reconcile every row, queue and process |
| `loc emit <kind> <endpoint> ...` | publish one lifecycle or activity event |
| `loc status` | the daemon, every seat, and the mail waiting |
| `loc status <endpoint>` | one agent's three facts, each with its reason |
| `loc topics` | topics with traffic in the window |
| `loc registry [<instance>] [--json]` | who is registered in an instance, asked of that instance's host; fails when no host answers |
| `loc watch [<instance>]` | follow an instance's event stream, read-only |
| `loc version` | version string |

`mcp` is how a seat attends: it is this tool's answer to what a background listener used to do — the
same three jobs (register, listen,
wake) done by one process the agent runtime launches instead of by a background listener.

---

## start

```
loc start
loc start --serve
```

Boots the deployment. The broker runs **inside** `loc`, in a process this one starts and leaves
running; the bare form is the launcher and `--serve` is that process. Nobody types `--serve`.

`loc start` probes `nats_url`. If something answers it reads `run/loc.pid`:

| what it finds | what it prints | exit |
|---|---|---|
| the pidfile names a live process | `already running, pid N` | 0 |
| no pidfile, or one naming a process that is gone | `port N is held by a process loc did not start` | 0 |
| nothing answers | `started, pid N`, once the port answers | 0 |
| nothing answers, and nothing answers within ten seconds | the failure, and the log to read | 1 |

**The first run writes `config`** from the key table and prints every default, so the values are
explicit at that boot. Every later start prints one line: the path, and that it is the place to
change settings.

**The listen address must be loopback.** `loc start` refuses any other, and says why. The broker has
no authentication in this version, so an address the network can reach would offer every queue in
the deployment to it.

The daemon then does five things and repeats the last one:

1. Deletes the heartbeat logs older than `heartbeat_log_retention_days`.
2. Boots the broker with JetStream on `$LOC_HOME/store`, which it creates itself.
3. Creates the `TOPICS` stream when the store does not hold it.
4. Writes `run/loc.pid`: the pid, then the start time the operating system reports.
5. Sweeps, and appends one line to `run/heartbeat/<date>.log`. **Every five minutes**, it sweeps and
   appends again — `<RFC3339> swept, N changes`, or `<RFC3339> sweep failed: <what>`.

A beat that finds nothing still writes its line. A log with no line for a period is a heartbeat that
did not run, and that is the failure worth seeing.

Its own output goes to `run/loc.log`, because a detached process has no terminal.

`SIGTERM` or `SIGINT` stops the ticker, shuts the broker down, removes the pidfile and exits **0**.
Later signals are ignored, so nothing interrupts that.

## stop

```
loc stop [--force]
```

| what it finds | what it prints | exit |
|---|---|---|
| a pidfile naming a live daemon | `stopped`, once the port closes | 0 |
| no live pidfile, and nothing on the port | `not running` | 0 |
| no live pidfile, and the port held | three lines: the port is held by a process `loc` did not start; stopping it could stop a broker another supervisor owns; `to stop it anyway: loc stop --force` | 1 |
| `--force`, and a pid found holding the port | `stopped` | 0 |
| `--force`, and no pid found | that no process was found on the port | 1 |

**The refusal is the point.** A broker on this port may be another supervisor's, and a stop is not
recoverable. `--force` finds the pid by asking the operating system — `lsof` on macOS, `ss` on
Linux, `netstat` on Windows. There is no monitoring port to ask.

`loc stop` removes the pidfile itself when the daemon did not, so a daemon that was killed outright
leaves nothing behind for the next `loc start` to misread.

---

## send

```
loc send <endpoint> <body>
```

Delivers one message. The roster is the ledger: the endpoint must be a registered seat, and an
unknown name is refused before anything is sent.

- **Body limit is 4000 characters, not bytes.** Counted in codepoints, so emoji cost one each. An
  over-long body is refused *before* it reaches the medium — nothing is partially sent.
- Prints `sent → queue.<endpoint> uid=<uuid>`. There is one form of that line and it does not vary
  with what is running at the far end. The `uid` is the message's id. Use it to find the message in
  the day's record, and to find the `read` line written under the same id when somebody takes it.
- A refused send writes a `failed` line to the day's record. The line carries the `uid`, the reason,
  and no body. See `OPERATORS.md`.
- **`loc send` puts the message in the queue and rings nothing.** The seat's own server is the one
  thing that notifies, through the notifier its registered type names: `tmux` types the bell into the
  seat's pane, `claude` sends it through a one-shot courier, and `none` rings nothing at all — a seat
  registered as `none` finds its mail on its next `read`. A sender that rang as well would be a
  second bell for one message, and it is the one that knows least: it cannot coalesce, it is not
  capped, and it does not know what else is waiting for that seat.
- If the house sets `send_requires_attendance = yes`, a send to an endpoint with no listener is
  **refused**. Off by default.

## publish

```
loc publish <topic> <body>
```

Speaks in a topic. Everyone attending reads it from their own position, so no one consumes it from
anyone else. **A `@name` mention is delivered to the topic and announced to nobody.** The named
endpoint reads the topic from its own position and finds the mention there. No bell is rung for a
mention, and its absence is the design rather than a defect.

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

Each message taken from your **queue** writes a `read` line to the day's record. The line carries the
message's `uid` and your endpoint name. It is written after the acknowledgement, so a recorded read
is a read that happened. A **topic** message writes nothing: everyone attending reads it from their
own position, so no one read is a fact about the message. `--peek` writes nothing, because it
consumes nothing. See `OPERATORS.md`.

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

There is a third loud answer. If the deployment's access control **refuses the reader a cursor on its own queue** (the consumer-create request on `QUEUE_<instance>_<agent>` is denied), `read` exits non-zero with `cannot read queue.<endpoint>: the medium refused <subject> (permissions) …` on standard error and prints nothing — never an empty mailbox with mail still stored. V0 sets no access control, so a broker that refuses here is not one `loc` configured; the fix lives in whatever wrote that broker's rules. An endpoint with no queue at all still reads as the two headings and exit 0; absence is not refusal.

`--peek` is much narrower than it looks, and this is worth knowing before relying on it:

- shows **at most one** queue message, not your backlog
- shows **no topics**, and says so in place of the heading rather than implying an empty room
- consumes nothing

> **This build refuses a misspelled flag.** Anything but exactly `--peek` prints the verb list and
> exits 1. The deleted shell tool ignored the argument instead, so `loc read --pekk` performed a
> normal, **consuming** read with no error. That trap went with it.

`--json` consumes exactly as a plain read does — the same backlog, the same topics, the same
per-message acknowledgement — but prints each envelope's raw wire bytes, one per line, with **no**
`── queue.x ──` or `── topics ──` headings. The output is JSON Lines and nothing else, for a hook
to parse. `--peek --json` together is refused, same as any other combination that isn't exactly
one recognised flag.

This build presents the queue and the topics, and **no spools**. The files `run/<endpoint>.spool`
and `.wake.spool.raw` belonged to the deleted shell tool. No separate listener process exists here.
The seat's own `mcp` server is the listener, and it writes no spool.

## transcript

```
loc transcript
loc transcript --seat <endpoint>
loc transcript --uid <uuid>
loc transcript --pending
loc transcript --json
loc transcript 2026-09-16
```

The day's record, read back as messages. It reads every `$LOC_HOME/run/log/*.jsonl` file. It joins
the lines by `uid`. It prints one block per message, oldest first.

```
2026-09-16T14:12:22Z workshop.scribe → workshop.clerk  read 14:12:40Z by workshop.clerk
  the ledger is ready
```

The first line holds the send stamp, the sender, the recipient and the outcome. The lines under it
hold the body as it was sent. A refused message carries no body, so it has no second line.

**It writes nothing.** It opens the log files for reading and nothing else. It touches no medium,
so it answers when the broker is the thing that is wrong.

**The outcome is derived. The record does not store it.** A `sent` line is written before anybody
reads, and the `read` line that answers it lands in the file for the day the reader was in. The
outcome comes from the last event under that `uid`, in file order and then line order.

| outcome | when |
|---|---|
| `read <ts> by <seat>` | the last event is `read` |
| `pending` | the last event is `sent` |
| `pending, bell failed <ts>: <reason>` | pending, and the recipient's seat has a later `bell-failed` |
| `lost: queue deleted <ts> (<reason>)` | pending, and the recipient's seat has a later `queue-deleted` |
| `failed: <reason>` | the last event is `failed` |

A seat event joins a message on two facts: the seat is the recipient, and the event is later than
the send. Where both a failed bell and a deleted queue apply, the later one wins. A stamp on the
day of the send prints as a time. A stamp on any other day prints whole, so a read after midnight
is never read as a read before the send.

Flags:

- `--seat <endpoint>` shows the messages that seat sent or was sent. It also shows that seat's own
  events, one line each, in time order among the blocks. An event already named in a message's
  outcome is not printed again.
- `--uid <uuid>` shows one message. It also prints that message's raw log lines under the body, so
  you can disagree with the outcome.
- `--pending` shows only the messages whose last event is `sent`. It excludes topic messages: a
  room message is read from each attender's own cursor, so it never gets a `read` line and would
  sit in this list for ever.
- `--json` prints one JSON object per message, one per line, with the fields `uid`, `from`, `to`,
  `sent_ts`, `body`, `outcome` and `events`. The `events` field holds the raw log lines in order.
  This flag prints no seat lines and no empty-record line.
- `date` limits the output to the messages **sent** on that UTC day. Write it `YYYY-MM-DD`. Their
  later events are still read from every file.

A record with no messages in it prints `(no messages in the record)` and exits **0**.

A line that is not a JSON object is skipped. The verb names the file and the line number on
standard error, then prints the rest. One truncated line must not hide the day under it. A log file
it cannot open is named the same way and skipped the same way. Neither one changes the exit code:
the read succeeded.

See `OPERATORS.md` §The day's record for the lines themselves.

## sub / unsub

**This build has no `sub` and no `unsub` verb.** They belonged to the shell tool, which was deleted
on 2026-09-07. This section is kept so that older links still land somewhere true.

Attendance is the `mcp` verb. The agent runtime launches one `loc mcp` server per seat. That server
registers the seat, watches its queue, and rings the seat's bell. See §mcp below.

The server's life is the runtime's life. It ends when the runtime that launched it ends.

**The queue keeps holding messages** when a seat is away. Nothing is lost by leaving.

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
loc sweep
```

Reconciles the three records a seat has: its **ledger row**, its **queue** on the medium, and its
**process**. It reads every row and lists every queue, then makes three passes in this order.

| the disagreement | the repair |
|---|---|
| a row whose process is gone | delete the queue, the row and the seat's server pidfile |
| a queue no row claims | delete the queue |
| a live row with no queue | create the queue; the row is not rewritten |

**It takes no argument.** A process id means something only on the machine holding it, so a sweep is
machine-wide by nature; reconciling one instance would leave the other instances' queues looking
like queues that nobody claims.

**A row it cannot read stops the second pass entirely,** and the sweep exits non-zero after naming
the row. That row may be the one that claims a queue the pass would destroy, and there is no way to
ask which. The other two passes still run.

**It publishes nothing.** A departure event says an agent left; a reap says a record was wrong, and
the record may have gone stale hours earlier. It prints one line per change and returns the number
of changes it made. With nothing to repair it prints nothing and exits **0**, which is what makes it
safe to run on a timer.

The order of the passes is load-bearing in `subscribe` and `unsubscribe` too. Each is two writes,
and a sweep can land between them, so `subscribe` writes the row and then creates the queue, and
`unsubscribe` removes the row and then deletes the queue. The gap is then always the state a sweep
resolves in the caller's favour: a subscribe interrupted mid-way is repaired, an unsubscribe
interrupted mid-way is finished.

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

Bare, it is the deployment's report, in three sections, and **it changes nothing**.

First one line for the daemon: `daemon: not running`, or `daemon: running, pid <n>, last beat
<time>`, read from `$LOC_HOME/run/loc.pid` and the last line of
`$LOC_HOME/run/heartbeat/<today>.log`. A daemon that has started and not yet beaten says `no beat
yet`.

Then one line per seat, from the same check the sweep acts on — so `status` is a dry run of the next
beat and the two can never disagree about what is wrong:

```
workshop.scribe: row ok, queue ok
workshop.crier: row ok, queue ok, bell FAILED 2026-09-16T22:47:36Z: notifier exited 127
workshop.clerk: queue missing, next beat repairs it
atelier.scribe: pid dead, next beat reaps it
workshop.legacy: row unreadable: <reason>
atelier.stray: queue with no row, next beat removes it
```

**It performs none of those repairs.** Run it twice and the answer is the same, because a read that
fixed what it reported would destroy the evidence the operator asked for.

The row gives three facts. Each fact has its own mechanism. No fact follows from another.

- *Registered* — the ledger holds a row for this seat. The seat exists.
- *Attending* — the queue exists. A message to this seat has somewhere to wait.
- *Notifiable* — the bell reaches this seat's operator. The operator learns that mail arrived.

`row ok` does not mean `can be told`. A seat can hold a row and a queue, and still take no more
mail, because nothing rings for its operator. Two seats ran in that state for a day on 2026-09-11.

`status` adds the `bell FAILED` field when the message log holds a `bell-failed` event for the seat.
The event must not be before the second of the seat's registration (a registration is stamped in
milliseconds, a bell failure in whole seconds, and a bell dead at startup fails in that same second);
an older one belongs to a previous occupant of
the endpoint. A `read` by that seat after the event removes the field again — the seat took its
mail, so something reaches it. The log is `$LOC_HOME/run/log/<day>.jsonl`. A log that `status`
cannot read adds no field, and it does not fail the report.

Then the unread count for each **registered seat**. The roster is the ledger, not the `endpoints`
file: a seat exists because it subscribed. If the medium is unreachable this prints `?` rather than
failing, so a `?` means "could not ask", not "zero". The number is what is still **waiting**: once
an endpoint has read, it has a cursor, and a message handed to it and not yet acknowledged is still
stored while no longer owed. Where there is no cursor yet, the queue's own count is the figure —
under work-queue retention a stored message is an untaken one.

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
`<instance>.<agent>`, or an `idle_timeout` this deployment cannot parse, exits **1**. An event counts
as current for `idle_timeout` after it arrived.

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

**This build has no `doctor` verb.** It belonged to the shell tool, which was deleted on 2026-09-07.
This heading is kept so that an older link lands somewhere true.

Nothing replaced it. The four checks it ran are gone, and no verb runs them now. `loc status` prints
whether the daemon runs and when it last beat, which is a smaller fact.

`--init` went with it. The daemon creates the `TOPICS` stream itself at boot, and `subscribe` creates
a seat's queue. This version authenticates nobody, so there is no admin identity to run anything
as.

## version

```
loc version
```

Prints the `git describe` stamp the build linked in. On a tagged commit it is the bare number. Off
a tag it carries `-N-g<sha>`, which names the commit. An unclean tree adds `-dirty`. A plain
`go build` stamps nothing and prints `dev`.

---

## mcp

```
loc mcp
```

Serves this seat to whatever agent runtime launched it, over stdin and stdout, speaking the Model
Context Protocol. It does not return: it holds the seat for the life of the runtime's session.

**Two environment variables are required, with no default:** `LOC_LISTENER_TYPE` — how this seat is
reached — and `LOC_LISTENER_ADDRESS` — where to reach it. `loc mcp` never guesses at either.

The type is a closed set of three, because it chooses the notifier that rings the seat. `subscribe`
refuses any other value and names these three.

| type | what it does | what the address is |
|---|---|---|
| `tmux` | types the bell into the seat's pane and submits it | a tmux pane target, such as `%42` |
| `claude` | runs a one-shot `claude -p` courier that delivers the bell with `SendMessage` | a tmux pane id the courier matches a session by |
| `none` | rings nothing; the seat finds its mail on the next `read` | unused, and still required |

The `tmux` notifier refuses to type when the pane is in copy mode, and refuses when the pane is not
an agent's input box: typing into a pane that has dropped to a shell executes the text. A refusal is
written to the delivery log and the message waits in the queue.

The `claude` notifier spawns at most one courier per seat per 30 seconds. A ring inside that window
is dropped and logged `rate-limited`. Nothing is lost, because the queue holds the message. If one is empty or
unset, the server writes one line to stderr and one to the seat's delivery log, then exits 1 before
touching the handshake or the registry — there is nothing to serve a seat as reachable through
nowhere.

**It is the host, in miniature.** `docs/PRESENCE.md` says whoever launches an agent holds its process
id, which is what makes registration mechanical rather than remembered. A stdio server is launched by
the runtime and dies with it, so it has that shape at seat scale: before it serves anything it
registers this endpoint with its PARENT's pid — the runtime's — `LOC_LISTENER_TYPE` as the agent
type, `LOC_LISTENER_ADDRESS` as the delivery address, the client's version from the initialize
handshake, the agent token as the display name and the working directory; when the runtime lets go
(stdin reaches EOF, or a signal arrives) it unsubscribes and exits 0. A seat held by
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
and removed at departure. **That file is two lines — the pid, then that process's start time
in the presence model's stamp** — because the pid alone starts naming a stranger the moment the
kernel reuses the number, and a reader asking "is this seat's server running" would then get a
confident yes about somebody else. Both lines are compared, by the same liveness the presence model
uses on the pids it records. **A server ended by SIGKILL leaves the file behind**, since removing it
is part of departing and a kill runs nothing. A reader asking whether this seat's server is running
compares both lines against the presence model's liveness and finds the file stale. No send depends
on the answer: a send only queues.

**It is also the listener, and the only one.** It holds a core subscription on this endpoint's own queue subject, which
sees every arrival and consumes nothing, and asks how much is waiting at start and after every
reconnect. Arrivals are coalesced across `wake_window_seconds` and capped by
`wake_breaker_per_minute` and `wake_breaker_per_hour`. Each wake hands the seat's notifier ONE LINE
and no body:

```
🔔 3 new → read
```

and appends `wake <endpoint> count=3` to `run/<endpoint>.delivery.log`. A tripped breaker says so
once. Nothing is lost to a suppressed wake: the queue keeps the truth.

**A bell that could not ring says so.** If the notifier refuses or fails,
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
workshop.clerk -> workshop.scribe   09:00
+ a question about the ledger

── topics ──
```

Every `read` appends `read <endpoint> handed=<n>` to the delivery log, so a message that was read and
never shown can still be found. **The acknowledgement is made while the result is being rendered,
and the result is returned afterwards** — so a client that drops between the two loses those
messages: they are gone from the queue and they never reached the agent, and the `handed=<n>` line
is all that is left of them. Acknowledging only after delivery is tracked separately, as issue #42.

**Configuring a runtime.** The server needs three things: to be launched, to be told which endpoint it
is, and to be told how and where this seat is reached. For Claude Code, `.mcp.json`:

```json
{"mcpServers": {"loc": {"command": "loc", "args": ["mcp"], "env": {
  "LOC_IDENTITY": "<instance>.<agent>",
  "LOC_LISTENER_TYPE": "tmux",
  "LOC_LISTENER_ADDRESS": "<addr>"
}}}}
```

For a runtime configured in TOML, `.codex/config.toml`:

```toml
[mcp_servers.loc]
command = "loc"
args = ["mcp"]
env = { LOC_IDENTITY = "<instance>.<agent>", LOC_LISTENER_TYPE = "tmux", LOC_LISTENER_ADDRESS = "<addr>" }
```

`LOC_LISTENER_TYPE` and `LOC_LISTENER_ADDRESS` are required; the server refuses to start without
them (above). Neither is validated or interpreted — see `subscribe`'s `--type` and `--address`.

**The fallback.** A runtime that cannot launch a stdio server uses the command-line verbs directly —
`loc read`, `loc send`, `loc status`, `loc topics`. What is lost is the automatic registration and
the bell; something else must then run `subscribe` and `unsubscribe` around the session.

---

## Configuration

`$LOC_HOME/config`, one `key = value` per line.

| key | default | effect |
|---|---|---|
| `provider` | `nats` | which transport loc talks to; only nats exists |
| `nats_url` | `nats://127.0.0.1:4222` | where the medium is |
| `idle_timeout` | `10m` | how long after an event `status <endpoint>` still reads *active* |
| `send_requires_attendance` | `no` | refuse sends to endpoints that are not attending. With the NATS provider the key is satisfied by the live queue: a subscribed peer attends, and an unsubscribed one has no queue, so its send is refused for absence. |
| `monitor_url` | none | the broker's HTTP monitoring endpoint; no verb reads it, and `loc start` opens no such port |
| `topic_window` | `7d` | how long topic messages live; `loc start` gives the `TOPICS` stream this age limit |
| `heartbeat_log_retention_days` | `7` | heartbeat log files older than this are deleted when loc starts |
| `wake_window_seconds` | `5` | wakes are coalesced across this window (`loc mcp`) |
| `wake_breaker_per_minute` | `6` | cap on wakes per minute (`loc mcp`) |
| `wake_breaker_per_hour` | `60` | cap on wakes per hour (`loc mcp`) |

**`idle_window` is the old name of `idle_timeout`.** `loc` never rewrites a config file that exists,
so a deployment that set the old key keeps a line that does nothing. The new key takes its own value,
which is `10m` unless the file sets it. Each run prints one line on stderr that names both keys and
the value in force.

**The defaults live in one table**, `internal/config/keys.go`. `loc start` writes the file from it on
first run, with each key's comment above it, so the file and the code cannot drift apart. The three
wake keys are the deployment's and are not in that table.

Every key above except `monitor_url` is read by this build. The three wake keys are read by the
seat's own `mcp` server, which is the only listener there is. `registry`'s wait for a host is fixed
in the code, not a key.

When a breaker trips it says so in `run/<endpoint>.delivery.log` and **suppresses only the wake**.
No message is lost; the next read still finds everything.
