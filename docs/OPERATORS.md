# Running a house — the operator's manual

The **house** is a set of endpoints sharing one medium on one machine. This document is for the
person who deploys and keeps it: what lives where, what each knob does, what each hook must do, and
how to undo anything.

## What lives under `$LOC_HOME` (default `~/.locutorium`)

| path | what | who writes it |
|---|---|---|
| `config` | `key = value` lines: the provider, the URL, the window, the retention | `loc start` (once), then you |
| `hooks/` | your deployment's own hooks; `loc` reads none of them | you |
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

## The service

`./install.sh` builds `loc`, copies the stamped binary into `lib/locutorium`, and links `loc` at that
copy. It supervises nothing. `loc start` runs the broker and the heartbeat; `loc stop` ends them.
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
| `wake_window_seconds` | `5` | the listener's coalescing window before it knocks |
| `wake_breaker_per_minute` | `6` | max wakes per endpoint per minute; excess is suppressed, loudly, and nothing is lost |
| `wake_breaker_per_hour` | `60` | the hourly cap |

Change a key by editing the line; revert it by deleting the line.

## The hooks — the deployment's half of the contract

Hooks are executables under `$LOC_HOME/hooks/`. `loc` runs them; it never assumes what they do.
A hook that is not executable fails in the quietest possible way, so keep them `755`.

| hook | called as | must |
|---|---|---|
| `identity` | `identity` | print the endpoint name for the calling process, or exit non-zero. Used when `LOC_IDENTITY` is unset (`loc_identity`). A refusal is correct; guessing is not. |
| `nudge` | `nudge <endpoint> <line>` | ring that endpoint's doorbell with one line — advisory; failure never loses a message (`loc_nudge`). |
| `register` | `register <endpoint>` | record how to reach the endpoint's surface (a terminal, a session); called at `loc sub`. |
| `wake` | `wake <endpoint> <count>` | present what the listener drained into `run/<endpoint>.spool`, then clear it only on confirmed presentation. |
| `alive` | `alive <endpoint>` | exit 0 while the endpoint's session lives; the listener exits when it stops. |

### Wake modes — a pattern, not a rule

The wake hook decides *how* to present. A common shape is a per-endpoint mode file,
`run/<endpoint>.wake_mode`, over a default in `config`, with three modes: **off** — spool only,
present on the next `loc read`; **knock** — the doorbell text only, the body waits in the spool;
**courier** — a helper carries the spooled bodies onto the endpoint's surface and clears the spool
only when presentation is confirmed. An unknown mode must refuse and retain the spool. Whatever your
hook does, the invariant is the product's: *a wake never makes a message unreadable.*

## Rollback

| what | how |
|---|---|
| a config key | delete the line |
| a hook | your deployment repository's history (keep the hooks in one) |
| `loc` | `git checkout vX.Y.Z && ./install.sh` — the binary is a copy of a moment, so the installer remakes and re-copies it; a checkout alone rolls back nothing, because the installed copy is deliberately not the tree |
| the medium's definition | edit the `config` line, then `loc stop && loc start` |
| attendance | `loc unsub`; the queue keeps holding messages |

## Uninstall

`./install.sh --uninstall` removes the `loc` link, the stamped copies in `lib/locutorium`, and the
agent, and prints the `rm -rf $LOC_HOME` line without running it. The link goes only if it points at
what this clone would have made; anything else is refused and left where it is.

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

## When something is wrong

`loc status` (unread per endpoint, and whether a seat is registered) ·
`loc registry` (who is attending) · `run/<endpoint>.delivery.log` (what the
listener did and when) · `loc read --peek` (look without taking) · `loc watch` (every envelope,
read-only).
