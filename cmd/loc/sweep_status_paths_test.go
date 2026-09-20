package main

// The failure paths of the reconciler and the report (#27).
//
// Every arm injects a REAL failure through a fixture — a planted garbage row, a
// provider whose one call fails, a writer that refuses — rather than calling a
// function to see it run.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	model "github.com/tdebasis/locutorium/internal/presence"
)

// refusingWriter is a reader that went away mid-report.
type refusingWriter struct{ after int }

func (r *refusingWriter) Write(b []byte) (int, error) {
	if r.after > 0 {
		r.after--
		return len(b), nil
	}
	return 0, fmt.Errorf("the reader went away")
}

// deadRow plants a registration whose process was never alive.
func deadRow(t *testing.T, d *presenceDeployment, endpoint string) {
	t.Helper()
	writeFile(t, d.ledger(endpoint+".json"), fmt.Sprintf(
		`{"endpoint":%q,"instance":%q,"process":{"pid":4242,"started":"1970-01-01T00:00:00.000Z"},"registered":"2026-01-14T09:12:04.318Z"}`,
		endpoint, model.Instance(endpoint)))
}

// liveRow plants a registration whose process really is running.
func liveRow(t *testing.T, d *presenceDeployment, endpoint string) {
	t.Helper()
	pid := os.Getpid()
	writeFile(t, d.ledger(endpoint+".json"), fmt.Sprintf(
		`{"endpoint":%q,"instance":%q,"process":{"pid":%d,"started":%q},"registered":"2026-01-14T09:12:04.318Z"}`,
		endpoint, model.Instance(endpoint), pid, model.StartedAt(pid)))
}

// ------------------------------------------------------------- sweep failures

// A queue the medium refuses to destroy stops the reap where it stands. The
// row is NOT removed, because a row removed beside a queue that survived
// leaves a queue with no row, which NOTHING now cleans up (#141) — and the
// operator has not been told the medium is refusing.
func TestSweepStopsWhenTheQueueCannotBeDestroyed(t *testing.T) {
	d := newPresenceDeployment(t)
	deadRow(t, d, "workshop.scribe")
	d.spy.exists["workshop.scribe"] = true
	d.spy.deleteErr = fmt.Errorf("cannot reach the medium")

	code, out, errOut := exec("sweep")
	if code != 1 || out != "" || !strings.Contains(errOut, "cannot reach the medium") {
		t.Errorf("exit=%d stdout=%q stderr=%q; want a refusal naming the medium", code, out, errOut)
	}
	if _, err := os.Stat(d.ledger("workshop.scribe.json")); err != nil {
		t.Error("the row was removed although its queue survived")
	}
}

// A queue the medium refuses to create stops the repair pass and is reported.
// The row stays: it was right, and the queue is what is missing.
func TestSweepStopsWhenTheQueueCannotBeRecreated(t *testing.T) {
	d := newPresenceDeployment(t)
	liveRow(t, d, "workshop.scribe")
	d.spy.createErr = fmt.Errorf("cannot reach the medium")

	code, out, errOut := exec("sweep")
	if code != 1 || out != "" || !strings.Contains(errOut, "cannot reach the medium") {
		t.Errorf("exit=%d stdout=%q stderr=%q; want a refusal naming the medium", code, out, errOut)
	}
	if _, err := os.Stat(d.ledger("workshop.scribe.json")); err != nil {
		t.Error("the sweep removed a live row it could only repair")
	}
}

// A ledger row that cannot be REMOVED fails the reap rather than reporting a
// change that did not happen.
func TestSweepStopsWhenARowCannotBeRemoved(t *testing.T) {
	d := newPresenceDeployment(t)
	deadRow(t, d, "workshop.scribe")
	// A directory where the activity record's file belongs: Remove cannot
	// unlink it, and the operating system says so.
	if err := os.Mkdir(d.ledger("workshop.scribe.activity.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.ledger("workshop.scribe.activity.json"), "held"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := exec("sweep")
	if code != 1 || out != "" || errOut == "" {
		t.Errorf("exit=%d stdout=%q stderr=%q; want a refusal", code, out, errOut)
	}
}

// A reader that goes away mid-report is reported, not swallowed. The sweep
// writes as it acts, so a write that fails is a change the caller never saw.
func TestSweepReportsAReaderThatGoesAway(t *testing.T) {
	d := newPresenceDeployment(t)
	deadRow(t, d, "workshop.scribe")
	d.spy.exists["workshop.scribe"] = true

	changes, err := sweepVerb(&refusingWriter{}, nil)
	if err == nil || !strings.Contains(err.Error(), "the reader went away") {
		t.Errorf("sweepVerb = %d, %v; want the write failure reported", changes, err)
	}
}

// Each pass reports its own line, and the change count is what the daemon
// logs. A QUEUE WITH NO ROW IS REPORTED AND NOT COUNTED: nothing was changed
// about it, in either instance, so the count stays at the reap and the repair.
func TestSweepCountsAndNamesEveryChange(t *testing.T) {
	d := newPresenceDeployment(t)
	deadRow(t, d, "workshop.scribe")
	liveRow(t, d, "workshop.clerk")
	d.spy.exists["workshop.scribe"] = true // the dead seat's queue
	d.spy.exists["workshop.stray"] = true  // a queue no row claims, in an instance the ledger holds
	d.spy.exists["atelier.stray"] = true   // a queue in an instance the ledger does not hold

	var out strings.Builder
	changes, err := sweepVerb(&out, nil)
	if err != nil {
		t.Fatalf("sweepVerb: %v", err)
	}
	if changes != 2 {
		t.Errorf("changes = %d, want 2 (one reap, one repair; a report is not a change)", changes)
	}
	want := "workshop.scribe: reaped, the process is gone\n" +
		"atelier.stray: queue with no row; nothing removes it; remove it with loc unsubscribe atelier.stray\n" +
		"workshop.stray: queue with no row; nothing removes it; remove it with loc unsubscribe workshop.stray\n" +
		"workshop.clerk: queue recreated\n"
	if out.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", out.String(), want)
	}
	// THE ONE DELETION IS THE DEAD SEAT'S OWN QUEUE. Neither queue with no
	// row was touched, whatever instance it sat in.
	if len(d.spy.deleted) != 1 || d.spy.deleted[0] != "workshop.scribe" {
		t.Errorf("the sweep deleted %v, want only the dead row's own queue", d.spy.deleted)
	}
}

// A report NAMES a queue in an instance the ledger holds no row for. The next
// beat will not touch it, and the operator cannot remove a queue the report
// keeps from them.
func TestStatusNamesAQueueInAnInstanceItDoesNotHold(t *testing.T) {
	d := newPresenceDeployment(t)
	liveRow(t, d, "workshop.scribe")
	d.spy.exists["workshop.scribe"] = true
	d.spy.exists["atelier.stray"] = true

	code, out, errOut := exec("status")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
	want := "atelier.stray: queue with no row; nothing removes it; remove it with loc unsubscribe atelier.stray\n"
	if !strings.Contains(out, want) {
		t.Errorf("status omits %q; whole report:\n%s", want, out)
	}
}

// ------------------------------------------------------------ status failures

// A report that cannot list the queues prints NOTHING and exits non-zero. Half
// a report is worse than none: the daemon line would stand alone and read as a
// deployment with no seats.
func TestStatusPrintsNothingWhenTheQueuesCannotBeListed(t *testing.T) {
	d := newPresenceDeployment(t)
	liveRow(t, d, "workshop.scribe")
	d.spy.queuesErr = fmt.Errorf("cannot reach the medium")

	code, out, errOut := exec("status")
	if code != 1 || out != "" || !strings.Contains(errOut, "cannot reach the medium") {
		t.Errorf("exit=%d stdout=%q stderr=%q; want nothing printed and a refusal", code, out, errOut)
	}
}

// A reader that goes away is reported rather than swallowed.
func TestStatusReportsAReaderThatGoesAway(t *testing.T) {
	d := newPresenceDeployment(t)
	liveRow(t, d, "workshop.scribe")
	d.spy.exists["workshop.scribe"] = true

	if err := statusVerb(&refusingWriter{}); err == nil {
		t.Error("statusVerb swallowed a write failure")
	}
	// And one that fails partway through the seat lines.
	if err := seatLines(&refusingWriter{}, d.spy); err == nil {
		t.Error("seatLines swallowed a write failure")
	}
	d.spy.exists["workshop.stray"] = true
	if err := seatLines(&refusingWriter{after: 1}, d.spy); err == nil {
		t.Error("seatLines swallowed a write failure on the queue-with-no-row line")
	}
}

// Every kind of disagreement has its own sentence, and each says what the next
// beat will do about it.
func TestStatusNamesEveryKindOfDisagreement(t *testing.T) {
	d := newPresenceDeployment(t)
	liveRow(t, d, "workshop.clerk")  // whole
	liveRow(t, d, "workshop.scribe") // live, queue missing
	deadRow(t, d, "atelier.scribe")  // pid dead
	writeFile(t, d.ledger("workshop.legacy.json"), "{not json at all")
	d.spy.exists["workshop.clerk"] = true
	d.spy.exists["atelier.stray"] = true

	var out strings.Builder
	if err := seatLines(&out, d.spy); err != nil {
		t.Fatalf("seatLines: %v", err)
	}
	for _, want := range []string{
		"atelier.scribe: pid dead, next beat reaps it\n",
		"workshop.clerk: row ok, queue ok\n",
		"workshop.legacy: row unreadable: ",
		"workshop.scribe: queue missing, next beat repairs it\n",
		"atelier.stray: queue with no row; nothing removes it; remove it with loc unsubscribe atelier.stray\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the report omits %q; whole report:\n%s", want, out.String())
		}
	}
	// The seats are in endpoint order, and the queues with no row come after
	// them.
	if i, j := strings.Index(out.String(), "atelier.scribe:"), strings.Index(out.String(), "workshop.clerk:"); i > j {
		t.Error("the seats are not in endpoint order")
	}
	if i, j := strings.Index(out.String(), "workshop.scribe:"), strings.Index(out.String(), "atelier.stray:"); i > j {
		t.Error("a queue with no row was printed among the seats")
	}
}

// A ledger the report cannot list fails the report.
func TestStatusReportsALedgerItCannotList(t *testing.T) {
	d := newPresenceDeployment(t)
	d.blockTheLedger(t)

	code, out, _ := exec("status")
	if code != 1 || out != "" {
		t.Errorf("exit %d, stdout %q; want 1 and nothing", code, out)
	}
}

// -------------------------------------------------------------- the daemon line

// daemonPID writes the two-line pidfile the daemon leaves behind.
func daemonPID(t *testing.T, home string, pid int, started string) {
	t.Helper()
	writeFile(t, filepath.Join(home, "run", "loc.pid"), fmt.Sprintf("%d\n%s\n", pid, started))
}

func TestDaemonLineReadsThePidfileAndTheHeartbeat(t *testing.T) {
	pid := os.Getpid()
	cases := []struct {
		name  string
		setup func(t *testing.T, home string)
		want  string
	}{
		{
			name:  "no pidfile at all",
			setup: func(t *testing.T, home string) {},
			want:  "daemon: not running\n",
		},
		{
			name: "a pidfile naming a process that is gone",
			setup: func(t *testing.T, home string) {
				daemonPID(t, home, 4242, "1970-01-01T00:00:00.000Z")
			},
			want: "daemon: not running\n",
		},
		{
			name: "a pidfile that is not a number",
			setup: func(t *testing.T, home string) {
				writeFile(t, filepath.Join(home, "run", "loc.pid"), "not-a-pid\n")
			},
			want: "daemon: not running\n",
		},
		{
			name: "a pidfile holding a pid of zero",
			setup: func(t *testing.T, home string) {
				writeFile(t, filepath.Join(home, "run", "loc.pid"), "0\n")
			},
			want: "daemon: not running\n",
		},
		{
			name: "running, with no heartbeat log yet",
			setup: func(t *testing.T, home string) {
				daemonPID(t, home, pid, model.StartedAt(pid))
			},
			want: "daemon: running, pid " + strconv.Itoa(pid) + ", last beat no beat yet\n",
		},
		{
			name: "running, with an empty heartbeat log",
			setup: func(t *testing.T, home string) {
				daemonPID(t, home, pid, model.StartedAt(pid))
				writeFile(t, filepath.Join(home, "run", "heartbeat", time.Now().Format("2006-01-02")+".log"), "\n")
			},
			want: "daemon: running, pid " + strconv.Itoa(pid) + ", last beat no beat yet\n",
		},
		{
			name: "running, and the last line of today's log is the last beat",
			setup: func(t *testing.T, home string) {
				daemonPID(t, home, pid, model.StartedAt(pid))
				writeFile(t, filepath.Join(home, "run", "heartbeat", time.Now().Format("2006-01-02")+".log"),
					"2026-09-09T10:00:00Z swept, 0 changes\n2026-09-09T10:05:00Z swept, 2 changes\n")
			},
			want: "daemon: running, pid " + strconv.Itoa(pid) + ", last beat 2026-09-09T10:05:00Z swept, 2 changes\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("LOC_HOME", home)
			tc.setup(t, home)
			if got := daemonLine(); got != tc.want {
				t.Errorf("daemonLine = %q, want %q", got, tc.want)
			}
		})
	}
}

// A pidfile carrying only a pid, with no start time, reads as NOT RUNNING.
// The pair is the identity: a bare number goes on looking alive the moment the
// operating system hands it to something else, which is what a restart does.
func TestDaemonLineRefusesAPidfileWithNoStartTime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	writeFile(t, filepath.Join(home, "run", "loc.pid"), strconv.Itoa(os.Getpid())+"\n")
	if got, want := daemonLine(), "daemon: not running\n"; got != want {
		t.Errorf("daemonLine = %q, want %q", got, want)
	}
}
