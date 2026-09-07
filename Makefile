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

.PHONY: build test clean

# Default: the thing this file is for.
build:
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
