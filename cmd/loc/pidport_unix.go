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
	out, err := portLookup(port)
	if err == nil {
		if pid, ok := pidFromLookup(runtime.GOOS, out, port); ok {
			return pid, nil
		}
	}
	return 0, fmt.Errorf("no process found listening on port %d", port)
}

// portLookup runs the tool this platform answers the question with.
func portLookup(port int) (string, error) {
	if runtime.GOOS == "darwin" {
		out, err := osexec.Command("lsof", "-ti", fmt.Sprintf("tcp:%d", port), "-sTCP:LISTEN").Output()
		return string(out), err
	}
	out, err := osexec.Command("ss", "-ltnp").Output()
	return string(out), err
}

// pidFromLookup reads that tool's output.
//
// THE PARSING IS APART FROM THE RUNNING, so both platforms' formats are read
// by a test on either platform. A parser that only one machine can exercise is
// a parser that breaks on the other one.
func pidFromLookup(goos, out string, port int) (int, bool) {
	if goos == "darwin" {
		// lsof -t prints one pid per line and nothing else.
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && pid > 0 {
				return pid, true
			}
		}
		return 0, false
	}
	// ss prints one row per socket: the local address, then the process.
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, fmt.Sprintf(":%d ", port)) {
			continue
		}
		if m := ssPID.FindStringSubmatch(line); m != nil {
			if pid, err := strconv.Atoi(m[1]); err == nil && pid > 0 {
				return pid, true
			}
		}
	}
	return 0, false
}
