package main

// THE WINDOW BETWEEN TAKING THE SEAT AND WATCHING FOR THE SIGNAL.
//
// The suite next door signals a server that is already serving, which is the
// easy half: by then the handler is installed and the signal is an ordinary
// ending. This case covers the stretch BEFORE that — the seat is registered,
// the queue is claimed, the first bell is being rung — during which the
// process had, until this was fixed, no handler at all. Go's default
// disposition for TERM is to kill the program where it stands, so a signal
// landing in that stretch killed a server that had ALREADY registered, and
// the seat was left in the registry naming a runtime that had gone.
//
// The stretch is held open on purpose rather than raced for: the deployment's
// nudge hook says when it has started and then sleeps, so the signal is
// delivered at a moment known to be inside the window instead of at a guessed
// number of milliseconds after launch.
//
// What is asserted is what the other suite asserts, for a signal that arrives
// earlier: the process ends CLEANLY, status 0, and the seat is no longer
// registered by the time it has. The registration is not abandoned half-made
// — it completes, and then the server departs through the one goodbye path
// there is.

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tdebasis/locutorium/internal/loctest"
)

// windowExitWait bounds the ending. It is the two seconds the held-open hook
// spends ringing plus room for the departure the server itself bounds at two.
const windowExitWait = 4 * time.Second

func TestMCP_ASignalDuringRegistrationIsCaughtNotObeyed(t *testing.T) {
	bin := buildLoc(t)
	p := newPresence(t)

	// A pane that says when the bell reached it and then HOLDS it. The first
	// ring is synchronous inside the server's startup, so a pane that answers
	// slowly keeps the window open for as long as it takes.
	started := filepath.Join(p.home, "nudge.started")
	dir := filepath.Join(p.home, "fakebin")
	loctest.Write(t, filepath.Join(dir, "tmux"), `#!/bin/sh
case "$1" in
  display) printf '0\n' ;;
  capture-pane) printf '⏵⏵ accept edits mode on\n❯ \n' ;;
  send-keys)
    for a in "$@"; do last="$a"; done
    if [ "$last" != "Enter" ]; then printf 'ringing\n' > `+started+`; sleep 2; fi
    ;;
esac
exit 0
`)
	if err := os.Chmod(filepath.Join(dir, "tmux"), 0o700); err != nil {
		t.Fatalf("make the fake tmux executable: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// One message already waiting, which is what makes the first ring happen
	// at all: the backlog is rung for at once, without the coalescing window.
	p.seedQueue(t, q1, "queue."+e1)
	if err := p.admin.Publish("queue."+e1, []byte(canned("m1", e2, e1, "waiting"))); err != nil {
		t.Fatalf("seed the seat's queue: %v", err)
	}
	if err := p.admin.Flush(); err != nil {
		t.Fatalf("flush the seed: %v", err)
	}

	p.as(t, e1)
	cmd, wait, _ := startSeat(t, bin, p.home, e1)

	// Inside the window by construction: the hook only runs once the seat has
	// been registered, and it has not returned yet.
	if !waitFor(10*time.Second, func() bool { _, err := os.Stat(started); return err == nil }) {
		t.Fatalf("the seat never reached its first bell, so the window under test was never entered; status said:\n%s",
			statusOf(t, e1))
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal the seat's server: %v", err)
	}

	select {
	case err := <-wait:
		if err != nil {
			t.Fatalf("a SIGTERM during registration did not end the server cleanly: %v — "+
				"a signal caught before the server is serving must lead to the same goodbye "+
				"as one caught after it", err)
		}
	case <-time.After(windowExitWait):
		t.Fatalf("the server was still running %s after a SIGTERM delivered during registration", windowExitWait)
	}

	if got := statusOf(t, e1); !strings.Contains(got, "registered: no") {
		t.Errorf("the server had gone but %s was still registered; status said:\n%s\n"+
			"a signal that lands between taking the seat and watching for signals must "+
			"still give the seat up", e1, got)
	}
}
