package loc

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The wire form, frozen. Captured from the shell implementation's
// loc_envelope on 2026-09-05 and reduced to its shape: the key order, the
// separators, the null and the empty list.
func TestEnvelopeWireForm(t *testing.T) {
	got, err := NewEnvelope("ada", "bob", "msg", "hi").Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)

	// Compact: no space after ':' or ','.
	if strings.Contains(s, ": ") || strings.Contains(s, ", ") {
		t.Errorf("envelope is not compact: %s", s)
	}
	// One line, unterminated.
	if strings.Contains(s, "\n") {
		t.Errorf("envelope must be one line: %q", s)
	}
	// The key order IS the format.
	wantOrder := []string{`"id":`, `"ts":`, `"from":`, `"to":`, `"kind":`, `"body":`, `"reply_to":`, `"refs":`}
	at := 0
	for _, k := range wantOrder {
		i := strings.Index(s[at:], k)
		if i < 0 {
			t.Fatalf("key %s missing or out of order in %s", k, s)
		}
		at += i + len(k)
	}
	if !strings.Contains(s, `"reply_to":null`) {
		t.Errorf("reply_to must be null: %s", s)
	}
	// [] and never null: a reader that must special-case a missing list is a
	// reader that will one day forget to.
	if !strings.Contains(s, `"refs":[]`) {
		t.Errorf("refs must be an empty array: %s", s)
	}
	if !strings.HasSuffix(s, `"refs":[]}`) {
		t.Errorf("refs must be last: %s", s)
	}
}

var (
	uuid4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	tsRe    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)
)

func TestEnvelopeIDAndTimestamp(t *testing.T) {
	var e struct {
		ID string `json:"id"`
		TS string `json:"ts"`
	}
	b, err := NewEnvelope("ada", "bob", "msg", "hi").Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatal(err)
	}
	if !uuid4Re.MatchString(e.ID) {
		t.Errorf("id is not a v4 UUID: %q", e.ID)
	}
	// SECONDS precision, UTC, and no fractional part: Go's RFC3339 constant
	// emits fractions whenever the clock has them, which is why the layout is
	// written out by hand.
	if !tsRe.MatchString(e.TS) {
		t.Errorf("ts is not RFC3339 at seconds precision: %q", e.TS)
	}
	parsed, err := time.Parse(tsLayout, e.TS)
	if err != nil {
		t.Fatalf("ts does not parse: %v", err)
	}
	if d := time.Since(parsed); d < -2*time.Second || d > time.Minute {
		t.Errorf("ts is not now: %v ago", d)
	}
}

func TestEnvelopeIDsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := uuid4()
		if seen[id] {
			t.Fatalf("duplicate id after %d draws: %s", i, id)
		}
		seen[id] = true
	}
}

// Go escapes '<', '>' and '&' by default. That is a browser habit, not JSON,
// and the shell implementation does not do it.
func TestEnvelopeDoesNotEscapeHTML(t *testing.T) {
	b, err := NewEnvelope("a", "b", "msg", "see <path> & note").Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"body":"see <path> & note"`) {
		t.Errorf("body was escaped: %s", b)
	}
}
