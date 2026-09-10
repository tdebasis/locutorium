package main

// Presence events have their own subject family.
//
// Lifecycle and activity events used to be spoken on `topic.<instance>`, which
// falls inside the message plane's own subject space: the TOPICS stream
// captures `topic.>`, so every registration was written into room history and
// handed to the next reader's cursor as though someone had said it. That
// capture was INCIDENTAL — a consequence of sharing a subject family with the
// rooms, never a promise that events are retained.
//
// Events now have their own plane, `presence.<instance>`, beside the queue and
// the room rather than inside either. These cases hold the separation at the
// SUBJECT level, which is where it belongs: the read-side skip is a guard for
// readers on older deployments (cmd/loc/read.go), and nothing here may lean on
// it. So the room stream is asked directly whether it holds anything at all.
//
// The deployment, the ACL user set and the seeding helpers belong to the
// presence suite (newPresence, seedTopics, witness, collect, e2, livePid,
// pidStr); they are reused rather than restated.

import (
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

// subscribing = the supervisor registers a seat, the one act that emits the
// heavy join event. Returned so each case reads as its own assertion rather
// than as three lines of setup.
func subscribing(t *testing.T, p *presence, endpoint string) {
	t.Helper()
	p.as(t, "host")
	if code, _, errOut := exec("subscribe", endpoint, "--pid", pidStr(livePid(t)),
		"--type", "tmux", "--version", "3.2.0"); code != 0 {
		t.Fatalf("subscribe exit %d, stderr %q", code, errOut)
	}
}

// roomStream is what the message plane is holding: the number of messages in
// TOPICS and the subjects they arrived on. Asked of the broker through the
// admin observer, never through the code under test.
func roomStream(t *testing.T, p *presence) (uint64, map[string]uint64) {
	t.Helper()
	info, err := p.adminJS.StreamInfo("TOPICS", &natsgo.StreamInfoRequest{SubjectsFilter: "topic.>"})
	if err != nil {
		t.Fatalf("stream info TOPICS: %v", err)
	}
	return info.State.Msgs, info.State.Subjects
}

// A join event is delivered on the instance's OWN subject, and the room stream
// never hears of it.
func TestAJoinEventIsSpokenOnThePresencePlaneAndNotInTheRoom(t *testing.T) {
	p := newPresence(t)
	p.seedTopics(t)

	// The witness is opened BEFORE the verb runs: events are historyless, so
	// one that is not caught as it flies cannot be found afterwards.
	sub := p.witness(t, "presence.workshop")
	subscribing(t, p, e2)

	events := collect(sub, 2*time.Second)
	if !strings.Contains(strings.Join(events, "\n"), "agent.subscribe") {
		t.Fatalf("no agent.subscribe event arrived on presence.workshop; saw %v", events)
	}

	msgs, subjects := roomStream(t, p)
	if n, ok := subjects["topic.workshop"]; ok {
		t.Errorf("the room stream captured the instance's events on topic.workshop (%d message(s)); "+
			"events are not room history", n)
	}
	if msgs != 0 {
		t.Errorf("the room stream holds %d message(s) after a registration and nothing said; "+
			"want 0, on subjects %v", msgs, subjects)
	}
}

// A reader who has been told nothing is shown nothing — and the proof is the
// stream, not the filter. Three seats register; the room is empty, so the read
// is the two headings with no event for the read-side skip to pass over.
func TestARegistrationIsNotRoomHistoryForTheNextReader(t *testing.T) {
	p := newPresence(t)
	p.seedTopics(t)
	for _, e := range []string{e1, e2, e3} {
		subscribing(t, p, e)
	}

	// WITHOUT RELYING ON THE SKIP: there is nothing in the room to skip.
	if msgs, subjects := roomStream(t, p); msgs != 0 {
		t.Fatalf("three registrations put %d message(s) in the room stream, on %v; want none",
			msgs, subjects)
	}

	code, out, errOut := exec("read")
	if code != 0 || errOut != "" {
		t.Fatalf("read exit %d, stderr %q", code, errOut)
	}
	if out != "── queue.host ──\n── topics ──\n" {
		t.Errorf("the reading is not the two headings alone: %q", out)
	}
}

// A follow hears the instance it was asked for and no other. The plane is
// per-instance, exactly as the old one was.
func TestAFollowOnThePresencePlaneHearsOnlyItsOwnInstance(t *testing.T) {
	p := newPresence(t)
	p.seedTopics(t)

	sub := p.witness(t, "presence.workshop")
	other := p.witness(t, "presence.atelier")
	subscribing(t, p, e2) // workshop.clerk

	if !strings.Contains(strings.Join(collect(sub, 2*time.Second), "\n"), e2) {
		t.Errorf("the workshop follow did not hear its own instance's join event")
	}
	if seen := collect(other, 500*time.Millisecond); len(seen) != 0 {
		t.Errorf("the atelier follow heard the workshop's event: %v", seen)
	}
}

// A DEPLOYMENT WHOSE GRANTS PREDATE THE SPLIT still subscribes. Its users were
// generated with `topic.>` and no rights on the events plane, so the server
// refuses the join event's publish. Emit is best-effort by design: the event is
// lost, the violation is the server's to log, and the seat is registered and
// reading its mail. The alternative — a subscribe that fails because a display
// could not be told about it — is the thing the carve-out exists to prevent.
func TestASeatWithOldGrantsStillSubscribesWhenItsEventIsRefused(t *testing.T) {
	p := newPresence(t)
	p.seedTopics(t)

	p.as(t, eOld)
	if code, _, errOut := exec("subscribe", eOld, "--pid", pidStr(livePid(t)),
		"--type", "tmux", "--version", "3.2.0"); code != 0 {
		t.Fatalf("a refused event publish failed the subscribe: exit %d, stderr %q", code, errOut)
	}
	if !p.qexists(qOld) {
		t.Errorf("the seat's queue was not created, so the subscribe did not take effect")
	}
	// And the refused event went nowhere at all — not to the room either.
	if msgs, subjects := roomStream(t, p); msgs != 0 {
		t.Errorf("the refused event landed in the room stream: %d message(s) on %v", msgs, subjects)
	}
}
