// Package nats is the NATS + JetStream adapter — the ONLY package in this
// tree that imports a NATS client.
//
// It translates the semantics into the medium:
//
//	queue.<endpoint>  -> stream QUEUE_<endpoint>, work-queue retention
//	                     (retained until the endpoint consumes = the delivery
//	                     guarantee), one durable pull consumer per endpoint
//	topic.<name>      -> one TOPICS stream, max_age = the availability window
//	                     (idle teardown falls out of retention)
//
// V0 has no authentication on the loopback listener, so this package opens
// its connection as nobody: it reads no credential and sends no user name.
// Only the verbs that speak resolve an identity, and they do it for
// themselves.
package nats

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/config"
	"github.com/tdebasis/locutorium/internal/presence"
	"github.com/tdebasis/locutorium/internal/provider"
)

func init() { provider.Register("nats", New) }

// ackTimeout is how long a send waits for the store to acknowledge. It
// matches the shell adapter's `--timeout 3s`; a send that takes longer than
// this is reported as unacknowledged rather than assumed delivered.
const ackTimeout = 3 * time.Second

// Provider holds at most one connection, opened on first use. A CLI process
// performs one operation and exits, so a connection that is never needed is
// never made.
type Provider struct {
	nc *natsgo.Conn
	js natsgo.JetStreamContext

	// pinned is the broker this provider talks to, whatever the file says
	// now. Empty means "ask the config at every use", which is what a CLI
	// verb wants. See At and brokerURL.
	pinned string

	// The read path's cursors — one bound pull subscription per backing
	// object, so a drain that fetches one message at a time does not re-bind
	// per message — and the messages it has handed out that have not been
	// acknowledged. See read.go: an untaken message goes back on Close.
	subs    map[string]*natsgo.Subscription
	unacked []*natsgo.Msg

	// What the deployment has refused this connection, by subject. The
	// server reports a refusal asynchronously, on a goroutine of the
	// client's, so the slice is guarded — see noteRefusal.
	mu      sync.Mutex
	refused []string
}

// New returns an unconnected provider. It reads the deployment's nats_url at
// every use, which is right for a CLI process that does one thing and exits.
func New() (provider.Provider, error) { return &Provider{}, nil }

// At returns an unconnected provider PINNED to url.
//
// THE DAEMON MUST SWEEP THE BROKER IT RUNS. The sweep re-read nats_url on
// every beat, so a config file rewritten under a running daemon moved the
// sweep onto another deployment's broker, where this ledger claims nothing and
// a reap acts on records it did not write. The daemon hands its own server's
// address here once, at boot, and no later edit of the file can move it.
//
// THE PIN IS ON THE PROVIDER AND NOT IN A PACKAGE VARIABLE. runDaemon runs
// in-process inside the test binary, and a package variable would re-point
// every case that came after it.
func At(url string) *Provider { return &Provider{pinned: url} }

// brokerURL is the address this provider dials: the pinned one when it has
// one, and otherwise what the deployment says right now.
func (p *Provider) brokerURL() string {
	if p.pinned != "" {
		return p.pinned
	}
	return config.Value(config.NATSURL)
}

// Close releases the connection, if one was ever opened.
func (p *Provider) Close() {
	if p.nc != nil {
		// Anything the reader was handed and never took goes back FIRST.
		// Closing on an outstanding message would leave it in-flight for the
		// whole ack-wait window, which is the one state a reader cannot tell
		// apart from having lost it.
		p.returnUnacked()
		p.nc.Close()
		p.nc = nil
		p.js = nil
		p.subs = nil
	}
}

// connect opens the one connection.
func (p *Provider) connect() error { return p.connectWithin(ackTimeout) }

// connectWithin is connect with the dial bounded by the caller. Every verb but
// one takes the ordinary timeout; the event emitter takes a much shorter one,
// because it runs inside an agent's lifecycle hook and the agent waits for
// whatever it waits for.
func (p *Provider) connectWithin(dial time.Duration) error {
	if p.nc != nil {
		return nil
	}
	url := p.brokerURL()
	// Dial carries the guard against the default broker under go test; see
	// guard.go and the outage it records.
	nc, err := Dial(url,
		natsgo.Name("loc"),
		natsgo.Timeout(dial),
		// THE ONLY PLACE A REFUSAL IS EVER SAID OUT LOUD. See noteRefusal.
		natsgo.ErrorHandler(p.noteRefusal),
		// A CLI process does one thing and leaves. Reconnect logic would only
		// turn a dead server into a long wait instead of a clear refusal.
		natsgo.NoReconnect(),
	)
	if err != nil {
		// The guard's refusal is said as itself: it names the address on
		// purpose, and "cannot reach the medium" would tell a test the
		// opposite of what happened. The missing config file is said as
		// itself for the same reason. The medium was never asked, and an
		// operator who reads "cannot reach the medium" goes looking for a
		// broker that is running.
		if errors.Is(err, ErrRefusedUnderTest) || errors.Is(err, config.ErrNoDeployment) {
			return err
		}
		// The client's error text never carries the password, but it can
		// carry the URL with a userinfo component if one were ever set there,
		// so callers wrap this rather than print it.
		return errConnect
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return errConnect
	}
	p.nc, p.js = nc, js
	return nil
}

// deniedSubject picks the subject out of the server's refusal line, which
// names it in double quotes after "Publish to" or "Subscription to".
var deniedSubject = regexp.MustCompile(`Permissions Violation for (?:Publish|Subscription) to "([^"]+)"`)

// noteRefusal records one asynchronous refusal, by the subject it names.
//
// A JetStream request the deployment does not permit is answered with an
// error line ON THE CONNECTION and nothing at all on the reply subject: the
// request simply expires. This callback is the only place that error is ever
// available, so the subject is kept for the read path to consult when a cursor
// comes back empty — the difference between "there is no mail" and "you may
// not look", which a caller otherwise cannot tell apart.
//
// It also displaces the client's default handler, which writes the same line
// straight to the process's standard error. A refusal belongs in the verb's
// own refusal, not interleaved with the mail.
func (p *Provider) noteRefusal(_ *natsgo.Conn, _ *natsgo.Subscription, err error) {
	if err == nil || !strings.Contains(err.Error(), "Permissions Violation") {
		return
	}
	m := deniedSubject.FindStringSubmatch(err.Error())
	if len(m) < 2 {
		return
	}
	p.mu.Lock()
	p.refused = append(p.refused, m[1])
	p.mu.Unlock()
}

// refusals is a copy of the subjects refused so far, taken under the lock so
// the read path never walks a slice the callback is appending to.
func (p *Provider) refusals() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.refused...)
}

// errConnect is a sentinel: the caller decides what an unreachable medium
// means for its verb, and no client-supplied text ever reaches the user.
var errConnect = fmt.Errorf("cannot reach the medium")

// request publishes an envelope and requires an acknowledgement carrying a
// sequence number.
//
// A stored message has a seq; anything else — an error document, an empty
// reply, a timeout — means the store did not take it, and the sender is told
// so rather than left believing.
func (p *Provider) request(subject, verb string, envelope []byte) error {
	unacked := fmt.Errorf("%s failed: no acknowledgement from the store (is the server up? try: loc doctor)", verb)
	if err := p.connect(); err != nil {
		if err == errConnect {
			return unacked
		}
		return err
	}
	msg, err := p.nc.Request(subject, envelope, ackTimeout)
	if err != nil {
		return unacked
	}
	if !strings.Contains(string(msg.Data), `"seq"`) {
		return fmt.Errorf("%s failed: unexpected acknowledgement: %s", verb, string(msg.Data))
	}
	return nil
}

// SendQueue delivers one envelope into an endpoint's queue.
func (p *Provider) SendQueue(endpoint string, envelope []byte) error {
	return p.request("queue."+endpoint, "send", envelope)
}

// PublishTopic speaks one envelope in a topic.
func (p *Provider) PublishTopic(topic string, envelope []byte) error {
	return p.request("topic."+topic, "publish", envelope)
}

// Topics writes the active topics, sorted, with their in-window counts.
//
// A medium that cannot be reached, or a stream that does not exist, reads as
// "nothing is being said" rather than as a failure: `loc topics` answers a
// question about the conversation, and an operator diagnoses the server with
// `loc doctor`.
func (p *Provider) Topics(w io.Writer) error {
	// A caller with no credentials is refused outright, before any output: it
	// is the one failure no retry fixes, and printing an empty room at it
	// would answer a question that was never asked.
	if err := p.connect(); err != nil && err != errConnect {
		return err
	}
	subjects := p.topicSubjects()
	names := make([]string, 0, len(subjects))
	for s := range subjects {
		if strings.HasPrefix(s, "topic.") {
			names = append(names, s)
		}
	}
	if len(names) == 0 {
		_, err := fmt.Fprintln(w, "(no active topics)")
		return err
	}
	sort.Strings(names)
	for _, s := range names {
		if _, err := io.WriteString(w, formatTopic(strings.TrimPrefix(s, "topic."), subjects[s])); err != nil {
			return err
		}
	}
	return nil
}

// topicSubjects answers nil for a medium that cannot be reached, and Topics
// prints that as quiet by design. The guard's refusal under go test does NOT
// arrive here as quiet: Topics calls connect first and returns every error
// that is not errConnect, and the refusal is passed through as itself
// (Assayer N4, 2026-09-16).
func (p *Provider) topicSubjects() map[string]uint64 {
	if err := p.connect(); err != nil {
		return nil
	}
	info, err := p.js.StreamInfo("TOPICS", &natsgo.StreamInfoRequest{SubjectsFilter: "topic.>"})
	if err != nil || info == nil {
		return nil
	}
	return info.State.Subjects
}

// Status writes one line per REGISTERED SEAT: how many messages that seat has
// not yet taken.
//
// THE LEDGER, NOT THE `endpoints` FILE. A seat exists because it
// subscribed, and the ledger is the record of that; the static file was a list
// somebody maintained by hand, so it reported seats that had gone and omitted
// seats that had arrived. A row the ledger cannot read is left out of the
// counts and reported by the caller, which knows what an unreadable row means
// for the rest of the report.
//
// A count that cannot be obtained prints "?" rather than failing the verb. A
// status report is most wanted exactly when something is wrong, and a report
// that refuses to print because one number is missing is no report.
func (p *Provider) Status(w io.Writer) error {
	connected := p.connect()
	if connected != nil && connected != errConnect {
		return connected
	}
	rows, _, err := presence.ListAll()
	if err != nil {
		return err
	}
	for _, r := range rows {
		pending := "?"
		if connected == nil {
			pending = p.unread(r.Endpoint)
		}
		if _, err := io.WriteString(w, formatStatus(r.Endpoint, pending)); err != nil {
			return err
		}
	}
	return nil
}

// unread is how many messages an endpoint has not taken, or "?" when the
// number cannot be had.
//
// THE COUNT IS THE QUEUE'S DEPTH. A queue keeps work-queue retention and
// explicit acknowledgements. A read takes one message, prints it, and
// acknowledges it at once, and the acknowledgement deletes it; an interrupted
// read puts back what it held (returnUnacked). So a stored message that is not
// waiting exists in two cases only: for the milliseconds of a read in flight,
// and after a reader died without putting its message back. In the second case
// the broker redelivers the message when the acknowledgement wait runs out.
// THE MAIL IS STILL OWED. The stream counts it. A consumer's pending figure
// does not, so it reports 0 for mail that is coming back.
//
// The stream's count also cannot read low. The broker stores a message before
// it acknowledges the publish (nats-server stream.go: StoreMsg, then the
// PubAck, then the signal to the consumers), and the consumer's count is
// raised on a separate goroutine after the PubAck has gone. A pending count
// can therefore be served short of an acknowledged send, and the broker
// corrects an over-count but never an under-count. That is the zero in #123.
//
// The backing object is named by substitution, because a subject may carry a
// dot where a durable object's name may not.
func (p *Provider) unread(endpoint string) string {
	if si, err := p.js.StreamInfo(presence.StreamName(endpoint)); err == nil && si != nil {
		return fmt.Sprintf("%d", si.State.Msgs)
	}
	return "?"
}

// formatTopic and formatStatus are the two lines these verbs print. They are
// named rather than inlined so that a test can hold them against the page
// without a server: the transcript in the README is the contract, and the
// suite fails if the page and the tool ever disagree.
func formatTopic(name string, n uint64) string {
	return fmt.Sprintf("#%s  (%d in window)\n", name, n)
}

func formatStatus(endpoint, pending string) string {
	return fmt.Sprintf("%-12s unread: %s\n", endpoint, pending)
}
