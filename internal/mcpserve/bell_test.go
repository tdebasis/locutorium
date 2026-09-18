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
	if got := courierName([]string{"workshop.scribe"}); got != "scribe" {
		t.Errorf("courierName(workshop.scribe) = %q, want scribe", got)
	}
	if got := courierName([]string{"workshop.scribe", "workshop.binder"}); got != "scribe+1" {
		t.Errorf("two senders = %q, want scribe+1", got)
	}
}
