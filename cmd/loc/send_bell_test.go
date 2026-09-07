package main

// ONE MESSAGE, ONE BELL.
//
// Two parties can ring a pane. `send` rings the deployment's nudge hook the
// moment the message is on the medium, which is the shell-era delivery path
// doing its job; and a seat that runs its own MCP server rings from the other
// end, when the arrival lands on the queue it is watching. Observed in the
// pane: both rang for one message, and the seat's occupant read two lines and
// had one message.
//
// The rule is decided by the recipient, and by ONE FACT ABOUT IT: is a serving
// process running for that seat. The serving child writes
// `run/<endpoint>.mcp.pid` when it takes the seat and removes it when it gives
// it up (internal/mcpserve), so that file, and nothing else, is what says a
// bell-ringer exists. A registration says something different — that a host
// registered a seat, and which runtime is at it — and a registration can be
// perfectly alive with nobody to ring: a seat registered by hand, a suite that
// registers its own pid, a seat whose server crashed while the runtime it
// named lives on. Suppressing on the registration silences all three.
//
// EACH CASE RUNS WITH NO SERVER AT ALL. That is what makes the claims
// assertable: with the seat's own bell absent, anything that reaches the spool
// can only have come from `send`, and the pid file is planted by hand to stand
// for the server the cases deliberately do not run.

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	model "github.com/tdebasis/locutorium/internal/presence"
)

// The conformance suite's shape, and the one the registration predicate got
// wrong: its seats are registered by `subscribe` with the suite's own pid, and
// no MCP server runs anywhere. Registered, alive, and nobody home.
func TestSend_ALiveRegistrationWithNoServingProcessIsStillRung(t *testing.T) {
	p := newPresence(t)
	spool := nudgeSpool(t, p.home)

	// Registered with THIS process's pid, which is alive by construction, and
	// no pid file: exactly a seat a host registered for a runtime that never
	// started a server, or whose server has gone.
	p.as(t, "host")
	subscribeSeat(t, e1, os.Getpid(), testClientName, testClientVersion)
	if reg := registration(t, e1); reg == nil || !reg.Alive() {
		t.Fatalf("the planted registration for %s does not read as alive; "+
			"the case is about a LIVE registration with no server, so there is nothing to test without one", e1)
	}
	if _, err := os.Stat(servingPIDPath(p.home, e1)); err == nil {
		t.Fatalf("a serving pid file exists at %s; this case is the one where none does",
			servingPIDPath(p.home, e1))
	}

	out := sendAs(t, "host", e1, "the clerk has a question")

	// THE RINGING PATH'S LINE IS UNCHANGED. It is the shipped one, pinned by
	// the frozen suite; only the path that stays quiet has something new to say.
	if want := "sent → queue." + e1 + "\n"; out != want {
		t.Errorf("send printed %q; when it rings the doorbell itself the line is %q, "+
			"exactly as it has always been", out, want)
	}

	got := rings(t, spool)
	if len(got) != 1 {
		t.Fatalf("send rang the pane %d time(s) for a registered seat with no serving process: %q\n"+
			"a registration says a host registered this seat, not that a bell-ringer is running; "+
			"with no server there is nobody to ring but the sender", len(got), got)
	}
	if want := "[LOC] 1 new → loc read"; got[0] != want {
		t.Errorf("send rang %q; it rings %q — the line names the CLI verb because a seat "+
			"without a server is a seat that reads with the command line", got[0], want)
	}
}

func TestSend_ASeatWithALiveServingProcessIsNotRungBySend(t *testing.T) {
	p := newPresence(t)
	spool := nudgeSpool(t, p.home)

	p.as(t, "host")
	subscribeSeat(t, e1, os.Getpid(), testClientName, testClientVersion)
	// THIS process stands in for the serving child, which is what the file
	// names in a real deployment. Planted rather than run, because a case that
	// started a real server would have that server's own bell in the spool and
	// could no longer tell whose line it was reading.
	writeServingPID(t, p.home, e1, os.Getpid(), model.StartedAt(os.Getpid()))

	out := sendAs(t, "host", e1, "the clerk has a question")

	// A SKIPPED BELL IS NEVER SILENT. The sender chose not to ring, and the
	// only person who can tell whether that choice was right is reading this
	// line; a quiet pane and a quiet stdout together are indistinguishable
	// from a bell that was supposed to ring and did not.
	if want := "sent → queue." + e1 + " (the seat's server rings)\n"; out != want {
		t.Errorf("send printed %q; when it skips its own ring the line says whose bell it is: %q",
			out, want)
	}

	if got := rings(t, spool); len(got) != 0 {
		t.Errorf("send rang the pane %d time(s) for a seat whose serving process is running: %q\n"+
			"that server is watching this very queue and will ring for the same message — "+
			"coalesced and breaker-capped, which the sender cannot do. "+
			"Two lines for one message is one line too many.", len(got), got)
	}
}

func TestSend_AStaleServingPIDFileIsRungBySend(t *testing.T) {
	p := newPresence(t)
	spool := nudgeSpool(t, p.home)

	p.as(t, "host")
	subscribeSeat(t, e1, os.Getpid(), testClientName, testClientVersion)
	// A file left behind by a server that died without removing it, and the
	// number it holds has since been handed to something else. THIS IS THE
	// REUSE HAZARD, written down as a case: the pid exists, so a kill(0) says
	// "alive" and the bell would be swallowed by a stranger. The start time is
	// what tells the two processes apart, so the file names a live pid — this
	// very process — with a start time that is not its own.
	writeServingPID(t, p.home, e1, os.Getpid(), "1999-01-01T00:00:00Z")

	_ = sendAs(t, "host", e1, "the clerk has a question")

	if got := rings(t, spool); len(got) != 1 {
		t.Errorf("send rang the pane %d time(s) for a pid file whose start time is not the "+
			"running process's: %q\n"+
			"the pid alone is not the identity — a recycled number would silence the bell "+
			"for a seat with no server at all, and a message nobody was told about is "+
			"worse than a line somebody scrolls past", len(got), got)
	}
}

// The other stale shape, kept because it is the ordinary one: the process is
// simply gone.
func TestSend_ADepartedServingProcessIsRungBySend(t *testing.T) {
	p := newPresence(t)
	spool := nudgeSpool(t, p.home)

	p.as(t, "host")
	subscribeSeat(t, e1, os.Getpid(), testClientName, testClientVersion)
	gone := departedPID(t)
	writeServingPID(t, p.home, e1, gone, "1999-01-01T00:00:00Z")

	_ = sendAs(t, "host", e1, "the clerk has a question")

	if got := rings(t, spool); len(got) != 1 {
		t.Errorf("send rang the pane %d time(s) for a pid file naming a departed process: %q",
			len(got), got)
	}
}

func TestSend_ASeatWithNoLiveRegistrationIsRungBySend(t *testing.T) {
	p := newPresence(t)
	spool := nudgeSpool(t, p.home)

	// A registered seat whose PROCESS IS GONE — a crashed runtime, or a
	// shell-era seat a sweep has not reached. It is registered, so it is not
	// the absence of a registration being tested; it is the absence of anyone
	// to ring.
	p.as(t, "host")
	subscribeSeat(t, e2, departedPID(t), "shell-listener", "1.0.0")
	if reg := registration(t, e2); reg == nil || reg.Alive() {
		t.Fatalf("the planted registration for %s reads as alive; "+
			"the case needs a seat whose process is gone", e2)
	}

	_ = sendAs(t, "host", e2, "a question for the clerk")

	got := rings(t, spool)
	if len(got) != 1 {
		t.Fatalf("send rang the pane %d time(s) for a seat with no live server: %q\n"+
			"with no server of its own to ring for it, the sender is the only bell there is", len(got), got)
	}
	if want := "[LOC] 1 new → loc read"; got[0] != want {
		t.Errorf("send rang %q; it rings %q — the line names the CLI verb because a seat "+
			"without a server is a seat that reads with the command line", got[0], want)
	}
}

// subscribeSeat plants one registration through the verb that makes them, so
// the queue the send needs exists for the same reason the registration does.
func subscribeSeat(t *testing.T, endpoint string, pid int, agentType, version string) {
	t.Helper()
	code, _, errOut := exec("subscribe", endpoint, "--pid", strconv.Itoa(pid),
		"--type", agentType, "--version", version)
	if code != 0 {
		t.Fatalf("plant the registration for %s: exit %d: %s", endpoint, code, errOut)
	}
}

// servingPIDPath is where the serving child records itself, in the deployment's
// own layout (internal/mcpserve). Spelled out here rather than imported so a
// change to that path breaks these cases loudly instead of silently agreeing
// with itself.
func servingPIDPath(home, endpoint string) string {
	return filepath.Join(home, "run", endpoint+".mcp.pid")
}

// writeServingPID plants the file a running server would have written: the pid
// on the first line and the START TIME the operating system gives for it on the
// second, which together are the identity — a pid alone begins naming a
// stranger the moment the kernel hands the number to somebody else.
func writeServingPID(t *testing.T, home, endpoint string, pid int, started string) {
	t.Helper()
	path := servingPIDPath(home, endpoint)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("make %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"+started+"\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// sendAs delivers one message as the named identity — the SUPERVISOR here,
// which is how a message reaches a seat in this deployment — and returns what
// the verb printed, because A SKIPPED BELL IS SAID OUT LOUD: the sender that
// stays quiet in the pane does not also stay quiet on its own stdout.
func sendAs(t *testing.T, identity, to, body string) string {
	t.Helper()
	t.Setenv("LOC_IDENTITY", identity)
	code, out, errOut := exec("send", to, body)
	if code != 0 {
		t.Fatalf("send to %s: exit %d: %s", to, code, errOut)
	}
	return out
}

// rings is EVERY line the nudge hook recorded, unlike bells, which keeps only
// the seat server's. What is being counted here is how many times the pane was
// spoken to at all, so a filter would hide the very thing under test.
func rings(t *testing.T, spool string) []string {
	t.Helper()
	b, err := os.ReadFile(spool)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// departedPID is a process id that named something and no longer does. It is
// started and reaped here rather than invented, because a number picked out of
// the air might belong to a running process and the case would then be testing
// the opposite of what it says.
func departedPID(t *testing.T) int {
	t.Helper()
	c := osexec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatalf("start the stand-in process: %v", err)
	}
	pid := c.Process.Pid
	_ = c.Process.Kill()
	_, _ = c.Process.Wait()
	return pid
}
