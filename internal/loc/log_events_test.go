package loc

// The record's OTHER four line shapes — what happened to a message after it
// was sent, and what happened to a seat.
//
// Each case reads the file back as raw JSON rather than into a struct, so an
// absent key and an empty one are different answers. That distinction is the
// whole of the two families: a reader tells a message event from a seat event
// by which of `uid` and `seat` is present, and a struct with both fields would
// report an empty string for either and hide the difference.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tdebasis/locutorium/internal/config"
)

// dayLines reads one day's file and returns its lines, each decoded as a raw
// map. A missing file is no lines, not a failure: several cases below assert
// that nothing was written at all.
func dayLines(t *testing.T, day time.Time) []map[string]any {
	t.Helper()
	path := filepath.Join(config.Home(), "run", "log", day.UTC().Format("2006-01-02")+".jsonl")
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

// keysInOrder returns the object's keys as they appear in the line, which
// json.Unmarshal into a map throws away.
func keysInOrder(t *testing.T, line string) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(line))
	if _, err := dec.Token(); err != nil { // the opening brace
		t.Fatalf("token: %v", err)
	}
	var keys []string
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			t.Fatalf("key token: %v", err)
		}
		keys = append(keys, k.(string))
		var discard any
		if err := dec.Decode(&discard); err != nil {
			t.Fatalf("value: %v", err)
		}
	}
	return keys
}

func oneLine(t *testing.T, day time.Time) string {
	t.Helper()
	path := filepath.Join(config.Home(), "run", "log", day.UTC().Format("2006-01-02")+".jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("want exactly one line, got %d: %q", len(lines), string(raw))
	}
	return lines[0]
}

// A refusal is recorded with the reason and WITHOUT the body. The message
// never reached the medium, so the record is about the attempt; keeping the
// body would put text on disk for every mistyped endpoint.
func TestLogFailedCarriesTheReasonAndNoBody(t *testing.T) {
	t.Setenv("LOC_HOME", t.TempDir())

	e := NewEnvelope("ada", "bob", "msg", "the body nobody received")
	LogFailed(e, "target not registered")

	line := oneLine(t, time.Now())
	if got, want := keysInOrder(t, line), []string{"uid", "status", "ts", "from", "to", "reason"}; !equal(got, want) {
		t.Fatalf("fields %v, want %v", got, want)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["uid"] != e.ID || got["status"] != "failed" || got["reason"] != "target not registered" {
		t.Errorf("line = %v", got)
	}
	if _, ok := got["body"]; ok {
		t.Error("the refused body is on disk; a refusal records the attempt, not the content")
	}
	if _, ok := got["seat"]; ok {
		t.Error("a message event carries no seat key")
	}
}

// An unattributable sender is the one refusal whose `from` is empty, and the
// EMPTY STRING IS THE FACT. Dropping the key would leave a line that looks
// like every other refusal.
func TestLogFailedKeepsAnEmptySender(t *testing.T) {
	t.Setenv("LOC_HOME", t.TempDir())

	LogFailed(NewEnvelope("", "bob", "msg", "hi"), "no identity")

	var got map[string]any
	if err := json.Unmarshal([]byte(oneLine(t, time.Now())), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	from, ok := got["from"]
	if !ok {
		t.Fatal("the from key is missing; on a no-identity refusal the empty sender is the reason")
	}
	if from != "" {
		t.Errorf("from = %q, want the empty string", from)
	}
}

// A read line repeats nothing. It carries when and by whom, under the uid
// that joins it to the send.
func TestLogReadCarriesOnlyWhenAndByWhom(t *testing.T) {
	t.Setenv("LOC_HOME", t.TempDir())

	at := time.Date(2026, 9, 16, 17, 2, 19, 0, time.UTC)
	LogRead("abc-123", "workshop.clerk", at)

	line := oneLine(t, at)
	if got, want := keysInOrder(t, line), []string{"uid", "status", "ts", "by"}; !equal(got, want) {
		t.Fatalf("fields %v, want %v", got, want)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["uid"] != "abc-123" || got["status"] != "read" || got["by"] != "workshop.clerk" {
		t.Errorf("line = %v", got)
	}
	if got["ts"] != "2026-09-16T17:02:19Z" {
		t.Errorf("ts = %v, want the caller's clock at seconds precision", got["ts"])
	}
}

// THE DAY ROLL. A read a second after midnight belongs in the new day's file,
// with a stamp that says the same day. A line stamped 00:00:01 at the foot of
// yesterday's file is a record whoever reads it has to know a rule to find.
//
// The message was sent the day before, so the pair is deliberately split
// across two files: that is the case the joining reader has to handle, and
// the one that cannot be exercised by waiting.
func TestLogReadLandsInTheDayItHappened(t *testing.T) {
	t.Setenv("LOC_HOME", t.TempDir())

	sent := time.Date(2026, 9, 16, 23, 59, 58, 0, time.UTC)
	read := time.Date(2026, 9, 17, 0, 0, 1, 0, time.UTC)
	LogRead("same-uid", "workshop.scribe", read)

	if lines := dayLines(t, sent); len(lines) != 0 {
		t.Errorf("the previous day's file holds %d lines, want none", len(lines))
	}
	lines := dayLines(t, read)
	if len(lines) != 1 {
		t.Fatalf("the new day's file holds %d lines, want 1", len(lines))
	}
	if lines[0]["ts"] != "2026-09-17T00:00:01Z" {
		t.Errorf("ts = %v; the file and the stamp must name the same day", lines[0]["ts"])
	}
}

// Both seat events carry `seat` and no `uid`. A seat event with an empty uid
// would join to every other seat event in the file, under a key that matches
// no message.
func TestSeatEventsCarryNoUID(t *testing.T) {
	at := time.Date(2026, 9, 16, 8, 30, 0, 0, time.UTC)
	cases := []struct {
		name   string
		write  func()
		status string
		reason string
	}{
		{"bell-failed", func() { LogBellFailed("workshop.scribe", "pane is in copy mode", at) },
			"bell-failed", "pane is in copy mode"},
		// `orphan` is a reason NO BUILD WRITES any more (#141). The format
		// still carries it, because the log holds records written before that
		// change and the readers still have to parse them.
		{"queue-deleted", func() { LogQueueDeleted("workshop.clerk", "orphan", at) },
			"queue-deleted", "orphan"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("LOC_HOME", t.TempDir())
			c.write()

			line := oneLine(t, at)
			if got, want := keysInOrder(t, line), []string{"seat", "status", "ts", "reason"}; !equal(got, want) {
				t.Fatalf("fields %v, want %v", got, want)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(line), &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got["status"] != c.status || got["reason"] != c.reason {
				t.Errorf("line = %v", got)
			}
			if _, ok := got["uid"]; ok {
				t.Error("a seat event carries no uid key")
			}
		})
	}
}

// The shapes share one file and one append, so a day that saw all of them
// reads back as one line each, in the order they were written.
func TestEveryShapeSharesTheDayFile(t *testing.T) {
	t.Setenv("LOC_HOME", t.TempDir())

	e := NewEnvelope("ada", "bob", "msg", "hi")
	LogSent(e)
	LogFailed(NewEnvelope("ada", "mallory", "msg", "hi"), "bus unreachable")
	LogRead(e.ID, "bob", time.Now().UTC())
	LogBellFailed("bob", "no such pane", time.Now().UTC())
	LogQueueDeleted("bob", "left", time.Now().UTC())

	lines := dayLines(t, time.Now())
	want := []string{"sent", "failed", "read", "bell-failed", "queue-deleted"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d", len(lines), len(want))
	}
	for i, w := range want {
		if lines[i]["status"] != w {
			t.Errorf("line %d status = %v, want %q", i, lines[i]["status"], w)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The Assayer's F1 on the first cut of this file: LogSent and LogFailed took
// the day file from time.Now and the line's ts from the envelope, and the two
// are separated by the attendance check and the publish inside send(). A send
// stamped one second before midnight could land, stamped yesterday, in
// today's file. The test hands each call an envelope stamped on a day that is
// not today, and asks which file it landed in.
func TestASendLandsInTheDayItWasStamped(t *testing.T) {
	t.Setenv("LOC_HOME", t.TempDir())

	stamped := time.Date(2026, 9, 15, 23, 59, 59, 0, time.UTC)
	e := NewEnvelope("workshop.scribe", "workshop.clerk", "msg", "late")
	e.TS = stamped.Format(tsLayout)
	LogSent(e)
	LogFailed(e, "too long")

	if lines := dayLines(t, time.Now().UTC()); time.Now().UTC().Format("2006-01-02") != "2026-09-15" && len(lines) != 0 {
		t.Errorf("today's file holds %d lines, want none: the line is stamped 2026-09-15", len(lines))
	}
	lines := dayLines(t, stamped)
	if len(lines) != 2 {
		t.Fatalf("the stamped day's file holds %d lines, want 2 (sent, failed)", len(lines))
	}
	for i, want := range []string{"sent", "failed"} {
		if lines[i]["status"] != want || lines[i]["ts"] != "2026-09-15T23:59:59Z" {
			t.Errorf("line %d = %v %v; want %s stamped 2026-09-15T23:59:59Z", i, lines[i]["status"], lines[i]["ts"], want)
		}
	}
}
