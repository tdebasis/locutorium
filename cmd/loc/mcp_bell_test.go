package main

// THE BELL. Three claims, each about how MANY lines reach the pane and when —
// which is the only thing a delivery policy can get wrong without losing a
// message. Nothing here asserts that mail survived: the queue is what holds
// the mail, and every suppressed wake in this file is followed by a read that
// still finds everything.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tdebasis/locutorium/internal/mcpserve"
)

// serveBell starts the seat's server on an in-memory pair and returns the
// pane's spool. It is the tools' harness without the client's session: these
// cases are about what reaches the PANE, not what an agent asked for.
func serveBell(t *testing.T, p *presence) string {
	t.Helper()
	spool := nudgeSpool(t, p.home)
	stampVersion(t, "1.4.2")
	d, release, err := mcpDeps()
	if err != nil {
		t.Fatalf("wire the server: %v", err)
	}
	t.Cleanup(release)

	serverT, clientT := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- mcpserve.Serve(context.Background(), d, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: testClientName, Version: testClientVersion}, nil)
	sess, err := client.Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(); <-done })
	if !waitFor(5*time.Second, func() bool { return registration(t, e1) != nil }) {
		t.Fatalf("the server did not register %s", e1)
	}
	return spool
}

// sendTo delivers one message to the seat, as the SUPERVISOR does — the host
// sends on an agent's behalf (docs/PRESENCE.md §Trust, in the inner parlor),
// and it is the one identity in this deployment whose rights let it ask
// whether an arbitrary endpoint is attended.
func sendTo(t *testing.T, p *presence, to, body string) {
	t.Helper()
	p.as(t, "host")
	var errOut bytes.Buffer
	if code := run([]string{"send", to, body}, io.Discard, &errOut); code != 0 {
		t.Fatalf("send -> %s: exit %d: %s", to, code, errOut.String())
	}
	p.as(t, to)
}

func TestMCPBell_OneArrivalRingsOnce(t *testing.T) {
	p := newPresence(t)
	tune(t, p.home, "wake_window_seconds = 1")
	p.as(t, e1)
	spool := serveBell(t, p)

	sendTo(t, p, e1, "one")

	if !waitFor(6*time.Second, func() bool { return len(bells(t, spool)) >= 1 }) {
		t.Fatalf("an arrival rang no bell at all")
	}
	// And it rings ONCE. Given a second window to ring again, it does not.
	time.Sleep(2 * time.Second)
	got := bells(t, spool)
	if len(got) != 1 || got[0] != "🔔 1 new → read" {
		t.Errorf("one arrival produced %d bells %q; want exactly one, %q",
			len(got), got, "🔔 1 new → read")
	}
}

func TestMCPBell_ArrivalsInsideTheWindowRingOnce(t *testing.T) {
	p := newPresence(t)
	tune(t, p.home, "wake_window_seconds = 3")
	p.as(t, e1)
	spool := serveBell(t, p)

	// Three, well inside one window. A bell per message is what a pane cannot
	// take; coalescing is why the line carries a COUNT rather than a body.
	for i := 0; i < 3; i++ {
		sendTo(t, p, e1, fmt.Sprintf("message %d", i))
	}

	if !waitFor(8*time.Second, func() bool { return len(bells(t, spool)) >= 1 }) {
		t.Fatalf("three arrivals rang no bell at all")
	}
	time.Sleep(2 * time.Second)
	got := bells(t, spool)
	if len(got) != 1 || got[0] != "🔔 3 new → read" {
		t.Errorf("three arrivals inside one window produced %d bells %q; want exactly one, %q",
			len(got), got, "🔔 3 new → read")
	}
}

// WAKE ON BACKLOG. Mail that arrived while nothing was listening was announced
// to nobody, so the count is asked for at start — otherwise a seat that comes
// up to a full queue sits in silence until somebody happens to write to it.
func TestMCPBell_ABacklogRingsBeforeAnythingNewArrives(t *testing.T) {
	p := newPresence(t)
	tune(t, p.home, "wake_window_seconds = 1")
	p.as(t, e1)

	// The queue exists and holds two, with no server yet: exactly the state a
	// seat whose runtime was restarted comes up into.
	p.seedQueue(t, q1, "queue."+e1)
	for _, id := range []string{"m1", "m2"} {
		if err := p.admin.Publish("queue."+e1, []byte(canned(id, e2, e1, "waiting: "+id))); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	_ = p.admin.Flush()

	spool := serveBell(t, p)

	if !waitFor(6*time.Second, func() bool { return len(bells(t, spool)) >= 1 }) {
		t.Fatalf("a seat that came up to a full queue was never told")
	}
	if got := bells(t, spool)[0]; got != "🔔 2 new → read" {
		t.Errorf("the backlog bell said %q; two messages were waiting", got)
	}
}
