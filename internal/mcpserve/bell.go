package mcpserve

import (
	"fmt"
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
// A suppressed wake loses NOTHING: the messages are in the queue, and the next
// read finds all of them. That is what makes a cap safe to have.

const (
	defaultWakeWindow    = 5
	defaultBreakerMinute = 6
	defaultBreakerHour   = 60
)

// defaultCourierName is what the courier is called when the bell cannot say
// who sent the mail. The startup backlog path asks the broker HOW MUCH mail is
// waiting and never who sent it, so it names no seat; a name invented there
// would be a guess standing in the one field a reader trusts.
const defaultCourierName = "loc-bell"

// bell coalesces arrivals and rings the seat's notifier.
type bell struct {
	s       *server
	window  time.Duration
	perMin  int
	perHour int

	mu      sync.Mutex
	pending int
	timer   *time.Timer
	stopped bool

	// The senders seen in the open window, in arrival order and each one
	// once. They name the courier and nothing else: they never reach the bell
	// line, which stays a count with no body.
	senders []string

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
		s:       s,
		window:  time.Duration(configInt("wake_window_seconds", defaultWakeWindow)) * time.Second,
		perMin:  configInt("wake_breaker_per_minute", defaultBreakerMinute),
		perHour: configInt("wake_breaker_per_hour", defaultBreakerHour),
	}
	s.b = b

	stop, err := s.d.Watch(s.d.Endpoint, b.arrived, b.backlog)
	if err != nil {
		return func() {}
	}
	// WAKE ON BACKLOG. A seat that starts with mail already waiting was never
	// told about it — the arrivals happened while nothing was listening — so
	// the count is asked for once at start, and again after every reconnect,
	// which is the other moment arrivals can have been missed.
	b.backlog()

	return func() {
		b.mu.Lock()
		b.stopped = true
		if b.timer != nil {
			b.timer.Stop()
			b.timer = nil
		}
		b.mu.Unlock()
		stop()
	}
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
	b.note(sender)
	if b.timer == nil {
		b.timer = time.AfterFunc(b.window, b.fire)
	}
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
// THE FIGURE IS SET, NOT ADDED. Unread is the consumer's NumPending, and it
// already counts every arrival the watcher has counted since the last ring:
// the two are views of ONE queue, not two sources of messages. Adding them
// rang for mail that does not exist — one message landing between the watcher
// going live and this sample rang the pane for two — so the broker's figure
// REPLACES the tally, and the larger of the two is kept: what the broker holds
// is the truth, and the watcher's count is only ever a lower bound on it.
func (b *bell) backlog() {
	if b.s.d.Unread == nil {
		return
	}
	n, err := b.s.d.Unread(b.s.d.Endpoint)
	if err != nil || n <= 0 {
		return
	}
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}
	if n > b.pending {
		b.pending = n
	}
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.mu.Unlock()
	b.fire()
}

// fire rings once for everything that has accumulated, if the breaker lets it.
func (b *bell) fire() {
	b.mu.Lock()
	n := b.pending
	b.pending = 0
	senders := b.senders
	b.senders = nil
	b.timer = nil
	if b.stopped || n <= 0 {
		b.mu.Unlock()
		return
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
		return
	}
	b.minuteN++
	b.hourN++
	b.mu.Unlock()

	// A BELL THAT COULD NOT RING SAYS SO, and is never recorded as a wake.
	// The breaker has already been charged for it, which is deliberate: a
	// notifier that fails will fail again, and a broken bell must not become a
	// way to hammer the pane once it is fixed.
	if err := b.s.d.Notify.Ring(b.s.d.Endpoint, b.s.address, fmt.Sprintf(bellLine, n), courierName(senders)); err != nil {
		b.s.warn(fmt.Sprintf("bell failed %s: %v", b.s.d.Endpoint, err))
		// THE WARNING AND THE EVENT GO TO DIFFERENT READERS. The warning
		// lands in this seat's own delivery log, which is plain text and is
		// found by somebody who already suspects this seat. The event lands
		// in the day's record beside the `sent` line for the mail that could
		// not be announced, which is where a sender asking "why has nobody
		// answered me" is already looking.
		//
		// It carries no uid. The watch reports an arrival and Unread reports
		// a count, so the server knows that mail is waiting and never which
		// message is waiting (WatchQueue, internal/provider/provider.go).
		//
		// The clock is the server's injected one, the same clock the breaker
		// rolls its buckets on, so a case that pins time pins this line too.
		loc.LogBellFailed(b.s.d.Endpoint, err.Error(), b.s.d.Now())
		return
	}
	b.s.log(fmt.Sprintf("wake %s count=%d", b.s.d.Endpoint, n))
}

// courierName is what the courier session is called. That name is the one
// place the receiving pane shows who the message is from, so it carries the
// sender (#124).
//
// One sender gives that seat's short name. Several give the first and a count
// of the rest, because the label has room for one name and a bell that named
// only the first would hide the others. None gives defaultCourierName.
func courierName(senders []string) string {
	if len(senders) == 0 {
		return defaultCourierName
	}
	first := shortSeat(senders[0])
	// THE WIRE VALUE IS NOT TRUSTED. `from` is written by the sending seat and
	// this house holds that a sender field is forgeable, so it reaches argv
	// only if it looks like a seat name. Unfiltered, a `from` of `-p` or
	// `--allowedTools` becomes a FLAG on the courier's own command line, and a
	// newline becomes a label carrying a line break. There is no shell on this
	// path — the spawn is exec.Command with an argv — so this is flag smuggling
	// rather than injection, and the remedy is the same: refuse the shape.
	if !seatNameShape(first) {
		return defaultCourierName
	}
	if len(senders) == 1 {
		return first
	}
	return fmt.Sprintf("%s+%d", first, len(senders)-1)
}

// seatNameShape reports whether s is safe to hand to a command line as a
// display name: letters, digits, dot, underscore and hyphen, and never leading
// with a hyphen, which is what makes a word a flag. The cap is deliberate —
// a label is read by a person in a prompt box, not parsed.
func seatNameShape(s string) bool {
	if s == "" || len(s) > 32 || s[0] == '-' {
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
// dot, so workshop.scribe reads as scribe. An endpoint with no dot is
// already the short name.
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

// configInt reads one of the wake keys, falling back to its default when the
// key is absent or is not a number. A deployment's typo must not silently
// become "no cap at all".
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
