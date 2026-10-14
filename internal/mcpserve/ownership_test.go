package mcpserve

// A departing server frees ONLY ITS OWN SEAT (#216).
//
// The unsubscribe a server runs on the way out removes the row and destroys
// the queue, mail included. Each case below arranges for the seat to belong to
// somebody else by the time this server leaves — another runtime, or a later
// server of the same runtime — and asserts that the server leaves it exactly
// as it found it.

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	model "github.com/tdebasis/locutorium/internal/presence"
)

// later is a registration time no row written during a test can carry, so a
// row stamped with it is recognisably not the server's own.
const later = "2099-01-01T00:00:00.000Z"

// heldNow is the seat's row as the ledger has it.
func heldNow(t *testing.T) *model.Registration {
	t.Helper()
	r, err := model.Load(seat)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return r
}

// leavesTheSeat asserts the server ran no unsubscribe of any kind.
func leavesTheSeat(t *testing.T, f *fake) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.unsubscribed) != 0 {
		t.Errorf("the server unsubscribed a seat it no longer holds, destroying its queue: %v", f.unsubscribed)
	}
}

// ANOTHER RUNTIME TOOK THE SEAT while this server was still running. Its row
// and its queue are its own, and a server leaving late must not remove them.
func TestDepart_LeavesASeatAnotherRuntimeNowHolds(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{}
	sess, done := serve(t, f.deps())
	f.ready(t)

	holdAt(t, 4242, "", later)
	stop(t, sess, done)

	leavesTheSeat(t, f)
	if r := heldNow(t); r == nil || r.Process.PID != 4242 || r.Registered != later {
		t.Errorf("the new holder's row did not survive: %+v", r)
	}
	want := "left " + seat + ": seat now held by pid 4242 (registered " + later + "); registration and queue untouched"
	if !strings.Contains(delivery(t, dir), want) {
		t.Errorf("the log does not say who holds the seat; want %q in:\n%s", want, delivery(t, dir))
	}
}

// THE SAME RUNTIME, A LATER SERVER. A runtime that reconnects its MCP server
// starts a second one with the same parent; its row names the same pid and
// start time, and only the registration time differs. The earlier server,
// leaving, must not take the seat back from it.
func TestDepart_LeavesASeatALaterServerOfTheSameRuntimeHolds(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{}
	sess, done := serve(t, f.deps())
	f.ready(t)

	ppid := os.Getppid()
	holdAt(t, ppid, model.StartedAt(ppid), later)
	stop(t, sess, done)

	leavesTheSeat(t, f)
	if r := heldNow(t); r == nil || r.Registered != later {
		t.Errorf("the later server's row did not survive: %+v", r)
	}
	want := fmt.Sprintf("left %s: seat now held by pid %d (registered %s); registration and queue untouched", seat, ppid, later)
	if !strings.Contains(delivery(t, dir), want) {
		t.Errorf("want %q in:\n%s", want, delivery(t, dir))
	}
}

// NO ROW, NOTHING TO REMOVE. Somebody already freed the seat; an unsubscribe
// now would still destroy a queue that a later subscribe may have adopted.
func TestDepart_WithNoRowRemovesNothing(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{}
	sess, done := serve(t, f.deps())
	f.ready(t)

	if err := model.Remove(seat); err != nil {
		t.Fatalf("remove: %v", err)
	}
	stop(t, sess, done)

	leavesTheSeat(t, f)
	if want := "left " + seat + ": no registration to remove"; !strings.Contains(delivery(t, dir), want) {
		t.Errorf("want %q in:\n%s", want, delivery(t, dir))
	}
}

// A SERVER THAT NEVER CONFIRMED ITS ROW FREES NOTHING. The subscribe reported
// success but no row reads back as this server's, so nothing on the way out can
// tell its seat from a stranger's.
func TestDepart_WithoutAConfirmedRegistrationRemovesNothing(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{}
	d := f.deps()
	d.Subscribe = func(args []string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.subscribed = append(f.subscribed, args)
		return nil
	}
	sess, done := serve(t, d)
	f.ready(t)
	stop(t, sess, done)

	leavesTheSeat(t, f)
	want := "left " + seat + ": this server never confirmed its registration; registration and queue untouched"
	if !strings.Contains(delivery(t, dir), want) {
		t.Errorf("want %q in:\n%s", want, delivery(t, dir))
	}
}

// claimed waits until the server has written its pid file, which happens just
// after the listener is up and so is not implied by ready.
func claimed(t *testing.T) string {
	t.Helper()
	p := model.ServerPIDFile(seat)
	if !waitFor(5*time.Second, func() bool { _, err := os.Stat(p); return err == nil }) {
		t.Fatal("the server never wrote its pid file")
	}
	return p
}

// THE PID FILE OF A LATER SERVER STAYS. It names the process now serving the
// seat; removing it would leave a served seat naming no server.
func TestDepart_LeavesAPidFileThatNamesAnotherServer(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{}
	sess, done := serve(t, f.deps())
	f.ready(t)
	p := claimed(t)

	other := os.Getppid()
	body := fmt.Sprintf("%d\n%s\n", other, model.StartedAt(other))
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	stop(t, sess, done)

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("the pid file naming another server was removed: %v", err)
	}
	if string(b) != body {
		t.Errorf("the pid file changed: %q, want %q", b, body)
	}
	if want := fmt.Sprintf("left %s: server pid file names pid %d; left in place", seat, other); !strings.Contains(delivery(t, dir), want) {
		t.Errorf("want %q in:\n%s", want, delivery(t, dir))
	}
}

// ITS OWN PID FILE GOES, as before.
func TestDepart_RemovesItsOwnPidFile(t *testing.T) {
	home(t, "provider = none\n")
	f := &fake{}
	sess, done := serve(t, f.deps())
	f.ready(t)
	p := claimed(t)
	stop(t, sess, done)

	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("the server's own pid file survived its departure: %v", err)
	}
}

// AN EARLIER SERVER OF THE SAME RUNTIME IS STILL REGISTERED. The seat is the
// runtime's either way, so this server takes it, but the queue holds mail sent
// to that runtime: only the row is replaced, and no unsubscribe runs before the
// subscribe.
func TestRegister_ReTakesFromAnEarlierServerOfTheSameRuntimeAndKeepsTheQueue(t *testing.T) {
	dir := home(t, "provider = none\n")
	const earlier = "2026-01-01T00:00:00.000Z"
	ppid := os.Getppid()
	holdAt(t, ppid, model.StartedAt(ppid), earlier)

	f := &fake{}
	sess, done := serve(t, f.deps())
	f.ready(t)

	f.mu.Lock()
	calls := append([]string(nil), f.calls...)
	f.mu.Unlock()
	if len(calls) != 1 || calls[0] != "subscribe" {
		t.Errorf("the re-take ran %v; want a subscribe and nothing before it, because an unsubscribe destroys the queue", calls)
	}
	if r := heldNow(t); r == nil || r.Registered == earlier || r.Process.PID != ppid {
		t.Errorf("the earlier server's row was not replaced: %+v", r)
	}
	want := fmt.Sprintf("re-took %s from an earlier server of the same runtime, pid %d (registered %s); queue kept", seat, ppid, earlier)
	if !strings.Contains(delivery(t, dir), want) {
		t.Errorf("want %q in:\n%s", want, delivery(t, dir))
	}

	// And the seat this server took is its own, so it frees it on the way out.
	stop(t, sess, done)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.unsubscribed) != 1 || !strings.Contains(strings.Join(f.unsubscribed[0], " "), "--reason clean") {
		t.Errorf("the server did not free its own seat: %v", f.unsubscribed)
	}
}
