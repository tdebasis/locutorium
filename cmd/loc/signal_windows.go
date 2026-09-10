//go:build windows

package main

import (
	"os"
)

// signalStop asks one process to end.
//
// WINDOWS HAS NO SIGTERM. os.Process.Signal carries only Kill to another
// process here, so the graceful ending is attempted first and its refusal is
// expected. A killed daemon leaves its pidfile behind; `loc stop` removes the
// file itself for that reason.
func signalStop(p *os.Process) error {
	if err := p.Signal(os.Interrupt); err == nil {
		return nil
	}
	return p.Kill()
}

// stopSignals are the endings the daemon listens for. os.Interrupt is what a
// console Ctrl-C delivers on Windows.
func stopSignals() []os.Signal { return []os.Signal{os.Interrupt} }
