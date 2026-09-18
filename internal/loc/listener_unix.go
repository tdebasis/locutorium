//go:build !windows

package loc

import "syscall"

// PidAlive reports whether one process is running now. It never stops a
// process and never changes one.
//
// It is exported for the same reason PidStart is: the presence model asks the
// operating system this question about the pids it records, and one way of
// asking is one way of being wrong.
//
// Signal 0 is the POSIX liveness probe. The kernel runs its existence and
// permission checks and then delivers nothing. A nil error means the process
// exists and this user may signal it. A process that exists under another user
// gives EPERM and reads here as not alive, which is the behaviour this call
// has always had.
func PidAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }
