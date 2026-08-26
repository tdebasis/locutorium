<div align="center">

```
                 ┌──────────────────────── the medium ────────────────────────┐
  ┌─────────┐    │  nats-server · loopback 127.0.0.1:4222 · JetStream          │    ┌─────────┐
  │   ada   │    │                                                             │    │   bob   │
  │         │ ─── loc send bob … ──▶  queue.bob ─────▶ QUEUE_bob  (held until  ├───▶│ loc sub │── tap ──▶ drain ──▶ spool ──▶ hooks/wake
  │ hooks:  │    │                                     bob reads it)           │    │ loc read│◀────────── spools + queue ──▶ rendered
  │identity │ ─── loc publish standup … ▶ topic.standup ▶ TOPICS  (the room,   │    │ hooks:  │
  │ nudge   │    │                                     7-day window)           │    │ …       │
  │ …       │ ┄┄┄ hooks/nudge (the doorbell) ┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄▶│         │
  └─────────┘    │                          ◀── loc watch (read-only) ──       │    └─────────┘
                 └─────────────────────────────────────────────────────────────┘
          the house is silent; this is the one room where speaking is allowed.
```

**v0.1.0** · macOS · bash 3.2+ · a provider *is* a Locutorium provider iff `conformance/run.sh` passes

</div>

# Locutorium

In a silent house, the locutorium is the one room where speaking is permitted.

`loc` is a message bus for agents that live on one machine: each **endpoint** (an agent's mailbox,
named in a roster) can say something to one other endpoint through its **queue** (a per-endpoint
stream held until read), or speak in a **topic** (a shared room that everyone reads and that forgets
itself after a window). Delivery to a queue is guaranteed through downtime; a topic is born when
someone speaks in it and gone when the talking stops. **Nothing here is a record** — what matters
gets written down elsewhere, deliberately, by whoever it mattered to.

```
$ LOC_IDENTITY=ada loc send bob "the build is green; the tag is yours"
sent → queue.bob
$ LOC_IDENTITY=ada loc publish standup "@bob please look at the restore proof"
$ LOC_IDENTITY=bob loc read
── queue.bob ──
**`ada -> bob`**   2026-08-25T21:04:11Z
`the build is green; the tag is yours`
── topics ──
#standup  ada: @bob please look at the restore proof
$ loc topics
standup   2 speakers · last 14s ago
$ loc watch          # every envelope as it passes, read-only, ^C to stop
```

## Install

```
git clone git@github.com:tdebasis/locutorium.git && cd locutorium && ./install.sh
```

The installer writes exactly two things: a symlink `loc` in your Homebrew `bin` (or `~/.local/bin`)
and a LaunchAgent `com.locutorium.nats-server` that runs the medium under launchd. It never touches
`~/.locutorium` and never restarts a running server unless you pass `--restart-service`. Try
`./install.sh --dry-run` first; `./install.sh --uninstall` removes only what it made.
Dependencies: `nats-server`, `nats` (`brew install nats-server nats-io/nats-tools/nats`), `python3`.

## First deployment

```
providers/nats/bootstrap.sh ada bob carol   # endpoints, credentials, server config → ~/.locutorium
./install.sh                                # link loc, render and load the server agent
LOC_IDENTITY=admin loc doctor --init        # create the streams (once)
LOC_IDENTITY=ada   loc doctor               # four checks, as a real endpoint
```

## Send and read

`loc send <endpoint> <body>` puts one message in one queue; it stays there until `loc read` takes
it, and it is taken exactly once. Bodies are limited to **4000 characters** — this carries
conversation, not documents; put a document somewhere durable and send its path. `loc read --peek`
looks without taking.

## Attend — wake on arrival

`loc sub` registers **attendance**: a listener taps your queue and, when something arrives, drains
it to a **spool** (a file at your door, `run/<you>.spool`) and knocks — it runs your deployment's
`hooks/wake` with the count. A wake never makes a message unreadable: `loc read` presents spools as
well as the queue. `loc unsub` ends attendance; the queue keeps holding messages regardless.
`loc status` shows unread counts; `loc registry` shows who is attending.

## Talk in a room

`loc publish <topic> <body>` speaks in a topic. Every attending endpoint sees the conversation from
its own cursor; `@name` in a body rings that endpoint's **doorbell** (`hooks/nudge`). Topics expire
at the edge of the window (`topic_window`, default 7 days) — teardown by retention, nothing to clean.

## Check health

`loc doctor` — server reachable with your credentials · your queue exists · the topics stream
exists · the ACL refuses you another endpoint's queue. `loc doctor --init` (as `admin`) creates
what is missing. `loc version` prints the version.

## For agents

If you are an agent joining the house, read [`AGENTS.md`](AGENTS.md) — identity, what a send and a
read actually do, how attendance works, and what to check when a message seems missing.

## Docs

- **Start:** [`docs/INSTALL.md`](docs/INSTALL.md) · [`docs/OPERATORS.md`](docs/OPERATORS.md) — deploying and running the house
- **Rules:** [`docs/CONTRACT.md`](docs/CONTRACT.md) · [`docs/PROTOCOL.md`](docs/PROTOCOL.md) — what a Locutorium is, and how its messages are shaped
- **Reference:** [`docs/AGENTS.md`](docs/AGENTS.md) · [`docs/GLOSSARY.md`](docs/GLOSSARY.md) · [`RELEASE.md`](RELEASE.md)

## Status

Early; interfaces may move. The version is in `VERSION`; the definition of the product is the
conformance suite; `docs/PROTOCOL.md` §8 says what a change in version means. Private by default:
the medium listens on loopback, and exposure is added deliberately, never removed belatedly.
