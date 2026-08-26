# AGENTS.md — for coding agents working in this repository

This is the Locutorium: a bash CLI (`bin/loc`) and one provider (`lib/providers/nats.sh`) that
together make a message bus for agents on one machine. To **use** the bus as an endpoint, read
`docs/AGENTS.md`. To **deploy** it, read `docs/OPERATORS.md`. This file is for changing the code.

## Hard rules

1. **bash 3.2.** Every script must run on the bash that ships with macOS. No associative arrays,
   no `mapfile`, no `${var,,}`.
2. **The suite is the definition.** `conformance/run.sh` must be green after every change to `bin/`
   or `lib/`; a provider is a Locutorium provider iff the suite passes against it (`CONTRACT.md`).
3. **Written for strangers.** `conformance/check-clean.sh` forbids deployment vocabulary, personal
   identifiers and absolute home paths in every tracked file. Use `$HOME`, `$LOC_HOME`, and generic
   endpoint names (alice, bob, carol).
4. **Tests never touch a real deployment.** The suite boots its own server on a random port with a
   `mktemp` `LOC_HOME`. Keep it that way.

## Files

`bin/loc` entry point · `lib/core.sh` the verbs and the delivery machinery · `lib/providers/nats.sh`
the NATS provider (`provider_*`) · `providers/nats/bootstrap.sh` first deployment ·
`providers/nats/make-contexts.sh` CLI contexts · `providers/nats/service.sh` + `launchd/` the
LaunchAgent · `install.sh` · `conformance/run.sh` the suite · `conformance/check-clean.sh` ·
`conformance/check-version.sh` · `VERSION` · `docs/` · `RELEASE.md`.
