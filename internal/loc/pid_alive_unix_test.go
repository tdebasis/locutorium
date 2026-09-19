//go:build !windows

package loc

import (
	"os"
	"os/exec"
	"testing"
)

// PidAlive asks the operating system, not a constant. This process is running,
// so it must read alive; a process this test starts and waits for has exited,
// so its pid must read dead.
//
// THE PLANT THAT TURNS THIS RED: make PidAlive return true unconditionally.
func TestPidAliveAsksTheOperatingSystem(t *testing.T) {
	if !PidAlive(os.Getpid()) {
		t.Error("PidAlive(os.Getpid()) = false, want true")
	}

	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}

	if PidAlive(pid) {
		t.Error("PidAlive(pid) = true for a process that has exited, want false")
	}
}
