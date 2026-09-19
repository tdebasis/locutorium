package nats

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/tdebasis/locutorium/internal/presence"
)

// The presence model in this medium:
//
//	queue.<instance>.<agent>  -> stream QUEUE_<instance>_<agent>, created on
//	                             subscribe and destroyed on unsubscribe, so
//	                             there is no mailbox for an agent that is not
//	                             running and nothing to drain or reconcile
//	presence.<instance>       -> one subject per instance, spoken plainly:
//	                             events are historyless, and a consumer builds
//	                             its own picture from what it hears
//	registry.<instance>       -> a request the instance's supervisor answers

// eventSubjectPrefix is the ONE place the events subject family is spelled, so
// that changing the family is changing this line and nothing else.
//
// EVENTS HAVE THEIR OWN PLANE. `presence.<instance>` is plane-first, exactly as
// `queue.<instance>.<agent>`, `topic.<room>` and `registry.<instance>` are, and
// two things follow from that shape. A grant is per plane — `presence.>` — so
// an operator authorises events without authorising rooms. And no room can ever
// collide with an instance name, which is what it did while events were spoken
// on `topic.<instance>`: the message plane's TOPICS stream captures `topic.>`,
// so every registration was written into room history and handed to the next
// reader's cursor as though someone had said it.
//
// THAT CAPTURE WAS INCIDENTAL, NEVER A RETENTION PROMISE. Nothing captures
// `presence.>` and nothing is meant to. Events stay core NATS and best-effort:
// `watch` is live, and a consumer that needs the past asks the registry rather
// than replaying a history that does not exist (docs/PRESENCE.md §After a host
// restart). Nothing else in this provider publishes here.
const eventSubjectPrefix = "presence."

// emitDial bounds every step of the one verb that must never make its caller
// wait. An adapter runs inside the agent's own lifecycle hook, so anything it
// waits on, the agent waits on: a broker that is down costs the event, not the
// agent's turn.
const emitDial = 1 * time.Second

// watchPoll is how often a follow looks up from the stream to notice that
// its connection has gone. A follow ends when the connection does; it does not
// need to be interrupted to find out.
const watchPoll = 500 * time.Millisecond

// queueConfig is the ONE shape an endpoint's queue has.
//
// Work-queue retention is the delivery guarantee: a message is retained until
// its endpoint has taken it. Memory storage is the inner parlor's whole
// bargain — an agent that stops with mail queued loses it, which is
// appropriate where agents are expected to be present, and is why nothing
// accumulates here.
func queueConfig(endpoint string) *natsgo.StreamConfig {
	return &natsgo.StreamConfig{
		Name:      presence.StreamName(endpoint),
		Subjects:  []string{"queue." + endpoint},
		Retention: natsgo.WorkQueuePolicy,
		Storage:   natsgo.MemoryStorage,
		Replicas:  1,
	}
}

// CreateQueue brings an endpoint's queue into being.
//
// It is idempotent by the store's own rule rather than by a pre-check: a
// create lands on an existing stream only when the configuration is identical,
// so a second subscribe to the same shape succeeds and a create that would
// change the shape is refused. A pre-check would have a gap between the look
// and the create; this has none.
func (p *Provider) CreateQueue(endpoint string) error {
	if err := p.connect(); err != nil {
		return err
	}
	if _, err := p.js.AddStream(queueConfig(endpoint)); err != nil {
		return fmt.Errorf("cannot create the queue for '%s': %v", endpoint, err)
	}
	return nil
}

// DeleteQueue destroys it. A queue that is already gone is a success: the
// caller asked for the endpoint to hold nothing, and it holds nothing.
func (p *Provider) DeleteQueue(endpoint string) error {
	if err := p.connect(); err != nil {
		return err
	}
	err := p.js.DeleteStream(presence.StreamName(endpoint))
	if err == nil || errors.Is(err, natsgo.ErrStreamNotFound) {
		return nil
	}
	return fmt.Errorf("cannot destroy the queue for '%s': %v", endpoint, err)
}

// QueueExists answers whether anyone is attending an endpoint.
//
// An unreachable medium is an ERROR, not a "no". A send refused because
// nobody is there and a send refused because we could not look are different
// facts, and only the first one is about the recipient.
func (p *Provider) QueueExists(endpoint string) (bool, error) {
	if err := p.connect(); err != nil {
		return false, err
	}
	_, err := p.js.StreamInfo(presence.StreamName(endpoint))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, natsgo.ErrStreamNotFound) {
		return false, nil
	}
	return false, fmt.Errorf("cannot tell whether '%s' is attended: %v", endpoint, err)
}

// Emit speaks one event in an instance's subject.
//
// NO ACKNOWLEDGEMENT IS REQUIRED and nothing is retried. This is the sole
// carve-out in a tool that otherwise reports every unacknowledged write: the
// event stream is best-effort by design, a dropped event resolves itself when
// the idle window passes, and the alternative is an agent waiting on a broker
// to tell a display what it is doing. The flush is bounded for the same
// reason — it is what makes a delivered event delivered, not what makes the
// caller wait.
func (p *Provider) Emit(instance string, event []byte) error {
	if err := p.connectWithin(emitDial); err != nil {
		return err
	}
	if err := p.nc.Publish(eventSubjectPrefix+instance, event); err != nil {
		return err
	}
	return p.nc.FlushTimeout(emitDial)
}

// Request asks a question of whoever is listening. Nobody listening is an
// error: an empty list and no list at all are different answers, and a
// command that prints nothing and exits zero cannot tell them apart.
func (p *Provider) Request(subject string, timeout time.Duration) ([]byte, error) {
	if err := p.connect(); err != nil {
		return nil, err
	}
	msg, err := p.nc.Request(subject, nil, timeout)
	if err != nil {
		return nil, err
	}
	return msg.Data, nil
}

// Watch follows an instance's events, writing each payload as it arrives.
//
// THE RAW PAYLOAD IS WHAT IS WRITTEN. A consumer must tolerate fields it does
// not recognise so that events can gain detail without every reader changing,
// and the surest way to tolerate an unknown field is not to have parsed it.
func (p *Provider) Watch(instance string, w io.Writer) error {
	if err := p.connect(); err != nil {
		return err
	}
	sub, err := p.nc.SubscribeSync(eventSubjectPrefix + instance)
	if err != nil {
		return err
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err := p.nc.Flush(); err != nil {
		return err
	}
	for {
		msg, err := sub.NextMsg(watchPoll)
		if err == nil {
			if _, err := fmt.Fprintln(w, string(msg.Data)); err != nil {
				return err
			}
			continue
		}
		if errors.Is(err, natsgo.ErrTimeout) && !p.nc.IsClosed() {
			continue
		}
		// The connection went away. A follow ENDS when there is no longer a
		// stream to follow; it has not failed at anything.
		return nil
	}
}

// queueListTimeout bounds the enumeration. A listing is a paged conversation
// with the store, so it needs a deadline of its own: the reconciler must fail
// rather than wait, because it acts on what the list said.
const queueListTimeout = 5 * time.Second

// endpointOfStream reads a backing-object name back as the endpoint it was
// derived from, and says whether it is one of ours.
//
// The reverse of StreamName's substitution is unambiguous because the endpoint
// character set bars the underscore (endpoint.go §segment), so every
// underscore in the name was a dot. Anything that is not a queue object —
// TOPICS, or whatever else shares the store — is not ours and is not returned.
func endpointOfStream(name string) (string, bool) {
	const prefix = "QUEUE_"
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	endpoint := strings.ReplaceAll(strings.TrimPrefix(name, prefix), "_", ".")
	if presence.ValidEndpoint(endpoint) != nil {
		return "", false
	}
	return endpoint, true
}

// Queues lists every endpoint that has a queue here.
//
// THE ERROR IS CHECKED AFTER THE CHANNEL DRAINS, WHICH IS THE WHOLE POINT. The
// lister pages through the store and closes its channel both when it has
// finished and when it has failed, so a caller that ranges and returns what it
// collected reports a partial listing as a complete one. Reading Err after the
// range is what makes "I could not finish" different from "there are no more".
func (p *Provider) Queues() ([]string, error) {
	if err := p.connect(); err != nil {
		return nil, err
	}
	js, err := jetstream.New(p.nc)
	if err != nil {
		return nil, fmt.Errorf("cannot list the queues: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), queueListTimeout)
	defer cancel()
	lister := js.ListStreams(ctx)
	var out []string
	for info := range lister.Info() {
		if endpoint, ok := endpointOfStream(info.Config.Name); ok {
			out = append(out, endpoint)
		}
	}
	if err := lister.Err(); err != nil {
		return nil, fmt.Errorf("cannot list the queues: %v", err)
	}
	sort.Strings(out)
	return out, nil
}
