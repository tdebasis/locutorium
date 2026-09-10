//go:build windows

package main

import (
	"fmt"
	osexec "os/exec"
	"strconv"
	"strings"
)

// pidHoldingPort asks the operating system which process listens on port.
//
// `netstat -ano` prints one row per socket and the pid is the last field:
//
//	TCP    127.0.0.1:4222    0.0.0.0:0    LISTENING    812
func pidHoldingPort(port int) (int, error) {
	out, err := osexec.Command("netstat", "-ano", "-p", "TCP").Output()
	if err != nil {
		return 0, fmt.Errorf("no process found listening on port %d", port)
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !strings.EqualFold(f[3], "LISTENING") {
			continue
		}
		if !strings.HasSuffix(f[1], fmt.Sprintf(":%d", port)) {
			continue
		}
		if pid, err := strconv.Atoi(f[4]); err == nil && pid > 0 {
			return pid, nil
		}
	}
	return 0, fmt.Errorf("no process found listening on port %d", port)
}
