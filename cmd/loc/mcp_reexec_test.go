package main

// A HARD KILL MUST NOT BE ABLE TO TAKE THE GOODBYE WITH IT.
//
// The suites next door cover the two endings a server can hear: the pipe
// reaches EOF, or a signal arrives. This one covers the ending it CANNOT
// hear. An agent runtime is free to end its MCP child with SIGKILL, and a
// process that is SIGKILLed runs no handler, closes nothing and says nothing —
// so a server that was itself the process the runtime launched would leave its
// registration behind every time, and the seat would read as attended by a
// process that is gone until the next server displaced it or a sweep ran.
//
// The shape that survives it is one generation removed: the process the
// runtime launches only re-executes itself and waits, and the process that
// SERVES is its child, holding the same three descriptors. Killing the parent
// is then not an ending at all — the child still holds the runtime's stdin, so
// it keeps the seat and keeps answering — and the goodbye happens on the one
// signal a kill cannot suppress, the runtime's own descriptors closing.
//
// What is asserted here is exactly that: kill the launched process outright,
// with the pipes untouched, and the seat is STILL held and STILL rings; close
// the pipes, and the seat is given up and the serving process is gone.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMCP_AHardKillOfTheLaunchedProcessDoesNotSilenceTheGoodbye(t *testing.T) {
	bin := buildLoc(t)
	p := newPresence(t)
	tune(t, p.home, "wake_window_seconds = 1")
	spool := nudgeSpool(t, p.home)

	cmd, wait, letGo := startSeat(t, bin, p.home, e1)

	if !waitFor(5*time.Second, func() bool { return statusSays(t, e1, "registered: yes") }) {
		t.Fatalf("the server never registered %s; status said:\n%s", e1, statusOf(t, e1))
	}

	// THE PID TRAVELS DOWN. The registration names the RUNTIME — this test
	// process — and not the process it launched, and not the one serving.
	// Under a re-exec that pid is no longer anybody's os.Getppid(): it is
	// handed to the serving generation explicitly, and this is the assertion
	// that it was handed down correctly.
	if got := registration(t, e1).Process.PID; got != os.Getpid() {
		t.Fatalf("registered pid %d; the runtime that launched this seat is %d — "+
			"a pid that stops at the process in between records the wrapper, not the agent",
			got, os.Getpid())
	}

	// The serving process names itself, because nothing else can: the
	// registration carries the runtime's pid, and the launched process is
	// about to stop existing.
	serving := servingPID(t, p.home, e1)
	if serving == cmd.Process.Pid {
		t.Fatalf("the process the runtime launched (pid %d) is the one serving the seat; "+
			"a runtime that ends it with SIGKILL takes the seat's goodbye with it", serving)
	}

	// ── the hard kill: no signal is delivered, and nothing is closed ────────
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill the launched process: %v", err)
	}
	select {
	case <-wait:
	case <-time.After(exitWait):
		t.Fatalf("the launched process was still running %s after SIGKILL", exitWait)
	}

	// The seat is still held — by a process the runtime never sees, holding
	// the descriptors the runtime still has open.
	if got := statusOf(t, e1); !strings.Contains(got, "registered: yes") {
		t.Fatalf("SIGKILL of the launched process gave up the seat; status said:\n%s\n"+
			"the runtime has not let go: its descriptors are still open", got)
	}
	if !alive(serving) {
		t.Fatalf("the serving process (pid %d) died with the process that launched it; "+
			"a child that cannot outlive its parent's kill has nothing to say goodbye with", serving)
	}

	// ── and still answering: one send, one bell ────────────────────────────
	// Sent by the SUPERVISOR, which is how a message reaches a seat here.
	p.as(t, "host")
	if code := run([]string{"send", e1, "the clerk has a question"}, os.Stdout, os.Stderr); code != 0 {
		t.Fatalf("send to the seat whose launcher was killed failed with %d", code)
	}
	if !waitFor(6*time.Second, func() bool { return len(bells(t, spool)) == 1 }) {
		t.Fatalf("one send rang %d bells: %q — a seat that survived the kill still rings, once",
			len(bells(t, spool)), bells(t, spool))
	}

	// ── the runtime lets go: the descriptors close, and THAT is the goodbye ─
	letGo()
	if !waitFor(2*time.Second, func() bool { return statusSays(t, e1, "registered: no") }) {
		t.Errorf("two seconds after the runtime's descriptors closed, %s was still registered; "+
			"status said:\n%s\nthe pipe closing is the one ending a kill cannot suppress",
			e1, statusOf(t, e1))
	}
	if !waitFor(2*time.Second, func() bool { return !alive(serving) }) {
		t.Errorf("the serving process (pid %d) was still running two seconds after EOF; "+
			"a server that keeps the pipe open after its runtime has gone is a process nobody owns", serving)
	}
	if _, err := os.Stat(mcpPIDPath(p.home, e1)); !os.IsNotExist(err) {
		t.Errorf("the serving process left its pid file behind at %s; "+
			"a file that outlives the process it names sends the next reader to a stranger",
			mcpPIDPath(p.home, e1))
	}
}

// mcpPIDPath is where the SERVING generation records itself. It is not the
// registration: the registration answers "who is at this seat", which is the
// runtime, and this answers "which process is serving it", which under the
// re-exec is neither the runtime nor the process the runtime launched.
func mcpPIDPath(home, endpoint string) string {
	return filepath.Join(home, "run", endpoint+".mcp.pid")
}

// servingPID reads that file, or fails saying what its absence means.
func servingPID(t *testing.T, home, endpoint string) int {
	t.Helper()
	path := mcpPIDPath(home, endpoint)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the serving process's pid at %s: %v — "+
			"the registration carries the RUNTIME's pid, so without this file "+
			"nothing can say which process is actually serving the seat", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("%s held %q, which is not a pid", path, strings.TrimSpace(string(b)))
	}
	return pid
}

// alive reports whether a pid still names a running process. Signal 0 is
// delivered to nothing and answers exactly that question.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }
