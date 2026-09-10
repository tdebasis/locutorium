package main

import (
	"fmt"
	"io"
	"os"
	"time"
)

// stopVerb ends the daemon this deployment started.
//
// IT REFUSES A BROKER IT DID NOT START. A port answering on 4222 is not proof
// that loc owns what is behind it; another supervisor may run the same broker
// for something else, and a stop is not recoverable. The refusal names the way
// past itself, so a person who does own it is not stuck.
func stopVerb(w io.Writer, args []string) error {
	force := false
	for _, a := range args {
		if a != forceFlag {
			return errUsage
		}
		force = true
	}

	host, port, err := listenAddr()
	if err != nil {
		return err
	}
	addr := dialAddr(host, port)

	if pid, alive := liveDaemon(); alive {
		return stopPID(w, pid, addr)
	}
	if !portAnswers(addr) {
		fmt.Fprintln(w, "not running")
		return nil
	}
	if !force {
		fmt.Fprintf(w, "port %d is held by a process loc did not start\n", port)
		fmt.Fprintln(w, "stopping it could stop a broker another supervisor owns")
		fmt.Fprintf(w, "to stop it anyway: loc stop %s\n", forceFlag)
		return exitStatus(1)
	}
	pid, err := pidHoldingPort(port)
	if err != nil {
		fmt.Fprintf(w, "%v\n", err)
		return exitStatus(1)
	}
	return stopPID(w, pid, addr)
}

// stopPID signals one process and waits for the port to close.
//
// THE PORT IS THE ANSWER, NOT THE SIGNAL. A signal that was accepted says the
// process was told; it does not say the broker let go of the socket, and the
// next `loc start` fails on exactly that gap.
func stopPID(w io.Writer, pid int, addr string) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("cannot reach process %d: %v", pid, err)
	}
	if err := signalStop(p); err != nil {
		return fmt.Errorf("cannot stop process %d: %v", pid, err)
	}
	deadline := time.Now().Add(readyWait)
	for time.Now().Before(deadline) {
		if !portAnswers(addr) {
			// The daemon removes its own pidfile. One that was killed did not,
			// and a stale file would make the next start read a dead pid.
			if err := releaseDaemonPID(); err != nil {
				return err
			}
			fmt.Fprintln(w, "stopped")
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("process %d was signalled but %s still answers after %s", pid, addr, readyWait)
}
