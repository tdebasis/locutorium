// Package provider is the seam between the Locutorium's semantics and
// whatever carries a message.
//
// Every operation that touches the medium goes through this interface, and
// exactly one implementation is selected by config at startup. Nothing above
// this line knows what a subject or a stream is.
package provider

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/tdebasis/locutorium/internal/config"
)

// Provider is the medium adapter.
//
// The envelope is passed as bytes, already rendered: a provider transports
// what it is given and never reshapes it.
type Provider interface {
	// SendQueue delivers one envelope to an endpoint's queue and returns only
	// once the store has acknowledged it. A send that is not acknowledged is
	// an error, never a silent drop.
	SendQueue(endpoint string, envelope []byte) error

	// PublishTopic speaks one envelope in a topic, with the same
	// acknowledgement requirement.
	PublishTopic(topic string, envelope []byte) error

	// Topics writes the active topics and their in-window counts.
	Topics(w io.Writer) error

	// Status writes one unread count per endpoint in the registry.
	Status(w io.Writer) error

	// Close releases whatever connection the provider holds.
	Close()
}

// Presence is the EXTENSION a medium implements when it can carry the presence
// model: ephemeral queues that exist exactly while an agent is subscribed, an
// event subject per instance, and a request the instance's supervisor answers.
//
// It is separate from Provider rather than folded into it because a medium is
// allowed to carry messages without carrying presence. A verb that needs these
// asks for them by type assertion and refuses by name when they are absent,
// which is a clearer answer than a Provider full of methods that return "not
// supported".
type Presence interface {
	// CreateQueue brings an endpoint's queue into being. It is idempotent:
	// the queue an already-subscribed endpoint has is the queue it needs.
	CreateQueue(endpoint string) error

	// DeleteQueue destroys it. A queue that is already gone is a success —
	// the caller asked for it to be absent, and it is.
	DeleteQueue(endpoint string) error

	// Queues lists every endpoint that has a queue on this medium.
	//
	// A LISTING THAT CANNOT COMPLETE IS AN ERROR, NEVER A SHORT LIST. The
	// caller is a reconciler: it destroys a queue no row claims, and it
	// recreates a queue no listing showed. A truncated list is therefore not a
	// smaller answer but a WRONG one, and it is wrong in the destructive
	// direction on one pass and the duplicating direction on the other.
	Queues() ([]string, error)

	// QueueExists answers whether an endpoint is attended. A medium that
	// cannot be reached is an ERROR here, never a "no": absence and ignorance
	// are different answers, and only one of them justifies refusing a send.
	QueueExists(endpoint string) (bool, error)

	// Emit speaks one event in an instance's subject, without waiting for the
	// store to acknowledge anything. It is the one path that must be bounded
	// hard, because the caller is an agent's lifecycle hook.
	Emit(instance string, event []byte) error

	// Request asks a question of whoever is listening and returns the reply.
	// Nobody listening is an error, which is the truthful answer.
	Request(subject string, timeout time.Duration) ([]byte, error)

	// Watch follows an instance's events, writing each as it arrives. It
	// blocks, and returns when the connection closes.
	Watch(instance string, w io.Writer) error
}

// Listener is the EXTENSION a medium implements when an endpoint can be TOLD
// that mail has arrived without taking any of it, and can be asked how much is
// waiting.
//
// It is separate from Reader for the reason Presence is separate from
// Provider: a medium may hand a reader its messages without being able to say
// anything the moment one lands. The two methods go together because they
// answer the same question at the two moments it can be asked — as mail
// arrives, and about mail that arrived while nobody was listening.
type Listener interface {
	// WatchQueue calls arrived once for every message that reaches endpoint's
	// queue, and reconnected once for every time the connection to the medium
	// has been re-established — the moment at which arrivals were missed. It
	// CONSUMES NOTHING: watching must never compete with reading for the mail.
	// The returned function stops the watch and releases whatever it holds.
	//
	// arrived is given the SENDER of the message, which is the envelope's
	// `from`. It is the only part of the mail the watcher looks at, and it is
	// looked at rather than taken. A payload this build cannot parse yields an
	// EMPTY sender and still rings: a bell says that mail arrived, and mail in
	// a form nobody here reads is still mail.
	WatchQueue(endpoint string, arrived func(sender string), reconnected func()) (stop func(), err error)

	// Unread is how many messages endpoint has not yet taken. Unlike Status,
	// which prints "?" rather than fail a whole report over one number, this
	// is an ERROR when the figure cannot be had: a caller deciding whether to
	// ring a bell needs "none" and "cannot say" to be different answers.
	Unread(endpoint string) (int, error)
}

// Factory builds a provider. Registration happens in an init function in the
// provider's own package, so selecting one is a matter of importing it.
type Factory func() (Provider, error)

var (
	mu         sync.Mutex
	registered = map[string]Factory{}
)

// Register makes a provider selectable by name. It panics on a duplicate:
// two modules claiming one name is a build mistake, not a runtime condition.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registered[name]; dup {
		panic("provider: duplicate registration for " + name)
	}
	registered[name] = f
}

// Open returns the provider named in config.
func Open(name string) (Provider, error) {
	if name == "" {
		return nil, fmt.Errorf("no provider configured (set 'provider = <name>' in %s/config)", config.Home())
	}
	mu.Lock()
	f, ok := registered[name]
	mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown provider '%s' (not built into this loc)", name)
	}
	return f()
}

// Message is one envelope a reader has been HANDED but has not yet taken.
//
// The two halves are separate on purpose. Reading is the only verb that
// destroys — a queue is retained until its endpoint consumes, so the
// acknowledgement is a delete with no recovery — and keeping the fetch apart
// from the ack is what lets a caller put the bytes in front of a reader
// BEFORE it forgets them. Everything fallible happens in between.
type Message interface {
	// Data is the raw envelope, exactly as it was sent.
	Data() []byte

	// Ack forgets it. Called only once the message has actually been shown.
	Ack() error
}

// Reader is the EXTENSION a medium implements when messages can be taken from
// it, following the same rule as Presence: a medium is allowed to carry
// messages without offering a reader's cursor, and a verb that needs one asks
// by type assertion and refuses by name when it is absent.
//
// The failure direction throughout is DUPLICATE, NEVER LOSS. An interrupted
// read costs a message shown twice, which the protocol's dedupe key exists
// for; the opposite costs mail that nobody ever saw.
type Reader interface {
	// NextQueued fetches at most one queued message for endpoint without
	// acknowledging it. ok=false means the queue is dry (or absent) — never
	// an error, because the caller loops until it is dry and a cold endpoint
	// has simply had no mail. An UNREACHABLE medium is an error: silence and
	// an absent broker are different facts.
	NextQueued(endpoint string, wait time.Duration) (m Message, ok bool, err error)

	// PeekQueued returns one queued message without consuming or reserving it
	// beyond ack-wait.
	PeekQueued(endpoint string) (m Message, ok bool, err error)

	// NextTopic fetches at most one topic message for reader's own cursor; a
	// topic is a room rather than a queue, so one reader taking a message does
	// not take it from anyone else. An absent or forbidden topic store reads
	// as dry, never as an error — the same rule Topics applies.
	NextTopic(reader string, wait time.Duration) (m Message, ok bool, err error)
}
