package mcpserve

import (
	"strings"
	"testing"
)

// The wire value reaches a command line, and a sender field is forgeable, so a
// `from` that looks like a flag must never become one. The Assayer measured the
// unguarded form: workshop.-p gave `-n -p`, workshop.--allowedTools gave
// `-n --allowedTools`, and a newline gave a label carrying a line break.
func TestCourierNameRefusesAnythingThatIsNotASeatName(t *testing.T) {
	refused := []string{
		"workshop.-p",
		"workshop.--allowedTools",
		"workshop.--dangerously-skip-permissions",
		"workshop.scribe\nsecond line",
		"workshop.scribe with spaces",
		"workshop." + strings.Repeat("x", 33),
		"workshop.",
	}
	for _, from := range refused {
		if got := courierName([]string{from}); got != defaultCourierName {
			t.Errorf("courierName(%q) = %q; a value that is not a seat name must fall back to %q",
				from, got, defaultCourierName)
		}
	}

	// CONTROL: the ordinary case still names the seat, or this test passes by
	// refusing everything.
	if got := courierName([]string{"workshop.scribe"}); got != "workshop.scribe" {
		t.Errorf("courierName(workshop.scribe) = %q, want workshop.scribe", got)
	}
	if got := courierName([]string{"workshop.scribe", "workshop.binder"}); got != "workshop.scribe+1" {
		t.Errorf("two senders = %q, want workshop.scribe+1", got)
	}
}

// THE BELL NAMES THE HOUSE, ALWAYS. Two houses on one bus can each have a seat
// called scribe, and a line that said "from scribe" read the same for both. The
// house is named every time, never only when it differs from the reader's: a
// rule with two cases asks the reader to know which one they are in.
func TestBellFromNamesTheSeatAndItsHouse(t *testing.T) {
	cases := []struct {
		senders []string
		want    string
	}{
		{[]string{"workshop.scribe"}, "scribe of house workshop"},
		{[]string{"workshop.scribe", "workshop.binder", "workshop.warden"}, "scribe of house workshop and 2 more"},
		{[]string{"ada"}, "ada"},
		{[]string{"ada", "bob"}, "ada and 1 more"},
		{nil, ""},
		{[]string{"workshop.-p"}, ""},
		{[]string{"workshop.scribe\nsecond line"}, ""},
	}
	for _, c := range cases {
		if got := bellFrom(c.senders); got != c.want {
			t.Errorf("bellFrom(%q) = %q, want %q", c.senders, got, c.want)
		}
	}
	// Two seats of one name in two houses must not read alike.
	if bellFrom([]string{"workshop.scribe"}) == bellFrom([]string{"library.scribe"}) {
		t.Errorf("two houses' scribes read alike: %q", bellFrom([]string{"workshop.scribe"}))
	}
}

// A LONG NAME KEEPS ITS SENDER. The courier label is capped at 32 characters
// and an endpoint is not, so a full name that does not fit the label must not
// cost the bell its sender: the line still names seat and house, and the label
// falls back to the seat's own part, which is what it carried before.
func TestBellLongNamesKeepTheirSender(t *testing.T) {
	long := "research-lab.literature-reviewer-x" // 34 characters
	if len(long) <= 32 {
		t.Fatalf("the fixture must be longer than the label cap; got %d", len(long))
	}
	if got, want := bellFrom([]string{long}), "literature-reviewer-x of house research-lab"; got != want {
		t.Errorf("bellFrom(%q) = %q, want %q", long, got, want)
	}
	if got := courierName([]string{long}); got != "literature-reviewer-x" {
		t.Errorf("courierName(%q) = %q, want the seat's own part", long, got)
	}
	// When even the seat's own part does not fit, the label falls back and the
	// line still names the sender.
	longer := "lab." + strings.Repeat("x", 33)
	if got := courierName([]string{longer}); got != defaultCourierName {
		t.Errorf("courierName(%q) = %q, want %q", longer, got, defaultCourierName)
	}
	if got := bellFrom([]string{longer}); got == "" {
		t.Errorf("bellFrom(%q) = \"\"; the line must still name the sender", longer)
	}
}
