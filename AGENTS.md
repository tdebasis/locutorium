# AGENTS.md — for coding agents working in this repository

This is the Locutorium: a message bus for agents on one machine. `loc` is `cmd/loc` with
`internal/`, made by `make build`, and it is gated by the conformance suite. A bash implementation
lived beside it until 2026-09-07 and was deleted: two implementations meant a layer between them,
and that layer is where the defects were. To **use** the bus as an endpoint, read `docs/AGENTS.md`.
To **deploy** it, read
`docs/OPERATORS.md`. This file is for changing the code.

## Hard rules

1. **bash 3.2.** Every script must run on the bash that ships with macOS. No associative arrays,
   no `mapfile`, no `${var,,}`. This binds the installer and the suite. The `go.mod` file binds the
   Go tree instead.
2. **The suite is the definition.** `conformance/run.sh` must be green after every change to `cmd/`
   or `internal/`; a provider is a Locutorium provider iff the suite passes against it
   (`CONTRACT.md`). `LOC_BIN_DIR` says which `loc` to drive, defaulting to `build/bin`. A seat's
   listener is the `mcp` verb — a stdio MCP server the runtime launches — which the suite does not
   speak; those cases are skipped **by name**, counted in the tally, and each names where the same
   property IS proven: `cmd/loc/mcp_test.go`,
   `cmd/loc/mcp_bell_test.go` and `internal/mcpserve` instead, because the suite speaks no MCP. A
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

**The tool.** `cmd/loc` the verbs and the dispatch · `internal/loc`, `internal/config`,
`internal/presence` the model · `internal/provider` + `internal/provider/nats` the adapter ·
`internal/mcpserve` the `mcp` verb — this build's listener, which is a stdio MCP server the agent
runtime launches rather than a background process (`docs/CLI.md` §mcp) ·
`Makefile` the one command that builds it, stamping `VERSION` in at link time · `build/` its
output, gitignored, because a binary is an artifact and not source.

**Around it.** `install.sh` (copies the stamped binary and links `loc` at it) ·
`conformance/run.sh` the suite · `conformance/check-clean.sh` ·
`conformance/check-version.sh` (asks the binary its version) · `VERSION` · `docs/` · `RELEASE.md`.
The broker runs inside the binary. The repository holds no provider script and no service file.
