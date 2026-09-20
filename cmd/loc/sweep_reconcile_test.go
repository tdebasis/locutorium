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
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
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

// A row the ledger cannot read is REPORTED, and the queue standing beside it
// survives. No pass deletes a queue on the absence of a row any more (#141),
// so an unreadable row no longer has a destructive pass to block: it is a fact
// about the evidence, and the sweep's job is to say it.
func TestFalsifierUnreadableRowIsReportedAndItsQueueSurvives(t *testing.T) {
	p := newPresence(t)
	p.subscribeLive(t, e1)
	p.subscribeLive(t, e2)
	// e2's row becomes unreadable while its queue stays. Read as a listing
	// that skips it, e2's queue is a queue with no row.
	writeFile(t, p.ledgerPath(e2), "{not json at all")

	p.as(t, "host")
	code, out, errOut := exec("sweep")
	t.Logf("sweep exit=%d stdout=%q stderr=%q", code, out, errOut)
	if code != 0 {
		t.Errorf("sweep exited %d (stderr %q); an unreadable row is reported, not a failure", code, errOut)
	}
	if !strings.Contains(out, e2+": row unreadable:") {
		t.Errorf("stdout %q does not name the unreadable row", out)
	}
	// Both facts about e2 are printed: its row cannot be read, and its queue
	// is unaccounted for until the row is fixed.
	if !strings.Contains(out, e2+": queue with no row;") {
		t.Errorf("stdout %q does not report the queue standing beside the unreadable row", out)
	}
	if !p.qexists(q2) {
		t.Error("the sweep destroyed the queue of the seat it could not read")
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
// so every registered seat must still be reported from it.
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
// Two writes make a subscribe and a beat can land between them. With the row
// written first, the gap is a row with no queue, which the repair pass fills.
// With the queue created first, the gap is a queue with no row, which no pass
// repairs.
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
// before the beat lands, so the beat sees a queue with no row: it reports it
// and removes nothing (#141). The verb then deletes the queue itself. What
// this arm proves is that the beat does not undo the removal — the queue does
// not come back, and the row does not come back.
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

// ------------------------------------------ arm 9: the queue with no row (#141)

// logToday is the message log this deployment has written today, or "" when it
// has written none. The sweep's own records go here.
func (p *presence) logToday(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(p.home, "run", "log", time.Now().UTC().Format("2006-01-02")+".jsonl"))
	if err != nil {
		return ""
	}
	return string(b)
}

// AN ABANDONED UNSUBSCRIBE LEAVES THE QUEUE, AND THE SWEEP LEAVES IT TOO.
//
// This is #141's own case. The seat is the only row of its instance. The
// unsubscribe is abandoned between its two writes — the row is gone, the queue
// is not — which is what a crash, a kill or a lost connection does there. The
// sweep then finds a queue no row claims, in a ledger that holds no other row
// for that instance.
//
// Every earlier build deleted that queue and the mail in it: before #140
// because no row claimed it, and after #140 whenever another row kept the
// instance alive. This build reports it and stops. The mail sent while the
// seat was away is still there when the seat subscribes again: a queue is held
// until it is read, and the address is the agent.
//
// THE PLANT THAT TURNS THIS RED: put `pr.DeleteQueue(f.Endpoint)` back in the
// QueueNoRow case of sweep.
func TestFalsifierSweepReportsAQueueWithNoRowAndKeepsItsMail(t *testing.T) {
	p := newPresence(t)
	p.subscribeLive(t, e1)

	// The unsubscribe is abandoned where issue #141 says it is abandoned: at
	// the seam between removing the row and deleting the queue. Goexit ends
	// the goroutine running the verb and runs its deferred cleanups, which is
	// as close as an in-process case gets to a process that died there.
	restore := betweenUnsubscribeWrites
	t.Cleanup(func() { betweenUnsubscribeWrites = restore })
	abandoned := make(chan struct{})
	betweenUnsubscribeWrites = func() {
		betweenUnsubscribeWrites = func() {}
		close(abandoned)
		runtime.Goexit()
	}
	p.as(t, "host")
	done := make(chan struct{})
	go func() {
		defer close(done)
		exec("unsubscribe", e1, "--force")
	}()
	<-done
	select {
	case <-abandoned:
	default:
		t.Fatal("the unsubscribe was never abandoned in its window; the arm proves nothing")
	}
	if _, err := os.Stat(p.ledgerPath(e1)); !os.IsNotExist(err) {
		t.Fatal("the row is still there; the arm is not in the state #141 describes")
	}
	if !p.qexists(q1) {
		t.Fatal("the queue went with the row; the arm is not in the state #141 describes")
	}

	// Mail arrives for the absent seat and is stored in the standing queue.
	if code, _, errOut := exec("send", e1, "held-until-read"); code != 0 {
		t.Fatalf("send exited %d (stderr %q)", code, errOut)
	}

	var out strings.Builder
	changes, err := sweepVerb(&out, nil)
	if err != nil {
		t.Fatalf("sweepVerb: %v", err)
	}
	if changes != 0 {
		t.Errorf("changes = %d, want 0: a report is not a change", changes)
	}
	want := e1 + ": queue with no row; nothing removes it; remove it with loc unsubscribe " + e1 + "\n"
	if out.String() != want {
		t.Errorf("the sweep printed:\n%q\nwant:\n%q", out.String(), want)
	}
	if !p.qexists(q1) {
		t.Fatal("the sweep deleted a queue no row claimed")
	}
	if n := p.qcount(q1); n != 1 {
		t.Errorf("the queue holds %d messages, want the 1 sent while the seat was away", n)
	}
	if log := p.logToday(t); strings.Contains(log, "queue-deleted") {
		t.Errorf("the sweep recorded a queue-deleted event:\n%s", log)
	}

	// THE ADOPTION. Subscribing again takes the standing queue AND its mail.
	p.subscribeLive(t, e1)
	p.as(t, e1)
	code, readOut, errOut := exec("read")
	t.Logf("read exit=%d stdout=%q stderr=%q", code, readOut, errOut)
	if code != 0 || !strings.Contains(readOut, "held-until-read") {
		t.Errorf("the mail sent while the seat was away did not read back: exit %d, stdout %q", code, readOut)
	}
}

// A SWEEP WITH AN EMPTY LEDGER DELETES NOTHING. This is the 2026-09-18 shape:
// a process holding no rows, reaching a broker full of another deployment's
// queues. It now reports each one and destroys none, and no rule about
// instances is doing the work — there is simply no code that deletes a queue
// without a dead row of its own beside it.
func TestFalsifierSweepWithAnEmptyLedgerDeletesNothing(t *testing.T) {
	p := newPresence(t)
	others := []struct{ endpoint, stream string }{
		{e1, q1}, {e2, q2}, {e3, q3},
	}
	for _, o := range others {
		p.seedQueue(t, o.stream, "queue."+o.endpoint)
	}
	if _, err := os.Stat(filepath.Join(p.home, "run", "presence")); !os.IsNotExist(err) {
		t.Fatal("the ledger is not empty; the arm proves nothing")
	}

	p.as(t, "host")
	var out strings.Builder
	changes, err := sweepVerb(&out, nil)
	if err != nil {
		t.Fatalf("sweepVerb: %v", err)
	}
	if changes != 0 {
		t.Errorf("changes = %d, want 0", changes)
	}
	for _, o := range others {
		if !p.qexists(o.stream) {
			t.Errorf("%s was destroyed by a sweep whose ledger holds nothing", o.stream)
		}
		if !strings.Contains(out.String(), o.endpoint+": queue with no row;") {
			t.Errorf("the sweep did not report %s; whole report:\n%s", o.endpoint, out.String())
		}
	}
	if log := p.logToday(t); strings.Contains(log, "queue-deleted") {
		t.Errorf("the sweep recorded a queue-deleted event:\n%s", log)
	}
}
