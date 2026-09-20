package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ════════════════════════════════════════════════════════════════════════════
// SEPARATE — message-plane loss-safety, NOT part of the presence set, exactly
// as read_test.go's case is. This one is its SUBPROCESS twin, and it exists
// because the in-process twin cannot see the defect it covers.
//
// read_test.go drives run() with a writer that returns an error after one
// byte. A Go writer returning an error is not a broken pipe: nothing about the
// process changes, and the verb's own loss-safety is all that gets exercised.
// A REAL reader that goes away is a different event. The write returns EPIPE,
// and a Go program that has not said otherwise is KILLED by SIGPIPE when the
// broken descriptor is 1 or 2 — before any deferred close runs, so the fetched
// message is never handed back and stays in flight for the consumer's ack-wait
// (30 s) while the very next read shows an empty mailbox. That is the shell
// suite's `loc read | head -c 1` case, and only a process boundary can put it.
// ════════════════════════════════════════════════════════════════════════════

// pipeCanary is the body seeded for the case. It carries the marker AND enough
// padding to exceed any pipe's buffer, which is what makes the test a test:
// the reader takes ONE byte and never takes another, so this single write can
// NEVER complete. The failure therefore lands on the write that carries the
// message — with the message fetched and unacknowledged — instead of racing
// the child to some later, harmless write it has already acknowledged past.
var pipeCanary = "loss-canary " + strings.Repeat("x", 256*1024)

func TestMessagePlane_ReadUnderAClosedPipeHandsTheMessageBack(t *testing.T) {
	goTool, err := osexec.LookPath("go")
	if err != nil {
		t.Skip("no 'go' on PATH: this case needs the real binary, because the defect is a signal")
	}

	p := newPresence(t)
	p.as(t, e1) // an agent reads its own queue
	p.seedQueue(t, q1, "queue."+e1)
	if _, err := p.adminJS.Publish("queue."+e1, []byte(fmt.Sprintf(
		`{"id":"m1","ts":"2026-01-14T09:00:00.000Z","from":"host","to":%q,"kind":"msg","body":%q}`,
		e1, pipeCanary))); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// The binary, not run(): the process boundary IS the subject here.
	bin := filepath.Join(t.TempDir(), "loc")
	if out, err := osexec.Command(goTool, "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	var childErr bytes.Buffer
	cmd := osexec.Command(bin, "read")
	cmd.Stdout = w // the real descriptor 1, so a broken pipe is a real SIGPIPE
	cmd.Stderr = &childErr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = w.Close() // the child now holds the only write end

	// ONE byte, then the reader is gone — `head -c 1`. Reading it is also the
	// proof that the child got as far as its first write, which happens only
	// after the message has been fetched: a child that died earlier would
	// leave the canary in the queue and the second read would find it there
	// whatever the build does, which would be a case that cannot fail.
	var one [1]byte
	if _, err := io.ReadFull(r, one[:]); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("the reader never got a first byte (%v); loc said: %s", err, childErr.String())
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close read end: %v", err)
	}

	waitErr := cmd.Wait()
	t.Logf("reader gone after one byte; loc exited with %v; stderr: %q", waitErr, childErr.String())

	// A WRITE ERROR, NOT A DEATH. Killed by the signal, the process skips the
	// close that hands the unshown message back.
	if ee, ok := waitErr.(*osexec.ExitError); ok && strings.Contains(ee.String(), "signal:") {
		t.Errorf("loc was killed by a signal (%v); a reader that goes away must be an ordinary write error", ee)
	}

	// And the message it never showed is owed to this reader, now — not in
	// half a minute.
	var out safeBuffer
	_ = run([]string{"read"}, &out, io.Discard)
	if !strings.Contains(out.String(), "loss-canary") {
		t.Errorf("a read whose reader went away did not re-present the message; the next read showed %q",
			truncate(out.String(), 200))
	}
}

// truncate keeps a failure line readable when the body under test is large.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
