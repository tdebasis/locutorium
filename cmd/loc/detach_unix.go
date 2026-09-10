//go:build !windows

package main

import (
	osexec "os/exec"
	"syscall"
)

// detach puts the daemon in a session of its own.
//
// The house's broker must outlive the terminal that started it. A new session
// gives the child no controlling terminal, so a hangup on that terminal does
// not reach it.
func detach(cmd *osexec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
