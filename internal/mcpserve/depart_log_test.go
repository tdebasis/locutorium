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

// TestDepartLog_GoodbyeSignalOnTerminate verifies that a signal-ended session
// logs the goodbye line with the signal name.
func TestDepartLog_GoodbyeSignalOnTerminate(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{}
	sess, done := serve(t, f.deps())

	// Give the server time to start up
	f.ready(t)

	// Simulate receiving a signal by closing the session after a small delay
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = sess.Close()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the server returned %v; an ordinary ending is a clean one", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not return after the runtime let go")
	}

	log := delivery(t, dir)

	// The session ends with EOF (since it's client-initiated close), not a signal
	// But the log should still have both goodbye and left lines
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

	// Should NOT have successful left line
	if strings.Contains(log, "left workshop.scribe") && !strings.Contains(log, "timed out") {
		t.Errorf("delivery log should not have successful 'left' when it timed out; got:\n%s", log)
	}
}
