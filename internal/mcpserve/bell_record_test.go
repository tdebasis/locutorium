package mcpserve

// A bell that could not ring, in the DAY'S RECORD rather than in this seat's
// own delivery log.
//
// The two files answer different people. The delivery log is per-seat and
// plain text, and somebody reads it once they already suspect this seat. The
// day's record holds the `sent` line for the mail that went unannounced, and
// it is where a sender asking why nobody answered is already looking.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The clock every case here pins. A fixed instant is what lets the case name
// the file the line must be in; the breaker rolls its buckets off this same
// clock, and one ring never fills a bucket.
var bellRecordClock = time.Date(2026, 9, 16, 14, 5, 6, 0, time.UTC)

// recordLines reads the pinned day's record out of a scratch deployment. A
// missing file is no lines rather than a failure, because one case below
// asserts that nothing was written.
func recordLines(t *testing.T, dir string) []map[string]any {
	t.Helper()
	path := filepath.Join(dir, "run", "log", bellRecordClock.Format("2006-01-02")+".jsonl")
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

// A notifier that cannot ring puts one line in the day's record, naming the
// seat and carrying the notifier's OWN reason. The reason is the notifier's
// text and not a word this code chose, because "the pane is in copy mode" and
// "there is no such pane" want different repairs.
func TestBell_AFailedBellIsInTheDaysRecord(t *testing.T) {
	dir := home(t, "provider = none\nwake_window_seconds = 1\n")
	f := &fake{nudgeErr: fmt.Errorf("exit status 3")}
	d := f.deps()
	d.Now = func() time.Time { return bellRecordClock }
	var stderr bytes.Buffer
	d.Stderr = &stderr
	sess, done := serve(t, d)
	defer stop(t, sess, done)

	f.ready(t)
	f.ring()
	if !waitFor(5*time.Second, func() bool { return len(recordLines(t, dir)) == 1 }) {
		t.Fatalf("the day's record holds %v, want one bell-failed line", recordLines(t, dir))
	}

	got := recordLines(t, dir)[0]
	if got["status"] != "bell-failed" {
		t.Errorf("status = %v, want bell-failed", got["status"])
	}
	if got["seat"] != seat {
		t.Errorf("seat = %v, want %q", got["seat"], seat)
	}
	if got["reason"] != "exit status 3" {
		t.Errorf("reason = %v; the notifier's own reason is what names the repair", got["reason"])
	}
	// THE STAMP IS THE INJECTED CLOCK and not the wall clock, so the line is
	// in the file the case can name.
	if got["ts"] != "2026-09-16T14:05:06Z" {
		t.Errorf("ts = %v, want the server's injected clock", got["ts"])
	}
	// NO UID. The server is told that mail arrived and never which message
	// arrived, so a per-message bell failure is not a claim it can make.
	if _, ok := got["uid"]; ok {
		t.Error("the line carries a uid; the server does not know which message it failed to announce")
	}
}

// A bell that RANG writes nothing here. Every wake would otherwise put a line
// in the record for a thing that worked, and the record would stop being the
// place a failure stands out.
func TestBell_ARungBellIsNotInTheDaysRecord(t *testing.T) {
	dir := home(t, "provider = none\nwake_window_seconds = 1\n")
	f := &fake{}
	d := f.deps()
	d.Now = func() time.Time { return bellRecordClock }
	sess, done := serve(t, d)
	defer stop(t, sess, done)

	f.ready(t)
	f.ring()
	if !waitFor(5*time.Second, func() bool { return len(f.bells()) == 1 }) {
		t.Fatalf("the bell never rang, so this case would pass on an absent bell: %v", f.bells())
	}
	if lines := recordLines(t, dir); len(lines) != 0 {
		t.Errorf("a bell that rang wrote %v to the day's record", lines)
	}
}
