package loc

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Envelope is the wire form of one message.
//
// THE FIELD ORDER IS THE WIRE FORMAT. encoding/json emits struct fields in
// declaration order, and the protocol fixes that order as
// id, ts, from, to, kind, body, reply_to, refs. Reordering these lines
// changes the bytes on the medium; do not tidy them.
type Envelope struct {
	ID      string   `json:"id"`
	TS      string   `json:"ts"`
	From    string   `json:"from"`
	To      string   `json:"to"`
	Kind    string   `json:"kind"`
	Body    string   `json:"body"`
	ReplyTo *string  `json:"reply_to"`
	Refs    []string `json:"refs"`
}

// tsLayout is RFC3339 at SECONDS precision. Go's time.RFC3339 renders
// fractional seconds when the clock has them; the protocol does not carry
// them, so the layout is written out explicitly rather than borrowed.
const tsLayout = "2006-01-02T15:04:05Z"

// NewEnvelope builds an envelope stamped now, in UTC.
func NewEnvelope(from, to, kind, body string) Envelope {
	return Envelope{
		ID:   uuid4(),
		TS:   time.Now().UTC().Format(tsLayout),
		From: from,
		To:   to,
		Kind: kind,
		Body: body,
		// null and [], never null and null: a reader that must special-case a
		// missing list is a reader that will one day forget to.
		ReplyTo: nil,
		Refs:    []string{},
	}
}

// Marshal renders the envelope compactly — no space after ':' or ','.
//
// HTML escaping is turned OFF. Go escapes '<', '>' and '&' as \u003c and
// friends by default, which is a browser-safety habit, not JSON; the shell
// implementation emits them literally, and a body that says "see <path>"
// should look the same whichever binary sent it.
func (e Envelope) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	// Encode terminates with a newline; the wire form is one line, unterminated.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// uuid4 returns a random (version 4) UUID in canonical form.
func uuid4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any platform this runs on; if it ever
		// did, a predictable id would be worse than a crash.
		panic("locutorium: no entropy for a message id: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:])
}

// ParseEnvelope reads one envelope back off the wire.
//
// A reader holds raw bytes, never the struct the sender built: the medium
// carries Marshal's output and nothing else. The record needs the `id` out of
// those bytes to join a `read` line to the `sent` line for the same message,
// and a reader that picked the id out with a string search would find one
// inside a body that quotes an envelope.
//
// A payload that is not an object is an error rather than a zero Envelope,
// because an empty id is a join key that matches every other empty id in the
// file.
func ParseEnvelope(raw []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return Envelope{}, err
	}
	return e, nil
}
