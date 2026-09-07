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
// The rule is decided by the recipient, not by the sender's mood: a seat that
// holds a LIVE presence registration has a server of its own, so the sender
// stays quiet and lets it ring — coalesced across the wake window and capped
// by the breaker, neither of which the sender can do. A seat with no live
// registration has nobody to ring for it, so the sender is the only bell there
// is and rings exactly as it always has.
//
// EACH CASE RUNS WITH NO SERVER AT ALL. That is what makes the first claim
// assertable: with the seat's own bell absent, anything that reaches the spool
// can only have come from `send`.

import (
	"os"
	osexec "os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestSend_ASeatWithALiveServerIsNotRungBySend(t *testing.T) {
	p := newPresence(t)
	spool := nudgeSpool(t, p.home)

	// Registered with THIS process's pid, which is alive by construction: the
	// test is the thing standing in for the seat's runtime.
	p.as(t, "host")
	subscribeSeat(t, e1, os.Getpid(), testClientName, testClientVersion)
	if reg := registration(t, e1); reg == nil || !reg.Alive() {
		t.Fatalf("the planted registration for %s does not read as alive; "+
			"the case is about a seat that HAS a server, so there is nothing to test without one", e1)
	}

	sendAs(t, "host", e1, "the clerk has a question")

	if got := rings(t, spool); len(got) != 0 {
		t.Errorf("send rang the pane %d time(s) for a seat that holds a live registration: %q\n"+
			"that seat runs its own server, which is watching this very queue and will ring for "+
			"the same message — coalesced and breaker-capped, which the sender cannot do. "+
			"Two lines for one message is one line too many.", len(got), got)
	}
}

func TestSend_ASeatWithNoLiveServerIsRungBySend(t *testing.T) {
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

	sendAs(t, "host", e2, "a question for the clerk")

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

// sendAs delivers one message as the named identity — the SUPERVISOR here,
// which is how a message reaches a seat in this deployment.
func sendAs(t *testing.T, identity, to, body string) {
	t.Helper()
	t.Setenv("LOC_IDENTITY", identity)
	code, _, errOut := exec("send", to, body)
	if code != 0 {
		t.Fatalf("send to %s: exit %d: %s", to, code, errOut)
	}
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
