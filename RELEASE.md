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
5. `git switch -c release/vX.Y.Z`, then `git commit -am "release: vX.Y.Z"`. The default branch is
   protected, so a direct push to it is refused and a release lands the way every other change
   does: as a pull request.
6. Push the branch, open the pull request, and let its checks run. `hygiene` runs
   `check-version.sh`, and `go` remakes the stamped binary and asks it for its number from an
   unrelated directory, so the stale stamp of step 3 is caught there as well as in step 4. The
   conformance job does NOT run on a release pull request — a version bump touches no product
   file, and the scope job is written to skip the suite when none are touched. Step 1 is where
   the suite is green for this release. It is a precondition, not a formality.
7. Merge on green. **THE TAG COMES AFTER THE MERGE, NEVER BEFORE.** Linear history is required, so
   the merge rebases or squashes the commit and what lands on the default branch has a different
   hash from the commit that was reviewed. A tag made on the release branch names a commit that
   never arrives — and `check-version.sh` cannot catch that, because it compares only a tag that
   is on HEAD, and that tag would not be.
8. Take the merged commit from the remote, check it there, and tag it:

   ```
   git switch main
   git pull --ff-only
   conformance/check-version.sh          # the merged tree
   git tag -a vX.Y.Z -m vX.Y.Z
   conformance/check-version.sh          # again — THIS is the run that sees the tag
   git push origin vX.Y.Z
   ```

   **It is run twice on purpose.** The tag comparison inside `check-version.sh` lives in a branch
   that executes only when HEAD is already tagged — `git describe --exact-match` fails otherwise
   and the whole comparison is skipped. Run before the tag exists, the script checks `VERSION`,
   both tools and the documents, says everything agrees, and never looks at the one thing this
   step is here to get right. The second run is where a mistyped tag is caught, and it catches it
   before the push rather than after.

   The tag is pushed ALONE, by name. `--follow-tags` would carry the branch in the same push, the
   branch half is refused by the protection and the tag half is not, and what a server does with a
   push whose refs disagree is its property and not this document's. Nothing needs pushing about
   the branch in any case — the merge already put the commit there.
9. A GitHub release entry: deferred until the repository is public.
10. The README's conformance badge says `passing` and carries no count. It used to
   name a number, which meant a fact in the documentation that nothing enforced and
   only a person could keep true — the very problem `check-version.sh` exists to
   solve for the version. It went stale the first time a case was added.

What a number means (`docs/PROTOCOL.md` §8): additive envelope fields are minor; changed meanings are
major; `kind` registry changes are minor. CLI-only changes that keep the envelope follow the same
rule — a verb that changes meaning is major.

Current: **v0.1.1** — the Go build reads, ships stamped beside the shell tool (`install.sh --go`), and
is proven by the identical conformance suite; the suite tears down on a cancelled run. The first tagged
version was the CLI, the NATS provider, the conformance suite, the installer, and these
documents. `build/` holds no releasable state — it is remade by `make build` from whatever is checked
out, which is what makes the rebuild in step 3 the whole of the artifact's provenance.
