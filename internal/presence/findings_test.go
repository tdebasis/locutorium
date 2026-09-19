package presence

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// live is a registration whose process really is running: this test process,
// which the operating system will confirm.
func live(t *testing.T, endpoint string) *Registration {
	t.Helper()
	pid := os.Getpid()
	return &Registration{
		Endpoint: endpoint,
		Instance: Instance(endpoint),
		Process:  Process{PID: pid, Started: StartedAt(pid)},
	}
}

// dead is a registration whose pid was never anything.
func dead(endpoint string) *Registration {
	return &Registration{
		Endpoint: endpoint,
		Instance: Instance(endpoint),
		Process:  Process{PID: 4242, Started: "1970-01-01T00:00:00.000Z"},
	}
}

func kinds(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Kind.String()+" "+f.Endpoint)
	}
	return out
}

// Three records that agree produce nothing to say. This is the case that runs
// every five minutes for the rest of the deployment's life.
func TestFindingsSaysNothingWhenTheRecordsAgree(t *testing.T) {
	rows := []*Registration{live(t, "workshop.scribe"), live(t, "workshop.clerk")}
	got := Findings(rows, nil, []string{"workshop.clerk", "workshop.scribe"})
	if len(got) != 0 {
		t.Errorf("Findings = %v, want nothing", kinds(got))
	}
}

// Each disagreement is named by its own kind, and a dead row is reported once
// — as a reap, never also as a queue to repair.
func TestFindingsNamesEachDisagreement(t *testing.T) {
	rows := []*Registration{
		dead("workshop.gone"),      // pid dead, queue still there
		dead("workshop.vanished"),  // pid dead, queue already gone
		live(t, "workshop.scribe"), // live, queue missing
		live(t, "workshop.clerk"),  // live and whole
	}
	queues := []string{"workshop.clerk", "workshop.gone", "workshop.stray"}
	got := kinds(Findings(rows, nil, queues))
	want := []string{
		"dead pid workshop.gone",
		"dead pid workshop.vanished",
		"queue with no row workshop.stray",
		"missing queue workshop.scribe",
	}
	if len(got) != len(want) {
		t.Fatalf("Findings = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("finding %d = %q, want %q (whole: %v)", i, got[i], want[i], got)
		}
	}
}

// THE ORDER IS THE REPAIR ORDER. Dead pids come before queues with no row, so
// the queue a reap destroys is not also reported as unclaimed in the same run;
// queues with no row come before missing queues, so a queue this run creates is
// never named as unclaimed by it.
func TestFindingsOrdersThePassesForACallerActingInSequence(t *testing.T) {
	rows := []*Registration{live(t, "workshop.scribe"), dead("workshop.gone")}
	got := Findings(rows, []Unreadable{{Endpoint: "workshop.bad", Err: fmt.Errorf("boom")}}, []string{"workshop.stray"})
	wantKinds := []FindingKind{DeadPID, QueueNoRow, MissingQueue, UnreadableRow}
	if len(got) != len(wantKinds) {
		t.Fatalf("Findings = %v, want four findings", kinds(got))
	}
	for i, k := range wantKinds {
		if got[i].Kind != k {
			t.Errorf("finding %d is %v, want %v (whole: %v)", i, got[i].Kind, k, kinds(got))
		}
	}
}

// An unreadable row does NOT count as a row, so the queue it may hold reports
// as a queue with no row, beside the unreadable row itself. Both facts are
// true and the caller prints both. Neither one deletes anything, so counting
// the unreadable row as a claim would only hide a queue from the operator.
func TestFindingsDoesNotLetAnUnreadableRowClaimAQueue(t *testing.T) {
	// `atelier.x` is in an instance no row of any kind names. It is reported
	// too: the report holds nothing back (#141).
	got := Findings(nil, []Unreadable{{Endpoint: "workshop.bad", Err: fmt.Errorf("boom")}}, []string{"workshop.bad", "atelier.x"})
	var noRow, unread int
	for _, f := range got {
		switch f.Kind {
		case QueueNoRow:
			noRow++
		case UnreadableRow:
			unread++
		}
	}
	if noRow != 2 || unread != 1 {
		t.Errorf("Findings = %v, want two queues with no row and one unreadable row", kinds(got))
	}
}

// ------------------------------------------- a queue with no row is reported

// AN EMPTY LEDGER REPORTS EVERY QUEUE AND THAT IS ALL. This is the shape of
// the 2026-09-18 incident: a process whose ledger held no rows reached a
// broker serving another deployment. It now has nothing to act on — every
// finding here is a report, and the caller deletes a queue only beside a dead
// row of its own, which an empty ledger cannot have.
func TestFindingsOnAnEmptyLedgerReportsEveryQueueAndDeletesNothing(t *testing.T) {
	queues := []string{
		"atelier.clerk", "atelier.herald", "atelier.scribe",
		"atelier.porter", "atelier.warden", "atelier.wright",
	}
	got := Findings(nil, nil, queues)
	if len(got) != len(queues) {
		t.Fatalf("Findings = %v, want one finding per queue", kinds(got))
	}
	for _, f := range got {
		if f.Kind != QueueNoRow {
			t.Errorf("%s is reported as %v; an empty ledger can only report", f.Endpoint, f.Kind)
		}
	}
}

// A queue in an instance no row names IS a finding. Nothing removes it, and
// the operator cannot remove what the report does not name.
func TestFindingsReportsAQueueInAnInstanceTheLedgerDoesNotHold(t *testing.T) {
	rows := []*Registration{live(t, "workshop.scribe")}
	queues := []string{"workshop.scribe", "atelier.clerk", "atelier.scribe"}
	got := kinds(Findings(rows, nil, queues))
	want := []string{"queue with no row atelier.clerk", "queue with no row atelier.scribe"}
	if len(got) != len(want) {
		t.Fatalf("Findings = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("finding %d = %q, want %q (whole: %v)", i, got[i], want[i], got)
		}
	}
}

// Every instance is reported in one listing, in endpoint order, with no
// instance treated differently from another.
func TestFindingsReportsAQueueWithNoRowInEveryInstanceAtOnce(t *testing.T) {
	rows := []*Registration{live(t, "workshop.scribe")}
	queues := []string{"workshop.scribe", "workshop.stray", "atelier.stray"}
	got := kinds(Findings(rows, nil, queues))
	want := []string{"queue with no row atelier.stray", "queue with no row workshop.stray"}
	if len(got) != len(want) {
		t.Fatalf("Findings = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("finding %d = %q, want %q (whole: %v)", i, got[i], want[i], got)
		}
	}
}

// A dead row is reported beside a queue with no row, and the two are different
// findings about different endpoints. The caller deletes the first and prints
// the second.
func TestFindingsSeparatesADeadRowFromAQueueWithNoRow(t *testing.T) {
	rows := []*Registration{dead("atelier.scribe")}
	queues := []string{"atelier.scribe", "atelier.stray"}
	got := kinds(Findings(rows, nil, queues))
	want := []string{"dead pid atelier.scribe", "queue with no row atelier.stray"}
	if len(got) != len(want) {
		t.Fatalf("Findings = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("finding %d = %q, want %q (whole: %v)", i, got[i], want[i], got)
		}
	}
}

// THE ROW'S STORED INSTANCE FIELD IS NOT READ. A row file's contents are not
// checked against its name, so the field can say anything; an empty one
// changes no finding.
func TestFindingsIgnoresAnEmptyStoredInstance(t *testing.T) {
	row := live(t, "workshop.scribe")
	row.Instance = ""
	got := kinds(Findings([]*Registration{row}, nil, []string{"workshop.scribe", "workshop.stray"}))
	want := []string{"queue with no row workshop.stray"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("Findings = %v, want %v", got, want)
	}
}

// A stored instance field that names ANOTHER instance counts for nothing
// either — it cannot make a finding and it cannot SUPPRESS one. A version that
// read the field and let a row claim a whole instance would hide every queue
// there from the report, and the operator would never learn the queues exist.
func TestFindingsAStoredInstanceCannotSuppressAFinding(t *testing.T) {
	row := live(t, "workshop.scribe")
	row.Instance = "atelier"
	got := kinds(Findings([]*Registration{row}, nil, []string{"workshop.scribe", "atelier.clerk"}))
	want := []string{"queue with no row atelier.clerk"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("Findings = %v, want %v: no row claims atelier.clerk, whatever the field says", got, want)
	}
}

// The unreadable finding carries the reason, because the operator has to fix
// the file and "unreadable" alone does not say what is wrong with it.
func TestFindingsCarriesTheReasonARowCouldNotBeRead(t *testing.T) {
	boom := fmt.Errorf("invalid character '{'")
	got := Findings(nil, []Unreadable{{Endpoint: "workshop.bad", Err: boom}}, nil)
	if len(got) != 1 || got[0].Err != boom {
		t.Errorf("Findings = %+v, want the row's own error", got)
	}
}

// ------------------------------------------------------------------- ListAll

// ListAll reports a row it cannot read instead of dropping it, and it crosses
// instance boundaries, because a reconciler owns the whole machine.
func TestListAllReportsWhatItCannotRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	dir := filepath.Join(home, "run", "presence")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Save(&Registration{Endpoint: "workshop.scribe", Instance: "workshop"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(&Registration{Endpoint: "atelier.scribe", Instance: "atelier"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workshop.bad.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	rows, unreadable, err := ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(rows) != 2 || rows[0].Endpoint != "atelier.scribe" || rows[1].Endpoint != "workshop.scribe" {
		t.Errorf("rows = %v, want both instances in endpoint order", rows)
	}
	if len(unreadable) != 1 || unreadable[0].Endpoint != "workshop.bad" {
		t.Fatalf("unreadable = %+v, want the one bad row named", unreadable)
	}
	if unreadable[0].Err == nil {
		t.Error("the unreadable row carries no reason")
	}
	// The old listing is the comparison: it drops the same row in silence.
	old, err := List("workshop")
	if err != nil {
		t.Fatal(err)
	}
	if len(old) != 1 {
		t.Errorf("List = %v, want the one readable workshop row", old)
	}
}

// A ledger directory that was never created is an empty deployment, not a
// failure: nothing has subscribed yet.
func TestListAllOnADeploymentWithNoLedger(t *testing.T) {
	t.Setenv("LOC_HOME", t.TempDir())
	rows, unreadable, err := ListAll()
	if err != nil || rows != nil || unreadable != nil {
		t.Errorf("ListAll = %v, %v, %v; want nothing and no error", rows, unreadable, err)
	}
}

// The daemon's pidfile is not a seat's pidfile, and the two never collide.
func TestDaemonPIDFileIsNotASeatsFile(t *testing.T) {
	t.Setenv("LOC_HOME", "/scratch")
	if got, want := DaemonPIDFile(), filepath.Join("/scratch", "run", "loc.pid"); got != want {
		t.Errorf("DaemonPIDFile = %q, want %q", got, want)
	}
	if DaemonPIDFile() == ServerPIDFile("workshop.scribe") {
		t.Error("the daemon's pidfile and a seat's pidfile are the same path")
	}
}
