//go:build !windows

package main

// A read killed by a signal must hand its in-flight message back (issue #30).
//
// THE CASE SENDS A REAL SIGNAL TO ITS OWN PROCESS. Nothing else proves the
// registration is live: a case that called the handler function directly would
// pass with no signal.Notify at all. The re-raise is pinned to a recorder for
// the length of the case, because the shipped one ends the process and this
// process is the test binary.

import (
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

var _ provider.Provider = (*closeRecorder)(nil)

// orderRecorder writes its own name into a shared channel, so the order of the
// two acts is read off one sequence rather than off two goroutines racing to
// report.
type orderRecorder struct {
	closeRecorder
	order chan string
}

func (o *orderRecorder) Close() { o.order <- "close" }

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

// A signalled read closes the provider, which is what negatively acknowledges
// the message it was handed and never showed. Without it that message stays in
// flight for the consumer's whole ack-wait and the next read shows an empty
// mailbox.
func TestASignalledReadReleasesItsMessageBeforeItExits(t *testing.T) {
	for _, s := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(s.String(), func(t *testing.T) {
			p := &closeRecorder{closed: make(chan struct{}, 1)}
			raised := make(chan os.Signal, 1)
			original := readSignalExit
			readSignalExit = func(got os.Signal) { raised <- got }
			t.Cleanup(func() { readSignalExit = original })

			stop := releaseOnSignal(p)
			defer stop()

			selfSignal(t, s)

			select {
			case <-p.closed:
			case <-time.After(5 * time.Second):
				t.Fatal("the provider was not closed: a read killed by a signal " +
					"strands its in-flight message for the whole ack-wait")
			}

			// THE SIGNAL IS NOT SWALLOWED. The handler releases and then sends
			// the same signal on, so the process still ends the way the caller
			// asked and the exit status still names the signal.
			select {
			case got := <-raised:
				if got != s {
					t.Fatalf("re-raised %v, want %v", got, s)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%v was swallowed: the read would exit as though nothing had happened", s)
			}
		})
	}
}

// The release is ordered BEFORE the re-raise. A re-raise that ran first would
// end the process with the message still in flight, which is the defect this
// handler exists to remove.
func TestTheReleaseRunsBeforeTheSignalIsSentOn(t *testing.T) {
	order := make(chan string, 2)
	p := &orderRecorder{order: order}
	original := readSignalExit
	readSignalExit = func(os.Signal) { order <- "raise" }
	t.Cleanup(func() { readSignalExit = original })

	stop := releaseOnSignal(p)
	defer stop()
	selfSignal(t, syscall.SIGINT)

	first := ""
	select {
	case first = <-order:
	case <-time.After(5 * time.Second):
		t.Fatal("neither the release nor the re-raise happened")
	}
	if first != "close" {
		t.Fatalf("the signal was sent on before the message was released (first: %s)", first)
	}
}

// A read that ends on its own must leave no handler behind. stop unregisters
// the subscription, and withProvider's own Close is what returns anything
// outstanding on that path.
func TestStoppingTheReleaseLeavesTheProviderAlone(t *testing.T) {
	p := &closeRecorder{closed: make(chan struct{}, 1)}
	stop := releaseOnSignal(p)
	stop()

	select {
	case <-p.closed:
		t.Fatal("the stopped handler closed the provider; a read's ordinary close is the only one here")
	case <-time.After(200 * time.Millisecond):
	}
}
