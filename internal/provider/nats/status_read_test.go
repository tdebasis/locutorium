package nats

// What `status` counts once there IS a reader.
//
// The figure is the queue's own message count, with a reader on it or without
// one. Work-queue retention plus an explicit acknowledgement makes the
// acknowledgement the moment a message is taken: a read acknowledges as soon
// as it prints, and an interrupted read puts back what it held. So a stored
// message that nobody has acknowledged is mail this endpoint is still owed,
// whether a reader holds it right now or a dead reader left it in flight for
// the broker to redeliver.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

func TestUnreadCountsAMessageAReaderHoldsAndHasNotAcknowledged(t *testing.T) {
	h := nsHarness(t, "workshop.scribe")
	p := h.as(t, "workshop.scribe")
	for _, b := range []string{"message-one", "message-two"} {
		if err := p.SendQueue("workshop.scribe", envelope(t, "workshop.scribe", "workshop.scribe", b)); err != nil {
			t.Fatalf("SendQueue: %v", err)
		}
	}

	// One is handed over and deliberately not acknowledged: it is still
	// STORED, and nothing has taken it.
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
	want := "workshop.scribe unread: 2\n"
	if out.String() != want {
		t.Errorf("got %q, want %q — a message held but not acknowledged is still owed",
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

// MAIL HANDED OVER AND NEVER ACKNOWLEDGED IS STILL OWED. A reader that dies
// between the fetch and the acknowledgement leaves the message in flight. The
// broker redelivers it after the acknowledgement wait, so the mail comes back
// and the count must say so. The consumer's pending figure says 0 here, which
// is the defect this test holds shut.
func TestUnreadCountsMailHandedOverAndNeverAcknowledged(t *testing.T) {
	h := newHarness(t, "bob")
	p := h.as(t, "bob")
	if err := p.SendQueue("bob", envelope(t, "bob", "bob", "message-one")); err != nil {
		t.Fatalf("SendQueue: %v", err)
	}

	// The fetch runs on the harness's own connection, so nothing the provider
	// holds can put the message back: this is a reader that went away.
	nc, js := h.admin(t)
	defer nc.Close()
	sub, err := js.PullSubscribe("queue.bob", "", natsgo.Bind("QUEUE_bob", "bob"))
	if err != nil {
		t.Fatalf("pull subscribe: %v", err)
	}
	msgs, err := sub.Fetch(1, natsgo.MaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("fetched %d messages, want 1", len(msgs))
	}
	// Neither Ack nor Nak. The message is in flight and still stored.

	var out strings.Builder
	if err := p.Status(&out); err != nil {
		t.Fatalf("Status: %v", err)
	}
	want := "bob          unread: 1\n"
	if out.String() != want {
		t.Errorf("got %q, want %q — a message nobody acknowledged is still owed", out.String(), want)
	}
	if n, err := p.Unread("bob"); err != nil || n != 1 {
		t.Errorf("Unread = (%d, %v), want (1, nil)", n, err)
	}
}

// THE COUNT IS 0 THE INSTANT A READ ACKNOWLEDGES. The count is the queue's
// depth, so it falls only when the broker removes the message. The broker
// removes a work-queue message while it handles the acknowledgement, and it
// answers the acknowledgement afterwards, so a reader that waits for that
// answer cannot be followed by a count that still holds what it read.
//
// The asking connection is opened BEFORE the acknowledgement and is not the
// reader's, so the only work between the acknowledgement and the question is
// one request. Two hundred rounds, a new message each time.
//
// THE PLANT THAT TURNS THIS RED: acknowledge with m.m.Ack() in place of
// AckSync in read.go.
func TestUnreadIsZeroImmediatelyAfterAReadAcknowledges(t *testing.T) {
	newHarness(t, "alice")
	t.Setenv("LOC_IDENTITY", "alice")

	sender := &Provider{}
	t.Cleanup(sender.Close)

	for i := 0; i < 200; i++ {
		if err := sender.SendQueue("alice", envelope(t, "alice", "alice", fmt.Sprintf("message-%d", i))); err != nil {
			t.Fatalf("round %d: SendQueue: %v", i, err)
		}

		reader := &Provider{}
		m, ok, err := reader.NextQueued("alice", fetchWindow)
		if err != nil || !ok {
			reader.Close()
			t.Fatalf("round %d: NextQueued: ok=%v err=%v", i, ok, err)
		}

		asker := &Provider{}
		if err := asker.connect(); err != nil {
			reader.Close()
			t.Fatalf("round %d: the asking connection: %v", i, err)
		}

		if err := m.Ack(); err != nil {
			reader.Close()
			asker.Close()
			t.Fatalf("round %d: Ack: %v", i, err)
		}

		n, err := asker.Unread("alice")
		reader.Close()
		asker.Close()
		if err != nil {
			t.Fatalf("round %d: Unread: %v", i, err)
		}
		if n != 0 {
			t.Fatalf("round %d: Unread = %d straight after the read acknowledged; want 0", i, n)
		}
	}
}
