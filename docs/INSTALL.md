# Installing the Locutorium

`loc` is a compiled binary, made by `make build` into `build/bin/loc`, with one command reference
(`CLI.md`) and one conformance suite that is the gate. It carries its provider and its version
inside it, so it answers from anywhere and a copy of it is simply `loc`. The version is stamped in
at link time from `VERSION`, which is why a stale binary is worth rebuilding rather than trusting.

`./install.sh` builds it, copies the stamped binary out of the build tree, links it into your PATH
as `loc`, and puts the medium (a `nats-server`) under launchd.

> A bash implementation lived here until 2026-09-07 and was deleted. It was interim, and keeping two
> implementations meant maintaining the layer between them — which is where its defects turned out
> to live, not in either tool.

## Dependencies

| need | why | install |
|---|---|---|
| `go`, `make` | `loc` is built from source; the installer refuses by name rather than installing a toolchain for you | `brew install go` |
| bash ≥ 3.2 | the installer, the suite and the provider scripts (macOS ships 3.2; that is the floor) | — |
| `nats` | `bootstrap.sh` shells out to the NATS CLI for its bcrypt hashes; `loc` itself speaks the protocol and never runs it | `brew install nats-io/nats-tools/nats` |
| `nats-server` | the medium itself, run by the LaunchAgent | `brew install nats-server` |
| `openssl` | `bootstrap.sh` generates credentials | ships with macOS |

Development only (the conformance suite): `jq`, `nats-server` on PATH, and BSD `sed` (`sed -i ''`);
the suite is macOS-only for now. `make build` first, then `bash conformance/run.sh` — `LOC_BIN_DIR`
defaults to `build/bin`. A seat's listener is the `mcp` verb, which the suite does not speak; those
cases are skipped by name, each printing its reason and where the property IS proven, and counted.

## What `./install.sh` writes — the whole list

Three things:

1. `$LIBDIR/loc-<version>-<sha>` — the built binary, **copied** out of `build/`. `$LIBDIR` is
   `lib/locutorium` beside the prefix. **A copy, not a link into `build/`**: a link would make the
   installed tool whatever was last compiled, so `make build` would silently change what every
   caller runs. The name carries the version and commit, so `readlink` answers which build a machine
   is on without executing anything.
2. `$PREFIX/loc` — a symlink to that copy. `$PREFIX` is `$(brew --prefix)/bin` if it is writable,
   else `~/.local/bin`; override with `--prefix DIR`.
3. `~/Library/LaunchAgents/com.locutorium.nats-server.plist` — rendered from
   `providers/nats/launchd/com.locutorium.nats-server.plist.in` with the `nats-server` path and
   `$LOC_HOME` resolved on your machine, then loaded with `launchctl bootstrap`.

It never writes under `$LOC_HOME` (default `~/.locutorium`: credentials, config, endpoints, store),
never runs `bootstrap.sh` for you, never edits your shell files (it prints the `PATH` line to add
if needed), and never restarts a loaded agent unless you pass `--restart-service`.

## Flags

| flag | effect |
|---|---|
| `--prefix DIR` | where the `loc` link goes; the stamped copy goes in `lib/locutorium` beside it |
| `--dry-run` | print NEW / CHANGED / UNCHANGED for each artifact (with a diff for the plist); write and load nothing |
| `--no-service` | install `loc` only; no LaunchAgent (use when the medium runs elsewhere, or on Linux) |
| `--restart-service` | the only way a running agent is stopped and started again — needed after the plist changes |
| `--uninstall` | remove the `loc` link (only if it points at a copy this clone made), the stamped copies, and the agent + plist; never `$LOC_HOME` |
| `-h`, `--help` | usage |

## What a run looks like

```
$ ./install.sh --dry-run
UNCHANGED  /opt/homebrew/bin/loc -> /opt/homebrew/lib/locutorium/loc-0.1.1-d616027
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

## Telling an agent runtime about it

The Go build serves one seat to an agent runtime over stdio. The runtime launches it, so nothing has
to be started or supervised; it needs the binary on `PATH` and the endpoint it is speaking as:

```json
{"mcpServers": {"loc": {"command": "loc", "args": ["mcp"], "env": {"LOC_IDENTITY": "<instance>.<agent>"}}}}
```

`docs/CLI.md` §mcp is what that server does and what a runtime configured in TOML wants instead.

## Exit codes

`0` done or nothing to do · `2` usage · `3` a dependency is missing (each is named with its
install line), or a `make build` that failed · `4` refusal — something is in the way that the
installer did not create · `5`
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
