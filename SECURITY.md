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
address and says why (`docs/CLI.md:62-63`). The broker binds loopback, and
nothing on the bus leaves the machine, as a design position for this version
(`README.md:94`).

The broker authenticates nobody in this version (`docs/CLI.md:63`,
`README.md:94`). An identity comes from the `LOC_IDENTITY` environment
variable and from nothing else (`docs/OPERATORS.md:135`). The system stamps
the `from` field from that value. It never checks who set the variable
(`docs/PROTOCOL.md:10-11`).

A local process on the same machine can set `LOC_IDENTITY` to any name it
chooses, then send or publish under that name. This is a stated design
position for v0. It is not a vulnerability to report.

### In scope

Report a finding in any of these:

- A path that reaches the broker from off the machine.
- A path that crosses the loopback boundary.
- A write outside `$LOC_HOME`. The tool's own state lives under this
  directory: `config`, `run/`, `store/`, and `forbidden`
  (`docs/OPERATORS.md:7-15`).

### Out of scope

Do not report this. It is the stated v0 design, not a defect:

- An unauthenticated local process can publish as any identity.
