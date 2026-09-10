//go:build !windows

package main

import (
	"os"
	"syscall"
)

// signalStop asks one process to end. The daemon catches this signal, shuts
// the broker down and removes its pidfile.
func signalStop(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

// stopSignals are the endings the daemon listens for.
func stopSignals() []os.Signal { return []os.Signal{syscall.SIGTERM, syscall.SIGINT} }
