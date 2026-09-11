package nats

import (
	"errors"
	"strings"
	"testing"
)

// THE REFUSAL LEDGER, TESTED WITHOUT A BROKER THAT REFUSES.
//
// V0 configures no access control, so no scratch deployment this suite can
// stand up will refuse anything (R12, 2026-09-09, docs/DECISIONS.md entry 12).
// The code that reads a refusal is still shipped, because a broker `loc` did
// not configure can still answer NO, and the one state a reader must never be
// given is "your mailbox is empty" when the medium said "you may not look".
//
// noteRefusal, deniedCursorSubject and refusedOwnQueue are pure functions over
// the connection's own error line, so the line is what these cases supply.
// That is the same input the client hands the callback; nothing here mocks the
// functions under test.

// violation is the line the NATS client delivers to the error handler.
func violation(verb, subject string) error {
	return errors.New("nats: Permissions Violation for " + verb + " to \"" + subject + "\"")
}

func TestNoteRefusalRecordsOnlyAPermissionsViolationWithASubject(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want []string
	}{
		{"a nil error is not a refusal", nil, nil},
		{"an ordinary error is not a refusal", errors.New("nats: connection closed"), nil},
		{"a violation naming no subject in quotes is not recorded",
			errors.New("nats: Permissions Violation for Publish"), nil},
		{"a refused publish is recorded by its subject",
			violation("Publish", "queue.house.bob"), []string{"queue.house.bob"}},
		{"a refused subscription is recorded by its subject",
			violation("Subscription", "$JS.API.CONSUMER.CREATE.QUEUE_house_ada"),
			[]string{"$JS.API.CONSUMER.CREATE.QUEUE_house_ada"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &Provider{}
			p.noteRefusal(nil, nil, c.err)
			got := p.refusals()
			if len(got) != len(c.want) {
				t.Fatalf("refusals() = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("refusals()[%d] = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// EVERY FAMILY THAT IS A CURSOR, AND NOTHING ELSE. Making the cursor, pulling
// from it and acknowledging what it handed over are the three; a refusal on
// any other object is somebody else's problem and must not close this seat's
// mailbox.
func TestDeniedCursorSubjectMatchesTheSeatsOwnStreamOnly(t *testing.T) {
	const stream = "QUEUE_house_ada"
	for _, c := range []struct {
		name    string
		refused string
		want    bool
	}{
		{"an acknowledgement on this stream", "$JS.ACK." + stream + ".durable.1.1.1.0.0", true},
		{"a consumer create on this stream", "$JS.API.CONSUMER.CREATE." + stream, true},
		{"a durable create on this stream", "$JS.API.CONSUMER.DURABLE.CREATE." + stream + ".ada", true},
		{"a pull on this stream", "$JS.API.CONSUMER.MSG.NEXT." + stream + ".ada", true},
		{"a consumer create on another seat's stream", "$JS.API.CONSUMER.CREATE.QUEUE_house_bob", false},
		{"an acknowledgement on another seat's stream", "$JS.ACK.QUEUE_house_bob.durable.1.1.1.0.0", false},
		{"a refusal on the room store", "$JS.API.CONSUMER.CREATE.TOPICS", false},
		{"a stream request that is not a cursor", "$JS.API.STREAM.INFO." + stream, false},
		{"an ordinary publish", "queue.house.ada", false},
		{"a prefix of the stream name is not the stream", "$JS.API.CONSUMER.CREATE.QUEUE_house_adam", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &Provider{refused: []string{c.refused}}
			got, ok := p.deniedCursorSubject(stream)
			if ok != c.want {
				t.Fatalf("deniedCursorSubject(%q) with %q: ok=%v, want %v", stream, c.refused, ok, c.want)
			}
			if ok && got != c.refused {
				t.Errorf("deniedCursorSubject returned %q, want %q", got, c.refused)
			}
		})
	}
}

func TestDeniedCursorSubjectWithNoRefusalAtAll(t *testing.T) {
	p := &Provider{}
	if got, ok := p.deniedCursorSubject("QUEUE_house_ada"); ok {
		t.Errorf("deniedCursorSubject on a clean connection returned %q, true", got)
	}
}

// A DRY CURSOR IS A FAILURE ONLY WHEN THE MEDIUM SAID NO. Both halves are
// asserted: the silence that is a cold endpoint returns nil, and the refusal
// names the subject so the reader can see WHICH request was shut.
func TestRefusedOwnQueueSpeaksOnlyWhenTheMediumRefused(t *testing.T) {
	const stream = "QUEUE_house_ada"
	if err := (&Provider{}).refusedOwnQueue("house.ada", stream); err != nil {
		t.Errorf("refusedOwnQueue on a clean connection: %v, want nil", err)
	}
	subject := "$JS.API.CONSUMER.CREATE." + stream
	err := (&Provider{refused: []string{subject}}).refusedOwnQueue("house.ada", stream)
	if err == nil {
		t.Fatal("refusedOwnQueue on a refused cursor returned nil")
	}
	for _, want := range []string{"cannot read queue.house.ada", subject, "permissions"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q omits %q", err, want)
		}
	}
}
