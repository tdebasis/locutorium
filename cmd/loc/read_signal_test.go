//go:build !windows

package main

// A read killed by a signal must hand its in-flight message back (issue #30).
//
// THE CASE SENDS A REAL SIGNAL TO ITS OWN PROCESS. Nothing else proves the
// registration is live: a case that called the handler function directly would
// pass with no signal.Notify at all. The signal is sent from inside the read,
// which runs after runRead has armed, so the default disposition is never in
// force when it arrives and the test binary is never killed by it.
//
// THE RE-RAISE IS PINNED TO A RECORDER for the length of the case, because the
// shipped one ends the process and this process is the test binary. runRead
// reads that variable on the case's own goroutine, so the restore in t.Cleanup
// cannot race it. The first cut read it on the handler's goroutine, and that
// was a data race the case could not run under -race to see.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/tdebasis/locutorium/internal/provider"
)

// closeRecorder is a provider that records the one act this case is about.
// Close may be called more than once, so the record is a non-blocking send
// rather than a channel close.
type closeRecorder struct{ closed chan struct{} }

func (c *closeRecorder) SendQueue(string, []byte) error    { return nil }
func (c *closeRecorder) PublishTopic(string, []byte) error { return nil }
func (c *closeRecorder) Topics(io.Writer) error            { return nil }
func (c *closeRecorder) Status(io.Writer) error            { return nil }

func (c *closeRecorder) Close() {
	select {
	case c.closed <- struct{}{}:
	default:
	}
}

func (c *closeRecorder) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

var _ provider.Provider = (*closeRecorder)(nil)

// orderRecorder writes its own name into a shared channel, so the order of the
// two acts is read off one sequence rather than off two goroutines racing to
// report.
type orderRecorder struct {
	closeRecorder
	order chan string
}

func (o *orderRecorder) Close() { o.order <- "close" }

// opener is withProvider's shape: it opens a provider, runs the body, and
// closes the provider on the way out. The deferred Close is the shipped one
// (withProvider, cmd/loc/main.go), copied here because the real opener needs a
// broker. A case pins the ORDER of the close and the re-raise; it does not
// prove withProvider defers its Close, which is read at that line.
func opener(p provider.Provider) func(func(provider.Provider) error) error {
	return func(fn func(provider.Provider) error) error {
		defer p.Close()
		return fn(p)
	}
}

// selfSignal sends one signal to this process.
func selfSignal(t *testing.T, s os.Signal) {
	t.Helper()
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("find this process: %v", err)
	}
	if err := self.Signal(s); err != nil {
		t.Fatalf("signal this process: %v", err)
	}
}

// waitForUnwind is a read that signals itself and then waits to be told to
// stop. It stands for the fetch-and-write loop in drain.
func waitForUnwind(t *testing.T, s os.Signal, saw func(provider.Provider)) func(provider.Provider, <-chan struct{}) error {
	return func(p provider.Provider, unwind <-chan struct{}) error {
		selfSignal(t, s)
		select {
		case <-unwind:
			if saw != nil {
				saw(p)
			}
			return errSignalled
		case <-time.After(5 * time.Second):
			return fmt.Errorf("the read was never told to unwind: %v left it inside its fetch "+
				"and the in-flight message stays in flight for the whole ack-wait", s)
		}
	}
}

// A signalled read unwinds, and the close on the way out is what negatively
// acknowledges the message it was handed and never showed.
func TestASignalledReadReleasesItsMessageBeforeItExits(t *testing.T) {
	for _, s := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(s.String(), func(t *testing.T) {
			p := &closeRecorder{closed: make(chan struct{}, 1)}
			raised := make(chan os.Signal, 1)
			original := readSignalExit
			readSignalExit = func(got os.Signal) { raised <- got }
			t.Cleanup(func() { readSignalExit = original })

			if err := runRead(opener(p), waitForUnwind(t, s, nil)); err != nil {
				t.Fatalf("the read failed: %v", err)
			}

			if !p.isClosed() {
				t.Fatal("the provider was not closed: a read killed by a signal " +
					"strands its in-flight message for the whole ack-wait")
			}

			// THE SIGNAL IS NOT SWALLOWED. The read releases and then sends the
			// same signal on, so the process still ends the way the caller
			// asked and the exit status still names the signal.
			select {
			case got := <-raised:
				if got != s {
					t.Fatalf("re-raised %v, want %v", got, s)
				}
			default:
				t.Fatalf("%v was swallowed: the read would exit as though nothing had happened", s)
			}
		})
	}
}

// THE HANDLER MUST NOT CLOSE THE PROVIDER. It says stop; the read's own exit
// closes. The first cut closed on the handler's goroutine while the read was
// still using the connection, which is a data race and a reachable nil
// dereference (Assayer, 2026-09-16). This case stands at the moment that cut
// would have closed — the read has been told to unwind and has not returned —
// and reads the provider as still open.
func TestTheHandlerDoesNotCloseTheProviderItself(t *testing.T) {
	p := &closeRecorder{closed: make(chan struct{}, 1)}
	original := readSignalExit
	readSignalExit = func(os.Signal) {}
	t.Cleanup(func() { readSignalExit = original })

	early := false
	run := waitForUnwind(t, syscall.SIGTERM, func(provider.Provider) {
		// Still inside the read. Nothing but the read may have closed this.
		early = p.isClosed()
	})
	if err := runRead(opener(p), run); err != nil {
		t.Fatalf("the read failed: %v", err)
	}
	if early {
		t.Fatal("the signal handler closed the provider while the read was still using it")
	}
}

// The release is ordered BEFORE the re-raise. A re-raise that ran first would
// end the process with the message still in flight, which is the defect this
// path exists to remove.
func TestTheReleaseRunsBeforeTheSignalIsSentOn(t *testing.T) {
	order := make(chan string, 2)
	p := &orderRecorder{order: order}
	original := readSignalExit
	readSignalExit = func(os.Signal) { order <- "raise" }
	t.Cleanup(func() { readSignalExit = original })

	if err := runRead(opener(p), waitForUnwind(t, syscall.SIGINT, nil)); err != nil {
		t.Fatalf("the read failed: %v", err)
	}

	first := ""
	select {
	case first = <-order:
	default:
		t.Fatal("neither the release nor the re-raise happened")
	}
	if first != "close" {
		t.Fatalf("the signal was sent on before the message was released (first: %s)", first)
	}
}

// A read that ends on its own must leave no handler behind, and must not report
// a signal that never came.
func TestAReadThatEndsOnItsOwnReportsNoSignal(t *testing.T) {
	p := &closeRecorder{closed: make(chan struct{}, 1)}
	original := readSignalExit
	readSignalExit = func(got os.Signal) {
		t.Errorf("a read that ended on its own re-raised %v", got)
	}
	t.Cleanup(func() { readSignalExit = original })

	err := runRead(opener(p), func(provider.Provider, <-chan struct{}) error { return nil })
	if err != nil {
		t.Fatalf("an ordinary read failed: %v", err)
	}
	if !p.isClosed() {
		t.Fatal("an ordinary read left the provider open")
	}
}

// errSignalled is the read's own ending and not a failure to print. A verb that
// returned it would put "read stopped by a signal" on standard error under
// every ^C.
func TestTheSignalEndingIsNotReportedAsAnError(t *testing.T) {
	p := &closeRecorder{closed: make(chan struct{}, 1)}
	original := readSignalExit
	readSignalExit = func(os.Signal) {}
	t.Cleanup(func() { readSignalExit = original })

	err := runRead(opener(p), waitForUnwind(t, syscall.SIGTERM, nil))
	if errors.Is(err, errSignalled) {
		t.Fatal("the signal ending reached the caller as an error")
	}
	if err != nil {
		t.Fatalf("the read failed: %v", err)
	}
}

// stop unregisters the subscription, and nothing closes the provider on that
// path but the read's own exit.
func TestStoppingTheHandlerLeavesTheProviderAlone(t *testing.T) {
	unwind, killedBy, stop := unwindOnSignal()
	stop()

	if signalled(unwind) {
		t.Fatal("a read that was never signalled was told to unwind")
	}
	if s := killedBy(); s != nil {
		t.Fatalf("a read that was never signalled reported %v", s)
	}
}

// signalAfterWriter sends one signal to this process once it has taken `after`
// writes, and then behaves as a writer that accepts everything. It puts the
// signal INSIDE the read, between a fetch and an acknowledgement, which is
// where the provider is in use.
type signalAfterWriter struct {
	t     *testing.T
	after int
	n     int
	sig   os.Signal
}

func (s *signalAfterWriter) Write(b []byte) (int, error) {
	s.n++
	if s.n == s.after {
		selfSignal(s.t, s.sig)
	}
	return len(b), nil
}

// THE RACE PROBE DRIVES THE READ PATH, NOT Status. The Assayer's probe drove
// Status while the signal arrived, and the read path is where the unguarded
// dereferences are: the connection, the stream and the subscriptions
// (internal/provider/nats). This case runs the shipped `read` verb against a
// real broker and a real nats provider, sends a real SIGTERM from inside the
// read's own write, and lets the verb close on its way out. Under -race it
// must print no DATA RACE.
//
// It is the case the first cut fails: there the signal handler called Close on
// its own goroutine while this read was between a fetch and an acknowledgement.
func TestNoRaceBetweenASignalAndTheReadPath(t *testing.T) {
	p := newPresence(t)
	p.as(t, e1)
	p.seedQueue(t, q1, "queue."+e1)
	for i := 0; i < 24; i++ {
		env := fmt.Sprintf(`{"id":"race-%02d","ts":"2026-01-14T09:00:00.000Z","from":"workshop.host",`+
			`"to":"%s","kind":"msg","body":"race probe %02d"}`, i, e1, i)
		if err := p.admin.Publish("queue."+e1, []byte(env)); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	_ = p.admin.Flush()

	original := readSignalExit
	readSignalExit = func(os.Signal) {}
	t.Cleanup(func() { readSignalExit = original })

	w := &signalAfterWriter{t: t, after: 3, sig: syscall.SIGTERM}
	if code := run([]string{"read"}, w, io.Discard); code != 0 {
		t.Fatalf("a signalled read exited %d; the signal is the ending, not a failure", code)
	}
	if w.n < 3 {
		t.Fatalf("the probe never reached the write it signals on (%d writes): "+
			"it did not drive the read path", w.n)
	}
}
