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
		"orphan queue workshop.stray",
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

// THE ORDER IS THE REPAIR ORDER. Dead pids come before orphan queues, so the
// queue a reap abandons is collected in the same run; orphan queues come
// before missing queues, so a queue this run creates is never destroyed by it.
func TestFindingsOrdersThePassesForACallerActingInSequence(t *testing.T) {
	rows := []*Registration{live(t, "workshop.scribe"), dead("workshop.gone")}
	got := Findings(rows, []Unreadable{{Endpoint: "workshop.bad", Err: fmt.Errorf("boom")}}, []string{"workshop.stray"})
	wantKinds := []FindingKind{DeadPID, OrphanQueue, MissingQueue, UnreadableRow}
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
// as an orphan. That is what makes the caller's blanket skip necessary, and a
// version that quietly counted it would hide the danger rather than remove it.
func TestFindingsDoesNotLetAnUnreadableRowClaimAQueue(t *testing.T) {
	// `atelier.x` is the fire control: no row of any kind names that instance,
	// so it must not be reported however the unreadable row is counted.
	got := Findings(nil, []Unreadable{{Endpoint: "workshop.bad", Err: fmt.Errorf("boom")}}, []string{"workshop.bad", "atelier.x"})
	var orphan, unread int
	for _, f := range got {
		switch f.Kind {
		case OrphanQueue:
			orphan++
		case UnreadableRow:
			unread++
		}
	}
	if orphan != 1 || unread != 1 {
		t.Errorf("Findings = %v, want one orphan and one unreadable row", kinds(got))
	}
}

// ------------------------------------------------------- the instance boundary

// An empty ledger reaps nothing. This is the shape of the failure the boundary
// exists to stop: a process whose ledger held no rows reached a broker serving
// another deployment, and its orphan pass destroyed every queue there.
func TestFindingsOnAnEmptyLedgerReapsNothing(t *testing.T) {
	queues := []string{
		"atelier.clerk", "atelier.herald", "atelier.scribe",
		"atelier.porter", "atelier.warden", "atelier.wright",
	}
	got := Findings(nil, nil, queues)
	if len(got) != 0 {
		t.Errorf("Findings = %v, want nothing", kinds(got))
	}
}

// A queue in an instance no row names is not a finding at all. It is never
// reported, so the caller can never delete it.
func TestFindingsIgnoresAQueueInAnInstanceTheLedgerDoesNotHold(t *testing.T) {
	rows := []*Registration{live(t, "workshop.scribe")}
	queues := []string{"workshop.scribe", "atelier.clerk", "atelier.scribe"}
	got := Findings(rows, nil, queues)
	if len(got) != 0 {
		t.Errorf("Findings = %v, want nothing", kinds(got))
	}
}

// The boundary bounds the orphan pass; it does not disable it. An orphan
// inside an instance the ledger holds is still reported.
func TestFindingsStillReportsAnOrphanInAnInstanceTheLedgerHolds(t *testing.T) {
	rows := []*Registration{live(t, "workshop.scribe")}
	queues := []string{"workshop.scribe", "workshop.stray", "atelier.stray"}
	got := kinds(Findings(rows, nil, queues))
	want := []string{"orphan queue workshop.stray"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("Findings = %v, want %v", got, want)
	}
}

// A dead row holds its instance. The seat whose process died is exactly the
// seat whose instance still needs cleaning up, so a ledger of dead rows must
// not stop reaping.
func TestFindingsADeadRowStillHoldsItsInstance(t *testing.T) {
	rows := []*Registration{dead("atelier.scribe")}
	queues := []string{"atelier.scribe", "atelier.stray"}
	got := kinds(Findings(rows, nil, queues))
	want := []string{"dead pid atelier.scribe", "orphan queue atelier.stray"}
	if len(got) != len(want) {
		t.Fatalf("Findings = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("finding %d = %q, want %q (whole: %v)", i, got[i], want[i], got)
		}
	}
}

// The instance comes from the ENDPOINT. A row file's contents are not checked
// against its name, so the stored Instance field can say anything; the
// endpoint is the one part the ledger's own naming guarantees.
func TestFindingsReadsTheInstanceFromTheEndpoint(t *testing.T) {
	row := live(t, "workshop.scribe")
	row.Instance = ""
	got := kinds(Findings([]*Registration{row}, nil, []string{"workshop.scribe", "workshop.stray"}))
	want := []string{"orphan queue workshop.stray"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("Findings = %v, want %v", got, want)
	}
}

// A stored instance field that names ANOTHER instance counts for nothing. The
// case above uses an empty field, which a version that reads the field and
// falls back to the endpoint would also pass. This one separates them: that
// version would let one row claim a whole second instance, and every queue
// there would become an orphan. It is the wrong-broker deletion again, reached
// through a row file's contents instead of an empty ledger.
func TestFindingsAStoredInstanceCannotClaimAnotherInstance(t *testing.T) {
	row := live(t, "workshop.scribe")
	row.Instance = "atelier"
	got := kinds(Findings([]*Registration{row}, nil, []string{"workshop.scribe", "atelier.clerk"}))
	if len(got) != 0 {
		t.Errorf("Findings = %v, want nothing: the row's endpoint is in workshop, so atelier is not this ledger's", got)
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
