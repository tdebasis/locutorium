# The Go build. There is nothing to build for the shell tool — the clone is its
# installation — so this file exists for exactly one artifact: a stamped `loc`
# binary under build/bin.
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

# The Go unit tests. The conformance suite is the gate for both implementations
# and is run by conformance/run.sh, not from here.
test:
	$(GO) test -count=1 ./...

clean:
	rm -rf build
