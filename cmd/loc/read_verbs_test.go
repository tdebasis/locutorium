package main

// The read verb, end-to-end against a real broker.
//
// MESSAGE PLANE, not presence — the same separation read_test.go states, kept
// so the presence count stays clean. What is pinned here is the one thing a
// reader depends on and cannot check for itself: that a message is shown
// before it is forgotten, and that nothing is forgotten that was not shown.
//
// The deployment, the ACL user set and the seeding helpers are the presence
// suite's (newPresence, seedQueue, witness, safeBuffer, errAfterWriter); they
// are reused rather than restated, so a change to the model's access control
// reaches these cases too.

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/loctest"
)

// seedTopics stands the rooms up by hand, as `doctor --init` would have. Its
// retention is LIMITS, not work-queue: a topic is heard by everyone attending,
// so no reader's cursor may remove what another has not read yet.
func (p *presence) seedTopics(t *testing.T) {
	t.Helper()
	if _, err := p.adminJS.AddStream(&natsgo.StreamConfig{
		Name:      "TOPICS",
		Subjects:  []string{"topic.>"},
		Retention: natsgo.LimitsPolicy,
		Storage:   natsgo.MemoryStorage,
		Replicas:  1,
	}); err != nil {
		t.Fatalf("seed TOPICS: %v", err)
	}
}

// post puts one envelope on a subject as the admin observer, so nothing under
// test is used to set up what is under test.
//
// THE PUBLISH GOES THROUGH THE STREAM, so that the store is done before this
// call returns. A core publish and a flush prove only that the server read the
// PUB: the stream stores on its own goroutine, so a count read after this call
// would be asserted against an order nothing established. The subject is the
// same one, so a core subscriber still sees the arrival.
//
// The same change was made in internal/provider/nats/listen_test.go for the
// same reason. This helper was missed by it, and a count assertion behind it
// failed in CI on a pull request that changed two documentation files.
func (p *presence) post(t *testing.T, subject, from, to, body string) {
	t.Helper()
	env := `{"id":"` + body + `","ts":"2026-01-14T09:00:00.000Z","from":"` + from +
		`","to":"` + to + `","kind":"msg","body":"` + body + `"}`
	if _, err := p.adminJS.Publish(subject, []byte(env)); err != nil {
		t.Fatalf("publish %s: %v", subject, err)
	}
}

// ------------------------------------------------------------ the backlog

// A dormant endpoint's whole backlog arrives on one read, in the order it was
// sent — and is gone from the next one. Handed over exactly once is the queue's
// guarantee; showing it twice would be a different tool.
func TestReadShowsTheBacklogInOrderAndTakesItOnce(t *testing.T) {
	p := newPresence(t)
	p.as(t, e1)
	p.seedQueue(t, q1, "queue."+e1)
	p.post(t, "queue."+e1, "host", e1, "message-one")
	p.post(t, "queue."+e1, "host", e1, "message-two")

	code, out, errOut := exec("read")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.HasPrefix(out, "── queue."+e1+" ──\n") {
		t.Errorf("the reading is not headed by the queue it is: %q", out)
	}
	one, two := strings.Index(out, "message-one"), strings.Index(out, "message-two")
	if one < 0 || two < 0 {
		t.Fatalf("the backlog did not arrive whole: %q", out)
	}
	if one > two {
		t.Errorf("out of order — a queue is per-sender FIFO: %q", out)
	}
	if !strings.Contains(out, "── topics ──\n") {
		t.Errorf("no topics heading: %q", out)
	}

	code, again, _ := exec("read")
	if code != 0 {
		t.Fatalf("second read exit %d", code)
	}
	if strings.Contains(again, "message-one") || strings.Contains(again, "message-two") {
		t.Errorf("a message was handed over twice: %q", again)
	}
}

// An endpoint with no prior state at all reads cleanly: the two headings and
// nothing else, and a zero exit. A cold mailbox is not a fault.
func TestReadOnAColdEndpointIsExactlyTheTwoHeadings(t *testing.T) {
	p := newPresence(t)
	p.as(t, e2)

	code, out, errOut := exec("read")
	want := "── queue." + e2 + " ──\n── topics ──\n"
	assertResult(t, code, out, errOut, 0, want, "")
}

// A read finishes even where there are no rooms to read: an absent topic store
// is a quiet house, not a broken one.
func TestReadFinishesWithNoTopicStore(t *testing.T) {
	p := newPresence(t)
	p.as(t, e1)
	p.seedQueue(t, q1, "queue."+e1)
	p.post(t, "queue."+e1, "host", e1, "message-one")

	code, out, errOut := exec("read")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "message-one") || !strings.HasSuffix(out, "── topics ──\n") {
		t.Errorf("got %q", out)
	}
}

// ------------------------------------------------------------------ peeking

// --peek shows at most one and takes nothing — and says plainly that the rooms
// are not shown, rather than implying an empty one.
func TestPeekShowsOneAndTakesNothing(t *testing.T) {
	p := newPresence(t)
	p.as(t, e1)
	p.seedQueue(t, q1, "queue."+e1)
	p.post(t, "queue."+e1, "host", e1, "peek-canary")

	code, out, errOut := exec("read", "--peek")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "peek-canary") {
		t.Errorf("a peek showed nothing: %q", out)
	}
	if !strings.HasSuffix(out, "── topics ── (not shown: --peek never consumes, "+
		"and topic reads cannot yet be non-consuming)\n") {
		t.Errorf("a peek must say why the rooms are absent, not imply they are empty: %q", out)
	}

	code, after, _ := exec("read")
	if code != 0 {
		t.Fatalf("the read after a peek exited %d", code)
	}
	if !strings.Contains(after, "peek-canary") {
		t.Errorf("a peek consumed the message it only looked at: %q", after)
	}
}

// Anything that is not exactly --peek is REFUSED. The shell tool silently
// ignores it and performs a normal, consuming read, so a misspelt peek
// destroys the backlog it was meant to leave alone; this build will not.
func TestReadRefusesAnythingButPeek(t *testing.T) {
	for _, args := range [][]string{
		{"read", "--pekk"},
		{"read", "--peek", "--peek"},
		{"read", "--peek", "extra"},
		{"read", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			newDeployment(t, "ada")
			code, out, errOut := exec(args...)
			assertResult(t, code, out, errOut, 1, usage, "")
		})
	}
}

// ------------------------------------------------------------- who sees what

// Two instances' agents of the SAME NAME never see each other's mail. The
// endpoint is the whole address, and the backing objects are injective.
func TestReadKeepsTwoInstancesApart(t *testing.T) {
	p := newPresence(t)
	p.seedQueue(t, q1, "queue."+e1)
	p.seedQueue(t, q3, "queue."+e3)
	p.post(t, "queue."+e1, "host", e1, "workshop-mail")

	p.as(t, e3)
	code, other, errOut := exec("read")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if strings.Contains(other, "workshop-mail") {
		t.Errorf("one instance's scribe read another's mail: %q", other)
	}

	p.as(t, e1)
	code, mine, _ := exec("read")
	if code != 0 || !strings.Contains(mine, "workshop-mail") {
		t.Errorf("exit %d; the addressed endpoint did not get it: %q", code, mine)
	}
}

// A room is not a queue. Every reader keeps its own position in it, so one
// reader taking a message does not take it from anyone else.
func TestEveryReaderHasItsOwnPositionInARoom(t *testing.T) {
	p := newPresence(t)
	p.seedTopics(t)
	p.post(t, "topic.standup", "host", "#standup", "please look")

	for _, who := range []string{"host", "admin"} {
		p.as(t, who)
		code, out, errOut := exec("read")
		if code != 0 || errOut != "" {
			t.Fatalf("%s: exit %d, stderr %q", who, code, errOut)
		}
		if !strings.Contains(out, "please look") {
			t.Errorf("%s did not hear what was said in the room: %q", who, out)
		}
	}
}

// ------------------------------------------------------------ loss and noise

// The frozen case says a failed render must not consume. This says the same
// thing from the other side: the message that was NOT reached is still there
// too, so a read interrupted after one is not a partial deletion.
func TestAReadInterruptedMidBacklogKeepsEverythingItDidNotShow(t *testing.T) {
	p := newPresence(t)
	p.as(t, e1)
	p.seedQueue(t, q1, "queue."+e1)
	p.post(t, "queue."+e1, "host", e1, "message-one")
	p.post(t, "queue."+e1, "host", e1, "message-two")

	// The heading is written, the first envelope is not.
	_ = run([]string{"read"}, &errAfterWriter{after: 1}, io.Discard)

	var out safeBuffer
	if code := run([]string{"read"}, &out, io.Discard); code != 0 {
		t.Fatalf("the read after the interruption exited %d", code)
	}
	for _, want := range []string{"message-one", "message-two"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("%s was destroyed by a read that never showed it; got %q", want, out.String())
		}
	}
}

// An unparseable line is SHOWN, not swallowed — and it is still consumed,
// because a queue that cannot be drained past one bad byte is a queue that
// stops working.
func TestReadShowsALineItCannotParse(t *testing.T) {
	p := newPresence(t)
	p.as(t, e1)
	p.seedQueue(t, q1, "queue."+e1)
	if _, err := p.adminJS.Publish("queue."+e1, []byte("this is not an envelope")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	code, out, errOut := exec("read")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "[unparseable] this is not an envelope") {
		t.Errorf("a reader who sees nothing cannot tell a quiet queue from a broken one: %q", out)
	}
}

// --------------------------------------------------------- when it must fail

// An unreachable medium is LOUD. Non-zero, the reason on standard error, and
// NOTHING on standard out — a heading printed before the failure would tell
// the reader its mailbox is a place that was just looked at.
func TestReadAgainstAnUnreachableMediumFailsLoudly(t *testing.T) {
	p := newPresence(t)
	p.as(t, e1)
	loctest.Write(t, filepath.Join(p.home, "config"),
		"provider = nats\nnats_url = "+loctest.ClosedPort(t)+"\n")

	checkOnStderr(t, "cannot reach the medium", "read")
}

// A medium that carries messages but cannot hand a reader its own is named in
// the refusal — "this provider cannot" and "you typed it wrong" are different
// answers, and only one of them is fixed by reading the usage.
func TestReadOnAMediumThatCarriesNoReader(t *testing.T) {
	d := newDeployment(t, "ada")

	code, out, errOut := exec("read")
	assertResult(t, code, out, errOut, 1, "",
		"loc: provider 'spy' cannot hand a reader its messages\n")
	if d.spy.closed != 1 {
		t.Errorf("provider closed %d times, want 1 even on failure", d.spy.closed)
	}
}

// An unattributable caller is refused before anything is read. There is no
// anonymous mailbox.
func TestReadRefusesAnUnattributableCaller(t *testing.T) {
	newPresence(t)
	t.Setenv("LOC_IDENTITY", "")

	checkOnStderr(t, "cannot determine sender identity", "read")
}
