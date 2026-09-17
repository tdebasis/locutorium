package main

// The third fact on the seat's row: the bell (#102).
//
// `row ok, queue ok` says the registry row exists and the queue exists. It
// does not say the seat's operator can be told. Every arm below seeds a real
// `run/log/<day>.jsonl` — the file the writers in internal/loc produce — and
// reads the row `status` prints from it.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	model "github.com/tdebasis/locutorium/internal/presence"
)

// liveRow plants a registration stamped 2026-01-14T09:12:04.318Z. Every bell
// stamp below sits either side of that on purpose.

// seedDayLog writes one day file of the message log into the deployment.
func seedDayLog(t *testing.T, d *presenceDeployment, day string, lines ...string) {
	t.Helper()
	writeFile(t, filepath.Join(d.home, "run", "log", day+".jsonl"), strings.Join(lines, "\n")+"\n")
}

func bellFailedLine(seat, ts, reason string) string {
	return `{"seat":"` + seat + `","status":"bell-failed","ts":"` + ts + `","reason":"` + reason + `"}`
}

func readLine(uid, by, ts string) string {
	return `{"uid":"` + uid + `","status":"read","ts":"` + ts + `","by":"` + by + `"}`
}

// wholeSeat plants a seat that is registered and attending: `row ok, queue ok`
// with nothing else wrong.
func wholeSeat(t *testing.T, d *presenceDeployment, endpoint string) {
	t.Helper()
	liveRow(t, d, endpoint)
	d.spy.exists[endpoint] = true
}

// seatRow returns the one report line for an endpoint.
func seatRow(t *testing.T, d *presenceDeployment, endpoint string) string {
	t.Helper()
	var out strings.Builder
	if err := seatLines(&out, d.spy); err != nil {
		t.Fatalf("seatLines: %v", err)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, endpoint+":") {
			return line
		}
	}
	t.Fatalf("the report has no row for %s:\n%s", endpoint, out.String())
	return ""
}

// A bell that failed after the seat registered is on the seat's row.
//
// FIRE CONTROL: the same deployment with no log at all prints the bare row, so
// the field cannot come from anywhere but the seeded event.
func TestStatusShowsABellThatFailedAfterRegistration(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")

	if got := seatRow(t, d, "workshop.scribe"); got != "workshop.scribe: row ok, queue ok" {
		t.Fatalf("control: with no log the row is %q, want the bare row", got)
	}

	seedDayLog(t, d, "2026-01-15",
		bellFailedLine("workshop.scribe", "2026-01-15T22:47:36Z", "notifier exited 127"))

	want := "workshop.scribe: row ok, queue ok, bell FAILED 2026-01-15T22:47:36Z: notifier exited 127"
	if got := seatRow(t, d, "workshop.scribe"); got != want {
		t.Errorf("row = %q\nwant  %q", got, want)
	}
}

// A bell that failed BEFORE the seat registered belongs to whoever held the
// endpoint last. It is not this seat's bell and it is not reported.
//
// FIRE CONTROL: the same reason and the same seat, moved to after the
// registration, does show — so the case tests the bound and not the wiring.
func TestStatusHidesABellThatFailedBeforeRegistration(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")
	seedDayLog(t, d, "2026-01-13",
		bellFailedLine("workshop.scribe", "2026-01-13T22:47:36Z", "notifier exited 127"))

	if got := seatRow(t, d, "workshop.scribe"); got != "workshop.scribe: row ok, queue ok" {
		t.Errorf("row = %q; a failure older than the registration was reported", got)
	}

	seedDayLog(t, d, "2026-01-15",
		bellFailedLine("workshop.scribe", "2026-01-15T22:47:36Z", "notifier exited 127"))
	if got := seatRow(t, d, "workshop.scribe"); !strings.Contains(got, "bell FAILED") {
		t.Errorf("control: the same failure after the registration is missing too: %q", got)
	}
}

// A read by that seat after the failure clears the line. The seat has shown it
// can take its mail.
//
// FIRE CONTROL: the identical log without the read line still shows the field.
func TestStatusHidesABellClearedByALaterRead(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")
	fail := bellFailedLine("workshop.scribe", "2026-01-15T22:47:36Z", "notifier exited 127")

	seedDayLog(t, d, "2026-01-15", fail)
	if got := seatRow(t, d, "workshop.scribe"); !strings.Contains(got, "bell FAILED") {
		t.Fatalf("control: the failure alone is not reported: %q", got)
	}

	seedDayLog(t, d, "2026-01-15", fail, readLine("u1", "workshop.scribe", "2026-01-15T23:00:00Z"))
	if got := seatRow(t, d, "workshop.scribe"); got != "workshop.scribe: row ok, queue ok" {
		t.Errorf("row = %q; a later read did not clear the bell", got)
	}

	// A read BEFORE the failure clears nothing: the seat answered its mail and
	// the bell died afterwards.
	seedDayLog(t, d, "2026-01-15", readLine("u1", "workshop.scribe", "2026-01-15T22:00:00Z"), fail)
	if got := seatRow(t, d, "workshop.scribe"); !strings.Contains(got, "bell FAILED") {
		t.Errorf("row = %q; a read older than the failure cleared it", got)
	}
}

// Two failures, and the row carries the LAST one's stamp and reason.
func TestStatusShowsTheLastOfTwoBellFailures(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")
	seedDayLog(t, d, "2026-01-15",
		bellFailedLine("workshop.scribe", "2026-01-15T10:00:00Z", "notifier exited 127"))
	seedDayLog(t, d, "2026-01-16",
		bellFailedLine("workshop.scribe", "2026-01-16T11:22:33Z", "no notifier configured"))

	want := "workshop.scribe: row ok, queue ok, bell FAILED 2026-01-16T11:22:33Z: no notifier configured"
	got := seatRow(t, d, "workshop.scribe")
	if got != want {
		t.Errorf("row = %q\nwant  %q", got, want)
	}
	if strings.Contains(got, "exited 127") {
		t.Error("the row carries the earlier failure's reason")
	}
}

// One seat's dead bell is not printed on another seat's row.
//
// FIRE CONTROL: the seat the event names does carry the field in the same run,
// so the log was read and the endpoint match is what kept it off the other row.
func TestStatusKeepsOneSeatsBellOffAnothersRow(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")
	wholeSeat(t, d, "workshop.clerk")
	seedDayLog(t, d, "2026-01-15",
		bellFailedLine("workshop.scribe", "2026-01-15T22:47:36Z", "notifier exited 127"))

	if got := seatRow(t, d, "workshop.clerk"); got != "workshop.clerk: row ok, queue ok" {
		t.Errorf("clerk row = %q; it carries the scribe's bell failure", got)
	}
	if got := seatRow(t, d, "workshop.scribe"); !strings.Contains(got, "bell FAILED") {
		t.Errorf("control: the scribe's own row is missing the failure: %q", got)
	}
}

// A seat that is in trouble already keeps the bell field too. A missing queue
// and a dead bell are two faults, and the next beat repairs only one of them.
func TestStatusAppendsTheBellToATroubledRow(t *testing.T) {
	d := newPresenceDeployment(t)
	liveRow(t, d, "workshop.scribe") // no queue for it
	seedDayLog(t, d, "2026-01-15",
		bellFailedLine("workshop.scribe", "2026-01-15T22:47:36Z", "notifier exited 127"))

	want := "workshop.scribe: queue missing, next beat repairs it, " +
		"bell FAILED 2026-01-15T22:47:36Z: notifier exited 127"
	if got := seatRow(t, d, "workshop.scribe"); got != want {
		t.Errorf("row = %q\nwant  %q", got, want)
	}
}

// No log directory at all: `status` is unchanged and exits 0.
//
// FIRE CONTROL: the same command with a seeded log prints the field, so the
// case cannot pass because `status` never reads a log.
func TestStatusIsUnchangedWhenTheLogIsAbsent(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")

	code, out, errOut := exec("status")
	if code != 0 {
		t.Fatalf("status exited %d (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, "workshop.scribe: row ok, queue ok\n") {
		t.Errorf("stdout %q; want the bare row", out)
	}
	if strings.Contains(out, "bell") {
		t.Errorf("stdout %q mentions a bell with no log to read", out)
	}

	seedDayLog(t, d, "2026-01-15",
		bellFailedLine("workshop.scribe", "2026-01-15T22:47:36Z", "notifier exited 127"))
	code, out, errOut = exec("status")
	if code != 0 || !strings.Contains(out, "bell FAILED 2026-01-15T22:47:36Z: notifier exited 127") {
		t.Errorf("control: exit=%d stdout=%q stderr=%q; want the field", code, out, errOut)
	}
}

// A log the reader cannot list adds nothing and fails nothing.
func TestStatusSurvivesALogItCannotList(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")
	// A FILE where the log directory belongs.
	writeFile(t, filepath.Join(d.home, "run", "log"), "not a directory")

	code, out, errOut := exec("status")
	if code != 0 {
		t.Fatalf("status exited %d (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, "workshop.scribe: row ok, queue ok\n") {
		t.Errorf("stdout %q; want the bare row", out)
	}
}

// A torn line and an unparsable stamp cost their own line and nothing else.
func TestStatusSkipsLinesItCannotRead(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")
	seedDayLog(t, d, "2026-01-15",
		`{"seat":"workshop.scribe","status":"bell-fai`,
		bellFailedLine("workshop.scribe", "yesterday", "a stamp nobody can parse"),
		bellFailedLine("workshop.scribe", "2026-01-15T22:47:36Z", "notifier exited 127"))

	want := "workshop.scribe: row ok, queue ok, bell FAILED 2026-01-15T22:47:36Z: notifier exited 127"
	if got := seatRow(t, d, "workshop.scribe"); got != want {
		t.Errorf("row = %q\nwant  %q", got, want)
	}
}

// A row whose registration stamp is unreadable gets no bell field: without a
// registration there is no bound to apply, and an unbounded failure could be a
// previous occupant's.
func TestStatusHidesTheBellWhenTheRegistrationStampIsUnreadable(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")
	writeFile(t, d.ledger("workshop.scribe.json"),
		`{"endpoint":"workshop.scribe","instance":"workshop","process":{"pid":`+alivePid()+
			`,"started":"`+model.StartedAt(os.Getpid())+`"},"registered":"whenever"}`)
	seedDayLog(t, d, "2026-01-15",
		bellFailedLine("workshop.scribe", "2026-01-15T22:47:36Z", "notifier exited 127"))

	if got := seatRow(t, d, "workshop.scribe"); strings.Contains(got, "bell") {
		t.Errorf("row = %q; a failure was reported against an unreadable registration", got)
	}
}
