package nats

// What `status` counts once there IS a reader.
//
// The stream's own message count was the right figure while nothing consumed a
// namespaced queue: work-queue retention drops a message when it is taken, so
// a stored message was exactly an untaken one. A reader's cursor breaks that
// identity — a message it has been handed and not yet acknowledged is still
// stored, and reporting it as waiting tells an operator mail is owed that has
// already been put in front of somebody.

import (
	"strings"
	"testing"
)

func TestUnreadCountsWhatIsPendingForANamespacedReader(t *testing.T) {
	h := nsHarness(t, "workshop.scribe")
	p := h.as(t, "workshop.scribe")
	for _, b := range []string{"message-one", "message-two"} {
		if err := p.SendQueue("workshop.scribe", envelope(t, "workshop.scribe", "workshop.scribe", b)); err != nil {
			t.Fatalf("SendQueue: %v", err)
		}
	}

	// One is handed over and deliberately not acknowledged: it is still
	// STORED, and it is no longer WAITING.
	if _, ok, err := p.NextQueued("workshop.scribe", fetchWindow); err != nil || !ok {
		t.Fatalf("NextQueued: ok=%v err=%v", ok, err)
	}
	if n := h.stored(t, "QUEUE_workshop_scribe"); n != 2 {
		t.Fatalf("stored %d, want 2 — the fetch must not have consumed", n)
	}

	var out strings.Builder
	if err := p.Status(&out); err != nil {
		t.Fatalf("Status: %v", err)
	}
	want := "workshop.scribe unread: 1\n"
	if out.String() != want {
		t.Errorf("got %q, want %q — the count is what is still waiting, not what is still stored",
			out.String(), want)
	}
}

// With no cursor on it, a namespaced queue is still counted by the stream: a
// stored message is an untaken one exactly while nothing has taken any.
func TestUnreadCountsTheStreamForANamespacedQueueWithNoReader(t *testing.T) {
	h := nsHarness(t, "workshop.scribe")
	p := h.as(t, "workshop.scribe")
	if err := p.SendQueue("workshop.scribe", envelope(t, "workshop.scribe", "workshop.scribe", "message-one")); err != nil {
		t.Fatalf("SendQueue: %v", err)
	}

	var out strings.Builder
	if err := p.Status(&out); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if want := "workshop.scribe unread: 1\n"; out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}
