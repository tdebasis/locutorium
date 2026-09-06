# Installing the Locutorium

There are **two implementations** of `loc` in this tree, one command reference for both (`CLI.md`),
and one conformance suite that is run against each.

- **The shell tool** — a bash CLI. There is no package: the clone *is* the installation, and the
  tool finds its own tree through the link. Copy it out of the tree and it refuses, because a copy
  has no `lib/` beside it and nothing to run.
- **The Go build** — a compiled binary, made by `make build` into `build/bin/loc`. It carries its
  provider and its version inside it, so it answers from anywhere and a copy of it is simply `loc`.
  The version is stamped in at link time from `VERSION`, which is why a stale binary is worth
  rebuilding rather than trusting.

`./install.sh` links the shell tool into your PATH as `loc` and puts the medium (a `nats-server`)
under launchd. `./install.sh --go` does that **and** builds the Go binary and links it beside the
first, as `loc-go`. The two are named apart deliberately: one PATH, two implementations, and never
a question about which one answered.

The shell tool answers every verb. The Go build answers every verb except `sub`, `unsub` and
`doctor` — it names those three and replies `not implemented in this build`.

## Dependencies

| need | why | install |
|---|---|---|
| bash ≥ 3.2 | the shell tool (macOS ships 3.2; that is the floor) | — |
| `nats` | the shell tool reaches the medium by shelling out to the NATS CLI for every verb (`_nats_env`, `provider_*`); the Go build speaks the protocol itself and never runs it | `brew install nats-io/nats-tools/nats` |
| `nats-server` | the medium itself, run by the LaunchAgent | `brew install nats-server` |
| `python3` | the shell tool's envelope JSON and character counting (`loc_envelope`, `loc_render`); the Go build does both itself | ships with the developer tools; `brew install python` |
| `curl` | the shell tool's `loc registry` reads the server's HTTP monitor endpoint; the Go build asks the instance's host over the bus instead and needs neither `curl` nor `monitor_url` | ships with macOS |
| `openssl` | `bootstrap.sh` generates credentials | ships with macOS |
| `go`, `make` | **only** for the Go build: `make build`, and `./install.sh --go`. Nothing else in the tree needs a toolchain, and the installer refuses `--go` by name rather than installing one for you | `brew install go` |

Development only (the conformance suite): `jq`, `nats-server` on PATH, and BSD `sed` (`sed -i ''`);
the suite is macOS-only for now. To run it against the Go build, `make build` first and then
`LOC_BIN_DIR="$PWD/build/bin" LOC_IMPL=go bash conformance/run.sh` — the same file, the same cases,
with the cases that are the shell tool's by nature skipped by name, each with its reason printed and counted.

## What `./install.sh` writes — the whole list

Two things, or three with `--go`:

1. `$PREFIX/loc` — a symlink to `bin/loc` in this clone, the shell tool. `$PREFIX` is
   `$(brew --prefix)/bin` if it is writable, else `~/.local/bin`; override with `--prefix DIR`.
2. `$PREFIX/loc-go` — **with `--go` only** — a symlink to `build/bin/loc` in this clone, the Go
   build. The flag is additive: it runs `make build` first (and refuses, naming `go`, if there is
   no toolchain) and never disturbs the `loc` link.
3. `~/Library/LaunchAgents/com.locutorium.nats-server.plist` — rendered from
   `providers/nats/launchd/com.locutorium.nats-server.plist.in` with the `nats-server` path and
   `$LOC_HOME` resolved on your machine, then loaded with `launchctl bootstrap`.

It never writes under `$LOC_HOME` (default `~/.locutorium`: credentials, config, endpoints, store),
never runs `bootstrap.sh` for you, never edits your shell files (it prints the `PATH` line to add
if needed), and never restarts a loaded agent unless you pass `--restart-service`.

## Flags

| flag | effect |
|---|---|
| `--prefix DIR` | where the `loc` link goes (and `loc-go`, with `--go`) |
| `--go` | also `make build` the Go binary and link it as `loc-go`; additive, the `loc` link is untouched |
| `--dry-run` | print NEW / CHANGED / UNCHANGED for each artifact (with a diff for the plist); write and load nothing |
| `--no-service` | link `loc` only; no LaunchAgent (use when the medium runs elsewhere, or on Linux) |
| `--restart-service` | the only way a running agent is stopped and started again — needed after the plist changes |
| `--uninstall` | remove both links (each only if it points into this clone) and the agent + plist; never `$LOC_HOME`. Not gated on `--go`: uninstall removes everything this clone made |
| `-h`, `--help` | usage |

## What a run looks like

```
$ ./install.sh --dry-run
UNCHANGED  /opt/homebrew/bin/loc -> ~/src/locutorium/bin/loc
CHANGED    ~/Library/LaunchAgents/com.locutorium.nats-server.plist
           --- (on disk)
           +++ (rendered)
           -    <string>/usr/local/opt/nats-server/bin/nats-server</string>
           +    <string>/opt/homebrew/bin/nats-server</string>
           --dry-run: nothing written, nothing loaded
--dry-run: 1 artifact(s) would change; nothing written.
```

A second real run prints `nothing to do (every artifact already matches).` Moved the clone?
Re-run `./install.sh`; the link is repointed and reported as CHANGED.

## Exit codes

`0` done or nothing to do · `2` usage · `3` a dependency is missing (each is named with its
install line — including `go` under `--go`, and a `make build` that failed) · `4` refusal — something is in the way that the installer did not create · `5`
`launchctl` failed (the command is echoed).

## Uninstall

```
./install.sh --uninstall
```

Removes the links and the agent. A link goes only if it points at what this clone would have made;
anything else is somebody's and is left alone, loudly. Prints, and does not run, the command that
would remove `~/.locutorium` — that directory holds your credentials and is yours to delete.

## Linux

The links work anywhere; there is no launchd, so run `nats-server -c ~/.locutorium/nats-server.conf`
under your own supervisor and pass `--no-service`. A systemd unit is a planned follow-up. The
conformance suite is macOS-only for now, so the Go build is exercised there, not here.
