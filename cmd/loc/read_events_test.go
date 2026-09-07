package main

// Presence events on the room cursor.
//
// The instance's topic carries TWO different things. Conversation is published
// on topic.<room>, and the presence model publishes its events on
// topic.<instance> — and the message plane's TOPICS stream captures topic.>,
// so one reader's room cursor delivers both. An event is not mail: rendering
// one puts an envelope with no `from`, no `to` and no body in front of a
// reader as though someone had written to them, which is what two seats' join
// events did to the first reader of a live instance.
//
// The deployment, the ACL user set and the seeding helpers belong to the
// presence suite (newPresence, seedQueue, seedTopics, post, safeBuffer, run);
// they are reused rather than restated.

import (
	"io"
	"strings"
	"testing"
)

// A join event on the instance's topic is NOT shown, and the room message
// beside it is — and both are gone from the reader's next read, because
// passing over an event still moves this reader's cursor past it.
func TestReadPassesOverAPresenceEventAndStillShowsTheRoom(t *testing.T) {
	p := newPresence(t)
	p.seedTopics(t)

	// The supervisor subscribes a seat; the join event lands on topic.workshop.
	p.as(t, "host")
	if code, _, errOut := exec("subscribe", e2, "--pid", pidStr(livePid(t)),
		"--type", "acme-cli", "--version", "3.2.0"); code != 0 {
		t.Fatalf("subscribe exit %d, stderr %q", code, errOut)
	}
	// And someone says something in a room.
	p.post(t, "topic.standup", "host", "#standup", "room-canary")

	code, out, errOut := exec("read")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	topics, ok := afterTopics(out)
	if !ok {
		t.Fatalf("no topics heading: %q", out)
	}
	if !strings.Contains(topics, "room-canary") {
		t.Errorf("the room message did not reach the reader: %q", topics)
	}
	if strings.Contains(topics, "? -> ?") {
		t.Errorf("a presence event was rendered as mail: %q", topics)
	}
	if strings.Contains(topics, "agent.subscribe") {
		t.Errorf("a presence event's payload was shown to a reader: %q", topics)
	}

	// The event was passed over, not left: this reader's cursor moved past it.
	code, again, _ := exec("read")
	if code != 0 {
		t.Fatalf("second read exit %d", code)
	}
	if again != "── queue.host ──\n── topics ──\n" {
		t.Errorf("the second read is not the two headings alone: %q", again)
	}
}

// A topic message that is not JSON at all is still SHOWN. The filter names
// presence events and nothing else; mail is never silently dropped, and an
// unreadable line is a thing a reader must be told about.
func TestReadStillShowsAnUnparseableTopicMessage(t *testing.T) {
	p := newPresence(t)
	p.seedTopics(t)
	p.as(t, "host")
	if err := p.admin.Publish("topic.standup", []byte("this is not an envelope")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := p.admin.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	var out safeBuffer
	if code := run([]string{"read"}, &out, io.Discard); code != 0 {
		t.Fatalf("read exited %d", code)
	}
	if !strings.Contains(out.String(), "[unparseable] this is not an envelope") {
		t.Errorf("a room line that could not be parsed was swallowed: %q", out.String())
	}
}

// afterTopics is everything a read printed below the topics heading.
func afterTopics(out string) (string, bool) {
	_, rest, ok := strings.Cut(out, "── topics ──\n")
	return rest, ok
}
