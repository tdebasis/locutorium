# Releasing

One version, stated once in `VERSION`, agreed everywhere. `conformance/check-version.sh` enforces
that `loc version` prints it, that every `vX.Y.Z` in the docs equals it, and that a release tag on
HEAD equals it.

1. `conformance/run.sh` green, twice.
2. Bump `VERSION` (plain `X.Y.Z`, no `v`).
3. `conformance/check-version.sh` — clean.
4. `git commit -am "release: vX.Y.Z"`
5. `git tag -a vX.Y.Z -m vX.Y.Z`
6. `git push origin main --follow-tags`
7. A GitHub release entry: deferred until the repository is public.

What a number means (`docs/PROTOCOL.md` §8): additive envelope fields are minor; changed meanings are
major; `kind` registry changes are minor. CLI-only changes that keep the envelope follow the same
rule — a verb that changes meaning is major.

Current: **v0.1.0** — the first tagged version: the CLI, the NATS provider, the conformance suite,
the installer, and these documents.
