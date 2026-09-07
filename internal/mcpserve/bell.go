package mcpserve

import (
	"fmt"
	"sync"
	"time"

	"github.com/tdebasis/locutorium/internal/config"
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

// bell coalesces arrivals and rings the deployment's nudge hook.
type bell struct {
	s       *server
	window  time.Duration
	perMin  int
	perHour int

	mu      sync.Mutex
	pending int
	timer   *time.Timer
	stopped bool

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
func (b *bell) arrived() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return
	}
	b.pending++
	if b.timer == nil {
		b.timer = time.AfterFunc(b.window, b.fire)
	}
}

// backlog asks the medium how much mail is waiting and rings for it at once,
// without the window: there is nothing to coalesce with, and a seat waiting on
// a five-second window to be told about mail that arrived an hour ago would be
// waiting for no reason.
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
	b.pending += n
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

	b.s.d.Nudge(b.s.d.Endpoint, fmt.Sprintf(bellLine, n))
	b.s.log(fmt.Sprintf("wake %s count=%d", b.s.d.Endpoint, n))
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
