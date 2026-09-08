# This file exists for exactly one artifact: a stamped `loc` binary under
# build/bin.
#
# VERSION IS THE SOURCE OF TRUTH AND IT IS STATED ONCE. The number is not copied
# into the Go tree; it is read from the file here and stamped into the binary at
# link time, which is what lets `loc version` answer from a directory that has no
# VERSION above it. Without the stamp the binary walks up from wherever it lives
# looking for the file, which works from the clone and nowhere else.
#
# build/ is gitignored: binaries are artifacts, not source.

VERSION := $(shell tr -d '[:space:]' < VERSION)
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

# THE PRIVATE-VOCABULARY CHECK RUNS WHERE THE LIST LIVES, AND NOWHERE ELSE.
# The forbidden list is deployment data: a maintainer's names, paths and
# deployment words, kept under $LOC_HOME and never in the tree. If it is
# present on this machine, a build runs the check and a hit fails the build. If
# it is absent (CI, a contributor's clone), this is one line and the build goes
# on. The check's exit code is passed through on purpose: an `|| true` here
# would swallow a real hit along with the absent-file case, and a guard that
# cannot fail is decoration.
clean-check:
	@f="$${LOC_FORBIDDEN_FILE:-$${LOC_HOME:-$$HOME/.locutorium}/forbidden}"; \
	if [ -r "$$f" ]; then bash conformance/check-clean.sh; \
	else echo "check-clean: no private list on this machine; skipped"; fi
