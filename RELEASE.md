# Releasing

One version, stated once in `VERSION`, agreed everywhere. `conformance/check-version.sh` enforces
that `loc version` prints it, that every `vX.Y.Z` in the docs equals it, and that a release tag on
HEAD equals it.

1. `conformance/run.sh` green, twice — and green again under `LOC_IMPL=go` with `LOC_BIN_DIR`
   pointing at `build/bin`, since the suite is the gate for both implementations.
2. Bump `VERSION` (plain `X.Y.Z`, no `v`).
3. `make build`. THE BINARY IS STAMPED, so it is a copy of a moment: `-ldflags -X` writes the
   number from `VERSION` into it at link time, and a binary built before the bump goes on saying
   the old number however current the tree is. Rebuild after the bump, never before, or the release
   ships an artifact that disagrees with the tag on it.
4. `conformance/check-version.sh` — clean. It asks the shell tool AND the built binary, so this is
   where a stale stamp is caught rather than shipped.
5. `git commit -am "release: vX.Y.Z"`
6. `git tag -a vX.Y.Z -m vX.Y.Z`
7. `git push origin main --follow-tags`
8. A GitHub release entry: deferred until the repository is public.
9. The README's conformance badge says `passing` and carries no count. It used to
   name a number, which meant a fact in the documentation that nothing enforced and
   only a person could keep true — the very problem `check-version.sh` exists to
   solve for the version. It went stale the first time a case was added.

What a number means (`docs/PROTOCOL.md` §8): additive envelope fields are minor; changed meanings are
major; `kind` registry changes are minor. CLI-only changes that keep the envelope follow the same
rule — a verb that changes meaning is major.

Current: **v0.1.0** — the first tagged version: the CLI, the NATS provider, the conformance suite,
the installer, and these documents. `build/` holds no releasable state — it is remade by `make
build` from whatever is checked out, which is what makes the rebuild in step 3 the whole of the
artifact's provenance.
