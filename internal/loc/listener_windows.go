//go:build windows

package loc

import "syscall"

// stillActive is what GetExitCodeProcess reports for a process that has not
// exited. The Windows headers call it STILL_ACTIVE.
const stillActive = 259

// PidAlive reports whether one process is running now. It never stops a
// process and never changes one. See the unix file for why it is exported.
//
// Windows has no signal 0, so the question goes to the process object: open a
// handle that may only query, then read the exit code. An open handle is not
// the answer by itself. A process that has already exited still opens while
// any handle to it is held, and it reports the code it exited with.
//
// PROCESS_QUERY_INFORMATION carries no right to terminate. A pid that does not
// exist, and a process this user may not open, both fail at OpenProcess and
// read as not alive. Those are the two outcomes the POSIX probe gives for
// ESRCH and EPERM.
//
// One case this route cannot separate: a process whose own exit code is 259
// reads as alive until the last handle to it closes. Windows offers no way to
// tell the two apart, and a listener exiting with 259 is not something the
// pidfile contract produces.
func PidAlive(pid int) bool {
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
