package presence

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// TSLayout is RFC 3339, UTC, at millisecond precision — the resolution the
// event schema fixes. It is spelled out rather than borrowed from time.RFC3339,
// which renders fractional seconds only when the clock happens to have them.
const TSLayout = "2006-01-02T15:04:05.000Z"

// The taxonomy. Event kinds are <subject>.<verb>, and there are two subjects:
// an agent's membership, and its work. A genuinely different thing being
// reported earns a new kind; the same thing happening for a different cause
// earns a new reason instead.
const (
	KindSubscribe     = "agent.subscribe"
	KindUnsubscribe   = "agent.unsubscribe"
	KindActivityStart = "activity.start"
	KindActivityEnd   = "activity.end"
	KindToolPre       = "tool.pre"
	KindToolPost      = "tool.post"
)

// Kinds is the whole taxonomy, in the order the schema lists it.
func Kinds() []string {
	return []string{KindSubscribe, KindUnsubscribe, KindActivityStart, KindActivityEnd, KindToolPre, KindToolPost}
}

// ValidKind reports whether kind is one this build knows.
func ValidKind(kind string) bool {
	for _, k := range Kinds() {
		if k == kind {
			return true
		}
	}
	return false
}

// RecordsActivity reports whether an event of this kind moves an agent's
// activity state. Membership events do not: leaving is a registration fact,
// not a working one.
func RecordsActivity(kind string) bool {
	switch kind {
	case KindActivityStart, KindActivityEnd, KindToolPre, KindToolPost:
		return true
	}
	return false
}

// Agent is what kind of program an endpoint holds. Registering the type once
// is what lets every later event stay light, and it is where a consumer's
// per-type allowances belong.
type Agent struct {
	Type    string `json:"type"`
	Version string `json:"version"`
}

// Process is the identity of a running agent instance. The pair is the
// identity; the number alone is not, because operating systems reuse them.
type Process struct {
	PID     int    `json:"pid"`
	Started string `json:"started"`
}

// Display is what a person or a display should call this agent.
type Display struct {
	Name string `json:"name"`
}

// Event is one thing that happened on the bus.
//
// THE FIELD ORDER IS THE WIRE FORMAT. encoding/json emits struct fields in
// declaration order, and the schema fixes the common envelope — id, ts, kind,
// endpoint — ahead of everything a particular kind adds. Do not tidy these
// lines. Everything after the envelope is omitted when empty, because only the
// join event carries the full picture and every later event is four fields.
type Event struct {
	ID       string   `json:"id"`
	TS       string   `json:"ts"`
	Kind     string   `json:"kind"`
	Endpoint string   `json:"endpoint"`
	Instance string   `json:"instance,omitempty"`
	Agent    *Agent   `json:"agent,omitempty"`
	Process  *Process `json:"process,omitempty"`
	Display  *Display `json:"display,omitempty"`
	Cwd      string   `json:"cwd,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Tool     string   `json:"tool,omitempty"`
	Refs     []string `json:"refs,omitempty"`
}

// NewEvent builds an event stamped now. A caller who knows when the thing
// actually happened overwrites TS with that: the timestamp is the emitter's
// statement about the moment, not the publisher's about the send.
func NewEvent(kind, endpoint string) Event {
	return Event{ID: NewID(), TS: Now(), Kind: kind, Endpoint: endpoint}
}

// Now is the current moment in the schema's stamp.
func Now() string { return time.Now().UTC().Format(TSLayout) }

// Marshal renders the event compactly — no space after ':' or ','.
//
// HTML escaping is turned OFF, the same convention the message envelope keeps:
// Go escapes '<', '>' and '&' by default, which is a browser-safety habit
// rather than JSON, and a working directory that contains one should look the
// same whichever emitter published it.
func (e Event) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	// Encode terminates with a newline; the wire form is one line, unterminated.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// NewID returns an event id: a short prefix, so a person reading a stream can
// tell what they are looking at, and 64 bits of randomness, which is what
// makes it unique enough to deduplicate on and to point a reference at.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any platform this runs on; if it ever
		// did, a predictable id would be worse than a crash.
		panic("presence: no entropy for an event id: " + err.Error())
	}
	return "ev_" + hex.EncodeToString(b[:])
}
