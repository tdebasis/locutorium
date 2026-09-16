package main

// The day's record, driven through the verbs that write it.
//
// internal/loc pins the SHAPES of the four new lines. These cases pin the
// WIRING: that a read writes one, that a peek and a room read write none,
// that a refusal writes one before the message has been anywhere, and that a
// destroyed queue writes one with the reason the verb already knew.
//
// Every case reads the record back as raw JSON. An absent key and an empty
// one are different answers here, and a struct with every field would report
// the empty string for both.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// record reads one day's file out of a scratch deployment. A missing file is
// no lines rather than a failure: three cases below assert that a verb wrote
// nothing at all, and the file not existing is the strongest form of that.
func record(t *testing.T, home string, day time.Time) []map[string]any {
	t.Helper()
	path := filepath.Join(home, "run", "log", day.UTC().Format("2006-01-02")+".jsonl")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// withStatus keeps only the lines of one status, so a case about reads is not
// disturbed by the sends that arranged it.
func withStatus(lines []map[string]any, status string) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["status"] == status {
			out = append(out, l)
		}
	}
	return out
}

// ----------------------------------------------------------------- read

// A backlog of two is two read lines, carrying the two uids IN ACK ORDER.
//
// The order is the claim worth making. A queue hands its backlog over in the
// order it was sent, the ack is what consumes each one, and the record is
// written after each ack — so the record's order IS the order the messages
// were taken. A record that wrote both lines at the end of the drain would
// satisfy a case that only counted them.
//
// The uids are known in advance because the admin observer posts the
// envelopes, and post's envelope carries the body as its id.
func TestRecord_ReadWritesOneLinePerMessageInAckOrder(t *testing.T) {
	p := newPresence(t)
	p.as(t, e1)
	p.seedQueue(t, q1, "queue."+e1)
	p.post(t, "queue."+e1, "host", e1, "message-one")
	p.post(t, "queue."+e1, "host", e1, "message-two")

	if got := readAs(t, e1); !strings.Contains(got, "message-two") {
		t.Fatalf("the backlog was not handed over, so there is nothing to have recorded: %q", got)
	}

	lines := record(t, p.home, time.Now())
	if len(lines) != 2 {
		t.Fatalf("the record holds %v, want two read lines", lines)
	}
	for i, want := range []string{"message-one", "message-two"} {
		if lines[i]["status"] != "read" {
			t.Errorf("line %d status = %v, want read", i, lines[i]["status"])
		}
		if lines[i]["uid"] != want {
			t.Errorf("line %d uid = %v, want %q; the record's order is the ack order",
				i, lines[i]["uid"], want)
		}
		if lines[i]["by"] != e1 {
			t.Errorf("line %d by = %v, want %q", i, lines[i]["by"], e1)
		}
	}
}

// A PEEK CONSUMES NOTHING, so it reads nothing. The message is still owed to
// this endpoint, and a line saying it was read would be a claim the next
// ordinary read disproves.
func TestRecord_PeekWritesNothing(t *testing.T) {
	p := newPresence(t)
	p.as(t, e1)
	p.seedQueue(t, q1, "queue."+e1)
	p.post(t, "queue."+e1, "host", e1, "message-one")

	code, out, errOut := exec("read", "--peek")
	if code != 0 || !strings.Contains(out, "message-one") {
		t.Fatalf("the peek showed nothing, so this case would pass on an empty queue: exit %d, %q, %q",
			code, out, errOut)
	}
	if lines := record(t, p.home, time.Now()); len(lines) != 0 {
		t.Errorf("a peek wrote %v to the record", lines)
	}
}

// A ROOM READ WRITES NOTHING. A room message is read from every attending
// seat's own position, so "this message was read" is not a fact about the
// message: it would need one line per attender, and it would still not be
// complete while one of them has not read yet.
func TestRecord_ATopicReadWritesNothing(t *testing.T) {
	p := newPresence(t)
	p.as(t, e1)
	p.seedQueue(t, q1, "queue."+e1) // present and EMPTY, so only the room is drained
	p.seedTopics(t)
	p.post(t, "topic.standup", "host", "#standup", "room-canary")

	out := readAs(t, e1)
	if !strings.Contains(out, "room-canary") {
		t.Fatalf("the room message was not handed over, so this case would pass on an empty room: %q", out)
	}
	if lines := record(t, p.home, time.Now()); len(lines) != 0 {
		t.Errorf("a room read wrote %v to the record", lines)
	}
}

// THE DAY ROLL, through the verb. A read stamped a second after midnight
// belongs in the new day's file. The message it took was sent the day before,
// so the pair is deliberately split across two files — which is the case a
// reader joining `sent` to `read` has to handle, and the one that cannot be
// exercised by waiting for midnight.
func TestRecord_AReadAfterMidnightLandsInTheNewDaysFile(t *testing.T) {
	p := newPresence(t)
	p.as(t, e1)
	p.seedQueue(t, q1, "queue."+e1)
	p.post(t, "queue."+e1, "host", e1, "message-one")

	yesterday := time.Date(2026, 9, 16, 23, 59, 58, 0, time.UTC)
	justAfter := time.Date(2026, 9, 17, 0, 0, 1, 0, time.UTC)
	prev := readNow
	readNow = func() time.Time { return justAfter }
	t.Cleanup(func() { readNow = prev })

	if got := readAs(t, e1); !strings.Contains(got, "message-one") {
		t.Fatalf("nothing was handed over: %q", got)
	}

	if lines := record(t, p.home, yesterday); len(lines) != 0 {
		t.Errorf("the previous day's file holds %v, want nothing", lines)
	}
	lines := record(t, p.home, justAfter)
	if len(lines) != 1 {
		t.Fatalf("the new day's file holds %v, want one read line", lines)
	}
	if lines[0]["ts"] != "2026-09-17T00:00:01Z" {
		t.Errorf("ts = %v; the file and the stamp must name the same day", lines[0]["ts"])
	}
}

// ----------------------------------------------------------------- send

// A refusal is recorded, and NOTHING REACHES THE MEDIUM. Both halves matter: a
// refusal that wrote a line and also sent the message would be worse than
// either failure alone.
func TestRecord_ARefusedSendIsRecordedAndPublishesNothing(t *testing.T) {
	d := newDeployment(t, "ada", "bob")
	t.Setenv("LOC_IDENTITY", "")

	code, out, errOut := exec("send", "bob", "hi")
	if code == 0 || out != "" || !strings.Contains(errOut, "cannot determine sender identity") {
		t.Fatalf("want the identity refusal; got exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if len(d.spy.sends)+len(d.spy.publishes) != 0 {
		t.Fatal("a refused message reached the medium")
	}

	lines := record(t, d.home, time.Now())
	if len(lines) != 1 {
		t.Fatalf("the record holds %v, want one failed line", lines)
	}
	got := lines[0]
	if got["status"] != "failed" || got["reason"] != "no identity" {
		t.Errorf("line = %v, want a failed line reading 'no identity'", got)
	}
	if got["to"] != "bob" {
		t.Errorf("to = %v, want bob", got["to"])
	}
	// A REFUSAL STILL HAS A NAME. The envelope is built before the first
	// check for exactly this: without a uid the line joins to nothing, and a
	// sender cannot tell four refusals apart.
	if uid, _ := got["uid"].(string); uid == "" {
		t.Error("the refusal has no uid; the envelope must be built before the checks")
	}
	if _, ok := got["body"]; ok {
		t.Error("the refused body is on disk")
	}
}

// ------------------------------------------------------------ queue-deleted

// An agent leaving its own seat takes its queue, and the record says which of
// the departures it was. `left` and `displaced` are the same delete to the
// medium and different answers to "why did this seat stop receiving mail".
func TestRecord_UnsubscribeRecordsWhyTheQueueWent(t *testing.T) {
	cases := []struct {
		name   string
		pid    func(t *testing.T) int
		args   []string
		reason string
	}{
		// A dead incumbent is cleared by an ordinary unsubscribe, which is
		// the ordinary end of a session.
		{"clean", deadPid, nil, "left"},
		// A LIVE incumbent is refused unless --force, so this is the one
		// departure that is one agent taking a seat from another.
		{"forced", livePid, []string{"--force"}, "displaced"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newPresence(t)
			p.as(t, "host")
			subscribeSeat(t, e1, c.pid(t), "tmux", "3.2.0")
			if !p.qexists(q1) {
				t.Fatalf("no queue was stood up, so there is nothing to delete")
			}

			if code, _, errOut := exec(append([]string{"unsubscribe", e1}, c.args...)...); code != 0 {
				t.Fatalf("unsubscribe exit %d: %s", code, errOut)
			}
			if p.qexists(q1) {
				t.Fatal("the queue survived, so a recorded deletion would be a false one")
			}

			lines := withStatus(record(t, p.home, time.Now()), "queue-deleted")
			if len(lines) != 1 {
				t.Fatalf("the record holds %v, want one queue-deleted line", lines)
			}
			if lines[0]["seat"] != e1 || lines[0]["reason"] != c.reason {
				t.Errorf("line = %v, want seat %q reason %q", lines[0], e1, c.reason)
			}
			if _, ok := lines[0]["uid"]; ok {
				t.Error("a seat event carries no uid")
			}
		})
	}
}

// AN UNSUBSCRIBE THAT DELETED NOTHING RECORDS NOTHING. There was no row and no
// queue, so there was no departure: the verb returns without touching the
// medium, and a line here would report a seat's mail stopping on a day when
// the seat was never there.
func TestRecord_AnUnsubscribeThatDeletesNothingWritesNothing(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	if p.qexists(q1) {
		t.Fatal("the fixture is wrong: this case needs an endpoint with no queue")
	}

	if code, _, errOut := exec("unsubscribe", e1); code != 0 {
		t.Fatalf("unsubscribe of an absent seat exit %d: %s", code, errOut)
	}
	if lines := record(t, p.home, time.Now()); len(lines) != 0 {
		t.Errorf("a no-op unsubscribe wrote %v", lines)
	}
}
