package mcpserve

// ONE FLAT RULE: WHILE THE SEAT HAS UNREAD MAIL, THE BELL TRIES.
//
// A try is a try whether it rang, whether a busy pane refused it, whether the
// breaker suppressed it, or whether the notifier failed. The gap between tries
// is fixed, the streak gets a fixed number of tries, and then the bell gives
// up. The result of a try is written down and it never changes the count.
//
// Every case here drives the bell by hand. The next-try timer goes through a
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

// The gap and the count every case below runs with, which are the table's own
// defaults said once.
const (
	testGap   = 60 * time.Second
	testTries = 3
)

// busy is what the tmux notifier returns for a transient refusal, built the
// way the notifier builds it so a case reads the same text the record does.
func busy(reason string) error {
	return busyRefusal{fmt.Errorf("refused: %s", reason)}
}

// schedule stands in for time.AfterFunc. It records what delay each try was
// scheduled for and holds the function, so the case decides when the try
// runs.
type schedule struct {
	delay []time.Duration
	fn    []func()
}

func (s *schedule) after(d time.Duration, f func()) *time.Timer {
	s.delay = append(s.delay, d)
	s.fn = append(s.fn, f)
	// A timer that cannot fire inside a test's lifetime. Stop on it still
	// works, which is what the bell calls when it cancels a waiting try.
	return time.AfterFunc(time.Hour, func() {})
}

func (s *schedule) delays() []time.Duration {
	return append([]time.Duration(nil), s.delay...)
}

// run is the scheduled try firing.
func (s *schedule) run(t *testing.T, i int) {
	t.Helper()
	if i >= len(s.fn) {
		t.Fatalf("try %d was never scheduled; the delays were %v", i+2, s.delay)
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
		gap:       testGap,
		maxTries:  testTries,
		afterFunc: sch.after,
	}
	s.b = b
	return b, sch, &stderr
}

// tryArmed reports whether the next try is waiting.
func tryArmed(b *bell) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tryTimer != nil
}

// results is what each `bell-try` line in the day's record says, in order, as
// `<try>/<of> <result>`. A `bell-gave-up` line reads as
// `gave-up/<after> rang=<rang> last=<last>`, so one slice shows the whole
// streak AND how it ended. The two end fields are in every expectation below
// on purpose: a streak that rang twice and a streak that never rang are two
// states, and a case that printed only `gave-up/3` could not tell them apart.
func results(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, line := range recordLines(t, dir) {
		switch line["status"] {
		case "bell-try":
			out = append(out, fmt.Sprintf("%v/%v %v", line["try"], line["of"], line["result"]))
		case "bell-gave-up":
			out = append(out, fmt.Sprintf("gave-up/%v rang=%v last=%v",
				line["after"], line["rang"], line["last"]))
		default:
			out = append(out, fmt.Sprintf("%v", line["status"]))
		}
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// THREE TRIES AND THEN THE BELL GIVES UP, for EVERY kind of result and for a
// mix of them. The result is written down and it changes nothing about the
// counting, which is the whole of the rule in one case.
//
// PLANT: in counted, arm the timer unconditionally (drop the `if !last`), and
// every arm here grows a fourth try.
// PLANT: in counted, skip the increment when the result is `rang`, and the
// `rang` arm never gives up.
func TestBell_ThreeTriesThenTheBellGivesUp(t *testing.T) {
	for _, c := range []struct {
		name  string
		errs  []error
		want  []string
		bells int
	}{
		{"every try rang", []error{nil, nil, nil},
			[]string{"1/3 rang", "2/3 rang", "3/3 rang", "gave-up/3 rang=3 last=rang"}, 3},
		{"every try was refused", []error{busy("pane_in_mode"), busy("pane_in_mode"), busy("pane_in_mode")},
			[]string{"1/3 refused", "2/3 refused", "3/3 refused", "gave-up/3 rang=0 last=refused"}, 3},
		{"every try broke", []error{errors.New("exit status 3"), errors.New("exit status 3"), errors.New("exit status 3")},
			[]string{"1/3 failed", "2/3 failed", "3/3 failed", "gave-up/3 rang=0 last=failed"}, 3},
		{"a mix of results", []error{busy("not a prompt"), nil, errors.New("exit status 3")},
			[]string{"1/3 refused", "2/3 rang", "3/3 failed", "gave-up/3 rang=1 last=failed"}, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := home(t, "provider = none\n")
			f := &fake{unread: 2, nudgeErrs: c.errs}
			b, sch, _ := retryBell(t, f, func() time.Time { return bellRecordClock })

			b.arrived("")
			b.fire()
			sch.run(t, 0)
			sch.run(t, 1)

			if got := len(f.bells()); got != c.bells {
				t.Errorf("the notifier saw %d rings; the streak is %d tries", got, c.bells)
			}
			if got := results(t, dir); !sameStrings(got, c.want) {
				t.Errorf("the record holds %v\nwant              %v", got, c.want)
			}
			// NO FOURTH TRY. The bell gave up, so nothing is waiting and
			// nothing more reaches the pane until mail arrives again.
			if tryArmed(b) {
				t.Error("a try is still waiting after the bell gave up")
			}
			if got := len(sch.delays()); got != c.bells-1 {
				t.Errorf("%d waits were scheduled for %d tries; the last try schedules none", got, c.bells)
			}
		})
	}
}

// THE GAP IS FIXED. It was the first wait doubled, up to a minute, and a
// streak that is bounded at three tries has no use for a growing wait: the
// mail is either announced inside three minutes or the bell stops.
//
// PLANT: in counted, replace `b.gap` with `time.Duration(try) * b.gap`.
func TestBell_TheGapBetweenTriesIsFixed(t *testing.T) {
	home(t, "provider = none\n")
	f := &fake{unread: 2, nudgeErr: busy("pane_in_mode")}
	b, sch, _ := retryBell(t, f, nil)

	b.arrived("")
	b.fire()
	sch.run(t, 0)

	want := []time.Duration{testGap, testGap}
	got := sch.delays()
	if len(got) != len(want) {
		t.Fatalf("the tries were scheduled %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("try %d waited %v; want %v. The whole run was %v", i+2, got[i], want[i], got)
		}
	}
}

// A NEW ARRIVAL RINGS AT ONCE AND STARTS THE COUNT AGAIN. The mail that just
// landed has been announced to nobody, and a streak that had used two of its
// three tries would give up on it after one more.
//
// PLANT: in fire, delete the `b.clearStreak()` call.
func TestBell_ANewArrivalRingsAtOnceAndRestartsTheCount(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{unread: 5, nudgeErr: busy("pane_in_mode")}
	b, sch, _ := retryBell(t, f, func() time.Time { return bellRecordClock })

	b.arrived("")
	b.fire()
	sch.run(t, 0) // try 2 of the first streak
	if got := results(t, dir); !sameStrings(got, []string{"1/3 refused", "2/3 refused"}) {
		t.Fatalf("the first streak reads %v; want two tries and no give-up", got)
	}

	// Mail arrives while the streak is open.
	b.arrived("workshop.binder")
	b.fire()

	if got := len(f.bells()); got != 3 {
		t.Errorf("the notifier saw %d rings; the new arrival must ring at once", got)
	}
	want := []string{"1/3 refused", "2/3 refused", "1/3 refused"}
	if got := results(t, dir); !sameStrings(got, want) {
		t.Errorf("the record holds %v\nwant              %v\nthe new arrival must start at try 1", got, want)
	}
	// AND THE NEW STREAK GETS ITS OWN THREE. Two more tries follow before the
	// bell gives up, not one.
	sch.run(t, 2)
	sch.run(t, 3)
	want = append(want, "2/3 refused", "3/3 refused", "gave-up/3 rang=0 last=refused")
	if got := results(t, dir); !sameStrings(got, want) {
		t.Errorf("the record holds %v\nwant              %v", got, want)
	}
}

// A QUEUE THAT READS EMPTY ENDS THE STREAK SILENTLY. The mail is read, so
// there is nothing left to announce, nothing to give up on and no line to
// write. This is the one way a streak ends without a `bell-gave-up`.
//
// PLANT: in again, delete the `if n <= 0` arm.
// PLANT: in again, replace the `n <= 0` arm with `b.counted(0, resultFailed,
// reasonNoCount)`, and a seat that READ its mail is recorded as given up on.
func TestBell_AQueueThatReadsEmptyEndsTheStreakWithNoGiveUp(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{unread: 0, nudgeErr: busy("pane_in_mode")}
	b, sch, _ := retryBell(t, f, func() time.Time { return bellRecordClock })

	b.arrived("")
	b.fire()
	sch.run(t, 0)

	if got := f.bells(); len(got) != 1 {
		t.Errorf("the seat was rung %v; the queue was empty at the second try", got)
	}
	if got := results(t, dir); !sameStrings(got, []string{"1/3 refused"}) {
		t.Errorf("the record holds %v; want the first try and nothing after it", got)
	}
	if log := delivery(t, dir); !strings.Contains(log, "bell "+seat+" stopped: the queue is empty") {
		t.Errorf("the stop was not written down: %q", log)
	}
	if tryArmed(b) {
		t.Error("a try is still waiting for a queue that is empty")
	}
}

// `wake_tries = 1` IS ONE TRY. The count is the deployment's, and a house that
// wants the old single ring on arrival sets it to one and gets exactly that:
// the ring, the give-up, and nothing waiting.
func TestBell_OneTryIsOneTry(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{unread: 2, nudgeErr: busy("pane_in_mode")}
	b, sch, _ := retryBell(t, f, func() time.Time { return bellRecordClock })
	b.maxTries = 1

	b.arrived("")
	b.fire()

	if got := len(f.bells()); got != 1 {
		t.Errorf("the notifier saw %d rings; the streak is one try", got)
	}
	if got := sch.delays(); len(got) != 0 {
		t.Errorf("a one-try streak scheduled %v; the last try schedules nothing", got)
	}
	if got := results(t, dir); !sameStrings(got, []string{"1/1 refused", "gave-up/1 rang=0 last=refused"}) {
		t.Errorf("the record holds %v; want one try and the give-up", got)
	}
	if tryArmed(b) {
		t.Error("a try is waiting after a one-try streak")
	}
}

// THE FIRST TRY IS THE RING ON ARRIVAL, and it carries what the window
// collected. The second carries the count the QUEUE holds, not the count the
// first ring remembered.
//
// PLANT: in again, ring with a remembered count instead of the one Unread
// returns.
func TestBell_ARefusedBellRingsAgainAndCarriesTheCount(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{unread: 3, nudgeErrs: []error{busy("pane_in_mode")}}
	b, sch, _ := retryBell(t, f, nil)

	b.arrived("")
	b.fire()
	if got := f.bells(); len(got) != 1 {
		t.Fatalf("the first ring was %v; want one attempt", got)
	}
	if got := sch.delays(); len(got) != 1 || got[0] != testGap {
		t.Fatalf("the first try scheduled %v; want one try at %v", got, testGap)
	}

	sch.run(t, 0)

	got := f.bells()
	if len(got) != 2 {
		t.Fatalf("the seat was rung %d times; a busy pane must be asked again", len(got))
	}
	if got[1] != "🔔 3 new → read" {
		t.Errorf("the second bell said %q; the queue holds three", got[1])
	}
	// A later try asks how much mail is waiting and never who sent it, so it
	// names no seat.
	if c := f.couriersRung()[1]; c != defaultCourierName {
		t.Errorf("the second try named the courier %q; want %q", c, defaultCourierName)
	}
	log := delivery(t, dir)
	if !strings.Contains(log, "wake "+seat+" count=3") {
		t.Errorf("the second try's wake was not written down: %q", log)
	}
	if !strings.Contains(log, "bell "+seat+" rang on try 2 of 3") {
		t.Errorf("the ring that worked did not say which try it was: %q", log)
	}
	// AND THE STREAK IS STILL OPEN. The mail is not read because the pane was
	// typed into; the queue is what says so, and the next try asks it.
	if !tryArmed(b) {
		t.Error("a ring that worked closed the streak; the mail is still unread")
	}
}

// A COUNT THAT CANNOT BE READ IS NOT AN EMPTY QUEUE, AND IT IS A TRY. The
// broker can time out with the connection still up, and the mail is still
// waiting. Before #144 this asked again for ever without counting; now it is
// one try of three, so the streak stays bounded and the record says what
// happened. The error's own text is NOT written down: a client error can
// carry the broker's address.
//
// PLANT: in again, make the `err != nil` arm call endStreak and return.
func TestBell_ATryThatCannotReadTheCountIsAFailedTry(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{unread: 3, nudgeErrs: []error{busy("not a prompt")}}
	b, sch, _ := retryBell(t, f, func() time.Time { return bellRecordClock })

	b.arrived("")
	b.fire()

	f.unreadErr = errors.New("context deadline exceeded on nats://10.0.0.7:4222")
	sch.run(t, 0)

	if got := f.bells(); len(got) != 1 {
		t.Errorf("the seat was rung %v; the count was not known, so there was nothing to ring for", got)
	}
	if !tryArmed(b) {
		t.Fatal("no try is waiting; a count that cannot be read was taken for an empty queue")
	}
	want := []string{"1/3 refused", "2/3 failed"}
	if got := results(t, dir); !sameStrings(got, want) {
		t.Errorf("the record holds %v\nwant              %v", got, want)
	}
	for _, line := range recordLines(t, dir) {
		if r, _ := line["reason"].(string); strings.Contains(r, "10.0.0.7") {
			t.Errorf("the record carries the broker's address: %v", line)
		}
	}

	// The broker answers again, and the mail that waited is announced.
	f.unreadErr = nil
	sch.run(t, 1)
	got := f.bells()
	if len(got) != 2 || got[1] != "🔔 3 new → read" {
		t.Errorf("the bells were %v; the three messages that waited must be announced", got)
	}
}

// A BROKEN BELL IS TRIED AGAIN. This REVERSES what stood before #144: no pane
// address, a tmux that cannot be read, a courier that exited non-zero were
// treated as beyond repair and recorded once, so a hook fixed a minute later
// announced nothing until the next message arrived. Under the flat rule the
// seat's mail is unread either way, and three tries is a bound a broken bell
// cannot outrun.
//
// PLANT: in counted, give the `failed` result its own arm that clears the
// streak.
func TestBell_ABrokenBellIsTriedAgain(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{unread: 2, nudgeErrs: []error{errors.New("exit status 3"), nil}}
	b, sch, stderr := retryBell(t, f, func() time.Time { return bellRecordClock })

	b.arrived("")
	b.fire()

	if got := sch.delays(); len(got) != 1 || got[0] != testGap {
		t.Fatalf("a broken bell scheduled %v; want one try at %v", got, testGap)
	}
	// CONTROL: it is still recorded and still said out loud, exactly as before.
	if log := delivery(t, dir); !strings.Contains(log, "bell failed "+seat+": exit status 3") {
		t.Errorf("the failure was not written down: %q", log)
	}
	if !strings.Contains(stderr.String(), "bell failed "+seat) {
		t.Errorf("nothing reached the runtime's log: %q", stderr.String())
	}

	sch.run(t, 0)
	if got := f.bells(); len(got) != 2 {
		t.Fatalf("the seat was rung %d times; a broken bell is tried again", len(got))
	}
	want := []string{"1/3 failed", "2/3 rang"}
	if got := results(t, dir); !sameStrings(got, want) {
		t.Errorf("the record holds %v\nwant              %v", got, want)
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

// EVERY TRY IS A LINE IN THE DAY'S RECORD, and so is the give-up. This
// REVERSES the old rule of one line per streak, under which a bell that rang
// left no line at all: after a `bell-failed`, "it was tried again and worked"
// and "nobody tried and the seat looked by itself" were the same record (#144,
// gap 2).
//
// PLANT: in counted, write the record line only when `try == 1`.
func TestBell_EveryTryIsALineInTheDaysRecord(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{unread: 2, nudgeErr: busy("pane_in_mode")}
	b, sch, stderr := retryBell(t, f, func() time.Time { return bellRecordClock })

	b.arrived("")
	b.fire()
	sch.run(t, 0)
	sch.run(t, 1)

	if got := f.bells(); len(got) != 3 {
		t.Fatalf("the notifier saw %d rings; three tries were due", len(got))
	}
	lines := recordLines(t, dir)
	if len(lines) != 4 {
		t.Fatalf("the day's record holds %v; want three tries and the give-up", lines)
	}
	for i, want := range []int{1, 2, 3} {
		if lines[i]["status"] != "bell-try" {
			t.Errorf("line %d status = %v, want bell-try", i, lines[i]["status"])
		}
		if lines[i]["try"] != float64(want) || lines[i]["of"] != float64(testTries) {
			t.Errorf("line %d = %v; want try %d of %d", i, lines[i], want, testTries)
		}
		if lines[i]["result"] != "refused" || lines[i]["reason"] != "refused: pane_in_mode" {
			t.Errorf("line %d = %v; want the notifier's own reason", i, lines[i])
		}
		if _, ok := lines[i]["uid"]; ok {
			t.Errorf("line %d carries a uid; the server does not know which message it announced", i)
		}
	}
	if lines[3]["status"] != "bell-gave-up" || lines[3]["after"] != float64(testTries) {
		t.Errorf("the last line is %v; want the give-up after %d tries", lines[3], testTries)
	}
	if n := strings.Count(stderr.String(), "bell failed "+seat); n != 3 {
		t.Errorf("the runtime's log holds %d lines; one per try. It said: %q", n, stderr.String())
	}
	// The seat's own plain-text log says the same thing in its own words, and
	// it says how many tries rang.
	want := "bell " + seat + " made 3 of 3 tries, 0 rang; no more until new mail"
	if log := delivery(t, dir); !strings.Contains(log, want) {
		t.Errorf("the delivery log does not carry %q: %q", want, log)
	}
}

// NO TIMER FIRES AFTER STOP. The server is departing, the seat is given up,
// and a ring after that types into a pane that belongs to nobody. The waiting
// timer is cancelled, and the stopped flag is checked again by again() and by
// ring, for the timer that had already fired and was waiting on the lock.
//
// PLANT: in stop, delete the `b.clearStreak()` call.
func TestBell_StopCancelsTheNextTry(t *testing.T) {
	home(t, "provider = none\n")
	f := &fake{unread: 4, nudgeErr: busy("pane_in_mode")}
	b, sch, _ := retryBell(t, f, nil)

	b.arrived("")
	b.fire()
	if !tryArmed(b) {
		t.Fatal("the first try scheduled no second one, so this case would pass on an absent timer")
	}

	b.stop()
	if tryArmed(b) {
		t.Error("stop left a try waiting")
	}

	// The timer that had already fired and is waiting on the lock still runs
	// its function. It must find the flag and ring nothing.
	sch.run(t, 0)
	if got := f.bells(); len(got) != 1 {
		t.Errorf("the notifier saw %v after stop; the seat was already given up", got)
	}
}

// A SUPPRESSED RING IS A TRY, and the streak carries on to the next one. The
// breaker un-trips on the hour, so a later try inside the same streak can find
// it open and reach the pane.
//
// PLANT: in outcome, return resultRang for errSuppressed, and a suppression
// would be recorded as a bell that reached the pane.
func TestBell_ASuppressedRingIsATryAndTheNextOneFollows(t *testing.T) {
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
	if got := sch.delays(); len(got) != 2 || got[1] != testGap {
		t.Fatalf("a suppressed ring scheduled %v; want a try at %v", got, testGap)
	}

	// Past the calendar minute the breaker tripped in, so the cap has room
	// again.
	now = now.Add(61 * time.Second)
	f.unread = 1
	sch.run(t, 1)

	got := f.bells()
	if len(got) != 2 {
		t.Fatalf("the seat was rung %d times; a suppressed try must be followed by another", len(got))
	}
	if got[1] != "🔔 1 new → read" {
		t.Errorf("the second bell said %q; the queue holds one", got[1])
	}
	// The record says the suppression happened and says it changed no count.
	want := []string{"1/3 rang", "1/3 suppressed", "2/3 rang"}
	if got := results(t, dir); !sameStrings(got, want) {
		t.Errorf("the record holds %v\nwant              %v", got, want)
	}
}

// A TRY THAT MEETS A STILL-TRIPPED BREAKER COUNTS AND DOES NOT RING. The
// breaker un-trips on the hour, and a try run before then finds the same cap
// still spent. It is a try like any other: the count grows, the next one is
// armed, and the streak still ends after three.
//
// PLANT: in counted, skip the increment when the result is `suppressed`.
func TestBell_ATryUnderATrippedBreakerCountsAndDoesNotRing(t *testing.T) {
	dir := home(t, "provider = none\n")
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

	// The clock does not move: each try lands in the same calendar minute the
	// breaker tripped in.
	sch.run(t, 1)
	sch.run(t, 2)

	if got := f.bells(); len(got) != 1 {
		t.Errorf("a try under a still-tripped breaker rang: %v", got)
	}
	want := []string{"1/3 rang", "1/3 suppressed", "2/3 suppressed", "3/3 suppressed", "gave-up/3 rang=0 last=suppressed"}
	if got := results(t, dir); !sameStrings(got, want) {
		t.Errorf("the record holds %v\nwant              %v", got, want)
	}
	if tryArmed(b) {
		t.Error("a try is still waiting after three suppressed tries")
	}
}

// STOP CANCELS A TRY THAT A SUPPRESSION SCHEDULED, exactly as it cancels one a
// busy refusal scheduled. The server is departing, the seat is given up, and a
// timer that still fires must ring nothing. ring already returns errSuppressed
// once stopped is set, and counted refuses to count once stopped is set, so
// this confirms the existing stop path already covers the suppressed case
// rather than planting a new defect.
func TestBell_StopCancelsATryScheduledForASuppression(t *testing.T) {
	now := bellRecordClock
	f := &fake{unread: 1}
	b, sch, _ := retryBell(t, f, func() time.Time { return now })
	b.perMin = 1

	b.arrived("")
	b.fire()
	b.arrived("")
	b.fire()
	if !tryArmed(b) {
		t.Fatal("a suppressed ring scheduled no try, so this case would pass on an absent timer")
	}

	b.stop()
	if tryArmed(b) {
		t.Error("stop left a try waiting")
	}

	sch.run(t, 1)
	if got := f.bells(); len(got) != 1 {
		t.Errorf("the notifier saw %v after stop; the seat was already given up", got)
	}
}

// THE END OF A STREAK SAYS WHETHER THE SEAT WAS EVER TOLD. `gave up after 3
// tries` read as "the bell never got through", which is one of the two states
// a finished streak can be in. A bell that rang and was not answered is a
// different fact from a bell that never rang, and the two want different
// repairs.
//
// This case asserts the give-up LINE, key by key, because that line is what
// `loc status` and `loc transcript` both read.
//
// PLANT: in counted, pass 0 for rangs to LogBellGaveUp.
// PLANT: in counted, pass resultRefused for last to LogBellGaveUp.
func TestBell_TheGiveUpSaysHowManyTriesRang(t *testing.T) {
	for _, c := range []struct {
		name string
		errs []error
		rang float64
		last string
	}{
		{"every try rang", []error{nil, nil, nil}, 3, "rang"},
		{"a mix counts only the rings", []error{busy("pane_in_mode"), nil, errors.New("exit status 3")}, 1, "failed"},
		{"no try rang", []error{busy("pane_in_mode"), busy("pane_in_mode"), busy("pane_in_mode")}, 0, "refused"},
		{"the last try rang", []error{busy("pane_in_mode"), busy("pane_in_mode"), nil}, 1, "rang"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := home(t, "provider = none\n")
			f := &fake{unread: 2, nudgeErrs: c.errs}
			b, sch, _ := retryBell(t, f, func() time.Time { return bellRecordClock })

			b.arrived("")
			b.fire()
			sch.run(t, 0)
			sch.run(t, 1)

			lines := recordLines(t, dir)
			got := lines[len(lines)-1]
			if got["status"] != "bell-gave-up" {
				t.Fatalf("the last line is %v; want the give-up", got)
			}
			if got["after"] != float64(testTries) {
				t.Errorf("after = %v, want %d", got["after"], testTries)
			}
			if got["rang"] != c.rang {
				t.Errorf("rang = %v, want %v. The whole streak was %v", got["rang"], c.rang, results(t, dir))
			}
			if got["last"] != c.last {
				t.Errorf("last = %v, want %q", got["last"], c.last)
			}
			// `rang` IS WRITTEN EVEN WHEN IT IS 0. That 0 is the fact a reader
			// needs, and an absent key reads as a line that does not know.
			if _, ok := got["rang"]; !ok {
				t.Error("the give-up line carries no rang key")
			}
		})
	}
}

// THE RING COUNT RESETS WITH THE STREAK. A new arrival starts a fresh count of
// tries, and the count of rings has to go with it: a ring that announced the
// EARLIER mail says nothing about the mail that just landed.
//
// PLANT: in clearStreak, delete the `b.rangs = 0` line.
func TestBell_ANewArrivalResetsTheRingCount(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{unread: 2, nudgeErrs: []error{
		nil,                  // the first streak's one try rings
		busy("pane_in_mode"), // then mail arrives and the pane is busy
		busy("pane_in_mode"),
		busy("pane_in_mode"),
	}}
	b, sch, _ := retryBell(t, f, func() time.Time { return bellRecordClock })

	b.arrived("")
	b.fire() // streak 1, try 1, rang
	b.arrived("workshop.binder")
	b.fire() // streak 2, try 1, refused
	sch.run(t, 1)
	sch.run(t, 2)

	lines := recordLines(t, dir)
	got := lines[len(lines)-1]
	if got["status"] != "bell-gave-up" {
		t.Fatalf("the last line is %v; want the give-up", got)
	}
	if got["rang"] != float64(0) {
		t.Errorf("rang = %v, want 0. The earlier streak's ring was carried into this one. The record: %v",
			got["rang"], results(t, dir))
	}
}
