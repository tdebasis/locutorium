package loc

// LogSent's contract: one intact JSON line per call, and file/directory modes
// that are set only when this call is the one that creates them.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tdebasis/locutorium/internal/config"
)

// A body carrying a double quote and a newline must not split the record: the
// whole thing is one JSON-encoded line, and every logged field round-trips.
func TestLogSentAppendsOneJSONLine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)

	e := NewEnvelope("ada", "bob", "msg", "she said \"hi\"\nand left")
	LogSent(e)

	path := filepath.Join(config.Home(), "run", "log", time.Now().UTC().Format("2006-01-02")+".jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("want exactly one line, got %d: %q", len(lines), string(raw))
	}

	var got map[string]string
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal: %v; line %q", err, lines[0])
	}
	if got["uid"] != e.ID {
		t.Errorf("uid = %q, want %q", got["uid"], e.ID)
	}
	if got["status"] != "sent" {
		t.Errorf(`status = %q, want "sent"`, got["status"])
	}
	if got["from"] != e.From {
		t.Errorf("from = %q, want %q", got["from"], e.From)
	}
	if got["to"] != e.To {
		t.Errorf("to = %q, want %q", got["to"], e.To)
	}
	if got["body"] != e.Body {
		t.Errorf("body = %q, want %q", got["body"], e.Body)
	}
}

// A fresh deployment's log is private from the moment it exists: the
// directory 0700, the file 0600 — the same pair internal/mcpserve's delivery
// log uses, so a reader looking for "who can see this" checks one convention.
func TestLogSentCreatesPrivateModes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)

	LogSent(NewEnvelope("ada", "bob", "msg", "hi"))

	dir := filepath.Join(config.Home(), "run", "log")
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o, want 0700", di.Mode().Perm())
	}

	path := filepath.Join(dir, time.Now().UTC().Format("2006-01-02")+".jsonl")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 0600", fi.Mode().Perm())
	}
}

// The mode guarantee is CREATION-TIME ONLY. LogSent opens with O_APPEND and
// never chmods, so a day's file that already exists — left behind with
// whatever mode its creator gave it — keeps that mode across every later
// append. This is what lets many processes share the file without one of
// them fighting another over its permissions.
func TestLogSentDoesNotChangeAnExistingFilesMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)

	dir := filepath.Join(home, "run", "log")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, time.Now().UTC().Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("precreate: %v", err)
	}

	LogSent(NewEnvelope("ada", "bob", "msg", "hi"))

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode changed to %o, want unchanged 0644 (creation-time only)", fi.Mode().Perm())
	}
}
