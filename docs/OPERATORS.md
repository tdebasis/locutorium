# Running a house — the operator's manual

The **house** is a set of endpoints sharing one medium on one machine. This document is for the
person who deploys and keeps it: what lives where, what each knob does, how a seat is told about
mail, and how to undo anything.

## What lives under `$LOC_HOME` (default `~/.locutorium`)

| path | what | who writes it |
|---|---|---|
| `config` | `key = value` lines: the provider, the URL, the window, the retention | `loc start` (once), then you |
| `run/` | live state: the daemon pidfile, the heartbeat log, delivery logs — never edit | `loc` |
| `store/` | JetStream data | the embedded broker |
| `run/loc.log` | the daemon's own output | `loc` |
| `forbidden` | your deployment's vocabulary, for the cleanliness check (below); mode 0600 | you |

## Your deployment's vocabulary

This tree is written to be readable by strangers, and `conformance/check-clean.sh` enforces that: it
refuses a working tree that carries your deployment's own words — org names, member names, internal
tool names, your absolute home path. Those words are **your** data, so they are not in the
repository. Write them, one per line, to `$LOC_HOME/forbidden` (`chmod 600`); point elsewhere with
`LOC_FORBIDDEN_FILE`. Each line is one extended-regex alternative; blank lines and lines starting
with `#` are ignored:

```
# names this house uses that a stranger must never read
acme-internal
codename-lyra
/[Uu]sers/ada
```

Run it with `conformance/check-clean.sh`; it prints the list it used and the number of patterns.
With no file installed it still runs, against a built-in generic list holding only absolute home
paths, and says on stderr that the deployment list is missing. A missing list is a weaker check,
never a silent pass.

## First run

`loc start` writes `config` when there is none, prints every default it wrote, and runs the broker.
It never rewrites a `config` that is already there, so a key you edited survives every later start.
There is no roster file: a seat exists while it is subscribed, and the ledger is what `loc status`
reads.

**If the `config` file goes missing, run `loc start`.** It writes the file again from the key table.
Every other verb that opens the broker refuses a home with no `config`, and says where it looked. A
live daemon keeps its own broker while the file is gone, but its sweep fails each beat, and the
heartbeat log records that. `loc start` is the way back.

The refusal reaches more than the sweep. No agent can send while the file is gone. An agent server
that loses its connection cannot connect again, so that agent receives nothing until the file is
back. A server that stays connected continues to work.

`loc start` writes the defaults. It cannot restore a key that a person edited in the lost file. After
a recovery, compare the new file with your own record of the old one, and edit it again.

## The service

`./install.sh` builds `loc`, copies the stamped binary into `lib/locutorium`, and links `loc` at that
copy. The default builds the newest release tag in a temporary worktree, so your checkout is not
touched. `./install.sh --main` builds this checkout's `HEAD`. With no release tag and no `--main`,
the installer exits 2 and names `--main`. It supervises nothing.
`loc start` runs the broker and the heartbeat. The heartbeat sweeps
every five minutes. `loc stop` ends them.
Logs: `$LOC_HOME/run/loc.log`. State of the daemon: the first line of `loc status`. Endpoints
survive a medium restart — queues are
durable; listeners reconnect.

## Config keys

| key | default | meaning |
|---|---|---|
| `provider` | — (required) | which medium adapter to use. The name is looked up in a registry compiled into the binary, so a name it was not built with is refused rather than searched for |
| `nats_url` | `nats://127.0.0.1:4222` | where the medium listens |
| `monitor_url` | — (no default) | the medium's HTTP monitor. `loc registry` asks the instance's host over the bus instead, so this key is optional |
| `topic_window` | `7d` | how long a topic's messages live |
| `send_requires_attendance` | `no` | *say-semantics*: refuse a send to an endpoint that is not attending, so the sender learns at the only moment it can act |
| `wake_window_seconds` | `5` | how long the seat's server coalesces arrivals before it rings |
| `wake_breaker_per_minute` | `6` | max wakes per endpoint per minute; excess is suppressed, loudly, and nothing is lost |
| `wake_breaker_per_hour` | `60` | the hourly cap |

Change a key by editing the line; revert it by deleting the line.

`loc start` writes every key above except the three `wake_` keys. Those three are read when a line
holds them and carry the default above when no line does. Add the line yourself to change one.

A default is not a deployment. A home with no `config` file at all is refused by every verb that
opens the broker, because the built-in `nats_url` is another deployment's live broker. A file that
exists and says nothing about one key still takes that key's default.

## The bell — how a seat is told

The bell is inside the binary. The seat's own `loc mcp` server rings it. `loc` runs no hook, and it
reads no `$LOC_HOME` directory of executables.

A seat says how it is reached with two environment variables, set where the agent runtime launches
it:

| variable | what it says |
|---|---|
| `LOC_LISTENER_TYPE` | which notifier rings this seat. `subscribe` refuses any value outside the set below. |
| `LOC_LISTENER_ADDRESS` | where that notifier delivers: a pane target for `tmux`, a session name for `claude`. |

| type | what it does |
|---|---|
| `tmux` | types one bell line into the seat's pane, then submits it. It first checks that the pane accepts input. |
| `claude` | starts one Claude session to carry the bell to the seat. At most one session per seat per 30 seconds. |
| `none` | rings nothing. The seat finds its mail on its next `loc read`. |

There is no fallback from one notifier to another. A bell that could not ring is written to
`run/<endpoint>.delivery.log`, and the message waits in the queue.

Identity comes from `LOC_IDENTITY` and from nothing else. A verb with no identity refuses
(`loc_identity`). A refusal is correct; guessing is not.

## The day's record

`run/log/YYYY-MM-DD.jsonl` holds one JSON object per line, appended by every process on the machine.
The day is the UTC day of the event. Each line is written in one call, so two processes never split
a line between them. Read it with `grep`, or with `jq` one line at a time.

There are two families of line. **Message events carry `uid`.** The `uid` is the message's id, and it
is what joins the lines for one message.

```json
{"uid":"1f0c…","status":"sent","ts":"2026-09-16T17:02:11Z","from":"workshop.scribe","to":"workshop.clerk","body":"the ledger is ready"}
{"uid":"1f0c…","status":"failed","ts":"2026-09-16T17:02:11Z","from":"workshop.scribe","to":"workshop.clerk","reason":"target not registered"}
{"uid":"1f0c…","status":"read","ts":"2026-09-16T17:04:40Z","by":"workshop.clerk"}
```

`sent` says the message reached the medium. `failed` says `send` refused it, and the reason is one of
`no identity`, `too long`, `target not registered` or `bus unreachable`. A `failed` line carries no
body, because the message never left the sender. `read` says a reader took the message off its queue,
and names who took it.

A `sent` line with no `read` line under the same `uid` is a message nobody has collected yet.

**Seat events carry `seat` and no `uid`.**

```json
{"seat":"workshop.scribe","status":"bell-failed","ts":"2026-09-16T17:02:12Z","reason":"exit status 3"}
{"seat":"workshop.scribe","status":"queue-deleted","ts":"2026-09-16T18:40:00Z","reason":"orphan"}
```

`bell-failed` says the seat's notifier could not ring. The mail is still in the queue, and the seat
finds it on its next `read`. The line names no message: the server is told that mail arrived and
never which message arrived.

`queue-deleted` says a queue is gone, so mail stops reaching that seat. The reason is one of `left`
(the agent unsubscribed), `displaced` (another agent took the seat with `--force`), `expired` (the
sweep found the registered process gone) or `orphan` (the sweep found a queue no row claims, in an instance its ledger holds).

The file holds message bodies in plain text. The writer creates the directory `0700` and the file
`0600`, and it sets those modes only when it creates them.

## Rollback

| what | how |
|---|---|
| a config key | delete the line |
| a seat's bell | edit `LOC_LISTENER_TYPE` where the agent runtime launches the seat |
| `loc` | `git checkout vX.Y.Z && ./install.sh --main` — the default mode ignores your checkout and builds the newest release tag, so a rollback names `--main`. The binary is a copy of a moment, so the installer remakes and re-copies it; a checkout alone rolls back nothing, because the installed copy is deliberately not the tree |
| the medium's definition | edit the `config` line, then `loc stop && loc start` |
| attendance | end the seat's `loc mcp` server; the queue keeps holding messages |

## Uninstall

`./install.sh --uninstall` removes the `loc` link and the stamped copies in `lib/locutorium`. It
prints the `rm -rf $LOC_HOME` line without running it. The link goes only if it points at what this
clone would have made; anything else is refused and left where it is.

## The suite on a machine that stays

Run the conformance suite in CI on a machine of your own rather than a fresh image, and a cancelled
job does not take the run with it. The step's shell is killed; the seats' listener loops and the
scratch server are not, because the run detached them. They live on into the next job, whose own
listener census counts them and whose registry and wake cases then read a second, stale attendance —
four failures with nothing wrong in the tree. The suite now tears itself down on `TERM` and `INT` as
well as on exit, taking the server it started and the listeners its own pidfiles name; and
`conformance/leftovers.sh reap` runs as a step before the suite to take away whatever an earlier job
still managed to leave. Its scope is the runner's own work tree and the runner's own temp directory,
both read from the environment and neither ever guessed: with neither named it refuses to run, and it
excludes its own shell, everything that started it, and everything it starts. If anything it named is
still standing afterwards the step fails, so a run never begins against processes that are not its
own.

**The suite refuses your live broker.** Under `go test`, the nats provider refuses the default
`nats_url`, `nats://127.0.0.1:4222`. It refuses `localhost` and `::1` at that port too. A test that
needs a broker must boot one and pin `nats_url` to its port. This holds on a machine that runs the
Locutorium, where the default address is the live daemon.

**A scratch home needs a `config` file.** The provider refuses a home that has none, whatever
address the defaults would give it. A test that wants a broker writes its own `nats_url`; a test
that wants none writes a closed port.

**The daemon sweeps the broker it runs.** It takes that address from its own server at boot and
keeps it for the life of the process. A `config` file rewritten under a running daemon moves no
later beat, so a restored backup cannot point the sweep at another deployment's queues.

## When something is wrong

`loc status` (unread per endpoint, and whether a seat is registered) ·
`loc registry` (who is attending) · `run/<endpoint>.delivery.log` (what the
listener did and when) · `run/log/YYYY-MM-DD.jsonl` (what happened to each message, and to each
seat) · `loc read --peek` (look without taking) · `loc watch` (every envelope,
read-only).
