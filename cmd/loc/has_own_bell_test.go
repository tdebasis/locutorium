package main

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	model "github.com/tdebasis/locutorium/internal/presence"
)

// hasItsOwnBell decides whether `send` stays quiet because the recipient's own
// server will ring. Two things now depend on it: the sender, and the bell
// fixture that waits on it before sending (mcp_bell_test.go).
//
// THAT IS WHY THIS TEST EXISTS. Before, the fixture waited on the registration
// row while the sender decided by the pid file — two different questions about
// "is the server up", and their disagreement is what made the startup race
// visible as a flaky double bell. Making the fixture ask the sender's own
// question removed the flake and removed that signal with it: a wrong
// hasItsOwnBell would now make the fixture and the sender wrong together, and
// the test would pass. So the predicate gets its own test, which shares nothing
// with either caller.
//
// The arms are the states a pid file is actually found in. The last one is the
// control: without a case that returns TRUE, a predicate that refused
// everything would pass every other arm here.
func TestHasItsOwnBell(t *testing.T) {
	const ep = "workshop.scribe"

	write := func(t *testing.T, body string) {
		t.Helper()
		home := t.TempDir()
		t.Setenv("LOC_HOME", home)
		if body == "" {
			return // no file at all
		}
		dir := filepath.Join(home, "run")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, ep+".mcp.pid"), []byte(body), 0o600); err != nil {
			t.Fatalf("write pidfile: %v", err)
		}
	}

	live := os.Getpid()
	liveStart := model.StartedAt(live)
	if liveStart == "" {
		t.Skip("this platform will not report a start time for our own pid")
	}

	t.Run("no file at all", func(t *testing.T) {
		write(t, "")
		if hasItsOwnBell(ep) {
			t.Error("no pid file, and it claims the seat rings")
		}
	})

	t.Run("one line only", func(t *testing.T) {
		// The file is written pid-then-start-time. One line means it was caught
		// half written, and half a fact is not a fact.
		write(t, strconv.Itoa(live)+"\n")
		if hasItsOwnBell(ep) {
			t.Error("a half-written pid file claims the seat rings")
		}
	})

	t.Run("not a number", func(t *testing.T) {
		write(t, "not-a-pid\n"+liveStart+"\n")
		if hasItsOwnBell(ep) {
			t.Error("an unreadable pid claims the seat rings")
		}
	})

	t.Run("live pid, wrong start time", func(t *testing.T) {
		// THE ONE THAT MATTERS AFTER A CRASH. The pid is alive because it was
		// reused by something else; the start time is the dead server's. Kill -0
		// alone would say yes here, which is why the stamp is compared at all.
		write(t, strconv.Itoa(live)+"\nnot-when-this-process-started\n")
		if hasItsOwnBell(ep) {
			t.Error("a stale start time claims the seat rings")
		}
	})

	t.Run("dead pid", func(t *testing.T) {
		// THE ORDINARY STALE PIDFILE, and the state a real seat is actually
		// found in after a crash: the server died, its file stayed, and nothing
		// took its number. Far more common than the reuse case above, which is
		// why it is worth an arm of its own rather than being assumed to follow.
		//
		// The pid is one we watched exit, not a large constant — a constant is
		// somebody else's live process on a busy machine, and the arm would go
		// flaky. If the number is taken again between the exit and the check,
		// the case cannot be posed and says so instead of passing quietly.
		c := osexec.Command("/usr/bin/true")
		if err := c.Run(); err != nil {
			t.Skipf("could not spawn a process to kill: %v", err)
		}
		dead := c.Process.Pid
		if syscall.Kill(dead, 0) == nil {
			t.Skipf("pid %d was reused before the check; cannot pose the dead case", dead)
		}
		write(t, strconv.Itoa(dead)+"\n"+liveStart+"\n")
		if hasItsOwnBell(ep) {
			t.Error("a dead server's leftover pid file claims the seat rings")
		}
	})

	t.Run("live pid and its real start time", func(t *testing.T) {
		// The control. Without this arm, a hasItsOwnBell that always returned
		// false would pass every arm above and prove nothing.
		write(t, strconv.Itoa(live)+"\n"+liveStart+"\n")
		if !hasItsOwnBell(ep) {
			t.Error("a live server's own pid file, and it says the seat is silent")
		}
	})
}
