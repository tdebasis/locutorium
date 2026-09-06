# Running a house — the operator's manual

The **house** is a set of endpoints sharing one medium on one machine. This document is for the
person who deploys and keeps it: what lives where, what each knob does, what each hook must do, and
how to undo anything.

## What lives under `$LOC_HOME` (default `~/.locutorium`)

| path | what | who writes it |
|---|---|---|
| `config` | `key = value` lines: the provider, URLs, the window, wake policy | bootstrap (once), then you |
| `endpoints` | the roster, one name per line | bootstrap (once), then you |
| `creds/<name>` | one credential per endpoint plus `admin` and `watch`, mode 0600 | bootstrap only |
| `nats-server.conf` | the medium's config: loopback listener, JetStream store, one user per credential with a per-endpoint ACL | bootstrap only |
| `hooks/` | your deployment's five hooks (below) | you |
| `run/` | live state: listener pidfiles, spools, delivery logs — never edit | `loc` |
| `store/` | JetStream data | the medium |
| `server.log` | the medium's stdout/stderr | launchd |
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

## Bootstrap

`providers/nats/bootstrap.sh <endpoint> [<endpoint> …]` writes everything above except `hooks/` and
`run/`. It **refuses to run over an existing deployment**. `--force` regenerates the deployment
wholesale: **every credential rotates** (every running client's password goes stale until
`make-contexts.sh` and the listeners are restarted) and `config` and `endpoints` are rewritten,
dropping any key you added by hand (`monitor_url`, `send_requires_attendance`). Adding an endpoint
to a live house without rotating the others is not supported yet; it is on the roadmap.

`providers/nats/make-contexts.sh` writes one NATS CLI context per credential for hand debugging and
selects none of them.

## The service

`./install.sh` renders `providers/nats/launchd/com.locutorium.nats-server.plist.in` with the
`nats-server` path and `$LOC_HOME` of the machine it runs on, and loads it. It also links the shell
tool as `loc`, and with `--go` builds and links the Go build beside it as `loc-go` — the flag is
additive and the `loc` link is untouched either way. `--restart-service` is
the only path that stops a running medium. Logs: `$LOC_HOME/server.log`. State of the agent:
`launchctl list com.locutorium.nats-server`. Endpoints survive a medium restart — queues are
durable; listeners reconnect.

## Config keys

| key | default | meaning |
|---|---|---|
| `provider` | — (required) | which medium adapter to use. The shell tool reads it as a filename and sources `lib/providers/<name>.sh`; the Go build looks the name up in a registry compiled into the binary, so a name it was not built with is refused rather than searched for. Same key, same value, two ways of finding the thing |
| `nats_url` | `nats://127.0.0.1:4222` | where the medium listens |
| `monitor_url` | — (no default) | the medium's HTTP monitor. Required by the shell tool's `loc registry`, which reads it over HTTP; the Go build asks the instance's host over the bus and needs neither the key nor `curl` |
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
| the shell tool | `git checkout vX.Y.Z && ./install.sh` — the link follows the tree, so the checkout is the rollback |
| the Go build | `git checkout vX.Y.Z && make build && ./install.sh --go` — the binary is a copy of a moment, so it must be remade; the checkout alone rolls back nothing |
| the medium's definition | edit or restore the plist, then `./install.sh --restart-service` |
| attendance | `loc unsub`; the queue keeps holding messages |

## Uninstall

`./install.sh --uninstall` removes both links — `loc` and, if it is there, `loc-go` — and the agent,
and prints the `rm -rf $LOC_HOME` line without running it. Each link goes only if it points at what
this clone would have made; anything else is refused and left where it is.

## When something is wrong

`loc doctor` (four checks, as any endpoint; the shell tool's — the Go build refuses it by name) ·
`loc status` (unread per endpoint) ·
`loc registry` (who is attending — the shell tool needs `monitor_url` for it) · `run/<endpoint>.delivery.log` (what the
listener did and when) · `loc read --peek` (look without taking) · `loc watch` (every envelope,
read-only).
