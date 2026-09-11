package nats

// The read path — the only place in this adapter that TAKES anything.
//
// Queues are work-queue retention: the acknowledgement is a delete, and there
// is no recovery from one. So the fetch and the ack are deliberately two
// operations with everything fallible in between, and the failure direction is
// fixed at DUPLICATE, NEVER LOSS (PROTOCOL.md §5: delivery is at-least-once
// and `id` is the dedupe key).
//
// Nothing here uses the acknowledging request path the send side uses. A read
// that acked at the moment it fetched is exactly the defect this seam exists to
// make impossible.

import (
	"fmt"
	"strings"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/presence"
	"github.com/tdebasis/locutorium/internal/provider"
)

// This medium carries a reader's cursor. The verb finds it by type assertion,
// so the compiler is told here rather than being left to a run-time refusal.
var _ provider.Reader = (*Provider)(nil)

// topicsStream is the one stream every topic lives in. A topic is a room, and
// each reader keeps its own position in it.
const topicsStream = "TOPICS"

// apiWait bounds one JetStream API call on the read path.
//
// It is short because the answer "you may not look at that" arrives as
// silence: an endpoint's access control grants it its own queue and nothing
// else, so a request about the topic store is simply not answered. A read must
// not spend the caller's patience discovering a room it may not enter.
const apiWait = 1 * time.Second

// peekWait is how long a peek waits for a message to show. A peek is one
// glance, not a drain.
const peekWait = 1 * time.Second

// message is one envelope the reader has been handed and has not yet taken.
type message struct {
	p *Provider
	m *natsgo.Msg
}

func (m *message) Data() []byte { return m.m.Data }

// Ack forgets the message — which is a deletion. It is called only once the
// bytes have actually reached the reader.
func (m *message) Ack() error {
	if err := m.m.Ack(); err != nil {
		return err
	}
	m.p.taken(m.m)
	return nil
}

// hand records a message as delivered-but-not-taken and wraps it for the
// caller.
func (p *Provider) hand(m *natsgo.Msg) provider.Message {
	p.unacked = append(p.unacked, m)
	return &message{p: p, m: m}
}

// taken drops a message from the outstanding set, because it has been.
func (p *Provider) taken(m *natsgo.Msg) {
	for i, u := range p.unacked {
		if u == m {
			p.unacked = append(p.unacked[:i], p.unacked[i+1:]...)
			return
		}
	}
}

// returnUnacked puts back everything the reader was handed and never took.
//
// THIS IS THE LOSS-SAFETY PROPERTY, and it is here rather than in the verb
// because only the medium can do it. Without it an interrupted read leaves the
// message in-flight for the consumer's whole ack-wait window, during which the
// next read shows an empty mailbox — a window an agent cannot tell apart from
// loss, and the live suspect behind "the nudge said one new and read showed
// nothing". Returned at once, an interrupted read costs a duplicate and
// nothing else.
func (p *Provider) returnUnacked() {
	if p.nc == nil {
		return
	}
	for _, m := range p.unacked {
		_ = m.Nak()
	}
	p.unacked = nil
	// A negative acknowledgement is a publish; the connection is about to go,
	// so it is made to leave first.
	_ = p.nc.FlushTimeout(ackTimeout)
}

// durableName is the object holding one reader's position.
//
// A NAMESPACED endpoint substitutes its dot, because a durable's name may not
// carry one — and the deployment's access control grants exactly that name
// (CONSUMER.DURABLE.CREATE.QUEUE_<s>.<s>, MSG.NEXT.QUEUE_<s>.<s>), so the
// spelling is a contract rather than a choice.
//
// A LEGACY un-namespaced identity keeps the raw name the shell adapter used,
// on QUEUE_<name>, so an existing deployment's consumers are the ones this
// build continues from rather than a second set beside them.
func durableName(endpoint string) string {
	if presence.ValidEndpoint(endpoint) == nil {
		return strings.ReplaceAll(endpoint, ".", "_")
	}
	return endpoint
}

// queueConsumer is the ONE shape a reader's queue cursor has: everything from
// the beginning, and nothing forgotten until it says so.
func queueConsumer(endpoint string) *natsgo.ConsumerConfig {
	return &natsgo.ConsumerConfig{
		Durable:       durableName(endpoint),
		DeliverPolicy: natsgo.DeliverAllPolicy,
		AckPolicy:     natsgo.AckExplicitPolicy,
	}
}

// topicConsumer is the same shape for the room, filtered to what is said in it.
func topicConsumer(reader string) *natsgo.ConsumerConfig {
	return &natsgo.ConsumerConfig{
		Durable:       durableName(reader),
		FilterSubject: "topic.>",
		DeliverPolicy: natsgo.DeliverAllPolicy,
		AckPolicy:     natsgo.AckExplicitPolicy,
	}
}

// puller returns the bound pull subscription for one cursor, creating it
// lazily.
//
// The create is IDEMPOTENT BY THE STORE'S OWN RULE rather than by a
// pre-check: a create lands on an existing consumer only when the
// configuration is identical, so a second read succeeds on the cursor the
// first one made. A look-then-create would have a gap between the look and the
// create; this has none.
//
// ok=false means "nothing came back" — no stream, or a deployment that does
// not let this reader near it. WHICH OF THE TWO IT WAS IS THE CALLER'S TO
// DECIDE, and the two callers decide differently: the topic store is an
// optional room, so a refusal there stays dry rather than making a reader's own
// mail unreadable; the caller's own queue is not optional, and a refusal on it
// is a failure (see refusedOwnQueue).
func (p *Provider) puller(stream string, cfg *natsgo.ConsumerConfig) (*natsgo.Subscription, bool) {
	key := stream + "/" + cfg.Durable
	if sub, ok := p.subs[key]; ok {
		return sub, true
	}
	if _, err := p.js.AddConsumer(stream, cfg, natsgo.MaxWait(apiWait)); err != nil {
		return nil, false
	}
	// The cursor's own filter is handed back to the bind: a consumer that
	// filters is refused a subscription that claims a wider subject than it
	// serves, and the queue cursor filters nothing.
	sub, err := p.js.PullSubscribe(cfg.FilterSubject, cfg.Durable, natsgo.Bind(stream, cfg.Durable))
	if err != nil {
		return nil, false
	}
	if p.subs == nil {
		p.subs = map[string]*natsgo.Subscription{}
	}
	p.subs[key] = sub
	return sub, true
}

// fetchOne takes at most one message off a cursor.
//
// ONE, never a batch. Over-asking makes the client wait out its whole timeout
// after the channel is already dry, and a timeout that expires mid-delivery
// strands a delivered-but-unshown message in its ack-wait window. The caller
// loops, and the loop ends on the first miss — which is why a miss of any kind
// is reported as dry rather than as a failure.
func (p *Provider) fetchOne(sub *natsgo.Subscription, wait time.Duration) (provider.Message, bool, error) {
	msgs, err := sub.Fetch(1, natsgo.MaxWait(wait))
	if err != nil || len(msgs) == 0 {
		return nil, false, nil
	}
	return p.hand(msgs[0]), true, nil
}

// deniedCursorSubject is the subject the medium refused this reader ON ITS OWN
// QUEUE'S BACKING OBJECT, if it refused one.
//
// Three families are the whole of a cursor: MAKING it (a CONSUMER CREATE,
// DURABLE CREATE or INFO naming the stream), PULLING from it (MSG.NEXT naming
// the stream) and ACKNOWLEDGING what it handed over ($JS.ACK on the stream).
// The stream's name is matched as a whole token, so nothing refused on another
// object — the topic store above all — is ever read as this reader's mailbox
// being shut.
func (p *Provider) deniedCursorSubject(stream string) (string, bool) {
	for _, subject := range p.refusals() {
		if strings.HasPrefix(subject, "$JS.ACK."+stream+".") {
			return subject, true
		}
		rest, ok := strings.CutPrefix(subject, "$JS.API.CONSUMER.")
		if !ok {
			continue
		}
		for _, token := range strings.Split(rest, ".") {
			if token == stream {
				return subject, true
			}
		}
	}
	return "", false
}

// refusedOwnQueue turns a dry cursor into a failure when the medium answered NO
// rather than NOTHING.
//
// A REFUSAL ON THE CALLER'S OWN QUEUE IS NEVER AN EMPTY MAILBOX. The server
// answers an ungranted JetStream request with silence, so without this the
// create expires, the fetch finds nothing, and a queue still holding mail reads
// as empty and exits 0 — the one state a reader cannot tell apart from having
// lost it. An absent stream records no refusal and stays dry, which is a cold
// endpoint and not a fault.
func (p *Provider) refusedOwnQueue(endpoint, stream string) error {
	subject, ok := p.deniedCursorSubject(stream)
	if !ok {
		return nil
	}
	return fmt.Errorf("cannot read queue.%s: the medium refused %s (permissions) — "+
		"the deployment grants this seat no cursor on its own queue; "+
		"V0 sets no access control, so a broker that refuses here is not one "+
		"loc configured", endpoint, subject)
}

// NextQueued fetches at most one of an endpoint's own messages, unacknowledged.
func (p *Provider) NextQueued(endpoint string, wait time.Duration) (provider.Message, bool, error) {
	if err := p.connect(); err != nil {
		return nil, false, err
	}
	stream := presence.StreamName(endpoint)
	sub, ok := p.puller(stream, queueConsumer(endpoint))
	if !ok {
		return nil, false, p.refusedOwnQueue(endpoint, stream)
	}
	m, got, err := p.fetchOne(sub, wait)
	if err != nil || got {
		return m, got, err
	}
	// A dry fetch is the other half of the same silence: the create may have
	// landed and the pull, or the acknowledgement of what came before it, been
	// the thing refused.
	return nil, false, p.refusedOwnQueue(endpoint, stream)
}

// PeekQueued shows one message without taking it.
//
// It is the ordinary fetch with the ack left undone. The caller never
// acknowledges what a peek returned, and an untaken message goes back when the
// provider closes — so a peek does not even hold it for the ack-wait window.
func (p *Provider) PeekQueued(endpoint string) (provider.Message, bool, error) {
	return p.NextQueued(endpoint, peekWait)
}

// NextTopic fetches at most one message from the reader's own position in the
// rooms. An absent or forbidden topic store reads as a quiet room.
func (p *Provider) NextTopic(reader string, wait time.Duration) (provider.Message, bool, error) {
	if err := p.connect(); err != nil {
		return nil, false, err
	}
	sub, ok := p.puller(topicsStream, topicConsumer(reader))
	if !ok {
		return nil, false, nil
	}
	return p.fetchOne(sub, wait)
}
