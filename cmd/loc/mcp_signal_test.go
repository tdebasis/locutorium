package main

// A SIGNAL IS AN ORDINARY ENDING, AND ORDINARY ENDINGS FREE THE SEAT.
//
// The subprocess suite next door covers the way a runtime usually lets go:
// it exits, the pipe reaches EOF, and the seat is free. This file covers the
// other way, the one a person or a supervisor causes — SIGTERM from a process
// manager, SIGINT from the keyboard. Neither can be observed in process: Go's
// default disposition for both is to kill the program where it stands, so a
// server that did not install a handler would die with its registration still
// in the registry and the seat would read as attended by a process that is
// gone. Only a real child taking a real signal can tell those two apart.
//
// What is asserted is the whole of the promise: the process ends CLEANLY —
// status 0, not "terminated by signal" — and by the time it has ended the
// seat is no longer registered. The second half is the point. Exiting fast is
// easy; exiting after unregistering is the behaviour the seat's occupant is
// entitled to.

import (
	"context"
	"os"
	osexec "os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// exitWait bounds the ending. It is generous next to the departure the server
// itself bounds at two seconds, and small enough that a server which ignored
// the signal and sat waiting on stdin fails here rather than hanging.
const exitWait = 3 * time.Second

func TestMCP_ASignalEndsTheSeatsAttendance(t *testing.T) {
	bin := buildLoc(t)

	for _, tc := range []struct {
		name string
		sig  syscall.Signal
	}{
		{"SIGTERM", syscall.SIGTERM},
		{"SIGINT", syscall.SIGINT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPresence(t)

			cmd, wait, _ := startSeat(t, bin, p.home, e1)

			// Registered as the runtime's seat, read through the same verb an
			// operator would use to ask.
			if !waitFor(5*time.Second, func() bool { return statusSays(t, e1, "registered: yes") }) {
				t.Fatalf("the server never registered %s; status said:\n%s", e1, statusOf(t, e1))
			}

			// TO THE CHILD, BY ITS OWN PROCESS HANDLE. Never to this test's
			// process: a signal delivered to the runner would take the suite
			// down and prove nothing about the server.
			if err := cmd.Process.Signal(tc.sig); err != nil {
				t.Fatalf("signal the seat's server with %v: %v", tc.name, err)
			}

			select {
			case err := <-wait:
				if err != nil {
					t.Fatalf("the server did not exit cleanly on %v: %v — "+
						"a signal is an ordinary ending, and an ordinary ending is status 0", tc.name, err)
				}
			case <-time.After(exitWait):
				t.Fatalf("the server was still running %s after %v; a seat whose server ignores "+
					"the signal that ends its runtime cannot be given up", exitWait, tc.name)
			}

			// The unregistration is not a race the caller has to wait out: it
			// runs before the process ends, so it has already happened by the
			// time the exit is observed.
			if got := statusOf(t, e1); !strings.Contains(got, "registered: no") {
				t.Errorf("after %v the server had gone but %s was still registered; status said:\n%s\n"+
					"a seat held by a process that no longer exists is a seat nobody can take",
					tc.name, e1, got)
			}
		})
	}
}

// startSeat launches `<bin> mcp` as a seat over real stdio pipes and speaks to
// it with a real MCP client, so the registration carries the runtime's name
// out of a real handshake.
//
// THE TEST OWNS THE PROCESS. The pipes are handed to the child as files and
// the transport is given only the reading and writing ends, so nothing but
// this function ever waits on the command — which is what lets a case make a
// claim about the exit status at all. The returned channel carries the result
// of that one wait.
//
// The third return is LETTING GO: closing this test's ends of both pipes, the
// way a runtime's exit closes its descriptors. It is separate from the cleanup
// so that a case can make the closing an event it observes rather than
// something that happens after it has stopped looking.
func startSeat(t *testing.T, bin, home, endpoint string) (*osexec.Cmd, <-chan error, func()) {
	t.Helper()

	// The child's stdout, which the client reads.
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("open the seat's stdout pipe: %v", err)
	}
	// The child's stdin, which the client writes. It stays open until the
	// signal arrives: closing it early would end the server by EOF, which is
	// the ending the other suite covers, not this one.
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("open the seat's stdin pipe: %v", err)
	}

	cmd := osexec.Command(bin, "mcp")
	cmd.Env = append(os.Environ(), "LOC_HOME="+home, "LOC_IDENTITY="+endpoint)
	cmd.Stdin = inR
	cmd.Stdout = outW
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the seat's server: %v", err)
	}
	// Our copies of the ends the child now holds.
	_ = outW.Close()
	_ = inR.Close()

	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()

	var once sync.Once
	letGo := func() { once.Do(func() { _ = outR.Close(); _ = inW.Close() }) }

	t.Cleanup(func() {
		// Only ever this child, and only by its own handle. A case that
		// already saw it exit finds nothing to kill.
		_ = cmd.Process.Kill()
		letGo()
	})

	client := mcp.NewClient(&mcp.Implementation{Name: testClientName, Version: testClientVersion}, nil)
	if _, err := client.Connect(context.Background(),
		&mcp.IOTransport{Reader: outR, Writer: inW}, nil); err != nil {
		t.Fatalf("connect to the seat's server: %v", err)
	}
	return cmd, wait, letGo
}

// statusOf is what `loc status <endpoint>` prints, run in this process.
func statusOf(t *testing.T, endpoint string) string {
	t.Helper()
	code, out, errOut := exec("status", endpoint)
	if code != 0 {
		t.Fatalf("status %s exited %d: %s", endpoint, code, errOut)
	}
	return out
}

// statusSays reports whether the status report carries a line.
func statusSays(t *testing.T, endpoint, want string) bool {
	t.Helper()
	return strings.Contains(statusOf(t, endpoint), want)
}
