package main

// A SIGNAL IS AN ORDINARY ENDING AT EVERY MOMENT OF THIS PROCESS'S LIFE, and
// the stretch before the first dial is the one that is easiest to leave on Go's
// default disposition by accident. mcpServe installs the handler at the top and
// hands the channel to Serve, so a signal that lands in that stretch is held
// and taken when the server looks. This case delivers one there.
//
// HOW THE WINDOW IS HELD OPEN. It used to be held by a slow identity hook,
// which R29 removed. The seam beforeDial replaces it: mcpDeps calls it once,
// immediately before it opens the provider, and this case blocks in it until
// the signal has been delivered.

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// predialExitWait bounds the ending. It is generous because much of what
// follows the signal is deliberately slow: the three seconds the server waits
// for a client that never introduces itself, and the two the departure is
// bounded at.
const predialExitWait = 25 * time.Second

func TestMCP_ASignalBeforeTheFirstDialIsAnOrdinaryEnding(t *testing.T) {
	p := newPresence(t)
	p.as(t, e1)
	stampVersion(t, "1.4.2")

	entered := make(chan struct{})
	released := make(chan struct{})
	restore := beforeDial
	beforeDial = func() {
		close(entered)
		<-released
	}
	t.Cleanup(func() { beforeDial = restore })

	done := make(chan error, 1)
	go func() { done <- mcpServe(os.Getpid()) }()

	// The window must actually be entered, or the case proves nothing: a seam
	// that did not hold is a signal delivered to a server that was already
	// serving, which is the suite next door.
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		close(released)
		select {
		case err := <-done:
			t.Fatalf("the startup never reached beforeDial: it ended first with %v", err)
		default:
			t.Fatalf("the startup never reached beforeDial, so the window under test was never entered")
		}
	}

	// TO THIS PROCESS, because the server under test is in it. mcpServe
	// installed the handler before it reached the seam, so the signal is held
	// in its buffered channel rather than ending the test binary. If that
	// handler is ever installed after the seam instead, this signal ends the
	// whole test binary rather than failing this case, so a package that dies
	// on SIGTERM should be read as this case's regression.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signal this process: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // let the notification land in the channel
	close(released)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a SIGTERM delivered before the first dial ended the seat with %v — "+
				"a signal is an ordinary ending at every moment of this process's life", err)
		}
	case <-time.After(predialExitWait):
		t.Fatalf("the seat was still running %s after a SIGTERM delivered before the first dial",
			predialExitWait)
	}

	if reg := registration(t, e1); reg != nil {
		t.Errorf("%s is still registered after the seat ended; a signal caught before the "+
			"first dial must still give the seat up", e1)
	}
}
