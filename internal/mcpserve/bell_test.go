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
	cases := map[string]string{
		"workshop.scribe":   "scribe of house workshop",
		"workshop.scribe+2": "scribe of house workshop and 2 more",
		"ada":               "ada",
		"ada+1":             "ada and 1 more",
	}
	for courier, want := range cases {
		if got := bellFrom(courier); got != want {
			t.Errorf("bellFrom(%q) = %q, want %q", courier, got, want)
		}
	}
	// Two seats of one name in two houses must not read alike.
	if bellFrom("workshop.scribe") == bellFrom("library.scribe") {
		t.Errorf("two houses' scribes read alike: %q", bellFrom("workshop.scribe"))
	}
}
