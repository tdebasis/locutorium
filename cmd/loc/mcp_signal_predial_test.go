package main

// THE WINDOW BEFORE Serve IS ENTERED AT ALL.
//
// The suite next door holds the stretch between taking the seat and watching
// for the signal — inside Serve, after the handler is installed. This one is
// EARLIER THAN THAT, and outside the function that installs anything.
//
// mcpServe resolves the seat, reads the version and opens the provider before
// it hands anything to Serve, and every one of those can take time: an
// identity hook is a program a deployment wrote, and opening the medium is a
// dial. Until this was fixed, that whole stretch ran with Go's DEFAULT
// disposition for TERM in place — kill the program where it stands — so a
// runtime that ended its server a moment after launching it got a process
// killed by a signal. NOTHING IS STRANDED by it: there is no registration
// yet. What is wrong is the ENDING. `loc mcp` is a process a runtime starts
// and stops as a matter of course, and one that reports a failure for an
// ordinary stop teaches every supervisor above it that stopping this tool
// goes wrong.
//
// THE WINDOW IS HELD OPEN, NOT RACED FOR. A dead nats_url does not hold it:
// opening a provider does not dial — the nats provider connects lazily, on
// first use, which is already inside Serve — so there is no wait to be had
// that way, and a refused connection on loopback returns at once in any case.
// The identity hook is a program the startup runs and WAITS FOR, so a hook
// that sleeps holds the window for as long as it sleeps. It is held for five
// seconds and the signal is delivered half a second in, an order of magnitude
// inside it.
//
// THE HOLD IS THE SERVING CHILD'S ALONE. Everything else that asks who this
// seat is — the test process, the supervising parent on its way out, the
// provider resolving its credentials later on — is answered at once, so the
// only thing the sleep can delay is the stretch under test. The hook tells
// the two apart by its caller's arguments: only the server carries --serve.
//
// WHAT THIS CASE DOES NOT PROVE, said plainly. It holds the ending and it
// passed on the code BEFORE the handler was moved, so it is a regression
// guard and not the demonstration of the fix. A serving generation forked by
// a Go parent inherits SIGTERM HELD rather than defaulted, and the pending
// signal is delivered the moment anything notifies — so through this launch
// path the window was already survivable, and the delivery log said
// `goodbye ... signal terminated` for a signal sent five seconds before Serve
// was reached. The default disposition is what a serving generation launched
// by something that is NOT a Go program gets, and there the window was fatal:
// the same binary, started from a shell with the same held identity hook and
// signalled inside it, ended at 143 — killed where it stood — before the
// handler was moved, and stopped being killed after. That comparison lives in
// the commit message rather than here, because reproducing it needs a
// launcher this suite does not have.

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/tdebasis/locutorium/internal/loctest"
)

const (
	// predialHold is how long the identity hook keeps the startup inside the
	// window, and predialAt is how far into it the signal is delivered.
	predialHold = "5"
	predialAt   = 500 * time.Millisecond

	// predialExitWait bounds the ending, and is generous because much of what
	// follows the signal is deliberately slow: the rest of the hook's hold,
	// the three seconds the server waits for a client that never introduces
	// itself, and the two the departure is bounded at.
	predialExitWait = 25 * time.Second
)

func TestMCP_ASignalBeforeTheFirstDialIsAnOrdinaryEnding(t *testing.T) {
	bin := buildLoc(t)
	p := newPresence(t)

	// Reached only because the seat is launched with an EMPTY LOC_IDENTITY:
	// the env var is the first answer Identity looks for, and the hook is
	// what it falls back to.
	held := filepath.Join(p.home, "identity.held")
	hook := filepath.Join(p.home, "hooks", "identity")
	loctest.Write(t, hook, "#!/bin/sh\n"+
		"case \"$(ps -o args= -p $PPID 2>/dev/null)\" in\n"+
		"*--serve*) if [ ! -f "+held+" ]; then : > "+held+"; sleep "+predialHold+"; fi ;;\n"+
		"esac\n"+
		"printf '"+e1+"\\n'\n")
	if err := os.Chmod(hook, 0o700); err != nil {
		t.Fatalf("make the identity hook executable: %v", err)
	}

	cmd, wait, _ := startSeat(t, bin, p.home, "" /* no LOC_IDENTITY: the hook answers */)

	time.Sleep(predialAt)

	// A REAL GROUP SIGNAL, TO THE TREE. startSeat put the launched process in
	// its own group, so this reaches the launched process and the serving
	// child in one delivery — which is how an agent runtime's own exit
	// delivers one — without landing on the test runner. Delivering it to the
	// launched process alone would prove less: that one forwards, and a
	// forwarded copy arrives at the child a moment later than the runtime's
	// own would.
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("read the seat tree's process group: %v", err)
	}
	if pgid != cmd.Process.Pid {
		t.Fatalf("the launched process (pid %d) is in group %d and does not lead it; "+
			"a group signal from this case would reach processes this case did not start",
			cmd.Process.Pid, pgid)
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
		t.Fatalf("signal the seat's process group: %v", err)
	}

	select {
	case err := <-wait:
		// The window must actually have been entered, or the case proved
		// nothing: a hook that was never reached is a signal delivered to a
		// server that was already serving, which is the suite next door.
		if _, statErr := os.Stat(held); statErr != nil {
			t.Fatalf("the seat never reached its identity hook, so the window under test "+
				"was never entered; it ended with %v", err)
		}
		if err != nil {
			t.Fatalf("a SIGTERM delivered before the first dial ended the seat with %v — "+
				"a signal is an ordinary ending at every moment of this process's life, "+
				"and an ordinary ending is status 0", err)
		}
	case <-time.After(predialExitWait):
		t.Fatalf("the seat was still running %s after a SIGTERM delivered before the first dial",
			predialExitWait)
	}
}
