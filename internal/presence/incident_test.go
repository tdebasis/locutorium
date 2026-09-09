package presence

// house.incident is the third subject. These tests hold its two outputs: the
// live event, which must be in the taxonomy and must not be mistaken for work,
// and the durable line, which must land in run/incidents with the same append
// discipline and the same modes as the message log.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tdebasis/locutorium/internal/config"
)

// incidentPath is the day's file, named the way LogIncident names it.
func incidentPath(home string) string {
	return filepath.Join(home, "run", "incidents", time.Now().UTC().Format("2006-01-02")+".jsonl")
}

// The taxonomy gains a third subject, and only a kind. THE SECOND ASSERTION IS
// A GUARD, NOT A FALSIFIER: RecordsActivity already returns false for every
// kind its switch does not name, so it passes on main too. It is here so that
// nobody later adds house.incident to that switch. An incident is the house
// reporting itself, and it says nothing about whether an agent is working.
func TestIncidentIsInTheTaxonomy(t *testing.T) {
	if !ValidKind(KindIncident) {
		t.Errorf("%q is not in the taxonomy", KindIncident)
	}
	if RecordsActivity(KindIncident) {
		t.Errorf("%q moves the activity state, but it is the house's health", KindIncident)
	}
	// The kind is appended, so no existing consumer's ordering changes.
	all := Kinds()
	if all[len(all)-1] != KindIncident {
		t.Errorf("the taxonomy ends with %q, want %q appended last", all[len(all)-1], KindIncident)
	}
}

// The varieties are told apart by reason, the way agent.unsubscribe tells
// clean from expiry. A reason from another kind is not an incident reason.
func TestIncidentReasons(t *testing.T) {
	want := []string{
		IncidentLedgerUnreadable,
		IncidentMediumUnreachable,
		IncidentQueueWrongShape,
		IncidentEnumerationRefused,
	}
	if len(IncidentReasons()) != len(want) {
		t.Errorf("there are %d incident reasons, want %d", len(IncidentReasons()), len(want))
	}
	for _, r := range want {
		if !ValidIncidentReason(r) {
			t.Errorf("%q is a reason but was not recognised", r)
		}
	}
	for _, r := range []string{"clean", "expiry", ""} {
		if ValidIncidentReason(r) {
			t.Errorf("%q was accepted as an incident reason", r)
		}
	}
}

// The detail is the free text that says which row, which object, which call.
// It rides in its own field, because reason is a closed set.
func TestNewIncidentCarriesTheDetail(t *testing.T) {
	ev := NewIncident("workshop", "workshop.scribe", IncidentLedgerUnreadable, "scribe.json: unexpected end of JSON input")
	if ev.Kind != KindIncident {
		t.Errorf("Kind = %q, want %q", ev.Kind, KindIncident)
	}
	if ev.Instance != "workshop" {
		t.Errorf("Instance = %q, want %q", ev.Instance, "workshop")
	}
	if ev.Endpoint != "workshop.scribe" {
		t.Errorf("Endpoint = %q, want %q", ev.Endpoint, "workshop.scribe")
	}
	if ev.Reason != IncidentLedgerUnreadable {
		t.Errorf("Reason = %q, want %q", ev.Reason, IncidentLedgerUnreadable)
	}
	if ev.Detail != "scribe.json: unexpected end of JSON input" {
		t.Errorf("Detail = %q, want the parse error", ev.Detail)
	}
	if !strings.HasPrefix(ev.ID, "ev_") || ev.TS == "" {
		t.Errorf("the envelope is not stamped: id %q, ts %q", ev.ID, ev.TS)
	}
}

// A detail carrying a double quote and a newline must not split the record.
// The whole event is one JSON-encoded line, and every field round-trips.
func TestLogIncidentAppendsOneJSONLine(t *testing.T) {
	scratch(t)

	ev := NewIncident("workshop", "workshop.scribe", IncidentQueueWrongShape,
		"AddStream refused \"QUEUE_workshop_scribe\"\nsubjects differ")
	LogIncident(ev)

	raw, err := os.ReadFile(incidentPath(config.Home()))
	if err != nil {
		t.Fatalf("read incidents: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("want exactly one line, got %d: %q", len(lines), string(raw))
	}

	var got map[string]string
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal: %v; line %q", err, lines[0])
	}
	if got["kind"] != KindIncident {
		t.Errorf("kind = %q, want %q", got["kind"], KindIncident)
	}
	if got["reason"] != IncidentQueueWrongShape {
		t.Errorf("reason = %q, want %q", got["reason"], IncidentQueueWrongShape)
	}
	if got["instance"] != "workshop" {
		t.Errorf("instance = %q, want %q", got["instance"], "workshop")
	}
	if got["endpoint"] != "workshop.scribe" {
		t.Errorf("endpoint = %q, want %q", got["endpoint"], "workshop.scribe")
	}
	if got["detail"] != ev.Detail {
		t.Errorf("detail = %q, want %q", got["detail"], ev.Detail)
	}
	if got["id"] != ev.ID {
		t.Errorf("id = %q, want %q", got["id"], ev.ID)
	}
	// THE FILE LINE IS THE WIRE LINE. A reader who saw the event on the bus
	// can grep the file for it and find the same bytes.
	wire, err := ev.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if lines[0] != string(wire) {
		t.Errorf("file line differs from the wire form:\n file %s\n wire %s", lines[0], wire)
	}
}

// The file is append-only. A second incident joins the first; it does not
// replace it.
func TestLogIncidentAppendsRatherThanReplaces(t *testing.T) {
	home := scratch(t)

	LogIncident(NewIncident("workshop", "workshop.scribe", IncidentLedgerUnreadable, "first"))
	LogIncident(NewIncident("workshop", "", IncidentMediumUnreachable, "second"))

	raw, err := os.ReadFile(incidentPath(home))
	if err != nil {
		t.Fatalf("read incidents: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want two lines, got %d: %q", len(lines), string(raw))
	}
	for i, want := range []string{"first", "second"} {
		var got map[string]string
		if err := json.Unmarshal([]byte(lines[i]), &got); err != nil {
			t.Fatalf("unmarshal line %d: %v", i, err)
		}
		if got["detail"] != want {
			t.Errorf("line %d detail = %q, want %q", i, got["detail"], want)
		}
	}
}

// A fresh deployment's incident record is private from the moment it exists:
// the directory 0700, the file 0600. That is the pair the message log uses, so
// a reader asking "who can see this" checks one convention.
func TestLogIncidentCreatesPrivateModes(t *testing.T) {
	home := scratch(t)

	LogIncident(NewIncident("workshop", "workshop.scribe", IncidentEnumerationRefused, "consumer listing denied"))

	dir := filepath.Join(home, "run", "incidents")
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o, want 0700", di.Mode().Perm())
	}

	fi, err := os.Stat(incidentPath(home))
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 0600", fi.Mode().Perm())
	}
}

// The mode guarantee is CREATION-TIME ONLY, the same limit the message log
// has. LogIncident opens with O_APPEND and never chmods, so a day's file that
// already exists keeps whatever mode its creator gave it. This is what lets
// many processes share the file without one of them fighting another over its
// permissions.
func TestLogIncidentDoesNotChangeAnExistingFilesMode(t *testing.T) {
	home := scratch(t)

	dir := filepath.Join(home, "run", "incidents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := incidentPath(home)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("precreate: %v", err)
	}

	LogIncident(NewIncident("workshop", "workshop.scribe", IncidentLedgerUnreadable, "hi"))

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode changed to %o, want unchanged 0644 (creation-time only)", fi.Mode().Perm())
	}
}

// AN INCIDENT NEED NOT NAME A SEAT. A medium the house cannot reach is a fault
// of the house, not of any one agent, so the endpoint is empty and both
// outputs still work. The field stays in the envelope, empty, because the
// schema fixes the envelope's four fields for every kind.
func TestIncidentWithNoEndpoint(t *testing.T) {
	home := scratch(t)

	ev := NewIncident("workshop", "", IncidentMediumUnreachable, "dial tcp 127.0.0.1:4222: connect: connection refused")
	b, err := ev.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"endpoint":""`) {
		t.Errorf("the empty endpoint left the envelope: %s", b)
	}

	LogIncident(ev)

	raw, err := os.ReadFile(incidentPath(home))
	if err != nil {
		t.Fatalf("read incidents: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(strings.TrimRight(string(raw), "\n")), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["endpoint"] != "" {
		t.Errorf("endpoint = %q, want empty", got["endpoint"])
	}
	if got["reason"] != IncidentMediumUnreachable {
		t.Errorf("reason = %q, want %q", got["reason"], IncidentMediumUnreachable)
	}
}

// LogIncident MUST SURVIVE A DIRECTORY IT CANNOT MAKE. run/incidents is a
// regular file here, so MkdirAll fails and LogIncident returns at that error
// instead of turning the fault into a panic. This proves the doc comment's
// claim for the MkdirAll error.
func TestLogIncidentSurvivesADirectoryItCannotMake(t *testing.T) {
	home := scratch(t)

	if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	blocker := filepath.Join(home, "run", "incidents")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatalf("precreate blocker: %v", err)
	}

	LogIncident(NewIncident("workshop", "workshop.scribe", IncidentLedgerUnreadable, "x"))

	fi, err := os.Stat(blocker)
	if err != nil {
		t.Fatalf("stat blocker: %v", err)
	}
	if fi.IsDir() {
		t.Errorf("blocker became a directory; want it unchanged")
	}
	if fi.Size() != 0 {
		t.Errorf("blocker size = %d, want 0 (unchanged)", fi.Size())
	}
}

// LogIncident MUST SURVIVE A FILE IT CANNOT OPEN. The day's path is a
// directory here, so OpenFile with O_WRONLY fails and LogIncident returns at
// that error instead of turning the fault into a panic. This proves the doc
// comment's claim for the OpenFile error.
func TestLogIncidentSurvivesAFileItCannotOpen(t *testing.T) {
	home := scratch(t)

	dir := filepath.Join(home, "run", "incidents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := incidentPath(home)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("precreate path as dir: %v", err)
	}

	LogIncident(NewIncident("workshop", "workshop.scribe", IncidentLedgerUnreadable, "x"))

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat path: %v", err)
	}
	if !fi.IsDir() {
		t.Errorf("path is no longer a directory")
	}
}

// MANY PROCESSES APPEND TO THE DAY'S FILE AT ONCE, and this holds the claim
// LogIncident's doc comment makes about that: the line is handed to Write once,
// so no other writer's line can land between two halves of it.
//
// 64 goroutines share one open-append-close cycle each, against one scratch
// home. The file must end with 64 lines and every line must be a whole event.
// A version that writes the bytes and the newline in two calls fails here: the
// interleaving splits records and the count comes out wrong.
func TestLogIncidentSurvivesConcurrentCallers(t *testing.T) {
	home := scratch(t)

	const callers = 64
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func(n int) {
			defer wg.Done()
			LogIncident(NewIncident("workshop", "workshop.scribe", IncidentLedgerUnreadable,
				"caller "+strconv.Itoa(n)))
		}(i)
	}
	wg.Wait()

	raw, err := os.ReadFile(incidentPath(home))
	if err != nil {
		t.Fatalf("read incidents: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != callers {
		t.Fatalf("want exactly %d lines, got %d", callers, len(lines))
	}
	for i, line := range lines {
		var got map[string]string
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d is not one whole event (%v): %q", i, err, line)
		}
		if got["kind"] != KindIncident {
			t.Errorf("line %d kind = %q, want %q", i, got["kind"], KindIncident)
		}
	}
}
