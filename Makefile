# This file exists for exactly one artifact: a stamped `loc` binary under
# build/bin.
#
# THE NUMBER COMES FROM `git describe`, NOT FROM A FILE. The tag on the commit
# is the number. `git describe` reads it here and the value is stamped into the
# binary at link time, so `loc version` answers from any directory. A tagged
# commit gives a bare number. Any other commit carries `-N-g<sha>`. An unclean
# tree adds `-dirty`.
#
# build/ is gitignored: binaries are artifacts, not source.

VERSION := $(shell git describe --tags --dirty --always 2>/dev/null | sed 's/^v//')
GO      ?= go
BIN     := build/bin/loc

.PHONY: build test clean clean-check

# Default: the thing this file is for. It refuses to build a tree that carries
# this machine's private vocabulary — see clean-check.
build: clean-check
	$(GO) build -trimpath -ldflags "-X main.buildVersion=$(VERSION)" -o $(BIN) ./cmd/loc

# The Go unit tests, and the shell tests that stand beside them. The
# conformance suite itself is the gate for both implementations and is run by
# conformance/run.sh, not from here — but the two files below are tests OF that
# suite's own machinery (its teardown, and the pre-run cleanup CI does on a
# persistent runner), and they neither boot a server nor lay a deployment, so
# they belong where `make test` will actually run them.
test:
	$(GO) test -count=1 ./...
	bash conformance/teardown_test.sh
	bash conformance/leftovers_test.sh

clean:
	rm -rf build

# THE PRIVATE-VOCABULARY CHECK ALWAYS RUNS, AND THE SCRIPT DECIDES WHAT IT CAN
# MEASURE. The forbidden list is deployment data: a maintainer's names, paths
# and deployment words, kept under $LOC_HOME and never in the tree. The script
# already handles an absent list correctly — it falls back to a generic list
# that holds a maintainer's absolute home path, which is wrong for everybody,
# and it says on stderr that it is doing so.
#
# THIS TARGET USED TO SKIP THE CHECK WHEN NO LIST WAS READABLE. A contributor
# has no list, so `make build` on their machine ran no cleanliness check at all,
# not even the generic one the script promises for exactly that case. The
# script's promise was true of the script and false at the entry point most
# people use, and the normal case for everyone but the maintainer was the
# skipped one.
#
# The FAILURE is passed through on purpose: an `|| true` here would swallow a
# real hit, and a guard that cannot fail is decoration.
#
# The exit CODE is not, and that is make's doing rather than a choice here.
# `measured:` with a control — a recipe that merely runs `exit 1` also makes
# make exit 2 — so make collapses any recipe failure into its own 2, and the
# script's 1 (a word was found) cannot be told from its 2 (nothing could be
# measured) at this entry point. Read the script's own output, or run it
# directly, to tell them apart.
clean-check:
	@bash conformance/check-clean.sh
