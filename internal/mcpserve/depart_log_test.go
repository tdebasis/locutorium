package mcpserve

import (
	"strings"
	"testing"
	"time"
)

// TestDepartLog_GoodbyeAndLeftOnEOF verifies that an EOF-ended session logs
// both goodbye and left lines to the delivery log.
func TestDepartLog_GoodbyeAndLeftOnEOF(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{}
	sess, done := serve(t, f.deps())
	stop(t, sess, done)

	log := delivery(t, dir)

	// Check for goodbye line with EOF reason
	if !strings.Contains(log, "goodbye workshop.scribe: eof") {
		t.Errorf("delivery log missing 'goodbye workshop.scribe: eof' line; got:\n%s", log)
	}

	// Check for left line (successful departure)
	if !strings.Contains(log, "left workshop.scribe") {
		t.Errorf("delivery log missing 'left workshop.scribe' line; got:\n%s", log)
	}

	// Verify the order: goodbye should come before left
	goodbyeIdx := strings.Index(log, "goodbye workshop.scribe: eof")
	leftIdx := strings.Index(log, "left workshop.scribe")
	if goodbyeIdx < 0 || leftIdx < 0 {
		t.Fatalf("required lines not found; log:\n%s", log)
	}
	if goodbyeIdx >= leftIdx {
		t.Errorf("goodbye (%d) should come before left (%d); log:\n%s", goodbyeIdx, leftIdx, log)
	}
}

// TestDepartLog_GoodbyeAndLeftWhenTheSeatWasFullyUp is the same ending as the
// case above, taken at the other moment: the client lets go only once the
// listener is wired and the seat is being served, rather than possibly while
// the server is still starting. Both write goodbye and left, which is the
// point — a departure is not a race the occupant has to win.
//
// NOTHING HERE IS SIGNALLED, whatever the name once said. A signal cannot be
// delivered in this process without taking the test binary down with it, so
// the signal ending is proved against a real child in cmd/loc
// (mcp_signal_test.go and mcp_signal_window_test.go) and never here.
func TestDepartLog_GoodbyeAndLeftWhenTheSeatWasFullyUp(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{}
	sess, done := serve(t, f.deps())

	// A client's Connect returns before the seat is registered, so the wait
	// is what makes this case the LATER moment rather than the same one.
	f.ready(t)
	stop(t, sess, done)

	log := delivery(t, dir)

	if !strings.Contains(log, "goodbye workshop.scribe: eof") {
		t.Errorf("delivery log missing 'goodbye workshop.scribe: eof' line; got:\n%s", log)
	}
	if !strings.Contains(log, "left workshop.scribe") {
		t.Errorf("delivery log missing 'left workshop.scribe' line; got:\n%s", log)
	}
}

// TestDepartLog_LeftTimedOut verifies that when Unsubscribe times out,
// the correct message is logged.
func TestDepartLog_LeftTimedOut(t *testing.T) {
	dir := home(t, "provider = none\n")
	// Use a very long slowLeave to force a timeout (departureWait is 2 seconds)
	f := &fake{slowLeave: 5 * time.Second}
	sess, done := serve(t, f.deps())
	stop(t, sess, done)

	log := delivery(t, dir)

	// Should have goodbye line
	if !strings.Contains(log, "goodbye workshop.scribe: eof") {
		t.Errorf("delivery log missing 'goodbye workshop.scribe: eof' line; got:\n%s", log)
	}

	// Should have timeout line instead of left line
	if !strings.Contains(log, "leave timed out workshop.scribe") {
		t.Errorf("delivery log missing 'leave timed out workshop.scribe' line; got:\n%s", log)
	}

	// There is no third assertion. The one that stood here asked for a log
	// that holds "left" and does NOT hold "timed out", which the assertion
	// directly above has just required it to hold: it could not fire whatever
	// the code did, and a guard that cannot fire is not a guard.
}
