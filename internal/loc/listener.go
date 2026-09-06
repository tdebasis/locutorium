package loc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/tdebasis/locutorium/internal/config"
)

// ListenerAlive reports whether an endpoint has a live listener: a RUNNING pid
// with a MATCHING start time.
//
// File existence alone must never stand in for liveness — a crash leaves a
// stale pidfile, and pid recycling can make a stale pid look alive. The
// pidfile's two-line format (pid, then the `ps -o lstart=` start time) is a
// frozen contract with the shell listener that writes it and with any
// out-of-tree reader.
func ListenerAlive(endpoint string) bool {
	pidfile := filepath.Join(config.Home(), "run", endpoint+".listener.pid")
	b, err := os.ReadFile(pidfile)
	if err != nil {
		return false
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) < 2 {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || pid <= 0 {
		return false
	}
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	return PidStart(pid) == lines[1]
}

// PidStart returns `ps -o lstart= -p <pid>` with leading whitespace stripped.
// It is exported because the presence model asks the operating system the same
// question about the pids it records, and one way of asking is one way of
// being wrong.
//
// SHELLED OUT ON PURPOSE. The stored line is the exact string BSD ps prints,
// and it is compared byte for byte against what the shell listener wrote.
// Computing a start time natively — from the kernel, in some other format —
// would produce a correct answer that never matches, so every send under
// say-semantics would be refused.
func PidStart(pid int) string {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimLeft(strings.TrimRight(string(out), "\n"), " \t")
}
