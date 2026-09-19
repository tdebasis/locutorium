package main

import (
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"time"

	"github.com/tdebasis/locutorium/internal/config"
)

// forceFlag lets `loc stop` end a broker it did not start.
const forceFlag = "--force"

// startVerb boots the deployment, or says why it did not have to.
//
// THE DAEMON IS A SECOND PROCESS AND THIS ONE EXITS. A person runs `loc start`
// from a terminal and closes it; the broker and the heartbeat have to survive
// that, so the work is re-executed detached and this process only reports what
// happened.
func startVerb(w io.Writer, args []string) error {
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == serveFlag:
		return serveDaemon(w)
	default:
		return errUsage
	}

	if err := config.RequireFile(); err != nil {
		return err
	}
	if err := surfaceConfig(w); err != nil {
		return err
	}
	host, port, err := listenAddr()
	if err != nil {
		return err
	}
	addr := dialAddr(host, port)

	// SOMETHING ANSWERS. Whether it is ours is the pidfile's answer, and the
	// two cases are not the same fact: one says the house is up, the other
	// says another supervisor owns this port and loc will not touch it.
	if portAnswers(addr) {
		if pid, alive := liveDaemon(); alive {
			fmt.Fprintf(w, "already running, pid %d\n", pid)
			return nil
		}
		fmt.Fprintf(w, "port %d is held by a process loc did not start\n", port)
		return nil
	}

	cmd, err := daemonCommand()
	if err != nil {
		return err
	}
	detach(cmd)
	log, err := daemonLog()
	if err != nil {
		return err
	}
	defer log.Close()
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot start the daemon: %v", err)
	}
	// The child is nobody's to wait for. It outlives this process, and the
	// operating system adopts it.
	child := cmd.Process.Pid
	_ = cmd.Process.Release()

	deadline := time.Now().Add(readyWait)
	for time.Now().Before(deadline) {
		if portAnswers(addr) {
			pid := child
			if p, alive := liveDaemon(); alive {
				pid = p
			}
			fmt.Fprintf(w, "started, pid %d\n", pid)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("the daemon did not answer on %s within %s; see %s",
		addr, readyWait, daemonLogPath())
}

// surfaceConfig writes the file on a first run, and names it on every later
// one.
//
// THE FIRST BOOT PRINTS THE VALUES; NO OTHER BOOT DOES. A person who has never
// seen this deployment needs the settings in front of them once. A person who
// starts it every day needs the path and nothing else.
func surfaceConfig(w io.Writer) error {
	path := config.File()
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(w, "config: %s — edit it to change settings\n", path)
		return nil
	}
	if err := config.WriteDefault(path); err != nil {
		return err
	}
	fmt.Fprintf(w, "wrote %s with these defaults:\n", path)
	for _, k := range config.Keys {
		fmt.Fprintf(w, "  %s = %s\n", k.Name, k.Default)
	}
	return nil
}

// daemonCommand builds the re-execution: this same binary, told to serve.
//
// A SEAM, for the reason selfCommand is one (cmd/loc/mcp.go): os.Executable
// inside a test binary names the test binary, and a test binary re-executing
// itself is a fork bomb rather than a daemon.
var daemonCommand = func() (*osexec.Cmd, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return osexec.Command(self, "start", serveFlag), nil
}

// daemonLogPath is where the detached daemon's own output goes.
func daemonLogPath() string { return filepath.Join(config.Home(), "run", "loc.log") }

// daemonLog opens that file.
//
// THE DAEMON HAS NO TERMINAL. Its refusals, and the lines its sweep prints
// when it reaps a seat, would otherwise go to a closed descriptor and be lost
// at the one moment somebody needs them.
func daemonLog() (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(daemonLogPath()), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(daemonLogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
}
