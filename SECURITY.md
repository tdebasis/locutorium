# Security Policy

## Reporting a vulnerability

Report a vulnerability through GitHub's private vulnerability reporting. Open the
repository's Security tab. Select "Report a vulnerability". Do not open a public
issue for a security problem.

## What to expect

This is a single-maintainer project. Expect an acknowledgement within 7 days.
This file promises nothing beyond that acknowledgement.

## Supported versions

This project is at version 0.x. Only the latest release gets a fix.

## Threat model for v0

The broker listens on loopback only. `loc start` refuses any other listen
address and says why (`docs/CLI.md` §start). The broker binds loopback, and
nothing on the bus leaves the machine, as a design position for this version
(`README.md`, the "Nothing leaves the machine" point).

The broker authenticates nobody in this version (`docs/CLI.md` §start,
`README.md`, the "Nothing leaves the machine" point). An identity comes from
the `LOC_IDENTITY` environment variable and from nothing else
(`docs/OPERATORS.md` §"The bell — how a seat is told"). The system stamps the
`from` field from that value (`docs/PROTOCOL.md` §"1. Identity — who a message
is from"). Because this version authenticates nobody, nothing verifies which
process set it.

A local process on the same machine can set `LOC_IDENTITY` to any name it
chooses, then send or publish under that name. The same process can also read
under that name. A read consumes: it takes the messages out of that identity's
queue, exactly once (`docs/CLI.md` §read), so they never reach the seat they
were addressed to. The day's record does not show who did it: each message
taken from a queue writes a line with the message's `uid` and the endpoint
name (`docs/CLI.md` §read), so a read under a borrowed name is recorded under
that name, exactly as a read by the seat itself would be. The record shows
that a read happened and under which name; with no authentication, a name is
not evidence of who acted. Authentication is what would constrain both
directions, and v0 has none. This is a stated design position for v0. It is
not a vulnerability to report.

### In scope

Report a finding in any of these:

- A path that reaches the broker from off the machine.
- A path that crosses the loopback boundary.
- A write outside `$LOC_HOME`. The tool's own state lives under this
  directory: `config`, `run/`, `store/`, and `forbidden`
  (`docs/OPERATORS.md` §"What lives under `$LOC_HOME`").

### Out of scope

Do not report this. It is the stated v0 design, not a defect:

- An unauthenticated local process can publish as any identity.
- An unauthenticated local process can read as any identity, and that read
  consumes the mail.
