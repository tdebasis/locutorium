//go:build !windows

package main

import (
	"fmt"
	osexec "os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// ssPID reads the pid out of one `ss` line: users:(("nats-server",pid=812,fd=9)).
var ssPID = regexp.MustCompile(`pid=(\d+)`)

// pidHoldingPort asks the operating system which process listens on port.
//
// `loc stop --force` is the only caller. There is no monitoring port in V0, so
// the tool cannot ask the broker who it is; it asks the machine instead.
func pidHoldingPort(port int) (int, error) {
	if runtime.GOOS == "darwin" {
		out, err := osexec.Command("lsof", "-ti", fmt.Sprintf("tcp:%d", port), "-sTCP:LISTEN").Output()
		if err != nil {
			return 0, fmt.Errorf("no process found listening on port %d", port)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && pid > 0 {
				return pid, nil
			}
		}
		return 0, fmt.Errorf("no process found listening on port %d", port)
	}
	out, err := osexec.Command("ss", "-ltnp").Output()
	if err != nil {
		return 0, fmt.Errorf("no process found listening on port %d", port)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, fmt.Sprintf(":%d ", port)) {
			continue
		}
		if m := ssPID.FindStringSubmatch(line); m != nil {
			if pid, err := strconv.Atoi(m[1]); err == nil && pid > 0 {
				return pid, nil
			}
		}
	}
	return 0, fmt.Errorf("no process found listening on port %d", port)
}
