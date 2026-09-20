# Contributing to the Locutorium

This file states what makes a change acceptable here. It does not explain forking or git. GitHub
already requires a fork from anyone without write access, and that path needs no tutorial.

## Where to start

Read the [issue list](https://github.com/tdebasis/locutorium/issues) before you write code. Open an
issue and discuss a large change before you start it. The maintainer may decline a design on
architecture grounds, and an issue catches that before the work exists.

## What the project wants

The open issue list is the current list of wanted work. If your idea is not there, open an issue and
discuss it before you write code.

## The rules a change must keep

Each rule below lives in one document. Read the source; this file does not restate any of them.

- **The conformance suite is the contract.** README.md §Status states this. `docs/CONTRACT.md`
  §Providers defines a provider as one the suite passes against.
- **`docs/PROTOCOL.md` §8 governs whether a change is breaking.** Read it before you change the wire
  shape.
- **`conformance/check-clean.sh` must pass.** `AGENTS.md` rule 3 states what it forbids.
- **`AGENTS.md`** states the house's style and the rules a change must keep. Read it in full before
  you write code.
- **Open an issue before large work.** See "Where to start" above.

## Development setup

Build with `make build`. It writes a stamped binary to `build/bin/loc`. `Makefile` states what each
target does and why.

Run `make test` for the Go unit tests and the suite's own machinery tests. These tests start their
own scratch servers. A guard in `internal/provider/nats/guard.go` refuses the default broker address
under `go test`, so a test run cannot reach a daemon running on the same machine. A change that
removes or weakens that guard removes that protection.

`conformance/run.sh` is the suite. `docs/INSTALL.md` §Dependencies lists what it needs: the `nats`
CLI, `jq`, and BSD `sed`; that section also states that the suite is macOS-only for now. The suite
needs no live deployment. It starts its own scratch server on a random port and tears it down when
it finishes.

CI runs four jobs on every pull request (`.github/workflows/checks.yml`):

- `scope` — decides whether the change touches product code, so a docs-only change skips
  `conformance`.
- `hygiene` — runs `conformance/check-clean.sh` and `conformance/check-scratch-only.sh`.
- `go` — builds, vets, and tests the Go tree, and checks the stamped binary.
- `conformance` — runs the suite, only when `scope` says the change touches product code.

`hygiene` and `go` run on every pull request; only `conformance` depends on `scope`.

## Pull request titles

A pull request title is typed: `feat:`, `fix:`, `docs:`, and so on. `RELEASE.md` §PR titles states
the exact form. This repository squash-merges every pull request, and the squash commit takes the
pull request title; that is how the title reaches the release tool. `RELEASE.md` §Merges states this.
A plain-sentence title releases nothing.

## What a version number promises

`docs/PROTOCOL.md` §8 states what a major or minor move means on the wire. `RELEASE.md` §What the
number means states what the number promises today.
