package main

// servingPID must wait for a pid file that has not been written yet, because
// the server that writes it registers the seat first and claims the file
// afterward. This case proves the wait: the file arrives late, on its own
// goroutine, and servingPID still returns the pid it holds.
//
// Before the fix, servingPID read the file once and failed as soon as it was
// missing. This case would have failed then, deterministically, because the
// file is absent at the first read every time it runs.

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	model "github.com/tdebasis/locutorium/internal/presence"
)

func TestServingPIDWaitsForAFileThatArrivesLate(t *testing.T) {
	home := t.TempDir()
	endpoint := "workshop.scribe"
	pid := os.Getpid()

	// The file is written directly, not through writeServingPID: that helper
	// calls t.Fatalf on a write failure, and t.Fatalf must run on the test's
	// own goroutine, not on one this case spawns. Any write error is carried
	// back on writeErr instead.
	var wg sync.WaitGroup
	writeErr := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(300 * time.Millisecond)
		path := mcpPIDPath(home, endpoint)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			writeErr <- err
			return
		}
		body := strconv.Itoa(pid) + "\n" + model.StartedAt(pid) + "\n"
		writeErr <- os.WriteFile(path, []byte(body), 0o600)
	}()
	// The goroutine must finish before this test returns, so it can never
	// write into a temp dir t.TempDir() has already removed.
	defer wg.Wait()

	if got := servingPID(t, home, endpoint); got != pid {
		t.Errorf("servingPID returned %d for a file that arrived after 300ms; want %d", got, pid)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write the late pid file: %v", err)
	}
}

// A pid file can be seen half written: os.WriteFile is not atomic, so a reader
// can find the first digits of the pid and nothing after them. Those digits
// parse as a pid, and it is the pid of a different process. The callers of
// servingPID send a signal to the pid it returns, so a torn read must read as
// "not yet". A first line counts only when a newline ends it.
//
// The file here holds "1" with no newline, then the whole body 300ms later.
// A servingPID that accepts the torn line returns 1 at once.
func TestServingPIDRefusesAFirstLineWithNoNewline(t *testing.T) {
	home := t.TempDir()
	endpoint := "workshop.scribe"
	pid := os.Getpid()
	if pid == 1 {
		t.Skip("this case cannot tell a torn \"1\" from the real pid when the test is pid 1")
	}

	path := mcpPIDPath(home, endpoint)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("make the run directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("1"), 0o600); err != nil {
		t.Fatalf("write the torn pid file: %v", err)
	}

	var wg sync.WaitGroup
	writeErr := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(300 * time.Millisecond)
		body := strconv.Itoa(pid) + "\n" + model.StartedAt(pid) + "\n"
		writeErr <- os.WriteFile(path, []byte(body), 0o600)
	}()
	defer wg.Wait()

	if got := servingPID(t, home, endpoint); got != pid {
		t.Errorf("servingPID returned %d from a file that first held a torn \"1\"; want %d", got, pid)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write the whole pid file: %v", err)
	}
}
