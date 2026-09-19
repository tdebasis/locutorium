package mcpserve

// A REFUSED BELL RINGS AGAIN UNTIL THE MAIL IS READ.
//
// The tmux notifier refuses a pane in copy mode and a pane that is not at an
// empty prompt. Both refusals are right and both are transient: the seat is
// busy. Before this, the message then waited in the queue with nothing to
// announce it, and one was measured waiting 3m57s.
//
// Every case here drives the bell by hand. The retry timer goes through a
// seam that records the delay and hands the case the function, so nothing
// sleeps and no case asserts on the wall clock.

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// busy is what the tmux notifier returns for a transient refusal, built the
// way the notifier builds it so a case reads the same text the record does.
func busy(reason string) error {
	return busyRefusal{fmt.Errorf("refused: %s", reason)}
}

// schedule stands in for time.AfterFunc. It records what delay each retry was
// scheduled for and holds the function, so the case decides when the retry
// runs.
type schedule struct {
	delay []time.Duration
	fn    []func()
}

func (s *schedule) after(d time.Duration, f func()) *time.Timer {
	s.delay = append(s.delay, d)
	s.fn = append(s.fn, f)
	// A timer that cannot fire inside a test's lifetime. Stop on it still
	// works, which is what the bell calls when it cancels a retry.
	return time.AfterFunc(time.Hour, func() {})
}

func (s *schedule) delays() []time.Duration {
	return append([]time.Duration(nil), s.delay...)
}

// run is the scheduled retry firing.
func (s *schedule) run(t *testing.T, i int) {
	t.Helper()
	if i >= len(s.fn) {
		t.Fatalf("retry %d was never scheduled; the delays were %v", i, s.delay)
	}
	s.fn[i]()
}

// retryBell builds a bell over the fake's seams with no MCP session around
// it. The window is an hour, so the case fires the bell itself and the
// coalescing timer never goes off on its own. The case must have called home
// first.
func retryBell(t *testing.T, f *fake, now func() time.Time) (*bell, *schedule, *bytes.Buffer) {
	t.Helper()
	d := f.deps()
	if now != nil {
		d.Now = now
	}
	var stderr bytes.Buffer
	d.Stderr = &stderr
	d = d.withDefaults()

	s := &server{d: d, address: "workshop:1.2"}
	sch := &schedule{}
	b := &bell{
		s:         s,
		window:    time.Hour,
		perMin:    defaultBreakerMinute,
		perHour:   defaultBreakerHour,
		retryBase: time.Duration(defaultWakeRetry) * time.Second,
		afterFunc: sch.after,
	}
	s.b = b
	return b, sch, &stderr
}

// retryArmed reports whether a retry is waiting.
func retryArmed(b *bell) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.retryTimer != nil
}

// The whole of the defect, in one case: the pane was busy, and the seat is
// told anyway once the pane is free. The second ring carries the count the
// QUEUE holds, not the count the first ring remembered.
//
// PLANT: in fire, replace `b.refused(err)` with `b.broken(err)`.
func TestBell_ARefusedBellRingsAgainAndCarriesTheCount(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{unread: 3, nudgeErrs: []error{busy("pane_in_mode")}}
	b, sch, _ := retryBell(t, f, nil)

	b.arrived("")
	b.fire()
	if got := f.bells(); len(got) != 1 {
		t.Fatalf("the first ring was %v; want one attempt", got)
	}
	if got := sch.delays(); len(got) != 1 || got[0] != 15*time.Second {
		t.Fatalf("a busy refusal scheduled %v; want one retry at 15s", got)
	}

	sch.run(t, 0)

	got := f.bells()
	if len(got) != 2 {
		t.Fatalf("the seat was rung %d times; a busy pane must be asked again", len(got))
	}
	if got[1] != "🔔 3 new → read" {
		t.Errorf("the second bell said %q; the queue holds three", got[1])
	}
	// The retry asks how much mail is waiting and never who sent it, so it
	// names no seat.
	if c := f.couriersRung()[1]; c != defaultCourierName {
		t.Errorf("the retry named the courier %q; want %q", c, defaultCourierName)
	}
	log := delivery(t, dir)
	if !strings.Contains(log, "wake "+seat+" count=3") {
		t.Errorf("the retry's wake was not written down: %q", log)
	}
	if !strings.Contains(log, "retry "+seat+" rang after 1 refusals") {
		t.Errorf("the end of the streak was not written down: %q", log)
	}
	if retryArmed(b) {
		t.Error("a retry is still waiting after the bell rang")
	}
}

// THE RETRY ASKS THE QUEUE FIRST. The seat may have read the mail while the
// pane was busy, and a bell for an empty queue is a wake the seat cannot act
// on.
//
// PLANT: in retry, delete the `if n <= 0` arm.
func TestBell_ARetryThatFindsAnEmptyQueueStops(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{unread: 0, nudgeErr: busy("pane_in_mode")}
	b, sch, _ := retryBell(t, f, nil)

	b.arrived("")
	b.fire()
	sch.run(t, 0)

	if got := f.bells(); len(got) != 1 {
		t.Errorf("the seat was rung %v; the queue was empty at retry time", got)
	}
	if log := delivery(t, dir); !strings.Contains(log, "retry "+seat+" stopped: the queue is empty") {
		t.Errorf("the stop was not written down: %q", log)
	}
	if retryArmed(b) {
		t.Error("a retry is still waiting for a queue that is empty")
	}
}

// A COUNT THAT CANNOT BE READ IS NOT AN EMPTY QUEUE. The broker can time out
// with the connection still up, and the mail is still waiting. A retry that
// ended the streak there would leave mail with no bell, no retry and no line
// in the log, which is the failure the retry exists to remove. The retry says
// that it could not read the count, and it asks again.
//
// PLANT: in retry, make the `err != nil` arm call endStreak and return.
func TestBell_ARetryThatCannotReadTheCountAsksAgain(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{unread: 3, nudgeErrs: []error{busy("not a prompt")}}
	b, sch, _ := retryBell(t, f, nil)

	b.arrived("")
	b.fire()

	f.unreadErr = errors.New("context deadline exceeded")
	sch.run(t, 0)

	if got := f.bells(); len(got) != 1 {
		t.Errorf("the seat was rung %v; the count was not known, so there was nothing to ring for", got)
	}
	if !retryArmed(b) {
		t.Fatal("no retry is waiting; a count that cannot be read was taken for an empty queue")
	}
	if got := sch.delays(); len(got) != 2 || got[1] != 30*time.Second {
		t.Errorf("the scheduled waits were %v; want [15s 30s]", got)
	}
	if log := delivery(t, dir); !strings.Contains(log, "retry "+seat+" could not read the count") {
		t.Errorf("the failed read was not written down: %q", log)
	}

	// The broker answers again, and the mail that waited is announced.
	f.unreadErr = nil
	sch.run(t, 1)
	got := f.bells()
	if len(got) != 2 || got[1] != "🔔 3 new → read" {
		t.Errorf("the bells were %v; the three messages that waited must be announced", got)
	}
}

// A BROKEN BELL IS NOT RETRIED. There is no pane address, or tmux cannot be
// read, or the courier exited non-zero: asking again repairs none of these,
// and a retry every fifteen seconds would be a loop with no end.
//
// PLANT: in ring, replace `errors.Is(err, ErrBusy)` in fire's switch with
// `err != nil`.
func TestBell_ABrokenBellIsNotRetried(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{unread: 2, nudgeErr: errors.New("exit status 3")}
	b, sch, stderr := retryBell(t, f, nil)

	b.arrived("")
	b.fire()

	if got := sch.delays(); len(got) != 0 {
		t.Errorf("a broken bell scheduled %v; nothing repairs it", got)
	}
	if retryArmed(b) {
		t.Error("a broken bell left a retry waiting")
	}
	// CONTROL: it is still recorded, exactly as before this change.
	if log := delivery(t, dir); !strings.Contains(log, "bell failed "+seat+": exit status 3") {
		t.Errorf("the failure was not written down: %q", log)
	}
	if !strings.Contains(stderr.String(), "bell failed "+seat) {
		t.Errorf("nothing reached the runtime's log: %q", stderr.String())
	}
}

// A BUSY REFUSAL DOES NOT SPEND THE BREAKER. The breaker caps how often the
// pane is TYPED INTO, and a refusal typed nothing. Charged for refusals, the
// breaker would trip on the very streak it is meant to sit out, and would then
// suppress the ring the seat is waiting for.
//
// PLANT: in ring, replace `b.giveBack(minute, hour)` with `_, _ = minute, hour`.
func TestBell_ABusyRefusalDoesNotSpendTheBreaker(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{unread: 1, nudgeErrs: []error{busy("pane_in_mode"), busy("not a prompt")}}
	// The clock is pinned, so every attempt lands in one calendar minute and
	// the case does not depend on where a minute boundary falls.
	b, _, _ := retryBell(t, f, func() time.Time { return bellRecordClock })
	b.perMin = 2

	for i := 0; i < 3; i++ {
		b.arrived("")
		b.fire()
	}

	if got := f.bells(); len(got) != 3 {
		t.Fatalf("the notifier saw %d rings; two refusals and one ring were due", len(got))
	}
	log := delivery(t, dir)
	if strings.Contains(log, "BREAKER TRIPPED") {
		t.Errorf("two refusals tripped a cap of two: %q", log)
	}
	if !strings.Contains(log, "wake "+seat+" count=1") {
		t.Errorf("the ring after the refusals never typed: %q", log)
	}
}

// THE WAIT DOUBLES AND IT STOPS DOUBLING. A pane busy now is likely busy in a
// moment, and a fixed fifteen seconds asks a long turn four times a minute for
// as long as it runs. A wait that kept doubling would leave the mail
// unannounced for an hour after the turn ended.
//
// PLANT: in retry, replace the retryDelay line with `b.retryDelay = b.retryBase`.
func TestBell_TheRetryWaitDoublesAndCaps(t *testing.T) {
	home(t, "provider = none\n")
	f := &fake{unread: 2, nudgeErr: busy("pane_in_mode")}
	b, sch, _ := retryBell(t, f, nil)

	b.arrived("")
	b.fire()
	for i := 0; i < 3; i++ {
		sch.run(t, i)
	}

	want := []time.Duration{15 * time.Second, 30 * time.Second, 60 * time.Second, 60 * time.Second}
	got := sch.delays()
	if len(got) != len(want) {
		t.Fatalf("the retry was scheduled %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("retry %d waited %v; want %v. The whole run was %v", i+1, got[i], want[i], got)
		}
	}
}

// ONE LINE IN THE DAY'S RECORD PER STREAK. A busy seat is ONE event, and a
// line every fifteen seconds would bury the day's record under a seat that is
// working. The delivery log keeps the per-attempt evidence: the notifier
// writes `nudge <endpoint> refused: <reason>` on every attempt.
//
// PLANT: in refused, replace the `if !first { return }` arm with `_ = first`.
func TestBell_ARefusalStreakIsOneLineInTheDaysRecord(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{unread: 2, nudgeErr: busy("pane_in_mode")}
	b, sch, stderr := retryBell(t, f, func() time.Time { return bellRecordClock })

	b.arrived("")
	b.fire()
	sch.run(t, 0)
	sch.run(t, 1)

	if got := f.bells(); len(got) != 3 {
		t.Fatalf("the notifier saw %d rings; one refusal and two retries were due", len(got))
	}
	lines := recordLines(t, dir)
	if len(lines) != 1 {
		t.Fatalf("the day's record holds %v; one streak is one line", lines)
	}
	if lines[0]["status"] != "bell-failed" {
		t.Errorf("status = %v, want bell-failed", lines[0]["status"])
	}
	if n := strings.Count(stderr.String(), "bell failed "+seat); n != 1 {
		t.Errorf("the runtime's log holds %d bell-failed lines; want 1. It said: %q", n, stderr.String())
	}
}

// A NEW ARRIVAL THAT RINGS ENDS THE STREAK. The mail was announced, so the
// waiting retry has nothing left to say, and the next streak starts its wait
// at the base again rather than at the doubled one.
//
// PLANT: in rang, delete the `b.clearRetry()` call.
func TestBell_ARingFromANewArrivalCancelsThePendingRetry(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{unread: 5, nudgeErrs: []error{busy("pane_in_mode"), nil, busy("not a prompt")}}
	b, sch, _ := retryBell(t, f, nil)

	b.arrived("")
	b.fire() // busy: a retry is now waiting
	if !retryArmed(b) {
		t.Fatal("a busy refusal scheduled no retry, so this case would pass on an absent one")
	}

	b.arrived("workshop.binder")
	b.fire() // rings

	if retryArmed(b) {
		t.Error("the ring left the retry waiting; it has nothing left to announce")
	}
	if !strings.Contains(delivery(t, dir), "retry "+seat+" rang after 1 refusals") {
		t.Errorf("the end of the streak was not written down: %q", delivery(t, dir))
	}

	// AND THE WAIT WAS FORGOTTEN. A streak that opens now starts at the base.
	b.arrived("")
	b.fire()
	want := []time.Duration{15 * time.Second, 15 * time.Second}
	if got := sch.delays(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("the scheduled waits were %v; want %v", got, want)
	}
}

// NO TIMER FIRES AFTER STOP. The server is departing, the seat is given up,
// and a ring after that types into a pane that belongs to nobody. The waiting
// timer is cancelled, and the stopped flag is checked again by retry and by
// ring, for the timer that had already fired and was waiting on the lock.
//
// PLANT: in stop, delete the `b.clearRetry()` call.
func TestBell_StopCancelsTheRetry(t *testing.T) {
	home(t, "provider = none\n")
	f := &fake{unread: 4, nudgeErr: busy("pane_in_mode")}
	b, sch, _ := retryBell(t, f, nil)

	b.arrived("")
	b.fire()
	if !retryArmed(b) {
		t.Fatal("a busy refusal scheduled no retry, so this case would pass on an absent one")
	}

	b.stop()
	if retryArmed(b) {
		t.Error("stop left a retry waiting")
	}

	// The timer that had already fired and was waiting on the lock still runs
	// its function. It must find the flag and ring nothing.
	sch.run(t, 0)
	if got := f.bells(); len(got) != 1 {
		t.Errorf("the notifier saw %v after stop; the seat was already given up", got)
	}
}

// A SUPPRESSED FIRST RING IS MADE LATER. Before this, fire's errSuppressed
// case did nothing: a first ring that met a tripped breaker never scheduled a
// retry, and the mail waited with no bell until the next arrival happened to
// come (#151).
//
// PLANT: in fire, make the errSuppressed case do nothing again.
func TestBell_ASuppressedRingIsMadeLater(t *testing.T) {
	dir := home(t, "provider = none\n")
	now := bellRecordClock
	f := &fake{unread: 1}
	b, sch, _ := retryBell(t, f, func() time.Time { return now })
	b.perMin = 1

	b.arrived("")
	b.fire()
	if got := f.bells(); len(got) != 1 {
		t.Fatalf("the first ring was %v; want one bell", got)
	}

	b.arrived("")
	b.fire()
	if got := f.bells(); len(got) != 1 {
		t.Fatalf("the suppressed ring reached the pane: %v", got)
	}
	if got := sch.delays(); len(got) != 1 || got[0] != 15*time.Second {
		t.Fatalf("a suppressed ring scheduled %v; want one retry at 15s", got)
	}

	// Past the calendar minute the breaker tripped in, so the cap has room
	// again.
	now = now.Add(61 * time.Second)
	f.unread = 1
	sch.run(t, 0)

	got := f.bells()
	if len(got) != 2 {
		t.Fatalf("the seat was rung %d times; a suppressed ring must be made later", len(got))
	}
	if got[1] != "🔔 1 new → read" {
		t.Errorf("the second bell said %q; the queue holds one", got[1])
	}
	if log := delivery(t, dir); strings.Contains(log, "rang after") {
		t.Errorf("a suppression is not a refusal, so no 'rang after' line belongs here: %q", log)
	}
}

// A RETRY THAT MEETS A STILL-TRIPPED BREAKER WAITS AGAIN AND DOES NOT RING.
// The breaker un-trips on the hour, and a retry run before then finds the
// same cap still spent.
//
// No existing case covers this. TestBell_TheBreakerCapsWakesAndTripsOnce
// (mcpserve_test.go) checks the bell count and the one-line trip warning, not
// what the retry timer does; nothing else in this package drives retry() into
// errSuppressed.
//
// PLANT: in retry, make the errSuppressed case do nothing.
func TestBell_ARetryUnderATrippedBreakerWaitsAndDoesNotRing(t *testing.T) {
	now := bellRecordClock
	f := &fake{unread: 1}
	b, sch, _ := retryBell(t, f, func() time.Time { return now })
	b.perMin = 1

	b.arrived("")
	b.fire()
	b.arrived("")
	b.fire()
	if got := f.bells(); len(got) != 1 {
		t.Fatalf("the suppressed ring reached the pane: %v", got)
	}

	// The clock does not move: the retry lands in the same calendar minute
	// the breaker tripped in.
	sch.run(t, 0)

	if got := f.bells(); len(got) != 1 {
		t.Errorf("a retry under a still-tripped breaker rang: %v", got)
	}
	if !retryArmed(b) {
		t.Fatal("no retry is waiting; a still-tripped breaker must be asked again")
	}
	want := []time.Duration{15 * time.Second, 30 * time.Second}
	if got := sch.delays(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("the scheduled waits were %v; want %v", got, want)
	}
}

// STOP CANCELS A RETRY THAT A SUPPRESSION SCHEDULED, exactly as it cancels one
// a busy refusal scheduled. The server is departing, the seat is given up,
// and a timer that still fires must ring nothing. ring already returns
// errSuppressed once stopped is set, and scheduleRetry already refuses to arm
// a timer once stopped is set, so this confirms the existing stop path
// already covers the suppressed case rather than planting a new defect.
func TestBell_StopCancelsARetryScheduledForASuppression(t *testing.T) {
	now := bellRecordClock
	f := &fake{unread: 1}
	b, sch, _ := retryBell(t, f, func() time.Time { return now })
	b.perMin = 1

	b.arrived("")
	b.fire()
	b.arrived("")
	b.fire()
	if !retryArmed(b) {
		t.Fatal("a suppressed ring scheduled no retry, so this case would pass on an absent one")
	}

	b.stop()
	if retryArmed(b) {
		t.Error("stop left a retry waiting")
	}

	sch.run(t, 0)
	if got := f.bells(); len(got) != 1 {
		t.Errorf("the notifier saw %v after stop; the seat was already given up", got)
	}
}
