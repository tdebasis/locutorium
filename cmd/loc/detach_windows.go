//go:build windows

package main

import (
	osexec "os/exec"
	"syscall"
)

// detachedProcess is DETACHED_PROCESS from the Windows API. The child gets no
// console of its own and does not inherit ours. Go's syscall package names
// CREATE_NEW_PROCESS_GROUP and does not name this one, so the value is spelled
// here.
const detachedProcess = 0x00000008

// detach is the Windows form of the same decision: a new process group, and no
// console. A Ctrl-C in the console that started the daemon then does not reach
// it.
func detach(cmd *osexec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess,
	}
}
