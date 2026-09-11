# Installing the Locutorium

`loc` is a compiled binary, made by `make build` into `build/bin/loc`, with one command reference
(`CLI.md`) and one conformance suite that is the gate. It carries its provider and its version
inside it, so it answers from anywhere and a copy of it is simply `loc`. The version is stamped in
at link time from `VERSION`, which is why a stale binary is worth rebuilding rather than trusting.

`./install.sh` builds it, copies the stamped binary out of the build tree, and links it into your
PATH as `loc`. The broker runs inside the binary, so the installer supervises nothing.

> A bash implementation lived here until 2026-09-07 and was deleted. It was interim, and keeping two
> implementations meant maintaining the layer between them — which is where its defects turned out
> to live, not in either tool.

## Dependencies

| need | why | install |
|---|---|---|
| `go`, `make` | `loc` is built from source; the installer refuses by name rather than installing a toolchain for you | `brew install go` |
| bash ≥ 3.2 | the installer and the suite (macOS ships 3.2; that is the floor) | — |
| `nats` | the conformance suite asserts stream state through the NATS CLI; `loc` itself speaks the protocol and never runs it | `brew install nats-io/nats-tools/nats` |

The broker is embedded in the binary, so no server package is needed. `loc start` runs it and
`loc stop` ends it. `loc start` also runs the heartbeat, which sweeps every five minutes.

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
to be started or supervised. It needs the binary on `PATH`, the endpoint it speaks as, and how the
bell reaches the seat:

```json
{"mcpServers": {"loc": {"command": "loc", "args": ["mcp"], "env": {
  "LOC_IDENTITY": "<instance>.<agent>",
  "LOC_LISTENER_TYPE": "tmux",
  "LOC_LISTENER_ADDRESS": "<pane target>"}}}}
```

`LOC_LISTENER_TYPE` is `tmux`, `claude` or `none`. The server refuses to start without both
listener variables.

`docs/CLI.md` §mcp is what that server does and what a runtime configured in TOML wants instead.

## Sample supervisor files

A supervisor is optional. `loc start` detaches, and it survives the terminal that ran it. Use a
supervisor when you want the broker back after a reboot or a logon.

These samples are documentation. This repository ships no code behind them. Each one runs
`loc start` and nothing else.

`loc start` launches the broker, detaches it, and exits 0. Give no sample a restart policy. A
supervisor that restarts the launcher would run it in a loop. A repeated run is otherwise safe:
`loc start` prints `already running, pid N` and exits 0 when the daemon is already up.

The macOS and Linux samples spell `/usr/local/bin/loc`. The Windows sample spells `loc.exe`. Use
the path your own `./install.sh` run printed.

### macOS

Save this as `~/Library/LaunchAgents/com.locutorium.loc.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.locutorium.loc</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/loc</string>
    <string>start</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
</dict>
</plist>
```

Load it with `launchctl load ~/Library/LaunchAgents/com.locutorium.loc.plist`. The sample sets
`RunAtLoad` and no `KeepAlive`, for the reason above.

One line in your login shell file, `~/.zprofile`, does the same job:

```sh
loc start >/dev/null 2>&1
```

### Linux

A systemd user unit, `~/.config/systemd/user/locutorium.service`:

```ini
[Unit]
Description=Locutorium broker

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/local/bin/loc start

[Install]
WantedBy=default.target
```

Enable it with `systemctl --user enable --now locutorium.service`.

### Windows

**The Windows build does not compile today.** `GOOS=windows go build ./...` fails at
`internal/loc/listener.go:36`, where `syscall.Kill` does not exist on Windows. The three
`*_windows.go` files under `cmd/loc/` ship uncompiled. This sample is here for the build that comes
later. Do not read it as a supported target.

This sample is a scheduled task and not a service. `loc start` exits as soon as the broker is up, so
it never answers the Windows service control manager, and `sc create` over it would fail. Save this
as `locutorium.xml`:

```xml
<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2"
  xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers>
    <LogonTrigger><Enabled>true</Enabled></LogonTrigger>
  </Triggers>
  <Actions>
    <Exec>
      <Command>loc.exe</Command>
      <Arguments>start</Arguments>
    </Exec>
  </Actions>
</Task>
```

The `UTF-16` declaration is unmeasured. The Task Scheduler exports the file that way, and nobody
here ran this sample. Make the declaration match the encoding you save the file in.

Register it with `schtasks /Create /TN Locutorium /XML locutorium.xml`.

## Exit codes

`0` done or nothing to do · `2` usage · `3` a dependency is missing (each is named with its
install line), or a `make build` that failed · `4` refusal — something is in the way that the
installer did not create.

## Uninstall

```
./install.sh --uninstall
```

Removes the links and the stamped copies. A link goes only if it points at what this clone would
have made; anything else is somebody's and is left alone, loudly. Prints, and does not run, the
command that would remove `~/.locutorium`. That directory holds your config and your store, and it
is yours to delete.

## Linux

The links work anywhere. `loc start` runs the broker inside `loc`, so no server package and no
supervisor are needed. The conformance suite is macOS-only for now, so the Go build is exercised
there, not here.
