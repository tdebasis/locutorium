package main

import (
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tdebasis/locutorium/internal/loctest"
	model "github.com/tdebasis/locutorium/internal/presence"
)

// Shared support for the `mcp` cases. The presence deployment itself, the
// broker and the admin observer all come from newPresence (presence_test.go);
// what is added here is the three things only a seat's server needs — a nudge
// hook that records the bell, a config line the cases tune, and a built
// binary — plus the polling every case does instead of sleeping.

// The runtime the tests declare themselves as. Generic, like every other name
// in this suite: what is under test is that the handshake's name is recorded,
// not which runtime it was.
const (
	testClientName    = "acme-runtime"
	testClientVersion = "9.9.9"
)

// bellSpool makes this package's bell observable and, more importantly, keeps
// it away from the machine's real tmux. It puts a fake `tmux` first on PATH:
// the fake answers the two guard questions the notifier asks, and appends the
// typed line to a file. The file is the pane, as far as these cases are
// concerned.
//
// THE FAKE IS A SAFETY DEVICE BEFORE IT IS A FIXTURE. The `tmux` notifier
// types into whatever pane the address resolves to, and a developer running
// this suite has a real tmux server with real panes in it. A test that reached
// the real binary would be typing into somebody's session.
func bellSpool(t *testing.T, home string) string {
	t.Helper()
	spool := filepath.Join(home, "pane.log")
	dir := filepath.Join(home, "fakebin")
	loctest.Write(t, filepath.Join(dir, "tmux"), `#!/bin/sh
case "$1" in
  display) printf '0\n' ;;
  capture-pane) printf '⏵⏵ accept edits mode on\n❯ \n' ;;
  send-keys)
    for a in "$@"; do last="$a"; done
    [ "$last" = "Enter" ] || printf '%s\n' "$last" >> `+spool+`
    ;;
esac
exit 0
`)
	if err := os.Chmod(filepath.Join(dir, "tmux"), 0o700); err != nil {
		t.Fatalf("make the fake tmux executable: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return spool
}

// bells returns the bell lines the hook has recorded so far. The SENDER's own
// nudge lands in the same file — `send` rings the doorbell itself, and that is
// the shell delivery path doing its job — so the seat's own bell is picked out
// by the one thing that distinguishes it.
func bells(t *testing.T, spool string) []string {
	t.Helper()
	b, err := os.ReadFile(spool)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if strings.HasPrefix(line, "🔔") {
			out = append(out, line)
		}
	}
	return out
}

// tune appends one key to the deployment's config. Appending is enough: the
// format takes the LAST matching line, which is what config.Get implements.
func tune(t *testing.T, home, line string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(home, "config"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open the deployment config: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatalf("tune the deployment config: %v", err)
	}
}

// buildLoc builds the binary under test, or skips: the process boundary is the
// subject of the subprocess case, and a machine with no toolchain cannot have
// one.
func buildLoc(t *testing.T) string {
	t.Helper()
	goTool, err := osexec.LookPath("go")
	if err != nil {
		t.Skip("no 'go' on PATH: this case needs the real binary, because the process boundary is the subject")
	}
	bin := filepath.Join(t.TempDir(), "loc")
	// Stamped, as `make build` stamps it: the version is what the server
	// introduces itself as in the handshake, and an unstamped binary would
	// introduce itself as `dev`.
	out, err := osexec.Command(goTool, "build",
		"-ldflags", "-X main.buildVersion=0.0.0-test", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

// registration is the seat's registration, or nil.
func registration(t *testing.T, endpoint string) *model.Registration {
	t.Helper()
	reg, err := model.Load(endpoint)
	if err != nil {
		t.Fatalf("read the registration for %s: %v", endpoint, err)
	}
	return reg
}

// canned is one envelope with every field fixed, so that the same message
// seeded twice renders byte for byte the same both times — which is what lets
// a tool's text and the command line's be diffed at all.
func canned(id, from, to, body string) string {
	return fmt.Sprintf(`{"id":%q,"ts":"2026-01-14T09:00:00.000Z","from":%q,"to":%q,"kind":"msg","body":%q}`,
		id, from, to, body)
}
