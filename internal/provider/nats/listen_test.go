package nats

// The listener's half of the adapter: being TOLD that mail has arrived, and
// asking how much is waiting. Both are driven against a real server this
// process starts, and both are asserted through the harness's independent
// admin connection.

import (
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tdebasis/locutorium/internal/loctest"
)

// A CORE SUBSCRIPTION SEES THE ARRIVAL AND TAKES NOTHING. The stream captures
// the same publish, so the message is still there for the read that follows
// the bell — which is the whole reason the listener does not use a cursor.
func TestWatchQueue_RingsOnArrivalAndConsumesNothing(t *testing.T) {
	h := newHarness(t, "alice")
	p := h.as(t, "alice")
	nc, _ := h.admin(t)
	defer nc.Close()

	var rings atomic.Int64
	stop, err := p.WatchQueue("alice", func() { rings.Add(1) }, nil)
	if err != nil {
		t.Fatalf("WatchQueue: %v", err)
	}
	defer stop()

	if err := nc.Publish("queue.alice", []byte(`{"id":"m1"}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	_ = nc.Flush()

	deadline := time.Now().Add(3 * time.Second)
	for rings.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if rings.Load() != 1 {
		t.Fatalf("one arrival rang %d times", rings.Load())
	}
	if got := h.stored(t, "QUEUE_alice"); got != 1 {
		t.Errorf("the queue holds %d after the watch saw it; watching must consume nothing", got)
	}
}

func TestWatchQueue_AnUnreachableMediumIsAnError(t *testing.T) {
	h := newHarness(t, "alice")
	p := h.as(t, "alice")
	write(t, filepath.Join(h.home, "config"), "nats_url = "+loctest.ClosedPort(t)+"\n")

	if stop, err := p.WatchQueue("alice", func() {}, nil); err == nil {
		stop()
		t.Fatal("a watch was established against a medium that is not there")
	}
}

// Unread is the figure `status` prints, as a number — and the DIFFERENCE
// between "none" and "cannot say" is preserved, because a caller deciding
// whether to ring a bell cannot act on "?".
func TestUnread_CountsWhatHasNotBeenTaken(t *testing.T) {
	h := newHarness(t, "alice")
	p := h.as(t, "alice")
	nc, _ := h.admin(t)
	defer nc.Close()

	for _, id := range []string{"m1", "m2", "m3"} {
		if err := nc.Publish("queue.alice", []byte(`{"id":"`+id+`"}`)); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	_ = nc.Flush()

	// One is fetched and not acknowledged, which puts a cursor on the queue:
	// that is the state a running reader is in, and the state in which the
	// stream's own count would over-report.
	if _, ok, err := p.NextQueued("alice", 2*time.Second); err != nil || !ok {
		t.Fatalf("NextQueued: (%v, %v)", ok, err)
	}
	n, err := p.Unread("alice")
	if err != nil {
		t.Fatalf("Unread: %v", err)
	}
	if n != 2 {
		t.Errorf("Unread = %d; three were sent and one has been handed over", n)
	}
}

func TestUnread_AFigureThatCannotBeHadIsAnError(t *testing.T) {
	h := newHarness(t, "alice")
	p := h.as(t, "alice")
	if _, err := p.Unread("nobody"); err == nil {
		t.Error("a queue that does not exist reported a count")
	}
}

func TestUnread_AnUnreachableMediumIsAnError(t *testing.T) {
	h := newHarness(t, "alice")
	p := h.as(t, "alice")
	write(t, filepath.Join(h.home, "config"), "nats_url = "+loctest.ClosedPort(t)+"\n")
	if _, err := p.Unread("alice"); err == nil {
		t.Error("an unreachable medium reported a count")
	}
}

// A LISTENER OUTLIVES A BREAK IN THE CONNECTION, and says when it has — the
// arrivals during the gap were seen by nobody, so the caller has to ask after
// the backlog again. Proved through a relay this test owns: the scratch server
// cannot be stopped and started under the client, but the path to it can be
// cut, which is the same event as far as the client is concerned.
func TestWatchQueue_ARestoredConnectionSaysSo(t *testing.T) {
	h := newHarness(t, "alice")
	p := h.as(t, "alice")

	relay := newRelay(t, h.url)
	write(t, filepath.Join(h.home, "config"), "nats_url = nats://"+relay.addr+"\n")

	var back atomic.Int64
	stop, err := p.WatchQueue("alice", func() {}, func() { back.Add(1) })
	if err != nil {
		t.Fatalf("WatchQueue through the relay: %v", err)
	}
	defer stop()

	relay.cut()

	deadline := time.Now().Add(20 * time.Second)
	for back.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if back.Load() == 0 {
		t.Fatal("the connection came back and the listener never said so; " +
			"a seat that missed the gap is never told to look again")
	}
}

// relay is a loopback TCP forwarder in front of the scratch server, so a test
// can cut the connection without touching the server itself.
type relay struct {
	addr string

	mu    sync.Mutex
	conns []net.Conn
}

func newRelay(t *testing.T, natsURL string) *relay {
	t.Helper()
	target := strings.TrimPrefix(natsURL, "nats://")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("relay listen: %v", err)
	}
	r := &relay{addr: ln.Addr().String()}
	t.Cleanup(func() { _ = ln.Close(); r.cut() })

	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", target)
			if err != nil {
				_ = client.Close()
				continue
			}
			r.mu.Lock()
			r.conns = append(r.conns, client, server)
			r.mu.Unlock()
			go func() { _, _ = io.Copy(server, client) }()
			go func() { _, _ = io.Copy(client, server) }()
		}
	}()
	return r
}

// cut drops every connection currently in flight. The client sees its socket
// close, exactly as it would if the server had gone away, and reconnects
// through the relay, which is still accepting.
func (r *relay) cut() {
	r.mu.Lock()
	conns := r.conns
	r.conns = nil
	r.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}
