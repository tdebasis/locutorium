package main

import (
	"io"
	"strings"
	"testing"
)

// ════════════════════════════════════════════════════════════════════════════
// SEPARATE — message-plane loss-safety, NOT part of the presence set.
// PRESENCE.md explicitly excludes message addressing and delivery. `read` is a
// message-plane verb; its loss-safety is recorded here, clearly labelled, so
// the presence count stays clean.
// ════════════════════════════════════════════════════════════════════════════

func TestMessagePlane_ReadDoesNotConsumeOnFailedRender(t *testing.T) {
	p := newPresence(t)
	p.as(t, e1) // an agent reads its own queue
	p.seedQueue(t, q1, "queue."+e1)
	if err := p.admin.Publish("queue."+e1,
		[]byte(`{"id":"m1","ts":"2026-01-14T09:00:00.000Z","from":"host","to":"workshop.scribe","kind":"msg","body":"loss-canary"}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	_ = p.admin.Flush()

	// A read that dies after one write must not have consumed what it never
	// showed: consumption is an ack, and an ack is a delete.
	_ = run([]string{"read"}, &errAfterWriter{after: 1}, io.Discard)

	var out safeBuffer
	_ = run([]string{"read"}, &out, io.Discard)
	if !strings.Contains(out.String(), "loss-canary") {
		t.Errorf("a read that failed mid-render did not re-present the message; got %q", out.String())
	}
}
