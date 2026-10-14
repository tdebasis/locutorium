package mcpserve

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tdebasis/locutorium/internal/config"
	"github.com/tdebasis/locutorium/internal/loc"
)

// THE BELL. A seat is woken, never fed.
//
// The listener holds a CORE subscription on the seat's own queue subject. A
// core subscription sees every publish the stream also captures and consumes
// nothing, so watching for arrivals never competes with the read tool for the
// mail — the queue keeps the truth and the bell only says there is some.
//
// Three policies sit between an arrival and the pane, and they are the SAME
// THREE KEYS the shell listener reads, so a deployment tunes one set of
// numbers whichever listener it runs:
//
//	wake_window_seconds      arrivals are coalesced across this window
//	wake_breaker_per_minute  cap per calendar minute
//	wake_breaker_per_hour    cap per calendar hour
//
// Three more keys belong to this listener alone. They sit in the config table,
// so the file the daemon writes carries them and their defaults live in one
// place:
//
//	wake_retry_seconds         the FIXED gap between one try and the next
//	wake_tries                 how many tries a streak gets
//	wake_held_recheck_seconds  the gap after a receiver said it holds the bell
//
// ONE FLAT RULE (#144). While the seat has unread mail, the bell tries. A try
// is a try whether it rang, whether a busy pane refused it, whether the
// breaker suppressed it, or whether the notifier failed. Every try is written
// to the day's record with its result, and the result NEVER changes the count.
// After the last try the bell gives up and does nothing more for that streak.
// A new arrival rings at once and starts a fresh count. A streak also ends,
// silently, the moment the unread count is 0.
//
// ONE EXCEPTION: A HELD BELL (#228). A webhook receiver may answer that it has
// the bell and will deliver it when it is safe. That try is written down as
// `held`, but it is not a try of the streak: it spends none of the tries,
// counts as no ring, gives the breaker its charge back, and the next try waits
// wake_held_recheck_seconds instead of the fixed gap. A receiver still waiting
// answers `held` again; one that has died answers nothing, that try is
// `failed`, and the streak counts on from there.
//
// A suppressed wake loses NOTHING: the messages are in the queue, and the next
// read finds all of them. That is what makes a cap safe to have.

const (
	defaultWakeWindow    = 5
	defaultBreakerMinute = 6
	defaultBreakerHour   = 60
)

// The five words the record uses for what one try came to. They are the whole
// vocabulary of the `result` field. The counting reads ONE of them, `held`,
// which is the receiver saying the try is not over yet (#228); the other four
// change nothing about the count.
const (
	resultRang       = "rang"
	resultRefused    = "refused"
	resultSuppressed = "suppressed"
	resultFailed     = "failed"
	resultHeld       = "held"
)

// reasonNoCount is what a try reports when the broker could not say how much
// mail is waiting. The error's own text is not used, because a client error
// can carry the broker's address.
const reasonNoCount = "the unread count could not be read"

// errSuppressed is what a ring returns when THE BREAKER refused it. Nothing
// reached the pane and nothing was typed. It is one outcome of a try like any
// other: the try is counted, the record says `suppressed`, and the mail keeps
// its place in the queue until a later try finds the breaker open; the breaker
// un-trips on the hour.
var errSuppressed = errors.New("suppressed by the breaker")

// defaultCourierName is what the courier is called when the bell cannot say
// who sent the mail. The startup backlog path asks the broker HOW MUCH mail is
// waiting and never who sent it, so it names no seat; a name invented there
// would be a guess standing in the one field a reader trusts. A later try of a
// streak asks the same question, and it names a seat only under the rule in
// laterNames.
const defaultCourierName = "loc-bell"

// bell coalesces arrivals and rings the seat's notifier.
type bell struct {
	s       *server
	window  time.Duration
	perMin  int
	perHour int

	// gap is the wait between one try and the next. It is fixed: every wait
	// in a streak is this long.
	gap time.Duration

	// heldGap is the wait after a try the receiver answered `held`. It is
	// longer than gap on purpose: the receiver has the bell, and asking again
	// is only how a receiver that died is found out.
	heldGap time.Duration

	// maxTries is how many tries a streak gets before the bell gives up.
	maxTries int

	// afterFunc is time.AfterFunc, and only the next-try timer goes through
	// it. A case replaces it to read the delay the try was scheduled for,
	// which is the thing under test; a case that measured the wall clock
	// would be testing the machine's scheduler instead.
	afterFunc func(time.Duration, func()) *time.Timer

	mu      sync.Mutex
	pending int
	timer   *time.Timer
	stopped bool

	// The streak. tryTimer is armed while the next try waits, tries counts
	// the tries made since the streak opened, and rangs counts how many of
	// them reached the pane. All three are cleared together, by clearStreak.
	//
	// rangs CHANGES NO SCHEDULE. It is carried so that the end of a streak
	// can say whether the seat was ever told. A bell that rang and was not
	// answered is a different fact from a bell that never rang, and a reader
	// who cannot tell them apart reads the first as the second.
	tryTimer *time.Timer
	tries    int
	rangs    int

	// The senders seen in the open window, in arrival order and each one
	// once. They name the courier and nothing else: they never reach the bell
	// line, which stays a count with no body.
	senders []string

	// blind says the window's count holds mail with no known sender: an
	// arrival whose sender did not parse, or a backlog that found more mail
	// than the watch saw. It is cleared when the window rings.
	blind bool

	// The streak's senders and the count its first ring announced. A later try
	// reads them to name the courier (laterNames). They are nil and 0 when the
	// first ring could not name every message, and clearStreak forgets them.
	streakSenders []string
	streakN       int

	// cut says the watch lost its connection and no count has been read since.
	// While it is set, mail can reach the queue with no arrival seen, so the
	// bell cannot know who sent what waits. It is set by disconnected and
	// cleared by the reconnect's backlog when its count is read, or by a later
	// try that finds the queue empty after the reconnect.
	cut bool

	// back says the watch reconnected after the loss that set cut. An empty
	// queue clears cut only when back is set: before the reconnect, mail can
	// still land unseen after the queue reads empty. Every loss clears it
	// (disconnected), and the reconnect's backlog sets it, whether or not its
	// count is read.
	back bool

	// The breaker's counters, bucketed by calendar minute and hour exactly as
	// the shell listener's are: a bucket that has rolled over starts empty,
	// and the hour rolling over is what un-trips the breaker.
	minuteBucket, hourBucket int64
	minuteN, hourN           int
	tripped                  bool
}

// startBell begins listening and returns the function that stops it. A medium
// that cannot be watched is NOT fatal: the seat stays registered and the tools
// keep working; what is lost is the wake, and the agent can still read.
func (s *server) startBell() func() {
	b := &bell{
		s:         s,
		window:    time.Duration(configInt("wake_window_seconds", defaultWakeWindow)) * time.Second,
		perMin:    configInt("wake_breaker_per_minute", defaultBreakerMinute),
		perHour:   configInt("wake_breaker_per_hour", defaultBreakerHour),
		gap:       time.Duration(tableInt(config.WakeRetrySeconds)) * time.Second,
		heldGap:   time.Duration(tableInt(config.WakeHeldRecheckSeconds)) * time.Second,
		maxTries:  tableInt(config.WakeTries),
		afterFunc: time.AfterFunc,
	}
	s.b = b

	stop, err := s.d.Watch(s.d.Endpoint, b.arrived, b.backlog, b.disconnected)
	if err != nil {
		return func() {}
	}
	// WAKE ON BACKLOG. A seat that starts with mail already waiting was never
	// told about it — the arrivals happened while nothing was listening — so
	// the count is asked for once at start, and again after every reconnect,
	// which is the other moment arrivals can have been missed.
	b.backlog()

	return func() {
		b.stop()
		stop()
	}
}

// stop shuts the bell down. Both timers are cancelled and the stopped flag is
// raised, so a timer that had already fired and is waiting on the lock finds
// the flag and rings nothing.
func (b *bell) stop() {
	b.mu.Lock()
	b.stopped = true
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.clearStreak()
	b.mu.Unlock()
}

// arrived records one arrival and opens the coalescing window if it is shut.
// The window is TRAILING: the wake fires once the arrivals have stopped
// coming, carrying however many there were, rather than once per message.
func (b *bell) arrived(sender string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return
	}
	b.pending++
	if sender == "" {
		b.blind = true
	}
	b.note(sender)
	if b.timer == nil {
		b.timer = time.AfterFunc(b.window, b.fire)
	}
}

// disconnected is the watch losing its connection. From now until the
// reconnect, an arrival reaches the queue and the watch does not see it, so
// what the bell remembers about senders stops being the whole story.
//
// A NAME THERE WOULD BE A GUESS. Mail from an unseen sender can land, and a
// read can then take some of the mail, and the count can still fit the
// senders the bell saw. Nothing in the count shows the difference. So while
// the flag is up, a later try names no seat (laterNames), and the backlog that
// the reconnect asks for treats the whole window as mail with no known sender.
func (b *bell) disconnected() {
	b.mu.Lock()
	b.cut = true
	b.back = false
	b.mu.Unlock()
}

// note records one sender. Called under the lock.
//
// EACH SENDER ONCE. Three messages from one seat are one sender, so a window
// full of one seat's mail names that seat rather than claiming a crowd. An
// empty sender is not a seat and is dropped: it is what an unparsable payload
// gives, and the wake it carries is already counted in pending.
func (b *bell) note(sender string) {
	if sender == "" {
		return
	}
	for _, s := range b.senders {
		if s == sender {
			return
		}
	}
	b.senders = append(b.senders, sender)
}

// backlog asks the medium how much mail is waiting and rings for it at once,
// without the window: there is nothing to coalesce with, and a seat waiting on
// a five-second window to be told about mail that arrived an hour ago would be
// waiting for no reason.
//
// THE FIGURE IS SET, NOT ADDED. Unread is the queue's own message count, and
// it already counts every arrival the watcher has counted since the last ring:
// the two are views of ONE queue, not two sources of messages. Adding them
// rang for mail that does not exist — one message landing between the watcher
// going live and this sample rang the pane for two — so the broker's figure
// REPLACES the tally, and the larger of the two is kept: what the broker holds
// is the truth, and the watcher's count is only ever a lower bound on it.
func (b *bell) backlog() {
	if b.s.d.Unread == nil {
		return
	}
	// The backlog runs at start and after every reconnect. At start nothing
	// is cut, so a cut here means the watch is back.
	b.mu.Lock()
	if b.cut {
		b.back = true
	}
	b.mu.Unlock()
	n, err := b.s.d.Unread(b.s.d.Endpoint)
	if err != nil {
		// No count was read, so a lost connection stays lost as far as the
		// names go.
		return
	}
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}
	// THE COUNT ENDS A CUT. A count read after the reconnect covers every
	// message the watch missed. The window rings for it below, blind and with
	// no names, so no name from before the cut reaches this ring or a later
	// try.
	wasCut := b.cut
	b.cut = false
	if wasCut {
		b.senders = nil
	}
	if n <= 0 {
		b.mu.Unlock()
		return
	}
	if n > b.pending || wasCut {
		// The watch saw fewer messages than the broker holds, or it was not
		// watching for a while, so some of this mail has no known sender.
		b.pending = max(b.pending, n)
		b.blind = true
	}
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.mu.Unlock()
	b.fire()
}

// fire rings once for everything that has accumulated, if the breaker lets it.
//
// A NEW ARRIVAL STARTS A FRESH STREAK. The count of tries goes back to zero
// and any waiting try is cancelled, so this ring is try 1 whatever an earlier
// streak had reached — including a streak that had given up.
func (b *bell) fire() {
	b.mu.Lock()
	n := b.pending
	b.pending = 0
	senders := b.senders
	b.senders = nil
	blind := b.blind
	b.blind = false
	// A WINDOW THAT RINGS WHILE THE WATCH IS DOWN NAMES NOBODY. Its senders
	// are the ones seen before the loss, and mail from others can have landed
	// since.
	if b.cut {
		blind = true
		senders = nil
	}
	b.timer = nil
	stopped := b.stopped
	b.clearStreak()
	// THE STREAK KEEPS ITS FIRST RING'S SENDERS only when they account for
	// every message that ring announced. A blind window keeps none.
	if !stopped && n > 0 && !blind && len(senders) > 0 {
		b.streakSenders, b.streakN = senders, n
	}
	b.mu.Unlock()
	if stopped || n <= 0 {
		return
	}
	b.try(n, courierName(senders), bellFrom(senders))
}

// ring hands the notifier one line for n, and reports what came back: nil
// means the pane took it, errSuppressed means the breaker refused it, an
// error wrapping ErrBusy means the seat is busy, an error wrapping ErrHeld
// means the receiver holds the bell for later, and anything else means the
// bell is broken. Called with the lock free.
func (b *bell) ring(n int, courier, from string) error {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return errSuppressed
	}
	now := b.s.d.Now()
	b.roll(now)
	if b.minuteN >= b.perMin || b.hourN >= b.perHour {
		first := !b.tripped
		b.tripped = true
		minN, hourN := b.minuteN, b.hourN
		b.mu.Unlock()
		// ONCE PER TRIP, not once per suppressed wake: a breaker that shouted
		// on every suppression would be the very noise it exists to stop.
		if first {
			b.s.log(fmt.Sprintf("BREAKER TRIPPED for %s (min %d/%d, hr %d/%d): "+
				"wakes suppressed, the queue keeps the truth",
				b.s.d.Endpoint, minN, b.perMin, hourN, b.perHour))
		}
		return errSuppressed
	}
	b.minuteN++
	b.hourN++
	minute, hour := b.minuteBucket, b.hourBucket
	b.mu.Unlock()

	// The line names the sender whenever the name survived the shape check. A
	// backlog, or a name that did not, falls back to the count alone rather than
	// printing a placeholder: "from loc-bell" would be noise wearing a fact's
	// clothes. The phrase and the courier label are computed apart, because a
	// label has a length cap the line does not need (bellFrom).
	line := fmt.Sprintf(bellLine, n)
	if from != "" {
		line = fmt.Sprintf(bellLineFrom, n, from)
	}
	err := b.s.d.Notify.Ring(b.s.d.Endpoint, b.s.address, line, courier)
	if errors.Is(err, ErrBusy) || errors.Is(err, ErrHeld) {
		// A BUSY REFUSAL TYPED NOTHING, so it gets the charge back. The cap
		// exists to stop the pane being hammered, and a refusal that never
		// reached the pane hammered nothing. Left charged, six refusals in a
		// minute would trip the breaker and silence the ring the seat is
		// waiting for.
		//
		// A HELD BELL GETS IT BACK TOO (#228). The receiver has typed nothing
		// yet either, and it is asked again every recheck for as long as the
		// person is busy; charged, a long wait on a busy evening would trip
		// the hourly cap and silence the bells after it.
		b.mu.Lock()
		b.giveBack(minute, hour)
		b.mu.Unlock()
	}
	return err
}

// try makes ONE TRY: it rings, and it hands the outcome to counted.
func (b *bell) try(n int, courier, from string) {
	result, reason := outcome(b.ring(n, courier, from))
	b.counted(n, result, reason)
}

// counted advances the streak by one try, writes the try down, and decides
// what follows it.
//
// THE RESULT NEVER CHANGES THE COUNT, with one exception. A ring, a busy
// refusal, a suppression and a broken notifier all leave the streak one try
// further on and all arm the next try for the same fixed gap. What the seat has, or has not, is
// unread mail; how a ring fared says nothing about that. While the streak has
// tries left the next one is armed; at the last one the bell gives up, says so
// in the record, and does nothing more until mail arrives again.
//
// THE EXCEPTION IS `held` (#228). The receiver has the bell and has not
// delivered it, so the try is not over: it is written down, but the streak
// stays where it was, no ring is counted, no give-up can follow it, and the
// next try waits heldGap. Its line carries the number of the try it stands in
// for — the next one the streak would make — so `2/3 held` then `2/3 failed`
// reads as one slot asked twice. It is not a failed bell, so it raises no
// warning.
//
// THE WARNING AND THE EVENT GO TO DIFFERENT READERS. The warning lands in
// this seat's own delivery log, which is plain text and is found by somebody
// who already suspects this seat. The event lands in the day's record beside
// the `sent` line for the mail that was announced or was not, which is where a
// sender asking "why has nobody answered me" is already looking.
//
// The clock is the server's injected one, the same clock the breaker rolls its
// buckets on, so a case that pins time pins these lines too.
func (b *bell) counted(n int, result, reason string) {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}
	held := result == resultHeld
	if !held {
		b.tries++
	}
	if result == resultRang {
		b.rangs++
	}
	try, of, rangs := b.tries, b.maxTries, b.rangs
	if held {
		try = b.tries + 1
	}
	final := !held && try >= of
	if !final {
		gap := b.gap
		if held {
			gap = b.heldGap
		}
		b.tryTimer = b.afterFunc(gap, b.again)
	}
	heldGap := b.heldGap
	b.mu.Unlock()

	// It carries no uid. The watch reports an arrival and Unread reports a
	// count, so the server knows that mail is waiting and never which message
	// is waiting (WatchQueue, internal/provider/provider.go).
	loc.LogBellTry(b.s.d.Endpoint, try, of, result, reason, b.s.d.Now())
	switch result {
	case resultRang:
		b.s.log(fmt.Sprintf("wake %s count=%d", b.s.d.Endpoint, n))
		if try > 1 {
			b.s.log(fmt.Sprintf("bell %s rang on try %d of %d", b.s.d.Endpoint, try, of))
		}
	case resultHeld:
		// A HELD BELL IS NOT A FAILED ONE. It goes to the delivery log and
		// not to the runtime's log, which is where a broken bell shouts.
		b.s.log(fmt.Sprintf("bell %s held by the receiver; asking again in %s",
			b.s.d.Endpoint, heldGap))
	default:
		// A BELL THAT COULD NOT RING SAYS SO, and is never recorded as a wake.
		b.s.warn(fmt.Sprintf("bell failed %s: %s", b.s.d.Endpoint, reason))
	}

	if final {
		// THE END OF A STREAK SAYS WHETHER THE SEAT WAS EVER TOLD. The count
		// of rings and the last try's result are carried here; neither of them
		// changed the counting that got us here.
		loc.LogBellGaveUp(b.s.d.Endpoint, of, rangs, result, b.s.d.Now())
		b.s.log(fmt.Sprintf("bell %s made %d of %d tries, %d rang; no more until new mail",
			b.s.d.Endpoint, of, of, rangs))
		b.mu.Lock()
		b.clearStreak()
		b.mu.Unlock()
	}
}

// outcome says what one try came to, in the record's own five words.
//
// The reason is the notifier's own text for a refusal and for a failure,
// because "the pane is in copy mode" and "there is no such pane" want
// different repairs. A ring has no reason and its line carries no such key.
func outcome(err error) (result, reason string) {
	switch {
	case err == nil:
		return resultRang, ""
	case errors.Is(err, errSuppressed):
		return resultSuppressed, errSuppressed.Error()
	case errors.Is(err, ErrHeld):
		return resultHeld, err.Error()
	case errors.Is(err, ErrBusy):
		return resultRefused, err.Error()
	default:
		return resultFailed, err.Error()
	}
}

// again is the next try of an open streak.
//
// IT ASKS THE QUEUE, NOT ITS OWN MEMORY. The seat may have read the mail since
// the last try, and a bell that rang for a number it remembered would wake a
// seat for an empty queue. AN EMPTY QUEUE ENDS THE STREAK SILENTLY: the mail
// is read, so there is nothing to give up on and no line to write.
func (b *bell) again() {
	b.mu.Lock()
	b.tryTimer = nil
	stopped := b.stopped
	b.mu.Unlock()
	if stopped {
		return
	}

	if b.s.d.Unread == nil {
		// A medium that cannot be asked cannot say the mail is unread, and the
		// rule is about unread mail. The streak ends.
		b.endStreak()
		return
	}
	n, err := b.s.d.Unread(b.s.d.Endpoint)
	if err != nil {
		// A COUNT THAT CANNOT BE READ IS NOT AN EMPTY QUEUE. The broker can
		// time out with the connection still up, so no reconnect comes to ask
		// for the backlog, and the mail is still waiting. This is a try that
		// did not reach the pane, and it counts as one: the streak stays
		// bounded and the record says what happened. The text of the error is
		// not logged, because a client error can carry the broker's address.
		b.counted(0, resultFailed, reasonNoCount)
		return
	}
	if n <= 0 {
		// AN EMPTY QUEUE ENDS A CUT, BUT ONLY AFTER THE RECONNECT. Once the
		// watch is back it sees every new arrival, so an empty queue then
		// means no unseen sender has mail waiting. Before the reconnect it
		// means nothing: mail can land unseen a moment later. Without this,
		// a reconnect whose backlog count failed would leave every ring
		// nameless until the next reconnect.
		b.mu.Lock()
		if b.back {
			b.cut = false
		}
		b.mu.Unlock()
		b.endStreak()
		b.s.log(fmt.Sprintf("bell %s stopped: the queue is empty", b.s.d.Endpoint))
		return
	}

	b.mu.Lock()
	names := b.laterNames(n)
	b.mu.Unlock()
	b.try(n, courierName(names), bellFrom(names))
}

// laterNames is who a later try may name, or nil when it may name no one.
// Called under the lock.
//
// A LATER TRY NAMES A SEAT ONLY WHEN THE COUNT ENTAILS IT. The try asks the
// broker how much mail waits and never who sent it. The streak remembers the
// senders its first ring named and how many messages that ring announced, and
// the open window holds any mail that arrived since. Those senders are named
// when every one of these is true:
//
//   - the first ring named every message it announced, and so did the window;
//   - the queue holds no more than the two counts together, so no mail from
//     an unseen sender can be waiting;
//   - with several senders, the queue holds exactly that many. A smaller count
//     means some mail was read, and the count cannot say whose;
//   - the watch has not lost its connection since the last count. While it
//     is lost, mail lands unseen (disconnected).
//
// Anything else names no seat, exactly as the backlog path does.
func (b *bell) laterNames(n int) []string {
	if len(b.streakSenders) == 0 || b.blind || b.cut {
		return nil
	}
	names := append([]string(nil), b.streakSenders...)
	for _, s := range b.senders {
		seen := false
		for _, have := range names {
			if have == s {
				seen = true
				break
			}
		}
		if !seen {
			names = append(names, s)
		}
	}
	known := b.streakN + b.pending
	if n > known {
		return nil
	}
	if len(names) > 1 && n != known {
		return nil
	}
	return names
}

// endStreak closes a streak that ended without a give-up.
func (b *bell) endStreak() {
	b.mu.Lock()
	b.clearStreak()
	b.mu.Unlock()
}

// clearStreak cancels a waiting try and forgets the count and the senders.
// Called under the lock.
func (b *bell) clearStreak() {
	if b.tryTimer != nil {
		b.tryTimer.Stop()
		b.tryTimer = nil
	}
	b.tries = 0
	b.rangs = 0
	b.streakSenders = nil
	b.streakN = 0
}

// giveBack returns the minute and hour charges for a ring that TYPED NOTHING.
// Called under the lock.
//
// THE BUCKETS ARE NAMED. A bucket that rolled over between the charge and the
// refund never held this charge, and a refund there would raise the cap for a
// minute that has spent nothing.
func (b *bell) giveBack(minute, hour int64) {
	if b.minuteBucket == minute && b.minuteN > 0 {
		b.minuteN--
	}
	if b.hourBucket == hour && b.hourN > 0 {
		b.hourN--
	}
}

// courierName is what the courier session is called. That name is the one
// place the receiving pane shows who the message is from, so it carries the
// sender (#124).
//
// One sender gives that seat's full endpoint, house and all: two houses on one
// bus can each have a scribe, and a label of "scribe" alone reads the same for
// both. The label has a length cap, and an endpoint has none, so a full name
// that does not fit falls back to the seat's own part, which is what the label
// carried before the house joined it. Several give the first and a count of
// the rest, because the label has room for one name and a bell that named
// only the first would hide the others. None gives defaultCourierName.
func courierName(senders []string) string {
	if len(senders) == 0 {
		return defaultCourierName
	}
	first := senders[0]
	if !seatNameShape(first) && seatNameShape(shortSeat(first)) {
		first = shortSeat(first)
	}
	// THE WIRE VALUE IS NOT TRUSTED. `from` is written by the sending seat and
	// this house holds that a sender field is forgeable, so it reaches argv
	// only if it looks like a seat name. Unfiltered, a `from` of `-p` or
	// `--allowedTools` becomes a FLAG on the courier's own command line, and a
	// newline becomes a label carrying a line break. There is no shell on this
	// path — the spawn is exec.Command with an argv — so this is flag smuggling
	// rather than injection, and the remedy is the same: refuse the shape.
	// Both the whole endpoint and the seat's own part must have the shape: the
	// whole is what reaches argv, and the part is what a house could smuggle a
	// flag into behind a harmless-looking instance.
	if !seatNameShape(first) || !seatNameShape(shortSeat(first)) {
		return defaultCourierName
	}
	if len(senders) == 1 {
		return first
	}
	return fmt.Sprintf("%s+%d", first, len(senders)-1)
}

// bellFrom is how the bell line says who wrote: the seat and then its house,
// so workshop.scribe reads as "scribe of house workshop". The house is always
// named, because a reader cannot tell from a bare "scribe" which of two houses
// on the bus it means, and a bell that named the house only when it differed
// would ask the reader to know which case they were in. Several senders read
// as the first "and 2 more". An endpoint with no dot has no house to name.
//
// THE LINE IS NOT THE LABEL. The courier label goes on a command line and is
// capped at 32 characters; this phrase is typed into a pane, and an endpoint
// has no length limit, so the phrase takes its own cap. A sender whose name
// fails the shape gives "", and the line falls back to the count alone.
func bellFrom(senders []string) string {
	if len(senders) == 0 {
		return ""
	}
	first := senders[0]
	if !nameShape(first, bellFromMaxChars) || !nameShape(shortSeat(first), bellFromMaxChars) {
		return ""
	}
	more := ""
	if len(senders) > 1 {
		more = fmt.Sprintf(" and %d more", len(senders)-1)
	}
	seat := shortSeat(first)
	if i := strings.LastIndex(first, "."); i >= 0 {
		return seat + " of house " + first[:i] + more
	}
	return seat + more
}

// bellFromMaxChars caps the sender's name in the bell line. A pane line is read
// by a person, and a name past this is not a name a person reads.
const bellFromMaxChars = 200

// seatNameShape reports whether s is safe to hand to a command line as a
// display name: letters, digits, dot, underscore and hyphen, and never leading
// with a hyphen, which is what makes a word a flag. The cap is deliberate —
// a label is read by a person in a prompt box, not parsed.
func seatNameShape(s string) bool {
	return nameShape(s, 32)
}

// nameShape is seatNameShape with the cap as an argument, because the bell
// line and the courier label need the same characters and different lengths.
func nameShape(s string, maxChars int) bool {
	if s == "" || len(s) > maxChars || s[0] == '-' {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// shortSeat is the seat's own name out of an endpoint: the part after the last
// dot, so workshop.scribe gives scribe. An endpoint with no dot is already the
// short name. The bell line and the courier label no longer show this name on
// its own; bellFrom puts the house beside it.
func shortSeat(endpoint string) string {
	if i := strings.LastIndex(endpoint, "."); i >= 0 {
		return endpoint[i+1:]
	}
	return endpoint
}

// roll advances the breaker's buckets. Called under the lock.
func (b *bell) roll(now time.Time) {
	if m := now.Unix() / 60; m != b.minuteBucket {
		b.minuteBucket, b.minuteN = m, 0
	}
	if h := now.Unix() / 3600; h != b.hourBucket {
		b.hourBucket, b.hourN = h, 0
		// A new hour is a fresh start, and the trip is part of what resets.
		b.tripped = false
	}
}

// tableInt reads one of the three bell keys the config table holds. A value the
// file cannot supply — a typo, a zero, a negative — falls back to the table's
// own default, for the reason configInt gives below. The minimum is 1: a
// streak of no tries is a bell that never rings, and a gap of no seconds is a
// loop.
func tableInt(key string) int {
	if n := config.Int(key); n >= 1 {
		return n
	}
	n, _ := strconv.Atoi(config.Default(key))
	return n
}

// configInt reads one of the wake keys the config table does NOT hold, falling
// back to its default when the key is absent or is not a number. A deployment's
// typo must not silently become "no cap at all".
func configInt(key string, def int) int {
	v := config.Get(key, "")
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
		return def
	}
	return n
}
