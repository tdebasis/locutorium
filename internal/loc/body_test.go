package loc

import (
	"strings"
	"testing"
)

// The limit is CODEPOINTS. Bytes would be unfair: an emoji is four of them,
// so two messages of visibly equal length would fail differently depending on
// how many status markers they carry.
func TestCheckBodyCountsCodepointsNotBytes(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		refuse bool
	}{
		{"empty", "", false},
		{"at the limit", strings.Repeat("a", MaxBodyChars), false},
		{"one over", strings.Repeat("a", MaxBodyChars+1), true},
		// 3999 emoji is 15996 BYTES and 3999 characters. A byte limit would
		// refuse this; the product's limit does not.
		{"3999 emoji", strings.Repeat("🌍", 3999), false},
		{"4000 emoji", strings.Repeat("🌍", MaxBodyChars), false},
		{"4001 emoji", strings.Repeat("🌍", MaxBodyChars+1), true},
	}
	for _, c := range cases {
		err := CheckBody(c.body)
		if c.refuse && err == nil {
			t.Errorf("%s: accepted, want refusal", c.name)
		}
		if !c.refuse && err != nil {
			t.Errorf("%s: refused (%v), want acceptance", c.name, err)
		}
	}
}

// A refusal that only says "too long" makes the sender guess how much to cut.
func TestCheckBodyNamesBothNumbers(t *testing.T) {
	err := CheckBody(strings.Repeat("🌍", 4321))
	if err == nil {
		t.Fatal("want refusal")
	}
	if !strings.Contains(err.Error(), "4321 characters") {
		t.Errorf("refusal does not name the actual count: %v", err)
	}
	if !strings.Contains(err.Error(), "limit 4000") {
		t.Errorf("refusal does not name the limit: %v", err)
	}
}
