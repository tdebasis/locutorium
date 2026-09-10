package nats

import (
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/presence"
)

// The presence half of the adapter, driven directly against a server this
// process starts. Everything is asserted through the harness's INDEPENDENT
// admin connection, so nothing checked here is reported by the code being
// checked.
//
// The endpoints are namespaced, as the model requires, and the harness's own
// flat endpoints are only the credential the provider speaks as.

const (
	scribe = "workshop.scribe"
	clerk  = "workshop.clerk"
)

// supervisor is a harness whose one credential is the role that launches
// agents, plus a provider speaking as it.
func supervisor(t *testing.T) (*harness, *Provider) {
	t.Helper()
	h := newHarness(t, "host")
	return h, h.as(t, "host")
}

// safeBuf is a writer safe for the follow, which is driven in a goroutine.
type safeBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *safeBuf) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *safeBuf) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// ----------------------------------------------------------- queue lifetime

// The queue is created when an agent subscribes and destroyed when it
// unsubscribes: there is no queue for an agent that is not running, and
// therefore no mailbox to accumulate, drain or reconcile.
func TestQueueLifetime(t *testing.T) {
	h, p := supervisor(t)
	nc, js := h.admin(t)
	defer nc.Close()

	exists, err := p.QueueExists(scribe)
	if err != nil || exists {
		t.Fatalf("before a subscribe: (%v, %v), want no queue and no error", exists, err)
	}
	if err := p.CreateQueue(scribe); err != nil {
		t.Fatalf("CreateQueue: %v", err)
	}
	info, err := js.StreamInfo(presence.StreamName(scribe))
	if err != nil {
		t.Fatalf("the backing object was not created: %v", err)
	}
	if len(info.Config.Subjects) != 1 || info.Config.Subjects[0] != "queue."+scribe {
		t.Errorf("the queue backs %v, want queue.%s", info.Config.Subjects, scribe)
	}
	if info.Config.Retention != natsgo.WorkQueuePolicy {
		t.Errorf("retention is %v, want work-queue — that is the delivery guarantee", info.Config.Retention)
	}
	if exists, err := p.QueueExists(scribe); err != nil || !exists {
		t.Errorf("after a subscribe: (%v, %v), want a queue", exists, err)
	}

	// Creating what is already there is a success: the queue a subscribed
	// endpoint has is the queue it needs.
	if err := p.CreateQueue(scribe); err != nil {
		t.Errorf("a second CreateQueue of the same shape: %v", err)
	}

	if err := p.DeleteQueue(scribe); err != nil {
		t.Fatalf("DeleteQueue: %v", err)
	}
	if _, err := js.StreamInfo(presence.StreamName(scribe)); err == nil {
		t.Error("the backing object outlived the subscription")
	}
	// And destroying what is already gone is what the caller asked for.
	if err := p.DeleteQueue(scribe); err != nil {
		t.Errorf("DeleteQueue on an endpoint that has none: %v", err)
	}
}

// Two instances, one agent name, two distinct objects — a collision made
// impossible by construction rather than avoided by convention.
func TestQueuesInTwoInstancesDoNotCollide(t *testing.T) {
	_, p := supervisor(t)

	for _, e := range []string{scribe, "atelier.scribe"} {
		if err := p.CreateQueue(e); err != nil {
			t.Fatalf("CreateQueue %s: %v", e, err)
		}
	}
	if err := p.DeleteQueue(scribe); err != nil {
		t.Fatalf("DeleteQueue: %v", err)
	}
	if exists, _ := p.QueueExists("atelier.scribe"); !exists {
		t.Error("destroying one instance's queue destroyed the other's")
	}
}

// The idempotency has a precise edge, and this is it: a create lands on an
// existing object only when the SHAPE IS IDENTICAL. One already standing in a
// different shape is refused, not adopted — which is what makes "creating what
// is already there is a success" a safe thing to say rather than a way to end
// up with a queue that does not keep its messages.
func TestCreateQueueRefusesAnObjectOfAnotherShape(t *testing.T) {
	h, p := supervisor(t)
	nc, js := h.admin(t)
	defer nc.Close()

	if _, err := js.AddStream(&natsgo.StreamConfig{
		Name:      presence.StreamName(scribe),
		Subjects:  []string{"queue." + scribe},
		Retention: natsgo.LimitsPolicy, // not the work-queue retention the model needs
		Storage:   natsgo.MemoryStorage,
		Replicas:  1,
	}); err != nil {
		t.Fatalf("seed a differently-shaped stream: %v", err)
	}

	err := p.CreateQueue(scribe)
	if err == nil {
		t.Fatal("an object of another shape was adopted as this endpoint's queue")
	}
	if !strings.Contains(err.Error(), scribe) {
		t.Errorf("the refusal does not name the endpoint: %v", err)
	}
}

// An unreachable medium is an error for all three, never a quiet "no":
// ignorance and absence are different answers, and a send refused for absence
// must be about the recipient.
func TestQueueOperationsOnAnUnreachableMedium(t *testing.T) {
	h, p := supervisor(t)
	write(t, filepath.Join(h.home, "config"), "nats_url = "+closedPort(t)+"\n")

	if err := p.CreateQueue(scribe); err == nil {
		t.Error("CreateQueue against a dead broker reported success")
	}
	if err := p.DeleteQueue(scribe); err == nil {
		t.Error("DeleteQueue against a dead broker reported success")
	}
	exists, err := p.QueueExists(scribe)
	if err == nil {
		t.Error("QueueExists against a dead broker answered")
	}
	if exists {
		t.Error("a medium that could not be reached reported an attended endpoint")
	}
	if _, err := p.Request("registry.workshop", 100*time.Millisecond); err == nil {
		t.Error("Request against a dead broker reported success")
	}
	if err := p.Watch("workshop", io.Discard); err == nil {
		t.Error("Watch against a dead broker reported success")
	}
	if err := p.Emit("workshop", []byte(`{"id":"ev_1"}`)); err == nil {
		t.Error("Emit against a dead broker reported success")
	}
}

// -------------------------------------------------------------------- emit

// The event reaches the instance's subject, spoken plainly — a core witness
// sees it whether it was published plainly or through the store, so the
// assertion does not presume how.
func TestEmitReachesTheInstanceSubject(t *testing.T) {
	h, p := supervisor(t)
	nc, _ := h.admin(t)
	defer nc.Close()

	sub, err := nc.SubscribeSync("presence.workshop")
	if err != nil {
		t.Fatal(err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}

	payload := []byte(`{"id":"ev_1","ts":"2026-01-14T09:31:20.114Z","kind":"activity.start","endpoint":"workshop.scribe"}`)
	if err := p.Emit("workshop", payload); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	msg, err := sub.NextMsg(2 * time.Second)
	if err != nil {
		t.Fatalf("the event did not arrive: %v", err)
	}
	if string(msg.Data) != string(payload) {
		t.Errorf("the event arrived as %s, want it byte for byte", msg.Data)
	}
}

// The one verb that must never make its caller wait returns FAST against a
// broker that is not there. An adapter runs inside an agent's lifecycle hook,
// so anything it waits on, the agent waits on.
func TestEmitAgainstADeadBrokerReturnsQuickly(t *testing.T) {
	h, p := supervisor(t)
	write(t, filepath.Join(h.home, "config"), "nats_url = "+closedPort(t)+"\n")

	start := time.Now()
	if err := p.Emit("workshop", []byte(`{"id":"ev_1"}`)); err == nil {
		t.Error("Emit reported success against a dead broker")
	}
	if elapsed := time.Since(start); elapsed > emitDial+time.Second {
		t.Errorf("Emit took %s against a dead broker; it is bounded at %s", elapsed, emitDial)
	}
}

// ---------------------------------------------------------------- registry

// Nobody answering is an ERROR. An empty roster and no supervisor at all are
// different facts, and a command that prints nothing and exits zero cannot
// tell them apart.
func TestRequestWithNobodyAnswering(t *testing.T) {
	_, p := supervisor(t)

	if _, err := p.Request("registry.workshop", 500*time.Millisecond); err == nil {
		t.Error("a request nobody answered came back as a reply")
	}
}

func TestRequestReturnsTheReply(t *testing.T) {
	h, p := supervisor(t)
	nc, _ := h.admin(t)
	defer nc.Close()

	responder, err := nc.Subscribe("registry.workshop", func(m *natsgo.Msg) {
		_ = m.Respond([]byte(`{"agents":[]}`))
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = responder.Unsubscribe() }()
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}

	reply, err := p.Request("registry.workshop", 2*time.Second)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if string(reply) != `{"agents":[]}` {
		t.Errorf("reply = %s, want what the responder said", reply)
	}
}

// Every one of these REPORTS a name the medium cannot address rather than
// answering as though it had looked. The model's own grammar makes such a name
// impossible before it gets here — that guard lives upstream, in the endpoint
// rules — and this is what happens if it is ever gone: a refusal, never a
// quiet "there is nothing there".
func TestNamesTheMediumCannotAddressAreReported(t *testing.T) {
	_, p := supervisor(t)
	const unaddressable = "work shop.scribe"

	if err := p.CreateQueue(unaddressable); err == nil {
		t.Error("CreateQueue accepted a name the store cannot hold")
	}
	if err := p.DeleteQueue(unaddressable); err == nil {
		t.Error("DeleteQueue reported success for a name the store cannot hold")
	}
	exists, err := p.QueueExists(unaddressable)
	if err == nil {
		t.Error("QueueExists answered about a name the store cannot hold")
	}
	if exists {
		t.Error("a name that could not be looked up reported an attended endpoint")
	}
	if err := p.Emit("work shop", []byte(`{"id":"ev_1"}`)); err == nil {
		t.Error("Emit accepted a subject the medium cannot address")
	}
	if err := p.Watch("work shop", io.Discard); err == nil {
		t.Error("Watch accepted a subject the medium cannot address")
	}
}

// ------------------------------------------------------------------- watch

// A follow presents each RAW PAYLOAD as it arrives — unparsed, which is the
// surest way to tolerate a field this build has never heard of — and it ENDS
// when the connection does rather than failing at anything.
func TestWatchPresentsRawPayloadsAndEndsWithTheConnection(t *testing.T) {
	h, p := supervisor(t)
	nc, _ := h.admin(t)
	defer nc.Close()

	var out safeBuf
	done := make(chan error, 1)
	go func() { done <- p.Watch("workshop", &out) }()
	// Longer than one poll interval, so the follow has looked up at least once
	// and gone back to waiting — a quiet stream is not a finished one.
	time.Sleep(watchPoll + 300*time.Millisecond)

	const unknown = `{"id":"ev_unknownfield","kind":"activity.start","future_field":"a consumer must not choke on this"}`
	if err := nc.Publish("presence.workshop", []byte(unknown)); err != nil {
		t.Fatal(err)
	}
	// An event from another instance must not appear: one subscription sees
	// every agent in ITS instance and nothing from any other.
	if err := nc.Publish("presence.atelier", []byte(`{"id":"ev_elsewhere"}`)); err != nil {
		t.Fatal(err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(out.String(), "ev_unknownfield") {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(out.String(), unknown) {
		t.Errorf("the follow did not present the payload as it arrived; saw %q", out.String())
	}
	if strings.Contains(out.String(), "ev_elsewhere") {
		t.Errorf("the follow presented another instance's event; saw %q", out.String())
	}

	p.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("a follow whose connection closed reported an error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("the follow did not end when its connection did")
	}
}

// A reader that goes away ends the follow, and says so: there is nowhere left
// to present anything.
func TestWatchStopsWhenTheReaderGoesAway(t *testing.T) {
	h, p := supervisor(t)
	nc, _ := h.admin(t)
	defer nc.Close()

	done := make(chan error, 1)
	go func() { done <- p.Watch("workshop", &failingWriter{after: 0}) }()
	time.Sleep(300 * time.Millisecond)

	if err := nc.Publish("presence.workshop", []byte(`{"id":"ev_1"}`)); err != nil {
		t.Fatal(err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "broken pipe") {
			t.Errorf("the follow ended with %v, want the reader's own error", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("the follow carried on with nobody reading")
	}
}
