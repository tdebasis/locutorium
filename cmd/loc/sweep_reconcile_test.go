package main

// The falsifiers for #27: the sweep reconciles three records, and `status`
// reports what the next beat would do without doing any of it.
//
// Every case here drives run() end-to-end against the embedded broker from
// internal/loctest, and asserts effects through the INDEPENDENT admin
// connection, never through the code under test.

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ledgerPath is where a presence deployment keeps one seat's row.
func (p *presence) ledgerPath(endpoint string) string {
	return filepath.Join(p.home, "run", "presence", endpoint+".json")
}

// serverPIDPath is the file the process serving a seat writes its own pid to.
func (p *presence) serverPIDPath(endpoint string) string {
	return filepath.Join(p.home, "run", endpoint+".mcp.pid")
}

// dropQueue destroys a seat's queue behind the tool's back, as a broker
// restart or an operator with nats-cli would.
func (p *presence) dropQueue(t *testing.T, stream string) {
	t.Helper()
	if err := p.adminJS.DeleteStream(stream); err != nil {
		t.Fatalf("drop %s: %v", stream, err)
	}
}

// subscribeLive registers a seat whose process really is running: this test
// process, whose pid and start time the operating system will confirm.
func (p *presence) subscribeLive(t *testing.T, endpoint string) {
	t.Helper()
	p.as(t, "host")
	if code, _, errOut := exec("subscribe", endpoint, "--pid", alivePid(),
		"--type", "tmux", "--version", "3.2.0"); code != 0 {
		t.Fatalf("subscribe %s exited %d (stderr %q)", endpoint, code, errOut)
	}
}

// ------------------------------------------------------ arm 1: the argument

// The sweep takes no argument. It reconciles every row on this machine, so an
// instance name is a caller asking for something the verb no longer offers.
func TestFalsifierSweepRefusesAnArgument(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")

	code, out, errOut := exec("sweep", "workshop")
	t.Logf("exit=%d stdout=%q stderr=%q", code, out, errOut)
	if code != 1 {
		t.Errorf("`loc sweep workshop` exited %d, want 1 (errUsage)", code)
	}
	if !strings.Contains(out, "sweep") {
		t.Errorf("stdout %q does not carry the verb list a usage error prints", out)
	}
}

// ------------------------------------------------- arm 2: the missing queue

// A live seat whose queue was destroyed behind the tool's back gets it back,
// and mail reaches the seat through it afterwards.
func TestFalsifierSweepRecreatesAMissingQueue(t *testing.T) {
	p := newPresence(t)
	p.subscribeLive(t, e1)
	p.dropQueue(t, q1)
	if p.qexists(q1) {
		t.Fatal("the queue was not actually dropped; the arm proves nothing")
	}

	p.as(t, "host")
	code, out, errOut := exec("sweep")
	t.Logf("sweep exit=%d stdout=%q stderr=%q", code, out, errOut)
	if code != 0 {
		t.Fatalf("sweep exited %d (stderr %q)", code, errOut)
	}
	if !p.qexists(q1) {
		t.Fatal("the sweep left the live seat with no queue")
	}

	if code, _, errOut := exec("send", e1, "reconciled"); code != 0 {
		t.Fatalf("send exited %d (stderr %q)", code, errOut)
	}
	p.as(t, e1)
	code, out, errOut = exec("read")
	t.Logf("read exit=%d stdout=%q stderr=%q", code, out, errOut)
	if code != 0 || !strings.Contains(out, "reconciled") {
		t.Errorf("the message did not read back: exit %d, stdout %q", code, out)
	}
}

// ---------------------------------------------------- arm 3: the dead process

// A seat whose process is gone loses all three records: the row, the queue and
// the server pidfile.
func TestFalsifierSweepReapsAllThreeRecords(t *testing.T) {
	p := newPresence(t)

	child := osexec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatalf("start the seat's process: %v", err)
	}
	pid := child.Process.Pid

	p.as(t, "host")
	if code, _, errOut := exec("subscribe", e1, "--pid", strconv.Itoa(pid),
		"--type", "tmux", "--version", "3.2.0"); code != 0 {
		t.Fatalf("subscribe exited %d (stderr %q)", code, errOut)
	}
	// The server that took the seat wrote its own pid beside the row.
	writeFile(t, p.serverPIDPath(e1), strconv.Itoa(pid)+"\n")

	if err := child.Process.Kill(); err != nil {
		t.Fatalf("kill the seat's process: %v", err)
	}
	_ = child.Wait()

	code, out, errOut := exec("sweep")
	t.Logf("sweep exit=%d stdout=%q stderr=%q", code, out, errOut)
	if code != 0 {
		t.Fatalf("sweep exited %d (stderr %q)", code, errOut)
	}
	if _, err := os.Stat(p.ledgerPath(e1)); !os.IsNotExist(err) {
		t.Error("the row survived the reap")
	}
	if p.qexists(q1) {
		t.Error("the queue survived the reap")
	}
	if _, err := os.Stat(p.serverPIDPath(e1)); !os.IsNotExist(err) {
		t.Error("the server pidfile survived the reap")
	}
}

// -------------------------------------------------- arm 4: the unreadable row

// A row the ledger cannot read stops the orphan pass entirely, because that
// row may be the one holding a queue the pass would otherwise destroy.
func TestFalsifierUnreadableRowStopsTheOrphanPass(t *testing.T) {
	p := newPresence(t)
	p.subscribeLive(t, e1)
	p.subscribeLive(t, e2)
	// e2's row becomes unreadable while its queue stays. Read as a listing
	// that skips it, e2's queue is an orphan and the orphan pass destroys it.
	writeFile(t, p.ledgerPath(e2), "{not json at all")

	p.as(t, "host")
	code, out, errOut := exec("sweep")
	t.Logf("sweep exit=%d stdout=%q stderr=%q", code, out, errOut)
	if code == 0 {
		t.Error("a sweep that could not read a row exited 0")
	}
	if !strings.Contains(out, e2) {
		t.Errorf("stdout %q does not name the unreadable row", out)
	}
	if !p.qexists(q2) {
		t.Error("the orphan pass destroyed the queue of the seat it could not read")
	}
	if !p.qexists(q1) {
		t.Error("the live seat lost its queue")
	}
}

// ----------------------------------------------------- arm 6: the daemon line

// `loc status` says whether the daemon is running before it says anything
// about the seats. Neither daemon file exists here.
func TestFalsifierStatusSaysTheDaemonIsNotRunning(t *testing.T) {
	p := newPresence(t)
	p.subscribeLive(t, e1)

	p.as(t, "host")
	code, out, errOut := exec("status")
	t.Logf("status exit=%d stdout=%q stderr=%q", code, out, errOut)
	if code != 0 {
		t.Fatalf("status exited %d (stderr %q)", code, errOut)
	}
	first := strings.SplitN(out, "\n", 2)[0]
	if first != "daemon: not running" {
		t.Errorf("first line = %q, want %q", first, "daemon: not running")
	}
}

// ------------------------------------------------- arm 7: status changes nothing

// `loc status` reports the repair the next beat would make, and makes none of
// it. Run twice, the queue is still missing.
func TestFalsifierStatusReportsButRepairsNothing(t *testing.T) {
	p := newPresence(t)
	p.subscribeLive(t, e1)
	p.dropQueue(t, q1)

	p.as(t, "host")
	code, out, errOut := exec("status")
	t.Logf("status exit=%d stdout=%q stderr=%q", code, out, errOut)
	if code != 0 {
		t.Fatalf("status exited %d (stderr %q)", code, errOut)
	}
	want := e1 + ": queue missing, next beat repairs it"
	if !strings.Contains(out, want) {
		t.Errorf("stdout %q does not carry %q", out, want)
	}
	if _, out2, _ := exec("status"); !strings.Contains(out2, want) {
		t.Errorf("the second status changed the answer: %q", out2)
	}
	if p.qexists(q1) {
		t.Error("status repaired the queue; it must touch nothing")
	}
}

// ------------------------------------------------ arm 8: status reads the ledger

// The unread report iterates the ledger. No `endpoints` file exists any more,
// so the ledger is the only roster there is, and every registered seat must
// still be reported from it.
func TestFalsifierStatusReadsTheLedger(t *testing.T) {
	p := newPresence(t)
	p.subscribeLive(t, e1)
	p.subscribeLive(t, e2)
	if _, err := os.Stat(filepath.Join(p.home, "endpoints")); !os.IsNotExist(err) {
		t.Fatalf("the deployment has an endpoints file; V0 writes none: %v", err)
	}

	p.as(t, "host")
	code, out, errOut := exec("status")
	t.Logf("status exit=%d stdout=%q stderr=%q", code, out, errOut)
	if code != 0 {
		t.Fatalf("status exited %d (stderr %q)", code, errOut)
	}
	for _, e := range []string{e1, e2} {
		if !strings.Contains(out, e+"  unread:") && !strings.Contains(out, e) {
			t.Errorf("stdout %q omits the registered seat %s", out, e)
		}
	}
	if strings.Count(out, "unread:") != 2 {
		t.Errorf("stdout %q does not carry one unread count per ledger seat", out)
	}
}

// ------------------------------------------------- arm 5: the write windows

// A beat that lands inside subscribe's write window leaves the seat WHOLE.
//
// Two writes make a subscribe and a beat can land between them. With the queue
// created first, the gap is a queue with no row, which the orphan pass
// destroys: the seat finishes subscribing and is registered and unreachable
// until a later beat rebuilds it. With the row written first, the gap is a row
// with no queue, which the repair pass fills.
func TestFalsifierABeatInsideSubscribesWindow(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")

	restore := betweenSubscribeWrites
	t.Cleanup(func() { betweenSubscribeWrites = restore })
	var swept bool
	betweenSubscribeWrites = func() {
		swept = true
		betweenSubscribeWrites = func() {} // the beat itself subscribes nothing
		code, out, errOut := exec("sweep")
		t.Logf("beat inside the window: exit=%d stdout=%q stderr=%q", code, out, errOut)
	}

	code, _, errOut := exec("subscribe", e1, "--pid", alivePid(), "--type", "tmux", "--version", "3.2.0")
	if code != 0 {
		t.Fatalf("subscribe exited %d (stderr %q)", code, errOut)
	}
	if !swept {
		t.Fatal("no beat landed inside the window; the arm proves nothing")
	}
	if _, err := os.Stat(p.ledgerPath(e1)); err != nil {
		t.Error("the seat has no row after subscribing")
	}
	if !p.qexists(q1) {
		t.Error("the seat has no queue after subscribing: a beat inside the window destroyed it")
	}
}

// A beat inside unsubscribe's window leaves the seat GONE. The row is removed
// before the beat lands, and it is the only row, so the ledger holds no row
// for the instance and the beat does nothing. The verb then deletes the queue
// itself. What this arm proves is that the beat does not undo the removal.
func TestFalsifierABeatInsideUnsubscribesWindow(t *testing.T) {
	p := newPresence(t)
	p.subscribeLive(t, e1)

	restore := betweenUnsubscribeWrites
	t.Cleanup(func() { betweenUnsubscribeWrites = restore })
	var swept bool
	betweenUnsubscribeWrites = func() {
		swept = true
		betweenUnsubscribeWrites = func() {}
		code, out, errOut := exec("sweep")
		t.Logf("beat inside the window: exit=%d stdout=%q stderr=%q", code, out, errOut)
	}

	p.as(t, "host")
	code, _, errOut := exec("unsubscribe", e1, "--force")
	if code != 0 {
		t.Fatalf("unsubscribe exited %d (stderr %q)", code, errOut)
	}
	if !swept {
		t.Fatal("no beat landed inside the window; the arm proves nothing")
	}
	if _, err := os.Stat(p.ledgerPath(e1)); !os.IsNotExist(err) {
		t.Error("the row survived the unsubscribe")
	}
	if p.qexists(q1) {
		t.Error("the queue survived: the beat rebuilt what the caller asked to be destroyed")
	}
}
