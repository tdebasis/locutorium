package main

// Presence events on the room cursor — the guard for older deployments.
//
// Events are spoken on presence.<instance> now, so nothing a fresh deployment
// lays can put one in a room. These cases hold the OTHER half: a room that was
// filled while events shared the message plane's subject family still carries
// them until the window ages them out, and a reader meeting one must not be
// shown it. An event is not mail: rendering one puts an envelope with no
// `from`, no `to` and no body in front of a reader as though someone had
// written to them, which is what two seats' join events did to the first
// reader of a live instance.
//
// So the events here are put in the room BY THE ADMIN OBSERVER, in the old
// shape, rather than by subscribing a seat — which is what an older
// deployment's rooms actually hold, and what the subject split no longer
// produces.
//
// The deployment, the ACL user set and the seeding helpers belong to the
// presence suite (newPresence, seedQueue, seedTopics, post, safeBuffer, run);
// they are reused rather than restated.

import (
	"io"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

// A join event on the instance's topic is NOT shown, and the room message
// beside it is — and both are gone from the reader's next read, because
// passing over an event still moves this reader's cursor past it.
func TestReadPassesOverAPresenceEventAndStillShowsTheRoom(t *testing.T) {
	p := newPresence(t)
	p.seedTopics(t)

	// A REAL join event, put where an older deployment's room holds it. The
	// supervisor subscribes a seat, the event is caught on the plane it is
	// spoken on now, and THAT PAYLOAD — not a hand-written imitation of one —
	// is republished into the room, which is exactly the shape a room filled
	// before the subject split still carries.
	sub := p.witness(t, "presence.workshop")
	p.as(t, "host")
	if code, _, errOut := exec("subscribe", e2, "--pid", pidStr(livePid(t)),
		"--type", "tmux", "--version", "3.2.0"); code != 0 {
		t.Fatalf("subscribe exit %d, stderr %q", code, errOut)
	}
	events := collect(sub, 2*time.Second)
	if len(events) == 0 {
		t.Fatalf("no join event was emitted, so there is nothing to age into a room")
	}
	if _, err := p.adminJS.Publish("topic.workshop", []byte(events[0])); err != nil {
		t.Fatalf("publish the event into the old room: %v", err)
	}
	// And someone says something in a room.
	p.post(t, "topic.standup", "host", "#standup", "room-canary")

	// THE FIXTURE IS LOAD-BEARING, SO IT IS ASSERTED. Without this the case
	// would pass on an empty room: every check below is a negative, and
	// "no event was rendered" is trivially true where no event exists.
	info, err := p.adminJS.StreamInfo("TOPICS", &natsgo.StreamInfoRequest{SubjectsFilter: "topic.>"})
	if err != nil {
		t.Fatalf("stream info TOPICS: %v", err)
	}
	if n := info.State.Subjects["topic.workshop"]; n != 1 {
		t.Fatalf("the room is not holding the event this case passes over: subjects %v", info.State.Subjects)
	}

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
	if _, err := p.adminJS.Publish("topic.standup", []byte("this is not an envelope")); err != nil {
		t.Fatalf("publish: %v", err)
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
