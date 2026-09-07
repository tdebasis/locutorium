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
	"errors"
	"io"
	"os"
	osexec "os/exec"
	"os/signal"
	"path/filepath"
	"slices"
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

// ── the supervision, without a binary to re-execute ─────────────────────────
//
// The case above is the whole mechanism through a real process; these are the
// parts of it that a real process HIDES. Everything mcpParent does after the
// re-execution — waiting, and turning the child's ending into this process's
// exit code — runs in the launched binary, where no test in this package can
// see it, and the one thing that cannot be exercised in place is the
// re-execution itself. So the stand-in child's ending is chosen instead, and
// what is asserted is the exit code the runtime is handed.

// stubChild replaces the re-execution with a shell that ends how the case
// says. The real selfCommand is put back when the case finishes.
func stubChild(t *testing.T, script string) {
	t.Helper()
	prev := selfCommand
	selfCommand = func(int) (*osexec.Cmd, error) {
		return osexec.Command("sh", "-c", script), nil
	}
	t.Cleanup(func() { selfCommand = prev })
}

func TestMCP_TheLaunchedProcessExitsWithItsServersStatus(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		want         int
	}{
		// A seat given up cleanly is a clean ending for the runtime too.
		{"a clean ending", "exit 0", 0},
		// A refusal — a live seat held by somebody else — has already been
		// printed by the child on the shared stderr, and its code is carried
		// up unchanged so the runtime shows the server as failed.
		{"a refusal keeps its code", "exit 1", 1},
		{"any other code survives", "exit 3", 3},
		// A child killed outright has no exit code of its own. Something went
		// wrong, and inventing what is not this process's business.
		{"a killed server is 1", "kill -9 $$", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubChild(t, tc.script)
			var errOut strings.Builder
			if got := run([]string{"mcp", launchFlag}, io.Discard, &errOut); got != tc.want {
				t.Errorf("the launched process exited %d for a server that ended with %q; want %d",
					got, tc.script, tc.want)
			}
			// SAID ONCE. The child shares this process's stderr and has
			// already spoken in this tool's one error shape; a second `loc:`
			// line here would make one failure read as two.
			if errOut.Len() != 0 {
				t.Errorf("the launched process added %q to stderr; the server had already said "+
					"whatever there was to say, on this very stream", errOut.String())
			}
		})
	}
}

// A SERVER THAT NEVER STARTED IS THIS TOOL'S OWN FAILURE, and is reported in
// this tool's own shape: nothing has been written to stderr yet, because there
// was no child to write it.
func TestMCP_AServerThatCannotBeStartedIsReportedHere(t *testing.T) {
	prev := selfCommand
	selfCommand = func(int) (*osexec.Cmd, error) {
		return osexec.Command(filepath.Join(t.TempDir(), "no-such-loc")), nil
	}
	t.Cleanup(func() { selfCommand = prev })

	var errOut strings.Builder
	if got := run([]string{"mcp", launchFlag}, io.Discard, &errOut); got != 1 {
		t.Errorf("exit %d when the server could not be started; want 1", got)
	}
	if !strings.HasPrefix(errOut.String(), "loc: ") {
		t.Errorf("the failure is not in this tool's one error shape: %q", errOut.String())
	}
}

// A WAIT THAT FAILED FOR SOME OTHER REASON is not an exit code and must not be
// dressed up as one: it is passed along as the error it is, so it prints.
func TestMCP_AnEndingThatIsNotAnExitCodeIsPassedAlong(t *testing.T) {
	if err := childStatus(nil); err != nil {
		t.Errorf("a child that ended cleanly gave %v; a clean ending is no error at all", err)
	}
	odd := errors.New("waitpid: no child processes")
	if got := childStatus(odd); got != odd {
		t.Errorf("childStatus rewrote %v as %v; only an exit code becomes an exit code", odd, got)
	}
	// The carrier is still a legible error, for anything that ever prints one.
	if got, want := exitStatus(3).Error(), "exit status 3"; got != want {
		t.Errorf("the exit-status carrier reads %q; want %q", got, want)
	}
}

// WHICH INVOCATIONS ARE LAUNCHES. Only a bare `loc mcp` from a command line
// is one. Everything else — the re-execution's own arguments, every other
// verb, and the bare form a test drives in process — passes through and is
// served or dispatched as it stands.
func TestMCP_OnlyABareMcpInvocationIsALaunch(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		want []string
	}{
		{"a bare mcp is the one that re-executes", []string{"mcp"}, []string{"mcp", launchFlag}},
		{"the re-execution's own arguments are left alone",
			[]string{"mcp", serveFlag, pidFlag, "42"}, []string{"mcp", serveFlag, pidFlag, "42"}},
		{"another verb is not a launch", []string{"read"}, []string{"read"}},
		{"nothing at all is not a launch", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := launchArgs(tc.in); !slices.Equal(got, tc.want) {
				t.Errorf("launchArgs(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

// THE RE-EXECUTION NAMES THIS BINARY, THE SERVE FLAG AND THE RUNTIME'S PID.
// Nothing else can check this: in the running tool the command is handed
// straight to the operating system, and by the time anything could look at it
// the arguments are a different process's.
func TestMCP_TheReExecutionCarriesTheRuntimesPid(t *testing.T) {
	cmd, err := selfCommand(4242)
	if err != nil {
		t.Fatalf("build the re-execution: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("ask for this executable: %v", err)
	}
	if cmd.Path != self {
		t.Errorf("the re-execution runs %q; it must run this same binary, %q", cmd.Path, self)
	}
	if want := []string{self, "mcp", serveFlag, pidFlag, "4242"}; !slices.Equal(cmd.Args, want) {
		t.Errorf("the re-execution is %q; want %q — the runtime's pid is passed down "+
			"because the child's own parent is the wrapper and no longer the runtime", cmd.Args, want)
	}
}

// A PID THAT IS NOT A PID IS A TYPO, and a typo is the usage failure, not a
// server that registers something meaningless.
func TestMCP_TheServeFormRefusesAPidThatIsNotOne(t *testing.T) {
	for _, bad := range []string{"zero", "-1", "0", ""} {
		t.Run("--pid "+bad, func(t *testing.T) {
			var out strings.Builder
			if got := run([]string{"mcp", serveFlag, pidFlag, bad}, &out, io.Discard); got != 1 {
				t.Errorf("exit %d for --pid %q; a pid that is not a positive number is a usage failure", got, bad)
			}
			if !strings.Contains(out.String(), "usage: loc") {
				t.Errorf("--pid %q did not print the verb list; someone who has not got the "+
					"invocation right yet is reading, not scripting", bad)
			}
		})
	}
}

// A DELIVERED SIGNAL IS PASSED DOWN, NOT OBEYED. Exiting here on a TERM would
// orphan a server still holding the seat; the child is the one that knows how
// to give a seat up, so it is the one told.
//
// THE SIGNAL IS SENT TO THIS PROCESS, which is only safe because the case
// arms its own handler for the whole of its life FIRST: with a notification
// registered, the runtime's default disposition — terminate — is off, whether
// or not the code under test has reached its own Notify yet.
func TestMCP_ADeliveredSignalIsForwardedToTheServer(t *testing.T) {
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGHUP)
	t.Cleanup(func() { signal.Stop(guard) })

	// A stand-in server that survives until it is told, and reports being told
	// with an exit code nothing else produces.
	started := filepath.Join(t.TempDir(), "started")
	stubChild(t, "trap 'exit 7' HUP; : > "+started+"; while :; do sleep 0.05; done")

	done := make(chan int, 1)
	go func() { done <- run([]string{"mcp", launchFlag}, io.Discard, io.Discard) }()

	if !waitFor(5*time.Second, func() bool { _, err := os.Stat(started); return err == nil }) {
		t.Fatalf("the stand-in server never started")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("signal this process: %v", err)
	}

	select {
	case got := <-done:
		if got != 7 {
			t.Errorf("the launched process exited %d; the server exits 7 when it is signalled, "+
				"so anything else means the signal stopped here instead of going down", got)
		}
	case <-time.After(exitWait):
		t.Fatalf("the launched process was still waiting %s after the signal; "+
			"a signal it neither obeys nor forwards leaves the seat held by nobody", exitWait)
	}
}
