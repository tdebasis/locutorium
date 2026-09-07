package main

// A SECOND SIGNAL MUST NOT CUT THE GOODBYE SHORT.
//
// The suite next door proves that ONE signal is an ordinary ending: the seat
// is given up and the process exits cleanly. What a real agent runtime sends
// is not one signal. Observed under a runtime's own /exit, read from a seat's
// delivery log:
//
//	goodbye house.scribe: signal interrupt
//	supervisor house.scribe: child ended exit 0
//
// and nothing after it — no `left`, no `leave failed`, no `leave timed out`.
// The registration stayed behind naming a process that was gone, and the
// serving process's pid file was left where it lay.
//
// The reading is that the goodbye was interrupted by a SECOND signal. A
// runtime signals the whole process tree, so both generations are hit; the
// launched process forwards every signal it is handed down to its child on
// top of that; and the child, once its wait had returned, had already run
// `signal.Stop` and put the default disposition — terminate where you stand —
// back. Everything after that point (the goodbye line, the unregistration,
// the pid file) ran with no protection at all, and the next signal to arrive
// ended the process in the middle of it.
//
// So the promise this case holds is not "a signal ends the seat cleanly" but
// the stronger one the observation demands: ONCE THE END HAS BEEN DECIDED, NO
// FURTHER SIGNAL CAN STOP THE SEAT BEING GIVEN UP. Only SIGKILL still wins,
// and that is the documented fact the two-generation shape exists to survive
// (mcp_reexec_test.go).

import (
	"errors"
	"io"
	"os"
	osexec "os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMCP_ASecondSignalDoesNotCutTheGoodbyeShort(t *testing.T) {
	bin := buildLoc(t)
	p := newPresence(t)

	cmd, wait, _ := startSeat(t, bin, p.home, e1)
	if !waitFor(5*time.Second, func() bool { return statusSays(t, e1, "registered: yes") }) {
		t.Fatalf("the server never registered %s; status said:\n%s", e1, statusOf(t, e1))
	}
	serving := servingPID(t, p.home, e1)

	// ── the first signal: a real group signal, to the tree ──────────────────
	// startSeat put the launched process in its own group, so this is the
	// delivery a runtime makes and not an imitation of one: the launched
	// process and the serving child are both hit, and the launched process
	// forwards its copy downward as well.
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("read the seat tree's process group: %v", err)
	}
	if pgid != cmd.Process.Pid {
		t.Fatalf("the launched process (pid %d) is in group %d and does not lead it; "+
			"a group signal from this case would reach processes this case did not start",
			cmd.Process.Pid, pgid)
	}
	if err := syscall.Kill(-pgid, syscall.SIGINT); err != nil {
		t.Fatalf("signal the seat's process group: %v", err)
	}

	// ── and the ones after it ───────────────────────────────────────────────
	// A runtime's second signal arrives at a moment nobody chooses, so a case
	// that sent exactly one more would be asserting a race rather than the
	// promise. These keep arriving for as long as the serving process is
	// there to receive them: every instant of its departure is covered, and
	// the only way through is to ignore them all.
	hammered := make(chan struct{})
	go func() {
		defer close(hammered)
		deadline := time.Now().Add(exitWait)
		for time.Now().Before(deadline) && alive(serving) {
			_ = syscall.Kill(serving, syscall.SIGINT)
			time.Sleep(2 * time.Millisecond)
		}
	}()

	select {
	case <-wait:
	case <-time.After(exitWait):
		t.Fatalf("the seat's process tree was still running %s after the group signal", exitWait)
	}
	<-hammered

	// ── the seat is free, and says so through the verb an operator would use ─
	if !waitFor(exitWait, func() bool { return statusSays(t, e1, "registered: no") }) {
		t.Errorf("%s was still registered %s after the signals; status said:\n%s\n"+
			"a goodbye that a second signal can interrupt is not a goodbye",
			e1, exitWait, statusOf(t, e1))
	}
	if _, err := os.Stat(mcpPIDPath(p.home, e1)); !os.IsNotExist(err) {
		t.Errorf("the serving process left its pid file behind at %s; "+
			"a file that outlives the process it names sends the next reader to a stranger",
			mcpPIDPath(p.home, e1))
	}

	// ── and the log says the whole of what happened ─────────────────────────
	// The goodbye alone is what the broken shape already wrote. What was
	// missing, and what is asserted here, is the line AFTER it.
	log := deliveryLog(t, p.home, e1)
	goodbye := strings.Index(log, "goodbye "+e1+": signal interrupt")
	left := strings.Index(log, "left "+e1)
	if goodbye < 0 {
		t.Fatalf("the delivery log does not say the seat was signalled; it said:\n%s", log)
	}
	if left < 0 {
		t.Fatalf("the delivery log carries the goodbye and nothing after it; it said:\n%s\n"+
			"a seat that announced its departure and never departed is worse than one that said nothing", log)
	}
	if goodbye >= left {
		t.Errorf("the departure (%d) is written before the goodbye (%d); log:\n%s", left, goodbye, log)
	}
	// The supervisor's own line is the second half of the record, and it must
	// name the child's real ending: a child that gave the seat up and exited
	// on its own, not one that was cut down.
	if want := "supervisor " + e1 + ": child ended exit 0"; !strings.Contains(log, want) {
		t.Errorf("the supervisor did not record %q; the log said:\n%s", want, log)
	}
}

// A CHILD THAT DIED BY A SIGNAL IS NOT A CHILD THAT EXITED 0.
//
// The supervisor's line is the only record of how the serving generation
// ended, and it is read by whoever is working out why a seat was left behind.
// A line that reports every ending as a clean one makes that record worse than
// absent: it actively rules out the explanation that is true.
func TestMCP_TheSupervisorRecordsHowTheChildActuallyEnded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", e1)

	// A stand-in server that is killed outright, which is the one ending with
	// no exit code of its own to report.
	stubChild(t, "kill -9 $$")
	if got := run([]string{"mcp", launchFlag}, io.Discard, io.Discard); got != 1 {
		t.Fatalf("the launched process exited %d for a server that was killed; want 1", got)
	}

	log := deliveryLog(t, home, e1)
	if strings.Contains(log, "child ended exit 0") {
		t.Errorf("the supervisor recorded a clean exit for a child that was killed; log:\n%s\n"+
			"the one line that explains an abandoned seat must not report the ending that rules it out", log)
	}
	if want := "supervisor " + e1 + ": child ended signal killed"; !strings.Contains(log, want) {
		t.Errorf("the supervisor did not record %q; the log said:\n%s", want, log)
	}
}

// deliveryLog is the seat's delivery log as it stands, or "" if nothing has
// been written to it. Both generations append to this one file, which is what
// makes it readable as a single account of the ending.
func deliveryLog(t *testing.T, home, endpoint string) string {
	t.Helper()
	b, err := os.ReadFile(home + "/run/" + endpoint + ".delivery.log")
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read the delivery log for %s: %v", endpoint, err)
	}
	return string(b)
}

// THE ENDINGS ARE TOLD APART BY NAME. Every branch below was reachable in the
// running tool and none of them could be seen there: mcpParent writes the line
// inside the launched process, one generation away from any test.
func TestMCP_TheChildsEndingIsRenderedByItsKind(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"a clean ending", nil, "exit 0"},
		{"a code is the code", ended(t, "exit 3"), "exit 3"},
		{"a refusal keeps its code", ended(t, "exit 1"), "exit 1"},
		{"a killed child has no code, only a signal", ended(t, "kill -9 $$"), "signal killed"},
		{"a wait that failed for some other reason says so",
			errors.New("waitpid: no child processes"), "error: waitpid: no child processes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := childEnding(tc.err); got != tc.want {
				t.Errorf("childEnding = %q; want %q", got, tc.want)
			}
		})
	}
}

// ended runs a shell that ends as the script says and returns the wait's
// error, which is the only way to get a real ExitError: its wait status is
// filled in by the operating system and cannot be constructed by hand.
func ended(t *testing.T, script string) error {
	t.Helper()
	err := osexec.Command("sh", "-c", script).Run()
	if err == nil {
		t.Fatalf("the stand-in child ended cleanly for %q; the case needs its failure", script)
	}
	return err
}
