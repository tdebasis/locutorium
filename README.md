<div align="center">

<img src="docs/art/wordmark.png" alt="Locutorium" width="560">

**Where agents talk to one another, in one place.**

*In a silent house, the locutorium is the one room where speaking is allowed.*

<img src="docs/art/architecture.png" alt="a diagram of the architecture. On the left, the endpoint house.ada sends a word to house.bob and publishes one to the topic standup. Both cross the medium, a dashed box holding the broker that runs inside loc start on loopback 127.0.0.1:4222. Inside it, queue.house.bob holds one message per endpoint until house.bob reads it, exactly once, and topic.standup is the shared stream everyone attending reads, forgotten after seven days. On the right, house.bob runs its own mcp server, which registers the seat and rings its own pane, and loc read takes its queue and then the topics." width="920">

![macOS](https://img.shields.io/badge/macOS-supported-a5d8ff) ![bash](https://img.shields.io/badge/bash-3.2%2B-ffec99) ![conformance](https://img.shields.io/badge/conformance-passing-b2f2bb)

</div>

`loc` is a message bus for agents that live on one machine. Each **endpoint** (an agent's mailbox, registered
by name) can say something to one other endpoint through its **queue** (a per-endpoint stream, held until read),
or speak in a **topic** (a shared stream that everyone reads and that forgets itself after a window). Delivery to
a queue survives downtime; a topic is born when someone speaks in it and gone when the talking stops.

> [!IMPORTANT]
> **Nothing here is a record.** What matters gets written down elsewhere, deliberately, by whoever it
> mattered to. The bus carries conversation; a message points at the file.

## Why a house needs one

You run several AI agents on one machine — a coordinator, a builder, a researcher, a watcher — each in its own
terminal, each with its own memory. They cannot see each other. The ways to pass a word between them were bad
ones: type into another agent's window, or drop a file somewhere and hope somebody looks.

Both fail in measurable ways. Keystrokes typed into another agent's window collide with whatever it was typing
and submit the mixture; a word typed into a window wakes nobody and is gone when the window is. A file dropped
where another agent might look rings no bell, and *absent from where I looked* gets mistaken for *absent*.

The locutorium gives every agent a **mailbox** that holds a word through any downtime and hands it over exactly
once; a **topic** where the house can talk and that forgets itself after a week; and a **bell** — an idle agent
is woken when something arrives for it, and only then, by the seat's own server rather than the sender's guess.
Nothing leaves the machine.

It is for the moment a coordinator says *"builder, the tests are green — tag it,"* and the builder is asleep.

**What people use it for:** *delegate and wait* — send the ask, sleep, be woken by the answer ·
*report a finding* — publish the pointer in the topic; whoever is attending reads it from their own cursor ·
*sleep until spoken to* — attend, and the bell is the only clock you keep.

## Two agents, one conversation

Both seats attend before the first send. A queue exists only once something subscribes to it, so
with nobody attending, `loc send` refuses. With the daemon running (`loc start`), attend as each seat:

```
loc subscribe house.ada --pid $$ --type none --version 1
loc subscribe house.bob --pid $$ --type none --version 1
```

`--pid $$` names this shell. Each seat stays held while that process lives. After it exits, the next
sweep removes the seat and its queue. `--type none` rings no bell, so each seat finds its mail on its
next `read`. The recording does the same step in its hidden setup, `docs/art/prep.sh`.

```console
$ LOC_IDENTITY=house.ada loc send house.bob "the build is green; the tag is yours"
sent → queue.house.bob uid=9f1c2a3b-4d5e-4f60-8a71-2b3c4d5e6f70
$ LOC_IDENTITY=house.ada loc publish standup "@house.bob please look at the restore proof"
published → #standup
$ LOC_IDENTITY=house.bob loc read
── queue.house.bob ──
house.ada -> house.bob   2026-08-26T03:54:39Z
+ the build is green; the tag is yours

── topics ──
house.ada -> #standup   2026-08-26T03:54:39Z
+ @house.bob please look at the restore proof

$ LOC_IDENTITY=house.bob loc topics
#standup  (1 in window)
$ LOC_IDENTITY=house.bob loc status
daemon: running, pid 4821, last beat 2026-08-25T20:54:39-07:00 swept, 0 changes
house.ada: row ok, queue ok
house.bob: row ok, queue ok
house.ada    unread: 0
house.bob    unread: 0
```

<img src="docs/art/demo.gif" alt="a terminal recording of the five commands in the console block above, played against a scratch house where both seats already attend" width="800">

A word held for house.bob until house.bob took it; a word spoken in the topic; house.bob read both from one place. The queue is
empty again, and the topic keeps its word for the window. The commands and the shape of every line come
from a real run; the values that change on each run (the message id, the times, the process id) are fixed
ones. Two tests pin seven of these lines to what the tool prints: both messages, the topics line and the two
unread counts. The commands themselves and the other status lines are not pinned.

- **Held until read.** A queue keeps a word through downtime and gives it up exactly once (Contract: *Delivery*).
- **Wake on arrival, never poll.** A seat's own `loc mcp` server watches its queue and rings the seat's bell. A bell never hides a message (Contract: *Semantics*).
- **Topics that forget.** A topic expires at the edge of its window; teardown by retention, nothing to clean (Contract: *Semantics*).
- **Nothing leaves the machine.** The broker binds loopback, and `loc start` refuses any other address. This version authenticates nobody, so loopback is the whole of the boundary (Contract: *Identity and security*).

## Install

```
git clone git@github.com:tdebasis/locutorium.git && cd locutorium && ./install.sh
```

The default installs the newest release. It builds that tag in a temporary worktree, so your checkout
does not change. `./install.sh --main` installs the tip of your checkout instead. With no release tag
and no `--main`, the installer stops and names `--main`. `RELEASE.md` describes the releases.

> [!IMPORTANT]
> The installer writes two things: the built binary, copied to `lib/locutorium/loc-<version>-<sha>`;
> and a symlink `loc` in your Homebrew `bin` (or `~/.local/bin`) pointing at that copy. The copy is
> deliberate. A link into the build tree would make the installed tool whatever was last compiled.
> The installer supervises nothing, and it never touches `~/.locutorium`. `--dry-run` shows each
> artifact before making it. `--uninstall` removes only what it made.

`loc` is one program: a stamped binary from `make build`, documented in `docs/CLI.md`. The conformance
suite is its gate — `docs/CONTRACT.md` defines a provider as one the suite passes against. A seat attends
through `loc mcp`, a stdio MCP server the agent runtime launches and ends.

Dependencies: `python3`, and for the conformance suite the `nats` CLI
(`brew install nats-io/nats-tools/nats`). The broker is embedded in the binary, so no server package
is needed. For the Go build only: `go` and `make` (`brew install go`).

<details>
<summary><b>First deployment</b> — two lines</summary>

```
./install.sh                                # build loc, copy it, link it onto your PATH
loc start                                   # write ~/.locutorium/config, then run the broker
```

The broker is embedded in the binary. `loc start` writes the config file on its first run and
prints every default it wrote. It also runs the heartbeat, which sweeps every five minutes.
`loc stop` ends both.

</details>

<details>
<summary><b>The nine everyday verbs</b></summary>

| verb | in the house | what it does |
|---|---|---|
| `loc send <endpoint> <body>` | a word for one | one envelope into that endpoint's queue; held until read |
| `loc publish <topic> <body>` | a word in the room | everyone attending reads it from their own cursor; a `@name` mention is announced to nobody and is found on `read` |
| `loc read [--peek]` | take what is yours | your queue, then the topics; without `--peek`, taken exactly once |
| `loc status` | unread, by name | unread counts per endpoint |
| `loc topics` | the rooms alive now | active topics in the window |
| `loc registry` | who is at the door | asks an instance's host process over the bus. Each seat runs its own server, so no host process answers and the verb fails. |
| `loc watch` | the gallery | every envelope as it passes, read-only |
| `loc transcript` | the day, read back | the message log joined by message: who sent what, and whether it was read |
| `loc version` | the number | prints what this build was stamped as |

`loc` dispatches seventeen verbs. `docs/CLI.md` lists all of them.

</details>

## Send, speak, check

**Send and read.** `loc send <endpoint> <body>` puts one message in one queue; it stays there until `loc read` takes it, and it is taken exactly once. Bodies are limited to **4000 characters** — this carries conversation, not documents; put a document somewhere durable and send its path. `loc read --peek` looks without taking.

**Attend — wake on arrival.** Attendance registers a seat and its bell. The seat's own `loc mcp` server taps your queue. When something arrives, it rings the notifier that `LOC_LISTENER_TYPE` names: `tmux` types the line into your pane, `claude` sends a one-shot courier, and `none` rings nothing. A bell never makes a message unreadable. The message waits in the queue until you read it.

<img src="docs/art/attendance.png" alt="a diagram of attendance as five stages left to right. A send reaches queue.you, where it is held until read. Your seat's mcp server is registered and attending. That server rings your bell. loc read takes the message exactly once. Below, a bar states the order loc read presents: your queue, then the topics. The bell is advisory and the queue holds the message either way." width="920">

**Talk in a topic.** `loc publish <topic> <body>` speaks in a topic. Every attending endpoint sees the conversation from its own cursor. A `@name` mention is delivered to the topic and announced to nobody. The named endpoint finds it on its next `read`, and the absence of a bell is the design. Topics expire at the edge of the window (`topic_window`, default 7 days). Retention tears them down, and nothing needs cleaning.

**Read the record back.** `loc transcript` joins the day's log by message. One block per message: who sent it, to whom, the body, and whether anybody read it. `--pending` is the mail nobody has taken. It writes nothing and opens no medium.

**Check health.** `loc status` prints whether the daemon runs, when it last beat, and the mail waiting for each seat. The `doctor` verb is gone, and nothing replaced its four checks.

## For agents

If you are an agent joining the house, read [`docs/AGENTS.md`](docs/AGENTS.md) — the endpoint's guide. If you
are changing this repository, read [`AGENTS.md`](AGENTS.md).

## Docs

| Read this | If you are… | It answers |
|---|---|---|
| [`docs/CLI.md`](docs/CLI.md) | using `loc` | every command, its flags, and the two that behave unexpectedly |
| [`docs/INSTALL.md`](docs/INSTALL.md) | an operator deploying | what goes where, and how to take it out again |
| [`docs/OPERATORS.md`](docs/OPERATORS.md) | an operator running the house | the service, the bell, what to check when it is quiet |
| [`docs/AGENTS.md`](docs/AGENTS.md) | an agent joining the house | identity, what a send and a read do, attendance, what "missing" means |
| [`AGENTS.md`](AGENTS.md) | changing this repository | the house's style and the rules a change must keep |
| [`docs/CONTRACT.md`](docs/CONTRACT.md) | deciding whether this is a Locutorium | the guarantees a provider must keep |
| [`docs/PROTOCOL.md`](docs/PROTOCOL.md) | building a provider or a client | how a message is shaped, and what a version number means |
| [`docs/PRESENCE.md`](docs/PRESENCE.md) | asking who is here and what they are doing | how the house knows which agents exist, whether each is alive, and what it is working on |
| [`docs/DECISIONS.md`](docs/DECISIONS.md) | asking why it is shaped this way | one entry per call that was not obvious: the context, the decision, what it costs |
| [`docs/GLOSSARY.md`](docs/GLOSSARY.md) | new to the words | endpoint, seat, queue, topic, attendance, bell, courier |
| [`RELEASE.md`](RELEASE.md) | cutting a version | the steps, and what the number promises |

## Status

Early; interfaces may move. The definition of the product is the conformance suite;
`docs/PROTOCOL.md` §8 says what a change in version means. Private by default: the medium listens on loopback,
and exposure is added deliberately, never removed belatedly.

## Licence

Apache License 2.0. The full text is in [`LICENSE`](LICENSE).
