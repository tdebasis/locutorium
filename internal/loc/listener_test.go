package loc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// File existence must never stand in for liveness: a crash leaves a stale
// pidfile, and pid recycling can make a stale pid look alive. The start time
// is what tells the two apart, and it is compared byte for byte.
func TestListenerAlive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	pidfile := filepath.Join(home, "run", "bob.listener.pid")

	if ListenerAlive("bob") {
		t.Error("no pidfile, yet a listener was reported")
	}

	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(pidfile, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	start := PidStart(pid)
	if start == "" {
		t.Fatal("ps told us nothing about a process we just started")
	}
	write(strconv.Itoa(pid) + "\n" + start + "\n")
	if !ListenerAlive("bob") {
		t.Error("a running pid with a matching start time was not seen as alive")
	}

	// A stale entry: the pid runs, but it is not the process that wrote this.
	write(strconv.Itoa(pid) + "\nWed Jan  1 00:00:00 2020\n")
	if ListenerAlive("bob") {
		t.Error("a mismatched start time was accepted")
	}

	// Half a file is not a licence either.
	write(strconv.Itoa(pid) + "\n")
	if ListenerAlive("bob") {
		t.Error("a pidfile with no start time was accepted")
	}
	write("not-a-pid\nwhenever\n")
	if ListenerAlive("bob") {
		t.Error("a pidfile with no pid was accepted")
	}

	// The process ends; the file outlives it.
	write(strconv.Itoa(pid) + "\n" + start + "\n")
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	if ListenerAlive("bob") {
		t.Error("a dead pid was reported as a live listener")
	}
}
