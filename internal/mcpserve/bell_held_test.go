package mcpserve

// A RECEIVER MAY HOLD THE BELL (#228).
//
// A webhook receiver that sits between a pane and an agent can have the bell
// and be unable to pass it on yet. It answers 202 with `Loc-Bell: held`. That
// try is written down as `held`, and it is the one result the counting reads:
// it spends no try, counts as no ring, is not charged to the breaker, and the
// next try waits the recheck gap instead of the fixed one.
//
// Every case drives the bell by hand through the same seams the retry cases
// use, so nothing sleeps and no case asserts on the wall clock.

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// held is what the webhook notifier returns for a held answer, built the way
// the notifier builds it so a case reads the same text the record does.
func held() error {
	return heldAnswer{errors.New("webhook status 202, Loc-Bell: held")}
}

// A HELD TRY IS WRITTEN DOWN, USES NO TRY AND COSTS THE BREAKER NOTHING. The
// cap is one ring a minute, so a held try that kept its charge would leave the
// arrival after it suppressed.
//
// PLANT: in ring, drop `|| errors.Is(err, ErrHeld)`, and the next arrival is
// suppressed.
// PLANT: in counted, increment b.tries for a held try too, and the record
// reads `2/3 held`.
// PLANT: in outcome, delete the ErrHeld arm, and the try reads `failed`.
func TestBell_AHeldTryUsesNoTryAndIsNotCharged(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{unread: 2, nudgeErrs: []error{held()}}
	b, sch, stderr := retryBell(t, f, func() time.Time { return bellRecordClock })
	b.perMin = 1

	b.arrived("")
	b.fire()

	if got := results(t, dir); !sameStrings(got, []string{"1/3 held"}) {
		t.Fatalf("the record holds %v; want one held try", got)
	}
	b.mu.Lock()
	tries, rangs, minN, hourN := b.tries, b.rangs, b.minuteN, b.hourN
	b.mu.Unlock()
	if tries != 0 {
		t.Errorf("a held try used %d of the streak's tries; it uses none", tries)
	}
	if rangs != 0 {
		t.Errorf("a held try counted %d rings; the seat was not told", rangs)
	}
	if minN != 0 || hourN != 0 {
		t.Errorf("a held try was charged (minute %d, hour %d); it typed nothing", minN, hourN)
	}
	if !tryArmed(b) {
		t.Error("no recheck is waiting after a held try")
	}
	// IT IS NOT A FAILED BELL. The delivery log says it was held; the
	// runtime's log, where a broken bell shouts, says nothing.
	if strings.Contains(stderr.String(), "bell failed") {
		t.Errorf("a held try was reported as a failed bell: %q", stderr.String())
	}
	if log := delivery(t, dir); !strings.Contains(log, "bell "+seat+" held by the receiver") {
		t.Errorf("the held try was not written to the delivery log: %q", log)
	}

	// The cap is one a minute, and nothing has been charged against it.
	b.arrived("workshop.binder")
	b.fire()
	if got := len(sch.delays()); got != 2 {
		t.Fatalf("the arrival scheduled nothing; the delays are %v", sch.delays())
	}
	if got := results(t, dir); !sameStrings(got, []string{"1/3 held", "1/3 rang"}) {
		t.Errorf("the record holds %v; want the held try and then a ring the breaker let through", got)
	}
	if log := delivery(t, dir); strings.Contains(log, "BREAKER TRIPPED") {
		t.Errorf("a held try tripped a cap of one: %q", log)
	}
}

// AFTER A HELD TRY THE BELL WAITS THE RECHECK GAP. The receiver has the bell,
// so asking again at the fixed gap would only ask it the same question
// sooner. The try after the recheck is an ordinary one and waits the fixed gap.
//
// PLANT: in counted, arm b.gap for a held try too.
func TestBell_AfterAHeldTryTheNextWaitsForTheRecheckGap(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{unread: 2, nudgeErrs: []error{held(), busy("pane_in_mode")}}
	b, sch, _ := retryBell(t, f, func() time.Time { return bellRecordClock })

	b.arrived("")
	b.fire()
	if got := sch.delays(); len(got) != 1 || got[0] != testHeldGap {
		t.Fatalf("a held try scheduled %v; want one recheck at %v", got, testHeldGap)
	}

	sch.run(t, 0)
	want := []time.Duration{testHeldGap, testGap}
	if got := sch.delays(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("the tries were scheduled %v; want %v", got, want)
	}
	if got := results(t, dir); !sameStrings(got, []string{"1/3 held", "1/3 refused"}) {
		t.Errorf("the record holds %v; the recheck is the same try asked again", got)
	}
}

// A RECEIVER THAT DIES AFTER `held` LEAVES NO MAIL SILENT. The recheck finds
// nothing listening, that try is `failed`, and the streak counts on from there
// to its give-up exactly as it does for any broken bell. No code is special to
// this case: the proof is that the ordinary path carries it.
//
// The notifier here is the real webhook notifier against a receiver of this
// test's own on loopback, closed after its first answer.
//
// PLANT: in the webhook Ring, delete the held arm, and the first try reads
// `rang`.
// PLANT: in counted, make a held try final, and the streak ends after it.
func TestBell_AReceiverThatDiesAfterHeldIsRecordedFailedAndTheStreakCarriesOn(t *testing.T) {
	dir := home(t, "provider = none\n")
	r := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.ReadAll(req.Body)
		w.Header().Set("Loc-Bell", "held")
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(r.Close)

	f := &fake{unread: 2}
	b, sch, _ := retryBell(t, f, func() time.Time { return bellRecordClock })
	b.s.address = r.URL
	b.s.d.Notify = newWebhookNotifier(b.s.log)

	b.arrived("")
	b.fire()
	if got := results(t, dir); !sameStrings(got, []string{"1/3 held"}) {
		t.Fatalf("the record holds %v; want the receiver's held answer", got)
	}

	r.Close() // the receiver dies with the bell in its hands
	sch.run(t, 0)
	sch.run(t, 1)
	sch.run(t, 2)

	want := []string{"1/3 held", "1/3 failed", "2/3 failed", "3/3 failed", "gave-up/3 rang=0 last=failed"}
	if got := results(t, dir); !sameStrings(got, want) {
		t.Errorf("the record holds %v\nwant              %v", got, want)
	}
	if got := sch.delays(); len(got) != 3 || got[0] != testHeldGap || got[1] != testGap || got[2] != testGap {
		t.Errorf("the tries were scheduled %v; want the recheck and then the fixed gap", got)
	}
	if tryArmed(b) {
		t.Error("a try is still waiting after the bell gave up")
	}
}

// A HELD TRY IS NOT A RING IN THE GIVE-UP. The give-up line says whether the
// seat was ever told, and a receiver that held the bell had not told it.
//
// PLANT: in counted, count a held try in b.rangs, and the line says rang=2.
func TestBell_AHeldTryIsNotCountedInTheGiveUpsRang(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{unread: 2, nudgeErrs: []error{
		nil, // try 1 rang
		held(),
		errors.New("webhook: the receiver could not be reached"),
		errors.New("webhook: the receiver could not be reached"),
	}}
	b, sch, _ := retryBell(t, f, func() time.Time { return bellRecordClock })

	b.arrived("")
	b.fire()      // 1/3 rang
	sch.run(t, 0) // 2/3 held
	sch.run(t, 1) // 2/3 failed
	sch.run(t, 2) // 3/3 failed, and the give-up

	want := []string{"1/3 rang", "2/3 held", "2/3 failed", "3/3 failed", "gave-up/3 rang=1 last=failed"}
	if got := results(t, dir); !sameStrings(got, want) {
		t.Errorf("the record holds %v\nwant              %v", got, want)
	}
}
