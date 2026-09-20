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
There is no single ledger file: a seat exists while it is subscribed, and the ledger — one file per
endpoint under `run/presence` — is what `loc status` reads.

**If the `config` file goes missing, run `loc start`.** It writes the file again from the key table.
Every other verb that opens the broker refuses a home with no `config`, and says where it looked. A
live daemon keeps its own broker while the file is gone, but its sweep fails each beat, and the
heartbeat log records that. `loc start` is the way back.

The refusal reaches more than the sweep. No agent can send or read while the file is gone, because
each send and each read opens a connection of its own. An agent server that is running keeps its
listener, and the bell still rings, but the read that follows is refused. An agent server that starts
while the file is gone is refused.

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
| `monitor_url` | — (no default) | the medium's HTTP monitor. Nothing in this build requires it, so this key is optional |
| `topic_window` | `7d` | how long a topic's messages live |
| `send_requires_attendance` | `no` | *say-semantics*: refuse a send to an endpoint that is not attending, so the sender learns at the only moment it can act |
| `wake_window_seconds` | `5` | how long the seat's server coalesces arrivals before it rings |
| `wake_breaker_per_minute` | `6` | max wakes per endpoint per minute; excess is suppressed, loudly, and nothing is lost |
| `wake_breaker_per_hour` | `60` | the hourly cap |
| `wake_retry_seconds` | `60` | the fixed gap between one try of a seat's bell and the next |
| `wake_tries` | `3` | how many times a bell tries while mail is unread, before it gives up |

Change a key by editing the line; revert it by deleting the line.

`loc start` writes every key above except `wake_window_seconds`, `wake_breaker_per_minute` and
`wake_breaker_per_hour`. Those three are read when a line
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

One flat rule governs the bell: while the seat has unread mail, the bell tries. A try is a try
whatever it came to. It rang, a busy pane refused it, the breaker suppressed it, or the notifier
failed. None of those changes what happens next.

The gap between tries is `wake_retry_seconds`, and it is fixed. A streak gets `wake_tries` tries.
After the last try the bell gives up, and it does nothing more until mail arrives again. A new
arrival rings at once and starts a fresh count. A queue that reads empty ends the streak, because
the mail is read.

Every try after the first asks the broker how much mail is waiting, so the bell carries the count
the queue holds.

A seat that is busy for longer than its tries last gets no further ring. It finds the mail on its
next `read`, and `loc status` shows the state until then.

A busy refusal typed nothing into the pane, so it does not count against `wake_breaker_per_minute` or
`wake_breaker_per_hour`. A ring that typed does count, and so does a broken bell.

Identity comes from `LOC_IDENTITY` and from nothing else. A verb with no identity refuses
(`cannot determine sender identity: set LOC_IDENTITY`). A refusal is correct; guessing is not.

## An unread count on the agent's status line

The bell is the only thing that tells a seat mail arrived, and one bell can fail. A busy pane
refuses the bell and the server asks the pane again. A broken bell is not retried: there is no pane
address, or tmux cannot be read, or the courier exited non-zero. Nothing asks again after those, so
the mail waits in the queue and nobody is told. A second signal is what catches that.

`loc unread <endpoint>` is that signal. It prints a number, or the word `unknown`. It never prints
an empty line. An empty line is also what a seat with no mail looks like. A failed reading must not
look like a count of zero.

Call it from the agent's status line:

```sh
#!/bin/sh
# One reading of the mail waiting for this seat.
n=$(loc unread "$LOC_IDENTITY" 2>/dev/null)
case "$n" in
  0)              ;;                       # caught up: show nothing
  *[!0-9]*|"")    printf 'mail: UNREADABLE (%s)\n' "${n:-no answer}" ;;
  *)              printf 'mail: %s\n' "$n" ;;
esac
```

The script cannot hang. **The verb answers within 2 seconds**, even against a broker that accepts
the connection and then says nothing.

The script must name the **full endpoint**, `<instance>.<agent>`. A bare agent name is not an
endpoint and the verb refuses it, with nothing on standard out. `LOC_IDENTITY` already holds the full
name.

The warning branch catches `unknown` and everything else the verb could not produce, an empty answer
included. Read it as "I cannot see your mail", not as "you have none". The reason is on standard
error; run the command by hand to read it.

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
{"seat":"workshop.scribe","status":"bell-try","ts":"2026-09-16T17:02:12Z","try":1,"of":3,"result":"rang"}
{"seat":"workshop.scribe","status":"bell-try","ts":"2026-09-16T17:03:12Z","try":2,"of":3,"result":"failed","reason":"exit status 3"}
{"seat":"workshop.scribe","status":"bell-gave-up","ts":"2026-09-16T17:04:12Z","after":3,"rang":1,"last":"failed"}
{"seat":"workshop.scribe","status":"queue-deleted","ts":"2026-09-16T18:40:00Z","reason":"orphan"}
```

`bell-try` is ONE TRY of the seat's bell. `try` and `of` place it in its streak, and `result` is one
of `rang`, `refused`, `suppressed` and `failed`. A try that rang carries no `reason`. The result is
written down and it changes no count.

`bell-gave-up` closes a streak that used all its tries. `rang` is how many of them reached the
pane, and it is written even when it is 0. `last` is the final try's result. A bell that rang and
was not answered is a different fact from a bell that never rang, and the two want different
repairs. A give-up line with no `last` was written before those two fields existed.

A bell line names no message: the server is told that mail arrived and never which message arrived.
Mail that no bell announced is still in the queue, and the seat finds it on its next `read`. The
notifier's own per-attempt lines are in `run/<endpoint>.delivery.log`.

`bell-failed` was the old line, one per busy streak. No build writes one. `loc transcript` still
reads one, and `loc status` ignores it.

`queue-deleted` says a queue is gone, so mail stops reaching that seat. The reason is one of `left`
(the agent unsubscribed), `displaced` (another agent took the seat with `--force`) or `expired` (the
sweep found the registered process gone). Older logs also hold `orphan`, from the sweep that used to
delete a queue no row claimed; no version writes that reason now.

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
`loc registry` (who is registered, and what each one registered) · `run/<endpoint>.delivery.log` (what the
listener did and when) · `run/log/YYYY-MM-DD.jsonl` (what happened to each message, and to each
seat) · `loc read --peek` (look without taking) · `loc watch` (every envelope,
read-only).

**A seat that has gone quiet with a `tmux` or `claude` listener may be ringing a stale address.**
`docs/CLI.md` §mcp has the warning and the check: a tmux server restart reassigns pane ids, and
nothing here announces it on its own.

A queue made before endpoints carried an instance name has a bare name, such as `QUEUE_scribe`.
The sweep cannot see such a queue, and it never removes one: the name does not say which
deployment made it. `loc status` prints one line for each of them. Remove them with the broker's
own command-line tool.

**A queue with no row.** `loc status` and `loc sweep` both print `<endpoint>: queue with no row;
nothing removes it; remove it with loc unsubscribe <endpoint>`. Mail sent to that endpoint is
accepted and stored there for nobody. Nothing in Locutorium removes it, because an absent row does
not say whether the agent left or the row was lost. You have two remedies, and they are opposites:

- `loc unsubscribe <endpoint>` destroys the queue and the mail in it.
- `loc subscribe <endpoint> …` takes the queue over, with the mail in it, for the agent you
  subscribe.

The usual cause is an `unsubscribe` interrupted between its two writes. Running it again finishes
the job.

