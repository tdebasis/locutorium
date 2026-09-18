# Decisions

*Why the house is shaped the way it is, kept beside the shape itself.*

The other docs describe what the Locutorium does (`PROTOCOL.md`, `CONTRACT.md`), the words it uses
(`GLOSSARY.md`), and how presence works (`PRESENCE.md`). This one keeps the record of *why*, for the
calls that weren't obvious the first time they came up — one entry per call, in Context / Decision /
Consequences form. New entries append; existing ones are not renumbered once another entry cites them.

Some calls that produced this record were about a deployment's own courier, not about the bus itself
— those are marked, and their reasoning generalizes even where the specific mechanism doesn't.

**An entry records a decision, which is not the same as a description.** Some describe behaviour that is
settled and not yet built, and they are written in the present tense because that is how a decision
reads. **The code is the authority on what exists today; this file is the authority on what was decided
and why.** Where the two disagree, one of them is a bug, and it is worth finding out which.

---

## 1. Why there are two ways to deliver a message, and neither is the default

**Context:** A wake may spawn a courier to carry a body onto an endpoint's surface (*Glossary:
courier*). There are two ways to do that, and each needs something the other does not.

| shape | how it delivers | what it needs |
|---|---|---|
| **mechanical** | writes onto the surface directly, such as a terminal pane | a runtime hosted somewhere you can type into |
| **agent-native** | pushes through the runtime's own instance-to-instance messaging | a runtime that offers such a feature |

A headless runtime gives mechanical nothing to write into. A runtime without instance-to-instance
messaging gives agent-native nothing to use. Neither shape works everywhere.

Today the imbalance is severe. Every runtime hosted in a terminal supports mechanical delivery. Only
Claude Code supports agent-native, because it is the only one that lets one instance push into
another. Gemini CLI, Codex CLI, OpenCode, Aider and Cline have no such feature, and Codex declined the
request for one. So agent-native is a category with a single member, and entry 4 is about that member.

Mechanical delivery types into a terminal, which is the collision this bus was built to avoid
(*README: "Keystrokes typed into another agent's window collide with whatever it was typing and submit
the mixture"*). Registering an endpoint does not solve that, because registered does not mean idle.
Two defects in the idle check are open today. One cannot tell a complete delivery from one that lost
its head. The other reports success while the recipient is busy. Agent-native delivery never touches
the terminal, so neither defect can affect it.

**Decision:** Support both. Each endpoint states its shape when it registers. There is no house-wide
default, because neither shape is available everywhere.

Where both work, prefer agent-native. It is slower than typing into a pane, and it is still preferred,
because it avoids the collision risk above.

**Consequences:** An endpoint's shape follows from where its runtime runs, not from one decision made
for the whole house. A house running mixed runtimes will have some endpoints with one option and some
with both. A headless runtime with no native messaging has no option at all, and would need a third
shape that does not exist yet.

If the two idle-check defects are fixed, the reason to prefer agent-native goes away.

*(`claude-courier` is one deployment's name for its agent-native courier, built on Claude Code's
session addressing. It is not a third shape.)*

---

## 2. Who fetches the message: the hook, not the courier

**Context:** A wake ends with a message body appearing on an endpoint's surface, by one of the two
shapes in entry 1. Which component should hold the bus credential? This has already gone wrong once:
an agent-native courier that could read live content reasoned about it and acted on its own judgment,
when its only job was to hand it over.

**Decision:** The wake hook fetches the message, with an ordinary read, under the endpoint's own
identity. Neither courier holds a bus credential or speaks on the bus. Each is handed the body it is
to place, and places it.

**Consequences:**

- **Both shapes have the same relationship to the bus.** The registered type only decides how a body
  reaches a surface. It does not decide how far the courier is trusted.
- The failure above becomes impossible rather than forbidden. A courier with no credential has nothing
  to read, so it cannot act on what it read.
- **The fetch finishes before the relay starts, so a failed relay loses that message.** A read hands a
  message over once and acknowledges it as it does. The bus cannot return it. This is answered by the
  message log in entry 10, which is what makes the loss recoverable instead of silent.
- **Fetch and relay could be made one transaction, but only inside a single process.** An
  acknowledgement belongs to the connection that fetched the message, so a hook cannot look, exit, and have a later process acknowledge what it saw. The envelope's `id` cannot address a stored message either, because the stream API works by sequence number with no cheap way between the two. What remains possible is one process that fetches, relays and acknowledges only on success, which would put a failed relay back on the queue. That is not built. It needs a new mode on the read verb, and it holds a message in flight for as long as the relay takes, which for an agent-native courier is
  several seconds and unbounded if it hangs.

---

## 3. Which process must be registered as proof an agent is alive

**Context:** `PRESENCE.md` already answers this in general. Register the process whose death ends the
session, never a shell or wrapper sitting in front of it (*PRESENCE.md: How an agent joins*).

Courier delivery makes the rule matter more. If the agent crashes but the shell around it survives, and
the shell was the registered process, the endpoint still reads as alive. The courier then delivers into
something that is not listening.

**Decision:** Courier delivery gets no exception. The existing presence rule applies unchanged.

**Consequences:** Registering a steadier process than the agent itself is the easier thing to do and
the wrong one. `PRESENCE.md` explains why.

---

## 4. Why a Claude Code session must be launched with a name you set

*(This entry is about Claude Code specifically. It is currently the only runtime with
instance-to-instance messaging, so it is also the only agent-native courier that exists.)*

**Context:** Claude Code gives every session a display name and addresses sessions by it. Left alone,
it generates that name itself, and the generated name can change while the session runs. One drifted
mid-session with nothing restarted.

Searching for the right session by name is not safe either. A machine can be running dozens of
unrelated sessions, and their generated names resemble each other.

Claude Code accepts a name at launch, with `-n`. A name set that way does not drift.

**Decision:** An agent-native courier addresses a session only by a name the deployment set at launch.
It never guesses a name and never searches for one. The Locutorium states this requirement but cannot
enforce it, because the name belongs to the runtime.

**Consequences:** A deployment that launches sessions without setting names will see delivery fail in
ways that look like bus faults and are not.

The part that outlives Claude Code: address an instance by an identifier you assigned, never by one the
runtime generated for display. Display names are for humans, and humans do not mind if they change.

---

## 5. Why every endpoint must state how it is reached, with no default

**Context:** An earlier draft let an endpoint's delivery shape fall back to a computed default when the
deployment did not say. That was rejected. A default also applies when the configuration is simply
wrong, so a misconfigured endpoint keeps running the wrong way instead of stopping.

**Decision:** Every endpoint states how it is reached. Registration checks for it and refuses to start
without it. This applies to all endpoints, not only the ones where a guess would be wrong.

**Consequences:** No endpoint can be running on a guess. This matches how the rest of the house handles
missing information (*PRESENCE.md: Properties this produces*).

---

## 6. What Locutorium owns, and what the deployment owns

**Context:** Attendance can be registered by the runtime itself, by whatever launched it, or by
something that notices later. These are different answers for different deployments, and none of them
changes the bus.

**Decision:** The Locutorium ships the primitives: register, watch, send, read. It also ships at most
one labelled reference implementation per delivery shape. It does not decide who calls the primitives,
or when. That belongs to whatever runs on top.

**Consequences:** Each choice carries its own risk. A runtime that registers itself may register before
it can actually be reached. A launcher registering on the runtime's behalf has the same race, and can
wait for a liveness signal first. Both are valid. Picking one, and handling its race, is the
deployment's job.

---

## 7. Why a courier resumes one session instead of restarting or running forever

*(About Claude Code, for the reason given in entry 4.)*

**Context:** A courier could work three ways: start a fresh session for every message, keep one session
running all the time, or reuse one session by a fixed id without keeping it running in between.

The always-running option was dropped without measuring it. It needs a supervisor and a liveness check
of its own, on top of the liveness this design already tracks for endpoints, and it holds a process open
continuously for work that happens rarely.

The other two were measured. Repeated timed round trips showed that resuming a session by a fixed id is
consistently faster than starting a fresh one, with no overlap between the two sets of results.

**Decision:** Resume a session by a fixed id (`claude --resume <id>`). Do not start cold each time. Do
not run continuously.

**Consequences:** This gets most of the speed of an always-running courier with nothing new to
supervise, because the courier still exits after each call.

Open: whether a resumed session slows down as its own history grows over many calls. A short measurement
cannot show this either way. Entry 9 bounds that growth rather than assuming it is harmless.

---

## 8. Why a courier's model and settings are pinned on every call

*(About Claude Code, for the reason given in entry 4.)*

**Context:** A cheaper model is easy to justify twice over, by also assuming it is the faster one. That
was checked against a more expensive model on the same task. At the sample size tested the result was
inconclusive: the cheaper model was not shown to be faster.

**Decision:** Pick the courier's model on cost, say that cost is the reason, and pass it explicitly on
every call (`--model`). Do not let it inherit whatever the account or environment currently defaults to.

**Consequences:** An inherited default means a later change to the account changes what every courier
call costs, and nothing here would notice. An explicit setting cannot drift that way.

---

## 9. How a courier's context is bounded without limiting how it signs in

*(About Claude Code, for the reason given in entry 4.)*

**Context:** One way to stop a resumed session's context from growing forever was `--bare`. It also
changes how the session signs in: under `--bare` only an API key is read, and the subscription login
this deployment runs on is ignored. It was therefore unusable, whatever it did for context.

**Decision:** Bound the context with a mechanism meant for that alone, such as a compaction floor
(`--autocompact`). Reject any approach that also narrows authentication, however well it bounds
context.

**Consequences:** Which mechanism exists, and what it's called, is deployment-specific by nature. What
generalizes is the ordering: satisfy authentication first, bound context second, and never let the
second override the first.

---

## 10. The message log, and what it can and cannot recover

**Context:** Entry 2 leaves a hole. The fetch acknowledges the message, the relay may then fail, and the
message is gone from the bus. Nothing on the bus can bring it back.

**Decision:** Every send is written to a log the moment it is sent, before delivery is attempted. Each
relay appends a second line saying whether it arrived.

One file per day, append-only, JSON lines. Every line carries the same six fields:

```json
{"uid":"01J...","status":"sent","ts":"2026-09-08T17:02:11Z","from":"workshop.scribe","to":"workshop.clerk","body":"..."}
{"uid":"01J...","status":"delivered","ts":"2026-09-08T17:02:19Z","from":"workshop.scribe","to":"workshop.clerk","body":"..."}
```

`status` is `sent`, `delivered` or `failed`. The lines for one message share its `uid`.

Every line repeats `from`, `to` and `body` so that any single line means something on its own: an agent
searching the day's file for its own name gets whole records back, not fragments it has to join. The
body is stored twice per message, which is a cost worth paying at this volume.

One file, shared by everyone, rather than one per sender or one per recipient. An endpoint finds its own
traffic with a single search of the day's file. The cost is that many processes append to the same file,
which makes one rule load-bearing: **write each line with a single write call.** A regular file opened
for appending serialises one write against other writers, so a line built in memory and written once
cannot be interleaved, whatever its size. A line written in two calls can be.

That is the whole design for now, and it is meant to be replaced when it stops being enough.

**Consequences:**

- A message dropped by a failed relay is still in the log, with its body, and can be read.
- **The log restores content, not delivery.** The message is off the bus for good. Re-sending what the
  log holds creates a new message with a new `uid`.
- Both lines are needed. A `sent` line with no matching `delivered` is how a lost message is found; the
  send line alone says what was sent and never what went missing.
- **The log holds message bodies in plain text on disk.** It lives under `$LOC_HOME`, with the rest of
  a deployment's run-time state, and never inside a source tree. File permissions are the only thing
  guarding it. The writer creates the directory `0700` and the file `0600`, which is what the delivery
  log beside it already does.

  Two limits, because a permission is easy to state and easy to over-read. A mode is applied **only when
  a file is created**: a log file that already exists with looser permissions keeps them, and nothing
  here re-asserts it. And permissions on one file are not a policy — anyone who puts sensitive traffic
  on the bus should decide what `$LOC_HOME` itself needs.
- **Keeping it out of a source tree is environmental, so a repository should enforce it too.** This one
  ignores `run/` outright rather than by file pattern. A pattern that matched only `*.log` would cover
  the delivery log and miss the message log, which is the file that actually holds bodies.

---

## 11. Why the bell is in the binary, and what that reverses

**Context:** The bell was a shell hook. `loc mcp` ran `hooks/nudge` from the deployment when mail
arrived, and `loc` ran `hooks/identity` when `LOC_IDENTITY` was unset. Entry 2 above put the fetch in
the wake hook, and the comment at `internal/mcpserve/serve.go:22` said the pane-specific last inch
stays in the deployment's `hooks/nudge`.

A shell hook does not run on Windows. It is the one component at the notification boundary that
cannot cross. CI runs none of it, and three defects in these hooks were found by hand in one week.

**Decision:** The binary carries the bell (R28, 2026-09-09). A seat picks its bell at `subscribe`
through the listener type, and there are three types: `tmux` types the line into the seat's pane,
`claude` runs a one-shot courier that delivers it through the runtime's own session messaging, and
`none` rings nothing. `hooks/nudge` goes. The value `claude-courier` is renamed `claude`.

The identity hook goes with it (R29). `LOC_IDENTITY` is required, and a caller without it is refused
with a message that names the variable.

**This reverses two things, and they are named here so the reversal is findable.** The first is
`serve.go:22`, approved on 2026-09-07 and rewritten now. The second is entry 2 above: the wake hook
that fetches the message no longer exists in the product, because there is no wake hook. What entry 2
was protecting is unchanged and now stronger — the courier still holds no bus credential, still
receives a fixed token rather than a body, and still cannot read what it announces.

**Consequences:**

- **The product carries first-party knowledge of tmux and of Claude Code.** Entry 1 already recorded
  that agent-native notification has exactly one implementation, so the code now says what the
  decision record said.
- **The product carries no seat names.** The hook selected its behaviour from a hard-coded list of
  this deployment's seats. The registered type replaces that list, and the bus never learns what the
  seats are called.
- **The `tmux` notifier refuses rather than types when the pane is not an agent's input box**, and
  refuses when the pane is in copy mode. Typing into a pane that has dropped to a shell executes the
  text, so this guard is the one part of the notifier that is not optional.
- **A `claude` courier spawns at most once per seat per 30 seconds** (R35c). A courier is a whole
  Claude session. A ring inside the window is dropped and logged; the message stays in the queue.
- **There is still no fallback from one notifier to another**, by the ruling of 2026-09-07. A failed
  bell is logged and the message waits.

---

## 12. What per-seat read isolation guaranteed, and why V0 drops it

**Decision:** V0 has no authentication on the loopback listener (R12, 2026-09-09). The two test
files that pinned per-seat read isolation are removed with it: `cmd/loc/read_acl_test.go` and
`internal/provider/nats/read_acl_test.go`.

**What those files asserted.** A seat held the per-seat access-control block the deployment shipped,
and nothing wider. Under that block a JetStream request the seat may not make is answered with
silence rather than a refusal, so a queue holding mail reads as empty. The cases held four
properties. A `read` whose cursor on the seat's own queue is refused exits non-zero, prints the
reason on standard error, and prints nothing on standard out. A `--peek` of the same refused cursor
answers the same way. The very same invocation, under a block that grants the cursor, reads the
message and exits 0. A watch credential that may not publish is refused by the broker.

**The property was not found wrong.** No case failed and no case was weakened. The block V0 removes
is the thing the cases needed in order to ask their question, so the question can no longer be put.

**Where authentication starts again.** Work from this entry and from the two files as they stand in
git history at `main` `9322605`. They carry the shipped per-seat template, the granted template, and
the harness that stands a deployment up under either one.

---

## 13. A test may never reach a live broker

**Decision (2026-09-16):** The nats provider refuses the table-default `nats_url` when
`testing.Testing()` is true. The guard runs before the dial, so a test binary that asks for
`nats://127.0.0.1:4222` opens no socket. The guard reads the host and the port, so it refuses
`localhost` and `::1` at that port as well.

**Context.** On 2026-09-16 a `go test ./...` in this repository deleted every queue on the
machine's live deployment. Two cases in `cmd/loc` made a scratch `LOC_HOME` and wrote no config
file. With no file, a verb takes the key table's default `nats_url`. On a machine that runs the
product, that address is the live broker. One case published a message onto it. The other ran a
sweep, which deletes a queue that no row in its registry claims. The scratch registry was empty, so
the sweep deleted all six live queues. Any message unread at that moment was lost. The next beat
recreated the queues. The outage lasted about three minutes.

**The guard is in the client, not in the harness.** Every connection the product opens goes through
one function, `nats.Dial`: the provider's `connectWithin`, the listener in `WatchQueue`, and the
daemon's `ensureTopics`. A refusal there holds in every package and needs no discipline from a test
author. The first cut put the guard inside `connectWithin` alone, and `WatchQueue` dialled past it. The harness was the other candidate and it failed twice: the same class happened on
2026-09-12, the answer then was a rule for test authors, and the rule did not survive to
2026-09-16. `cmd/loc` `TestMain` already refused to BIND the default port. Neither that guard nor
the scratch-home guard covered a CLIENT that dials.

**Consequences:**

- **A test that wants a broker boots its own** on a port the kernel picks, and pins `nats_url` to
  it. `internal/loctest.Boot` does this.
- **A test that wants no broker pins a closed port.** `internal/loctest.ClosedPort` reserves one and
  gives it straight back.
- **The shipped binary is unchanged.** `testing.Testing()` is false outside `go test`. The import of
  `testing` adds no behaviour to the product.
- **CI could never have caught this.** A CI runner has no live deployment, so the two cases passed
  there. The defect was visible only on a machine that runs the product.
- **The guard is not a sandbox.** It stops this one address. It does not stop a test that pins the
  live broker on purpose. A broker that refuses an unauthenticated client is the real fix, and it is
  a product change (issue #103, "Follow-up").

---

## 14. Why the record stays JSON lines, and what the events are keyed by

**Context:** Entry 10 above laid one file per day, append-only, JSON lines. Every line said `sent`.
Nothing said what happened next.

The receiving side wrote plain text to a second file, `run/<endpoint>.delivery.log`. A line there
said `read <endpoint> handed=9`. It did not say which nine messages, and the two files shared no
key. So a deployment could not answer one question: was a message read, and when. A sender saw
`sent` and nothing more.

A database was designed for this. It would have held messages, reads and bells in tables, and it
would have answered the question with a query. It was not built.

**Decision:** The record stays JSON lines. The same file takes more kinds of line.

The reason is what the record is for. An operator reads it with `grep` on a machine they are already
logged in to, at the moment something is wrong. A SQLite file needs a client, and it needs the
schema. A text line is read by every tool on the machine and by the person looking over the
operator's shoulder. The volume is a house of agents talking, which is thousands of lines a day and
not millions.

There are two families of line, and a reader tells them apart by which key is present.

**Message events carry `uid`.** `sent` is what entry 10 laid. `failed` is a send that was refused,
with one of four reasons: `no identity`, `too long`, `target not registered`, `bus unreachable`.
`read` is a message taken off a queue, with the reader's name. The three share the message's id, so
the lines for one message join on it.

**Seat events carry `seat` and no `uid`.** `bell-failed` is a notifier that could not ring.
`queue-deleted` is a queue that some process destroyed, with one of four reasons: `left`,
`displaced`, `expired`, `orphan`.

**A bell failure names no message. This is a limit of the code.** The seat's server
is told that mail arrived and is never told which message arrived: the watch hands it an arrival,
and the count comes from a separate question to the store. A per-message bell event is not a claim
this code is in a position to make.

**Consequences:**

- A message sent at one seat and read at another has two lines with the same `uid`. The second line
  says when it was read and by whom.
- `loc send` prints the `uid` on its success line. A sender that was never told the `uid` would have
  to match on a body to find the `read` line.
- A `read` line is written after the acknowledgement and never before it. A line saying a message
  was read, written for a message still in the queue, is a claim the next read disproves.
- **A topic message gets no `read` line.** Everyone attending reads a topic from their own position,
  so one reader taking it is not a fact about the message. The complete answer would be one line per
  attender, and it would still be incomplete while one of them has not read.
- The record's day file is the UTC day of the event, and the event's own stamp names the same day. A
  line stamped just after midnight sits in the new day's file.
- **The bell half of issue #85 was retired by the project owner on 2026-09-16.** That work proposed a
  per-message bell and a database under it. The events keyed by `uid` are the surviving half, and
  they are here rather than in a table.
- A line is still written in one call, as entry 10 requires. Every new kind of line is built in
  memory first.

---

## 15. A sweep touches only what its own deployment holds

**Decision (2026-09-18):** Three changes, each stopping a different step of one failure.

1. **The orphan pass is bounded by the instances the ledger holds.** A queue in any other instance is
   not a finding at all: it is never reported and never deleted. A ledger with no rows therefore
   reaps nothing. This is the change made under issue #134, and it stops the deletion itself.
2. **A process with no config file refuses instead of taking the default address.** The key table's
   default `nats_url` is the live broker on a machine that runs the product, so a process with a
   scratch home silently pointed at somebody else's mail. Refusing stops the wrong broker being
   reached. This is decided and lands in the change that follows this one.
3. **The daemon sweeps only the broker embedded in its own process.** A daemon that dials an address
   it did not start is reconciling records it did not write. This stops a leftover daemon acting on a
   broker that outlived it. This is decided and lands in the change that follows this one.

**Context.** A daemon whose home directory had been removed kept running. Its ledger was therefore
empty. It reached a broker serving a different deployment's instance, and its orphan pass deleted
every queue there on each five-minute beat. Mail waiting in those queues was destroyed.

The root is the same as entry 13 above: a process with an empty registry, holding the default
address, deleting queues it has no record of. Entry 13 stopped that root inside `go test`. It did
nothing for the shipped binary, because the guard it added reads `testing.Testing()`.

**The design documents said this was safe.** PRESENCE.md said two deployments may share a broker and
that instance namespacing makes a collision impossible by construction. That was true for delivery
and false for cleanup, because the sweep had no notion of which instances were its own. Change 1
makes the document true.

**Rejected: a daemon that exits when its home or its pidfile vanishes.** It was the obvious answer to
a daemon that outlived its own home. With changes 1 and 3 in place a leftover daemon is harmless: it
holds no rows, so it reaps nothing, and it sweeps only a broker it started. Self-exit would add a new
way for a live broker to stop, keyed on a filesystem check that a backup, a sync client or a rename
can trip. A new way to lose a broker is a poor trade for a case the other changes already cover.

**Consequences:**

- **The last seat of an instance strands its queue.** When it leaves uncleanly, no row is left to
  hold the instance, so no sweep reaps the queue. `loc unsubscribe <endpoint>` removes it by hand. A
  memory-backed queue ends with the broker.
- **Two deployments that use the SAME instance name on one broker are not protected.** To the broker
  they are one namespace. Each ledger holds the instance and neither holds the other's rows, so each
  sweep reads the other's queues as orphans and deletes them. Nothing here separates them, and
  nothing else does either.
- **A row the ledger cannot read still holds its instance.** The queue it may claim is still reported
  as an orphan, which is what makes the caller's blanket skip of the orphan pass fire. A version that
  quietly dropped such a row would hide the danger instead of removing it.
- **`loc status` inherits the boundary with no change of its own.** The report and the sweep read one
  function, so they cannot disagree about which queues are this deployment's business.
