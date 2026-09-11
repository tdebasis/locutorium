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
| `nats` | the conformance suite asserts stream state through the NATS CLI; `loc` itself speaks the protocol and never runs it | `brew install nats-io/nats-tools/nats` |

The broker is embedded in the binary, so no server package is needed. `loc start` runs it and
`loc stop` ends it.

Development only (the conformance suite): `jq`, the `nats` CLI on PATH, and BSD `sed` (`sed -i ''`);
the suite is macOS-only for now. `make build` first, then `bash conformance/run.sh` — `LOC_BIN_DIR`
defaults to `build/bin`. A seat's listener is the `mcp` verb, which the suite does not speak; those
cases are skipped by name, each printing its reason and where the property IS proven, and counted.

## What `./install.sh` writes — the whole list

Two things:

1. `$LIBDIR/loc-<version>-<sha>` — the built binary, **copied** out of `build/`. `$LIBDIR` is
   `lib/locutorium` beside the prefix. **A copy, not a link into `build/`**: a link would make the
   installed tool whatever was last compiled, so `make build` would silently change what every
   caller runs. The name carries the version and commit, so `readlink` answers which build a machine
   is on without executing anything.
2. `$PREFIX/loc` — a symlink to that copy. `$PREFIX` is `$(brew --prefix)/bin` if it is writable,
   else `~/.local/bin`; override with `--prefix DIR`.

It never writes under `$LOC_HOME` (default `~/.locutorium`: config, store), never edits your shell
files (it prints the `PATH` line to add if needed), and starts and supervises nothing. `loc start`
is the lifecycle.

## Flags

| flag | effect |
|---|---|
| `--prefix DIR` | where the `loc` link goes; the stamped copy goes in `lib/locutorium` beside it |
| `--dry-run` | print NEW / CHANGED / UNCHANGED for each artifact; write nothing |
| `--uninstall` | remove the `loc` link (only if it points at a copy this clone made) and the stamped copies; never `$LOC_HOME` |
| `-h`, `--help` | usage |

## What a run looks like

```
$ ./install.sh --dry-run
           --dry-run: 'make build' not run; /opt/homebrew/bin/loc would point at /opt/homebrew/lib/locutorium/loc-0.1.1-d616027
UNCHANGED  /opt/homebrew/bin/loc -> /opt/homebrew/lib/locutorium/loc-0.1.1-d616027
--dry-run: 0 artifact(s) would change; nothing written.
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
installer did not create.

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
