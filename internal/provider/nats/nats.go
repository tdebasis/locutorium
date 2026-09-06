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
// Credentials live at $LOC_HOME/creds/<identity>, mode 0600. They are read
// here and handed to the client, and they are never printed, logged, or put
// in an error message.
package nats

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/config"
	"github.com/tdebasis/locutorium/internal/loc"
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
}

// New returns an unconnected provider.
func New() (provider.Provider, error) { return &Provider{}, nil }

// Close releases the connection, if one was ever opened.
func (p *Provider) Close() {
	if p.nc != nil {
		p.nc.Close()
		p.nc = nil
		p.js = nil
	}
}

// connect opens the one connection, as the caller's own identity.
//
// The identity is resolved here and not passed in, so that every path to the
// medium is authenticated as the same endpoint the semantics layer thinks is
// speaking.
func (p *Provider) connect() error { return p.connectWithin(ackTimeout) }

// connectWithin is connect with the dial bounded by the caller. Every verb but
// one takes the ordinary timeout; the event emitter takes a much shorter one,
// because it runs inside an agent's lifecycle hook and the agent waits for
// whatever it waits for.
func (p *Provider) connectWithin(dial time.Duration) error {
	if p.nc != nil {
		return nil
	}
	id, err := loc.Identity()
	if err != nil {
		return err
	}
	credfile := filepath.Join(config.Home(), "creds", id)
	secret, err := os.ReadFile(credfile)
	if err != nil {
		// The path, never the content.
		return fmt.Errorf("no credentials for '%s' at %s", id, credfile)
	}
	// Written with printf, so there is no trailing newline to strip; a hand
	// edited file may have one, and the shell's $(cat ...) would have dropped
	// it too.
	pass := strings.TrimRight(string(secret), "\r\n")

	url := config.Get("nats_url", "nats://127.0.0.1:4222")
	nc, err := natsgo.Connect(url,
		natsgo.UserInfo(id, pass),
		natsgo.Name("loc"),
		natsgo.Timeout(dial),
		// A CLI process does one thing and leaves. Reconnect logic would only
		// turn a dead server into a long wait instead of a clear refusal.
		natsgo.NoReconnect(),
	)
	if err != nil {
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

// Status writes one line per endpoint in the registry: how many messages that
// endpoint has not yet taken.
//
// A count that cannot be obtained prints "?" rather than failing the verb. A
// status report is most wanted exactly when something is wrong, and a report
// that refuses to print because one number is missing is no report.
func (p *Provider) Status(w io.Writer) error {
	connected := p.connect()
	if connected != nil && connected != errConnect {
		return connected
	}
	for _, e := range loc.Endpoints() {
		pending := "?"
		if connected == nil {
			if ci, err := p.js.ConsumerInfo("QUEUE_"+e, e); err == nil && ci != nil {
				pending = fmt.Sprintf("%d", ci.NumPending)
			}
		}
		if _, err := io.WriteString(w, formatStatus(e, pending)); err != nil {
			return err
		}
	}
	return nil
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
