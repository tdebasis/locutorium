package nats

// The read seam, driven directly against a real broker.
//
// READING IS THE ONE VERB THAT DESTROYS. Work-queue retention makes the ack a
// delete with no recovery, so every case here is about the boundary between
// "fetched" and "consumed": a message may be handed to a reader as often as
// necessary, and may be forgotten only once the reader has actually shown it.
// The assertions are made through the INDEPENDENT admin connection — a stream's
// own message count — never through the provider under test.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"

	"github.com/tdebasis/locutorium/internal/loctest"
	"github.com/tdebasis/locutorium/internal/provider"
)

// fetchWindow is the MaxWait these cases hand the fetch. It is short because a
// dry queue is the common case and the caller loops until it is dry.
const fetchWindow = 1 * time.Second

// nsHarness is newHarness for NAMESPACED endpoints.
//
// Two differences, both forced by the model: the backing object's name
// substitutes dots for underscores (a stream name may not carry a dot), so it
// cannot simply be "QUEUE_"+endpoint; and NO consumer is created here, because
// the read path creating its own, lazily and idempotently, is one of the things
// under test.
func nsHarness(t *testing.T, endpoints ...string) *harness {
	t.Helper()
	home := t.TempDir()
	users := append([]string{"admin"}, endpoints...)
	uu := make([]*natsserver.User, 0, len(users))
	for _, u := range users {
		uu = append(uu, &natsserver.User{Username: u, Password: testPassword})
	}
	srv := loctest.Boot(t, uu, false)

	h := &harness{srv: srv, home: home, url: srv.URL, endpoints: endpoints}
	write(t, filepath.Join(home, "config"), "nats_url = "+h.url+"\n")
	write(t, filepath.Join(home, "endpoints"), strings.Join(endpoints, "\n")+"\n")
	for _, u := range users {
		write(t, filepath.Join(home, "creds", u), testPassword)
	}
	t.Setenv("LOC_HOME", home)

	nc, js := h.admin(t)
	defer nc.Close()
	for _, e := range endpoints {
		if _, err := js.AddStream(queueConfig(e)); err != nil {
			t.Fatalf("add stream for %s: %v", e, err)
		}
	}
	return h
}

// stored is the backing stream's own message count, read through admin.
func (h *harness) stored(t *testing.T, stream string) int64 {
	t.Helper()
	nc, js := h.admin(t)
	defer nc.Close()
	info, err := js.StreamInfo(stream)
	if err != nil {
		return -1
	}
	return int64(info.State.Msgs)
}

// ------------------------------------------------------------------- the seam

// The nats provider is a Reader. Asserted at compile time as well, in read.go,
// but stated here too: the verb finds this by type assertion, and a seam that
// silently stopped being implemented would otherwise fail as "this medium
// cannot read" at run time.
func TestTheNatsProviderCarriesTheReadSeam(t *testing.T) {
	var p provider.Provider = &Provider{}
	if _, ok := p.(provider.Reader); !ok {
		t.Fatal("the nats provider does not implement provider.Reader")
	}
}

// ------------------------------------------------------------- fetch and ack

// A fetch is NOT a consumption. The message is still stored after NextQueued
// returns it, and only the ack deletes it — which is what lets the caller put
// the bytes in front of a reader before forgetting them.
func TestNextQueuedFetchesWithoutConsuming(t *testing.T) {
	h := newHarness(t, "ada", "bob")
	sender := h.as(t, "ada")
	if err := sender.SendQueue("bob", envelope(t, "ada", "bob", "message-one")); err != nil {
		t.Fatalf("SendQueue: %v", err)
	}

	p := h.as(t, "bob")
	m, ok, err := p.NextQueued("bob", fetchWindow)
	if err != nil || !ok {
		t.Fatalf("NextQueued: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(string(m.Data()), "message-one") {
		t.Errorf("fetched %q, want the envelope", m.Data())
	}
	if n := h.stored(t, "QUEUE_bob"); n != 1 {
		t.Errorf("stored %d after a fetch, want 1 — a fetch is not a consumption", n)
	}

	if err := m.Ack(); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if !waitForCount(func() int64 { return h.stored(t, "QUEUE_bob") }, 0) {
		t.Errorf("stored %d after the ack, want 0 — an ack is a delete", h.stored(t, "QUEUE_bob"))
	}
}

// A dry queue is an ANSWER, not a failure: the caller loops until it is dry, so
// "nothing more" has to be ordinary.
func TestNextQueuedOnADryQueueIsNotAnError(t *testing.T) {
	h := newHarness(t, "ada")
	p := h.as(t, "ada")

	m, ok, err := p.NextQueued("ada", 200*time.Millisecond)
	if err != nil || ok || m != nil {
		t.Errorf("NextQueued on a dry queue: m=%v ok=%v err=%v, want nil,false,nil", m, ok, err)
	}
}

// So is a queue that was never stood up. A cold endpoint reads cleanly rather
// than reporting a fault at someone who has simply had no mail.
func TestNextQueuedWithNoBackingQueueIsDry(t *testing.T) {
	h := newHarness(t, "ada")
	nc, js := h.admin(t)
	defer nc.Close()
	if err := js.DeleteStream("QUEUE_ada"); err != nil {
		t.Fatalf("delete stream: %v", err)
	}

	p := h.as(t, "ada")
	if m, ok, err := p.NextQueued("ada", fetchWindow); err != nil || ok || m != nil {
		t.Errorf("NextQueued with no queue: m=%v ok=%v err=%v, want nil,false,nil", m, ok, err)
	}
}

// An unreachable medium is the one thing that is NOT dry. Silence and an
// absent server are different facts, and a reader told "no mail" by a broker
// it never reached has been told something false.
func TestNextQueuedAgainstAnUnreachableServer(t *testing.T) {
	h := newHarness(t, "ada")
	write(t, filepath.Join(h.home, "config"), "nats_url = "+closedPort(t)+"\n")

	p := h.as(t, "ada")
	if _, ok, err := p.NextQueued("ada", fetchWindow); err == nil || ok {
		t.Errorf("NextQueued against a closed port: ok=%v err=%v, want false and an error", ok, err)
	}
}

// The consumer is created lazily and idempotently: a second read of the same
// queue lands on the consumer the first one made, and continues from it.
func TestNextQueuedCreatesItsConsumerOnceAndContinuesFromIt(t *testing.T) {
	h := nsHarness(t, "workshop.scribe")
	p := h.as(t, "workshop.scribe")
	for _, b := range []string{"message-one", "message-two"} {
		if err := p.SendQueue("workshop.scribe", envelope(t, "workshop.scribe", "workshop.scribe", b)); err != nil {
			t.Fatalf("SendQueue: %v", err)
		}
	}

	for _, want := range []string{"message-one", "message-two"} {
		m, ok, err := p.NextQueued("workshop.scribe", fetchWindow)
		if err != nil || !ok {
			t.Fatalf("NextQueued(%s): ok=%v err=%v", want, ok, err)
		}
		if !strings.Contains(string(m.Data()), want) {
			t.Errorf("got %q, want %q — a queue is per-sender FIFO", m.Data(), want)
		}
		if err := m.Ack(); err != nil {
			t.Fatalf("Ack: %v", err)
		}
	}

	// The durable's name is pinned by the deployment's access control, which
	// grants exactly <instance>_<agent> — so this is a contract, not a detail.
	nc, js := h.admin(t)
	defer nc.Close()
	if _, err := js.ConsumerInfo("QUEUE_workshop_scribe", "workshop_scribe"); err != nil {
		t.Errorf("no durable named workshop_scribe on QUEUE_workshop_scribe: %v", err)
	}
}

// ------------------------------------------------------------------- peeking

// A peek shows without taking. The message is still stored afterwards, and a
// later read still receives it.
func TestPeekQueuedConsumesNothing(t *testing.T) {
	h := newHarness(t, "ada", "bob")
	sender := h.as(t, "ada")
	if err := sender.SendQueue("bob", envelope(t, "ada", "bob", "peek-canary")); err != nil {
		t.Fatalf("SendQueue: %v", err)
	}

	p := h.as(t, "bob")
	m, ok, err := p.PeekQueued("bob")
	if err != nil || !ok {
		t.Fatalf("PeekQueued: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(string(m.Data()), "peek-canary") {
		t.Errorf("peeked %q", m.Data())
	}
	if n := h.stored(t, "QUEUE_bob"); n != 1 {
		t.Errorf("stored %d after a peek, want 1", n)
	}
	p.Close()

	again := h.as(t, "bob")
	m2, ok, err := again.NextQueued("bob", 3*time.Second)
	if err != nil || !ok {
		t.Fatalf("a read after a peek: ok=%v err=%v — a peeked message must come back", ok, err)
	}
	if !strings.Contains(string(m2.Data()), "peek-canary") {
		t.Errorf("read %q after a peek, want the peeked message", m2.Data())
	}
}

func TestPeekQueuedOnADryQueueIsNotAnError(t *testing.T) {
	h := newHarness(t, "ada")
	p := h.as(t, "ada")
	if m, ok, err := p.PeekQueued("ada"); err != nil || ok || m != nil {
		t.Errorf("PeekQueued on a dry queue: m=%v ok=%v err=%v, want nil,false,nil", m, ok, err)
	}
}

func TestPeekQueuedAgainstAnUnreachableServer(t *testing.T) {
	h := newHarness(t, "ada")
	write(t, filepath.Join(h.home, "config"), "nats_url = "+closedPort(t)+"\n")
	p := h.as(t, "ada")
	if _, ok, err := p.PeekQueued("ada"); err == nil || ok {
		t.Errorf("PeekQueued against a closed port: ok=%v err=%v, want false and an error", ok, err)
	}
}

// ------------------------------------------------- what an unacked message is

// A message the reader was handed and never acknowledged goes BACK when the
// provider closes. This is the whole loss-safety property in one line: an
// interrupted read costs a duplicate, and the duplicate is available at once
// rather than after the ack-wait window — which is the window an agent cannot
// tell apart from loss.
func TestAnUnacknowledgedMessageGoesBackWhenTheProviderCloses(t *testing.T) {
	h := newHarness(t, "ada", "bob")
	sender := h.as(t, "ada")
	if err := sender.SendQueue("bob", envelope(t, "ada", "bob", "loss-canary")); err != nil {
		t.Fatalf("SendQueue: %v", err)
	}

	p := &Provider{}
	t.Setenv("LOC_IDENTITY", "bob")
	if _, ok, err := p.NextQueued("bob", fetchWindow); err != nil || !ok {
		t.Fatalf("NextQueued: ok=%v err=%v", ok, err)
	}
	p.Close() // the message was never acked

	again := h.as(t, "bob")
	m, ok, err := again.NextQueued("bob", 3*time.Second)
	if err != nil || !ok {
		t.Fatalf("after an unacked fetch: ok=%v err=%v — the message was lost", ok, err)
	}
	if !strings.Contains(string(m.Data()), "loss-canary") {
		t.Errorf("got %q, want the unacked message back", m.Data())
	}
}

// --------------------------------------------------------------------- topics

// Every reader has its OWN cursor: a topic is a room, not a queue, and one
// reader taking a message does not take it from anyone else.
func TestNextTopicGivesEachReaderItsOwnCursor(t *testing.T) {
	h := newHarness(t, "ada", "bob")
	speaker := h.as(t, "ada")
	if err := speaker.PublishTopic("standup", envelope(t, "ada", "#standup", "please look")); err != nil {
		t.Fatalf("PublishTopic: %v", err)
	}

	for _, who := range []string{"ada", "bob"} {
		p := h.as(t, who)
		m, ok, err := p.NextTopic(who, fetchWindow)
		if err != nil || !ok {
			t.Fatalf("NextTopic(%s): ok=%v err=%v", who, ok, err)
		}
		if !strings.Contains(string(m.Data()), "please look") {
			t.Errorf("%s read %q", who, m.Data())
		}
		if err := m.Ack(); err != nil {
			t.Fatalf("Ack: %v", err)
		}
		if m, ok, _ := p.NextTopic(who, 200*time.Millisecond); ok {
			t.Errorf("%s read %q twice — a cursor advances", who, m.Data())
		}
	}
}

// No TOPICS stream is a quiet room, not a fault — the same rule Topics()
// applies, and for the same reason: a reader asked what is being said, and
// "nothing" is a true answer.
func TestNextTopicWithoutATopicsStreamIsDry(t *testing.T) {
	h := newHarness(t, "ada")
	nc, js := h.admin(t)
	defer nc.Close()
	if err := js.DeleteStream("TOPICS"); err != nil {
		t.Fatalf("delete TOPICS: %v", err)
	}

	p := h.as(t, "ada")
	if m, ok, err := p.NextTopic("ada", fetchWindow); err != nil || ok || m != nil {
		t.Errorf("NextTopic with no TOPICS: m=%v ok=%v err=%v, want nil,false,nil", m, ok, err)
	}
}

// A reader the deployment does not let near TOPICS reads it as dry too. An
// endpoint's access control grants it its own queue and nothing else; refusing
// the whole verb over a room it may not enter would make its own mail
// unreadable.
func TestNextTopicIsDryWhenTheApiRefuses(t *testing.T) {
	home := t.TempDir()
	srv := loctest.Boot(t, []*natsserver.User{
		{Username: "admin", Password: testPassword},
		{Username: "ada", Password: testPassword, Permissions: &natsserver.Permissions{
			Publish:   &natsserver.SubjectPermission{Allow: []string{"queue.>", "topic.>", "$JS.API.INFO"}},
			Subscribe: &natsserver.SubjectPermission{Allow: []string{"queue.ada", "_INBOX.>"}},
		}},
	}, false)
	write(t, filepath.Join(home, "config"), "nats_url = "+srv.URL+"\n")
	write(t, filepath.Join(home, "endpoints"), "ada\n")
	write(t, filepath.Join(home, "creds", "ada"), testPassword)
	write(t, filepath.Join(home, "creds", "admin"), testPassword)
	t.Setenv("LOC_HOME", home)

	t.Setenv("LOC_IDENTITY", "ada")
	p := &Provider{}
	t.Cleanup(p.Close)
	if m, ok, err := p.NextTopic("ada", fetchWindow); err != nil || ok || m != nil {
		t.Errorf("NextTopic with no rights on TOPICS: m=%v ok=%v err=%v, want nil,false,nil", m, ok, err)
	}
}

func TestNextTopicAgainstAnUnreachableServer(t *testing.T) {
	h := newHarness(t, "ada")
	write(t, filepath.Join(h.home, "config"), "nats_url = "+closedPort(t)+"\n")
	p := h.as(t, "ada")
	if _, ok, err := p.NextTopic("ada", fetchWindow); err == nil || ok {
		t.Errorf("NextTopic against a closed port: ok=%v err=%v, want false and an error", ok, err)
	}
}

// waitForCount polls until getting want, or gives up. Deleting an acked
// message is asynchronous in the store.
func waitForCount(get func() int64, want int64) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if get() == want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return get() == want
}
