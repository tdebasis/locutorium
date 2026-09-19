package main

// The third fact on the seat's row: the bell (#102, #144).
//
// `row ok, queue ok` says the registry row exists and the queue exists. It
// does not say the seat's operator can be told. Every arm below seeds a real
// `run/log/<day>.jsonl` — the file the writers in internal/loc produce — and
// reads the row `status` prints from it.
//
// WHAT THE FIELD MEANS CHANGED WITH #144. It was the last bell FAILURE that
// still stood. It is now the streak that is running while the seat has unread
// mail: how many tries the bell has made, what the last one came to, or that
// it has given up. A seat that read its mail has no field at all, which is the
// same read bound as before, now carrying the whole of the meaning.
//
// These cases run in CI. This machine holds a live deployment and a CI runner,
// so cmd/loc is not run here.

import (
	"os"
	"path/filepath"
	"strconv"
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

// bellTryLine is one try of a seat's bell, shaped as internal/loc writes it.
func bellTryLine(seat, ts string, try, of int, result, reason string) string {
	line := `{"seat":"` + seat + `","status":"bell-try","ts":"` + ts +
		`","try":` + strconv.Itoa(try) + `,"of":` + strconv.Itoa(of) + `,"result":"` + result + `"`
	if reason != "" {
		line += `,"reason":"` + reason + `"`
	}
	return line + "}"
}

// bellGaveUpLine closes a streak that used all its tries.
func bellGaveUpLine(seat, ts string, after int) string {
	return `{"seat":"` + seat + `","status":"bell-gave-up","ts":"` + ts + `","after":` + strconv.Itoa(after) + `}`
}

// bellFailedLine is the OLD line, written by no build since #144. Old day
// files hold them and the reader must not trip over one.
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

// A streak that opened after the seat registered is on the seat's row, with
// the count and the last try's own result.
//
// FIRE CONTROL: the same deployment with no log at all prints the bare row, so
// the field cannot come from anywhere but the seeded event.
func TestStatusShowsABellStreakAfterRegistration(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")

	if got := seatRow(t, d, "workshop.scribe"); got != "workshop.scribe: row ok, queue ok" {
		t.Fatalf("control: with no log the row is %q, want the bare row", got)
	}

	seedDayLog(t, d, "2026-01-15",
		bellTryLine("workshop.scribe", "2026-01-15T22:46:36Z", 1, 3, "rang", ""),
		bellTryLine("workshop.scribe", "2026-01-15T22:47:36Z", 2, 3, "refused", "pane busy"))

	want := "workshop.scribe: row ok, queue ok, mail unread, bell tried 2 of 3, " +
		"last refused (pane busy) at 2026-01-15T22:47:36Z"
	if got := seatRow(t, d, "workshop.scribe"); got != want {
		t.Errorf("row = %q\nwant  %q", got, want)
	}
}

// A TRY THAT RANG HAS NO REASON, so the field prints none. The mail is still
// unread — the pane was typed into, and nobody has taken the mail — which is
// what the field is about.
func TestStatusShowsATryThatRangWithNoReason(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")
	seedDayLog(t, d, "2026-01-15",
		bellTryLine("workshop.scribe", "2026-01-15T22:47:36Z", 2, 3, "rang", ""))

	want := "workshop.scribe: row ok, queue ok, mail unread, bell tried 2 of 3, " +
		"last rang at 2026-01-15T22:47:36Z"
	if got := seatRow(t, d, "workshop.scribe"); got != want {
		t.Errorf("row = %q\nwant  %q", got, want)
	}
}

// A STREAK THAT GAVE UP SAYS SO. This is the state the rule's known cost
// produces: a seat busy for longer than its three tries gets no further ring
// until more mail arrives or it looks, and this row is where that shows.
func TestStatusShowsABellThatGaveUp(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")
	seedDayLog(t, d, "2026-01-15",
		bellTryLine("workshop.scribe", "2026-01-15T22:47:36Z", 3, 3, "refused", "pane busy"),
		bellGaveUpLine("workshop.scribe", "2026-01-15T22:47:36Z", 3))

	want := "workshop.scribe: row ok, queue ok, mail unread, bell gave up at 2026-01-15T22:47:36Z after 3 tries"
	if got := seatRow(t, d, "workshop.scribe"); got != want {
		t.Errorf("row = %q\nwant  %q", got, want)
	}
}

// A NEW STREAK REPLACES A GIVE-UP. More mail arrived, the bell is trying
// again, and the row must say that rather than that it gave up.
func TestStatusShowsANewStreakAfterAGiveUp(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")
	seedDayLog(t, d, "2026-01-15",
		bellTryLine("workshop.scribe", "2026-01-15T22:47:36Z", 3, 3, "refused", "pane busy"),
		bellGaveUpLine("workshop.scribe", "2026-01-15T22:47:36Z", 3),
		bellTryLine("workshop.scribe", "2026-01-15T22:55:00Z", 1, 3, "rang", ""))

	want := "workshop.scribe: row ok, queue ok, mail unread, bell tried 1 of 3, " +
		"last rang at 2026-01-15T22:55:00Z"
	got := seatRow(t, d, "workshop.scribe")
	if got != want {
		t.Errorf("row = %q\nwant  %q", got, want)
	}
	if strings.Contains(got, "gave up") {
		t.Error("the row still says the bell gave up; a later try is a new streak")
	}
}

// AN OLD `bell-failed` LINE IS IGNORED. No build writes one, it carries no
// count, and the state it reported is not a state this field has any more.
//
// FIRE CONTROL: a `bell-try` at the same stamp does show, so the case tests
// the status word and not the wiring.
func TestStatusIgnoresAnOldBellFailedLine(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")
	seedDayLog(t, d, "2026-01-15",
		bellFailedLine("workshop.scribe", "2026-01-15T22:47:36Z", "notifier exited 127"))

	if got := seatRow(t, d, "workshop.scribe"); got != "workshop.scribe: row ok, queue ok" {
		t.Errorf("row = %q; an old bell-failed line was read into the new field", got)
	}

	seedDayLog(t, d, "2026-01-15",
		bellTryLine("workshop.scribe", "2026-01-15T22:47:36Z", 1, 3, "failed", "notifier exited 127"))
	if got := seatRow(t, d, "workshop.scribe"); !strings.Contains(got, "bell tried 1 of 3") {
		t.Errorf("control: a try at the same stamp is missing too: %q", got)
	}
}

// A streak that opened BEFORE the seat registered belongs to whoever held the
// endpoint last. It is not this seat's bell and it is not reported.
//
// FIRE CONTROL: the same try and the same seat, moved to after the
// registration, does show — so the case tests the bound and not the wiring.
func TestStatusHidesABellFromBeforeRegistration(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")
	seedDayLog(t, d, "2026-01-13",
		bellTryLine("workshop.scribe", "2026-01-13T22:47:36Z", 1, 3, "failed", "notifier exited 127"))

	if got := seatRow(t, d, "workshop.scribe"); got != "workshop.scribe: row ok, queue ok" {
		t.Errorf("row = %q; a streak older than the registration was reported", got)
	}

	seedDayLog(t, d, "2026-01-15",
		bellTryLine("workshop.scribe", "2026-01-15T22:47:36Z", 1, 3, "failed", "notifier exited 127"))
	if got := seatRow(t, d, "workshop.scribe"); !strings.Contains(got, "mail unread") {
		t.Errorf("control: the same try after the registration is missing too: %q", got)
	}
}

// A READ BY THAT SEAT CLEARS THE FIELD. The mail is read, so there is no
// unread mail to report and no streak that means anything. This is what the
// field's "mail unread" rests on.
//
// FIRE CONTROL: the identical log without the read line still shows the field.
func TestStatusHidesTheBellAfterALaterRead(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")
	try := bellTryLine("workshop.scribe", "2026-01-15T22:47:36Z", 2, 3, "refused", "pane busy")

	seedDayLog(t, d, "2026-01-15", try)
	if got := seatRow(t, d, "workshop.scribe"); !strings.Contains(got, "mail unread") {
		t.Fatalf("control: the streak alone is not reported: %q", got)
	}

	seedDayLog(t, d, "2026-01-15", try, readLine("u1", "workshop.scribe", "2026-01-15T23:00:00Z"))
	if got := seatRow(t, d, "workshop.scribe"); got != "workshop.scribe: row ok, queue ok" {
		t.Errorf("row = %q; a later read did not clear the bell field", got)
	}

	// A read BEFORE the last try clears nothing: the seat answered that mail
	// and the bell is ringing for mail that came after it.
	seedDayLog(t, d, "2026-01-15", readLine("u1", "workshop.scribe", "2026-01-15T22:00:00Z"), try)
	if got := seatRow(t, d, "workshop.scribe"); !strings.Contains(got, "mail unread") {
		t.Errorf("row = %q; a read older than the try cleared it", got)
	}
}

// Two streaks, and the row carries the LAST one's count, result and stamp.
func TestStatusShowsTheLastOfTwoStreaks(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")
	seedDayLog(t, d, "2026-01-15",
		bellTryLine("workshop.scribe", "2026-01-15T10:00:00Z", 1, 3, "failed", "notifier exited 127"))
	seedDayLog(t, d, "2026-01-16",
		bellTryLine("workshop.scribe", "2026-01-16T11:22:33Z", 1, 3, "failed", "no notifier configured"))

	want := "workshop.scribe: row ok, queue ok, mail unread, bell tried 1 of 3, " +
		"last failed (no notifier configured) at 2026-01-16T11:22:33Z"
	got := seatRow(t, d, "workshop.scribe")
	if got != want {
		t.Errorf("row = %q\nwant  %q", got, want)
	}
	if strings.Contains(got, "exited 127") {
		t.Error("the row carries the earlier streak's reason")
	}
}

// One seat's bell is not printed on another seat's row.
//
// FIRE CONTROL: the seat the event names does carry the field in the same run,
// so the log was read and the endpoint match is what kept it off the other row.
func TestStatusKeepsOneSeatsBellOffAnothersRow(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")
	wholeSeat(t, d, "workshop.clerk")
	seedDayLog(t, d, "2026-01-15",
		bellTryLine("workshop.scribe", "2026-01-15T22:47:36Z", 1, 3, "failed", "notifier exited 127"))

	if got := seatRow(t, d, "workshop.clerk"); got != "workshop.clerk: row ok, queue ok" {
		t.Errorf("clerk row = %q; it carries the scribe's bell", got)
	}
	if got := seatRow(t, d, "workshop.scribe"); !strings.Contains(got, "mail unread") {
		t.Errorf("control: the scribe's own row is missing the field: %q", got)
	}
}

// A seat that is in trouble already keeps the bell field too. A missing queue
// and mail nobody could announce are two faults, and the next beat repairs
// only one of them.
func TestStatusAppendsTheBellToATroubledRow(t *testing.T) {
	d := newPresenceDeployment(t)
	liveRow(t, d, "workshop.scribe") // no queue for it
	seedDayLog(t, d, "2026-01-15",
		bellTryLine("workshop.scribe", "2026-01-15T22:47:36Z", 1, 3, "failed", "notifier exited 127"))

	want := "workshop.scribe: queue missing, next beat repairs it, " +
		"mail unread, bell tried 1 of 3, last failed (notifier exited 127) at 2026-01-15T22:47:36Z"
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
		bellTryLine("workshop.scribe", "2026-01-15T22:47:36Z", 1, 3, "failed", "notifier exited 127"))
	code, out, errOut = exec("status")
	if code != 0 || !strings.Contains(out, "mail unread, bell tried 1 of 3, last failed (notifier exited 127) at 2026-01-15T22:47:36Z") {
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
		`{"seat":"workshop.scribe","status":"bell-tr`,
		bellTryLine("workshop.scribe", "yesterday", 1, 3, "failed", "a stamp nobody can parse"),
		bellTryLine("workshop.scribe", "2026-01-15T22:47:36Z", 1, 3, "failed", "notifier exited 127"))

	want := "workshop.scribe: row ok, queue ok, mail unread, bell tried 1 of 3, " +
		"last failed (notifier exited 127) at 2026-01-15T22:47:36Z"
	if got := seatRow(t, d, "workshop.scribe"); got != want {
		t.Errorf("row = %q\nwant  %q", got, want)
	}
}

// A row whose registration stamp is unreadable gets no bell field: without a
// registration there is no bound to apply, and an unbounded streak could be a
// previous occupant's.
func TestStatusHidesTheBellWhenTheRegistrationStampIsUnreadable(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")
	writeFile(t, d.ledger("workshop.scribe.json"),
		`{"endpoint":"workshop.scribe","instance":"workshop","process":{"pid":`+alivePid()+
			`,"started":"`+model.StartedAt(os.Getpid())+`"},"registered":"whenever"}`)
	seedDayLog(t, d, "2026-01-15",
		bellTryLine("workshop.scribe", "2026-01-15T22:47:36Z", 1, 3, "failed", "notifier exited 127"))

	if got := seatRow(t, d, "workshop.scribe"); strings.Contains(got, "bell") {
		t.Errorf("row = %q; a streak was reported against an unreadable registration", got)
	}
}

// THE #79 CASE ITSELF. The server registers and then rings the backlog with
// no window, so a notifier that is dead at startup fails within the same
// second as the registration. The registration is stamped in milliseconds
// and the try in whole seconds, so the try's stamp truncates to a time BEFORE
// the registration's, and a strict "after" comparison hides the one failure
// this field exists for (Assayer F1, 2026-09-16: 82ms after the registration →
// bare row).
//
// liveRow registers at 09:12:04.318Z. A try stamped 09:12:04Z is that same
// second and shows. FIRE CONTROL: 09:12:03Z is before the registration's
// second and stays hidden, so the case tests the bound and not the wiring.
func TestStatusShowsABellThatFailedInTheRegistrationsOwnSecond(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")

	seedDayLog(t, d, "2026-01-14",
		bellTryLine("workshop.scribe", "2026-01-14T09:12:03Z", 1, 3, "failed", "notifier exited 127"))
	if got := seatRow(t, d, "workshop.scribe"); got != "workshop.scribe: row ok, queue ok" {
		t.Errorf("control: a try the second before the registration was reported: %q", got)
	}

	seedDayLog(t, d, "2026-01-14",
		bellTryLine("workshop.scribe", "2026-01-14T09:12:04Z", 1, 3, "failed", "notifier exited 127"))
	want := "workshop.scribe: row ok, queue ok, mail unread, bell tried 1 of 3, " +
		"last failed (notifier exited 127) at 2026-01-14T09:12:04Z"
	if got := seatRow(t, d, "workshop.scribe"); got != want {
		t.Errorf("row = %q\nwant  %q\n(a failure in the registration's own second is the startup case)", got, want)
	}
}
