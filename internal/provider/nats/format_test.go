package nats

import (
	"os"
	"strings"
	"testing"
)

// The two status lines, held against the published transcript. Neither needs a
// server: the shape is the contract, and it is what a reader compares against
// the page.
func TestLineShapesMatchREADME(t *testing.T) {
	b, err := os.ReadFile("../../../README.md")
	if err != nil {
		t.Fatalf("README.md: %v", err)
	}
	page := string(b)

	for _, line := range []string{
		formatTopic("standup", 1),
		formatStatus("ada", "0"),
		formatStatus("bob", "0"),
		formatStatus("carol", "0"),
	} {
		if !strings.Contains(page, line) {
			t.Errorf("README does not contain %q", line)
		}
	}
}

// Every name is padded into the same column, and a name longer than the column
// pushes the count right rather than being cut.
func TestStatusPadding(t *testing.T) {
	if got, want := formatStatus("ada", "3"), "ada          unread: 3\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := formatStatus("a-very-long-endpoint", "3"), "a-very-long-endpoint unread: 3\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
