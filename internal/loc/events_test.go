package loc

// ReadEvents' contract: the append order, a torn line skipped, and an absent
// log that is no events and no error.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seedLog writes one day file into a scratch LOC_HOME.
func seedLog(t *testing.T, dir, day, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, day+".jsonl"), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", day, err)
	}
}

// The reader returns both families, keyed as they are on disk, with the day
// files oldest first and the lines inside each file in the order they were
// appended.
func TestReadEventsReturnsBothFamiliesInAppendOrder(t *testing.T) {
	dir := t.TempDir()
	// Seeded newest-first so that a reader which returned directory order
	// rather than sorted order would fail this case.
	seedLog(t, dir, "2026-01-16",
		`{"seat":"workshop.scribe","status":"bell-failed","ts":"2026-01-16T10:00:00Z","reason":"no notifier"}`+"\n")
	seedLog(t, dir, "2026-01-15",
		`{"uid":"u1","status":"sent","ts":"2026-01-15T09:00:00Z","from":"a","to":"b","body":"hi"}`+"\n"+
			`{"uid":"u1","status":"read","ts":"2026-01-15T09:30:00Z","by":"workshop.scribe"}`+"\n")

	got, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(got), got)
	}
	if got[0].UID != "u1" || got[0].Status != "sent" || got[0].Body != "hi" {
		t.Errorf("first event = %+v; want the sent line of the older day", got[0])
	}
	if got[1].Status != "read" || got[1].By != "workshop.scribe" {
		t.Errorf("second event = %+v; want the read line", got[1])
	}
	if got[2].Seat != "workshop.scribe" || got[2].Status != "bell-failed" || got[2].Reason != "no notifier" {
		t.Errorf("third event = %+v; want the newer day's seat line", got[2])
	}
	// The two families stay apart by which key is set.
	if got[2].UID != "" {
		t.Errorf("the seat event carries a uid %q", got[2].UID)
	}
	if got[0].Seat != "" {
		t.Errorf("the message event carries a seat %q", got[0].Seat)
	}
}

// A torn line costs that line and nothing else.
//
// FIRE CONTROL: the same file with the torn line removed must give the same
// two events, so the case cannot pass by returning nothing.
func TestReadEventsSkipsATornLine(t *testing.T) {
	good := `{"uid":"u1","status":"sent","ts":"2026-01-15T09:00:00Z"}` + "\n" +
		`{"uid":"u2","status":"sent","ts":"2026-01-15T09:01:00Z"}` + "\n"
	torn := `{"uid":"u1","status":"sent","ts":"2026-01-15T09:00:00Z"}` + "\n" +
		`{"uid":"u9","status":"se` + "\n" +
		`{"uid":"u2","status":"sent","ts":"2026-01-15T09:01:00Z"}` + "\n"

	for _, c := range []struct{ name, content string }{{"whole", good}, {"torn", torn}} {
		dir := t.TempDir()
		seedLog(t, dir, "2026-01-15", c.content)
		got, err := ReadEvents(dir)
		if err != nil {
			t.Fatalf("%s: ReadEvents: %v", c.name, err)
		}
		if len(got) != 2 || got[0].UID != "u1" || got[1].UID != "u2" {
			t.Errorf("%s: got %+v; want the two whole lines", c.name, got)
		}
	}
}

// An absent log directory is no events and no error.
//
// FIRE CONTROL: the same directory with one day file in it returns that day,
// so the case cannot pass by always returning nothing.
func TestReadEventsTreatsAnAbsentLogAsNoEvents(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run", "log")
	got, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("an absent log is an error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("an absent log gave %d events", len(got))
	}
	seedLog(t, dir, "2026-01-15", `{"uid":"u1","status":"sent","ts":"2026-01-15T09:00:00Z"}`+"\n")
	if got, err = ReadEvents(dir); err != nil || len(got) != 1 {
		t.Errorf("the seeded log gave %d events, err %v; want 1 and nil", len(got), err)
	}
}

// Files that are not day files are not read, and neither are subdirectories.
func TestReadEventsReadsOnlyDayFiles(t *testing.T) {
	dir := t.TempDir()
	seedLog(t, dir, "2026-01-15", `{"uid":"u1","status":"sent","ts":"2026-01-15T09:00:00Z"}`+"\n")
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a log\n"), 0o600); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "old.jsonl"), 0o700); err != nil {
		t.Fatalf("mkdir old.jsonl: %v", err)
	}
	got, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(got) != 1 || got[0].UID != "u1" {
		t.Errorf("got %+v; want the one day file", got)
	}
}

// A log directory that cannot be listed is an error the caller decides about.
func TestReadEventsReportsADirectoryItCannotList(t *testing.T) {
	home := t.TempDir()
	// A FILE where the directory belongs: the shape of a broken run area.
	path := filepath.Join(home, "log")
	if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ReadEvents(path); err == nil {
		t.Error("ReadEvents swallowed a directory it cannot list")
	}
}

// At parses the stamp the writers emit, and refuses one it cannot read.
func TestEventAtParsesTheStamp(t *testing.T) {
	at, ok := Event{TS: "2026-01-15T09:00:00Z"}.At()
	if !ok || !at.Equal(time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("At() = %v, %v; want the stamp and true", at, ok)
	}
	// The ledger's millisecond precision parses too.
	if _, ok := (Event{TS: "2026-01-14T09:12:04.318Z"}).At(); !ok {
		t.Error("At() refused a stamp with milliseconds")
	}
	if _, ok := (Event{TS: "yesterday"}).At(); ok {
		t.Error("At() accepted a stamp it cannot parse")
	}
	if _, ok := (Event{}).At(); ok {
		t.Error("At() accepted an absent stamp")
	}
}

// LogDir is the directory LogEvent writes into, so the reader and the writer
// cannot drift apart.
func TestLogDirIsWhereTheWriterWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	LogBellFailed("workshop.scribe", "no notifier", time.Now())
	got, err := ReadEvents(LogDir())
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(got) != 1 || got[0].Seat != "workshop.scribe" || got[0].Reason != "no notifier" {
		t.Errorf("got %+v; want the line LogBellFailed just wrote", got)
	}
}
