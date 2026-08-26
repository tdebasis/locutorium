# Installing the Locutorium

`loc` is a bash CLI. It is installed by cloning this repository and running `./install.sh`, which
links `loc` into your PATH and puts the medium (a `nats-server`) under launchd. There is no package;
the clone *is* the installation, and `loc` finds its own tree through the link.

## Dependencies

| need | why | install |
|---|---|---|
| bash ≥ 3.2 | the CLI (macOS ships 3.2; that is the floor) | — |
| `nats` | every verb talks to the medium through the NATS CLI (`_nats_env`, `provider_*`) | `brew install nats-io/nats-tools/nats` |
| `nats-server` | the medium itself, run by the LaunchAgent | `brew install nats-server` |
| `python3` | envelope JSON and character counting (`loc_envelope`, `loc_render`) | ships with the developer tools; `brew install python` |
| `curl` | `loc registry` reads the server's monitor endpoint | ships with macOS |
| `openssl` | `bootstrap.sh` generates credentials | ships with macOS |

Development only (the conformance suite): `jq`, `nats-server` on PATH, and BSD `sed` (`sed -i ''`);
the suite is macOS-only for now.

## What `./install.sh` writes — the whole list

1. `$PREFIX/loc` — a symlink to `bin/loc` in this clone. `$PREFIX` is `$(brew --prefix)/bin` if it
   is writable, else `~/.local/bin`; override with `--prefix DIR`.
2. `~/Library/LaunchAgents/com.locutorium.nats-server.plist` — rendered from
   `providers/nats/launchd/com.locutorium.nats-server.plist.in` with the `nats-server` path and
   `$LOC_HOME` resolved on your machine, then loaded with `launchctl bootstrap`.

It never writes under `$LOC_HOME` (default `~/.locutorium`: credentials, config, endpoints, store),
never runs `bootstrap.sh` for you, never edits your shell files (it prints the `PATH` line to add
if needed), and never restarts a loaded agent unless you pass `--restart-service`.

## Flags

| flag | effect |
|---|---|
| `--prefix DIR` | where the `loc` link goes |
| `--dry-run` | print NEW / CHANGED / UNCHANGED for each artifact (with a diff for the plist); write and load nothing |
| `--no-service` | link `loc` only; no LaunchAgent (use when the medium runs elsewhere, or on Linux) |
| `--restart-service` | the only way a running agent is stopped and started again — needed after the plist changes |
| `--uninstall` | remove the link (only if it points into this clone) and the agent + plist; never `$LOC_HOME` |
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
install line) · `4` refusal — something is in the way that the installer did not create · `5`
`launchctl` failed (the command is echoed).

## Uninstall

```
./install.sh --uninstall
```

Removes the link and the agent. Prints, and does not run, the command that would remove
`~/.locutorium` — that directory holds your credentials and is yours to delete.

## Linux

The link works anywhere; there is no launchd, so run `nats-server -c ~/.locutorium/nats-server.conf`
under your own supervisor and pass `--no-service`. A systemd unit is a planned follow-up.
