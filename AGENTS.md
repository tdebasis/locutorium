# AGENTS.md — for coding agents working in this repository

This is the Locutorium: a message bus for agents on one machine, in **two implementations** of the
same command reference. The shell tool is `bin/loc` with one provider (`lib/providers/nats.sh`);
the Go build is `cmd/loc` with `internal/`, made by `make build`. Both are gated by the same
conformance suite. To **use** the bus as an endpoint, read `docs/AGENTS.md`. To **deploy** it, read
`docs/OPERATORS.md`. This file is for changing the code.

## Hard rules

1. **bash 3.2.** Every script must run on the bash that ships with macOS. No associative arrays,
   no `mapfile`, no `${var,,}`. This binds the shell tool, the installer and the suite; the Go tree
   is bound by `go.mod` instead.
2. **The suite is the definition.** `conformance/run.sh` must be green after every change to `bin/`,
   `lib/`, `cmd/` or `internal/`; a provider is a Locutorium provider iff the suite passes against
   it (`CONTRACT.md`). It is ONE file run against both implementations: `LOC_BIN_DIR` says which
   `loc` to drive, `LOC_IMPL` (`shell` by default, `go`) says which implementation is being driven.
   Under `go` the listener's cases — everything reached through `sub`, `unsub` and `doctor`, which
   that build names and does not implement — are skipped **by name** and counted in the tally. A
   case that is skipped without appearing in the output is a case nobody knows was not run, so
   A case may be skipped only where the two implementations differ on purpose, and then only by name, with its reason printed, and counted.
3. **Written for strangers.** `conformance/check-clean.sh` forbids deployment vocabulary, personal
   identifiers and absolute home paths anywhere in the working tree. The word list is deployment
   data, not source: it lives in `$LOC_HOME/forbidden` (override with `LOC_FORBIDDEN_FILE`), so the
   tree never carries the words it exists to keep out. With no list installed the check falls back
   to a generic one — absolute home paths only — and says so. Use `$HOME`, `$LOC_HOME`, and generic
   endpoint names (alice, bob, carol).
4. **Tests never touch a real deployment.** The suite boots its own server on a random port with a
   `mktemp` `LOC_HOME`. Keep it that way.

## Files

**The shell tool.** `bin/loc` entry point · `lib/core.sh` the verbs and the delivery machinery ·
`lib/providers/nats.sh` the NATS provider (`provider_*`).

**The Go build.** `cmd/loc` the verbs and the dispatch · `internal/loc`, `internal/config`,
`internal/presence` the model · `internal/provider` + `internal/provider/nats` the adapter ·
`Makefile` the one command that builds it, stamping `VERSION` in at link time · `build/` its
output, gitignored, because a binary is an artifact and not source.

**Both.** `providers/nats/bootstrap.sh` first deployment · `providers/nats/make-contexts.sh` CLI
contexts · `providers/nats/service.sh` + `launchd/` the LaunchAgent · `install.sh` (`--go` links the
binary beside the shell tool) · `conformance/run.sh` the suite · `conformance/check-clean.sh` ·
`conformance/check-version.sh` (asks both tools their version) · `VERSION` · `docs/` · `RELEASE.md`.
