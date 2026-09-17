package main

// THE SENDER ONLY QUEUES.
//
// A send puts the message in the recipient's queue and does nothing else
// (R36, 2026-09-10). The seat's own server is the one thing that notifies,
// through the notifier its registered type names, so a sender never types into
// a pane and never spawns a courier.
//
// Five cases used to live here, each asserting that `send` rang a doorbell for
// a recipient with no serving process. The behaviour they described is gone.
// What survives them is here: the line a send prints, which the frozen
// conformance suite pins, and the claim underneath the whole arrangement —
// a message sent to a seat with nobody home is in the queue, and a read finds
// it. Neither depends on who rings.

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	model "github.com/tdebasis/locutorium/internal/presence"
)

// THE SHIPPED LINE, AND IT IS THE SAME LINE FOR EVERY RECIPIENT. `send` used
// to print a second form for a seat whose server would ring instead. There is
// no second form now, because there is no second bell to explain.
func TestSend_PutsTheMessageInTheQueueAndPrintsTheShippedLine(t *testing.T) {
	for _, c := range []struct {
		name    string
		serving bool
	}{
		{"a live registration with no serving process", false},
		{"a seat whose serving pid file is present", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newPresence(t)
			p.as(t, "host")
			subscribeSeat(t, e1, os.Getpid(), "claude", testClientVersion)
			if c.serving {
				plantServingPID(t, p.home, e1, os.Getpid())
			}

			out := sendAs(t, "host", e1, "the clerk has a question")
			if want := "sent → queue." + e1 + " uid="; !strings.HasPrefix(out, want) || !strings.HasSuffix(out, "\n") {
				t.Errorf("send printed %q; the shipped line starts %q and carries the uid, "+
					"and it does not vary with what is running at the far end", out, want)
			}

			// AND THE MESSAGE IS THERE. A send that printed the right line and
			// queued nothing would pass the assertion above and lose the mail.
			if got := readAs(t, e1); !strings.Contains(got, "the clerk has a question") {
				t.Errorf("a read at %s did not find the message; it said:\n%s", e1, got)
			}
		})
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

// plantServingPID plants the file a running server would have written for a
// LIVE process, which is the shape a serving seat has.
func plantServingPID(t *testing.T, home, endpoint string, pid int) {
	t.Helper()
	writeServingPID(t, home, endpoint, pid, model.StartedAt(pid))
}

// readAs reads one seat's mail and returns what the verb printed.
func readAs(t *testing.T, endpoint string) string {
	t.Helper()
	t.Setenv("LOC_IDENTITY", endpoint)
	code, out, errOut := exec("read")
	if code != 0 {
		t.Fatalf("read at %s: exit %d: %s", endpoint, code, errOut)
	}
	return out
}

// sendAs delivers one message as the named identity — the SUPERVISOR here,
// which is how a message reaches a seat in this deployment — and returns what
// the verb printed.
func sendAs(t *testing.T, identity, to, body string) string {
	t.Helper()
	t.Setenv("LOC_IDENTITY", identity)
	code, out, errOut := exec("send", to, body)
	if code != 0 {
		t.Fatalf("send to %s: exit %d: %s", to, code, errOut)
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
