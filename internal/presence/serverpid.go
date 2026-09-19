package presence

import (
	"os"
	"path/filepath"

	"github.com/tdebasis/locutorium/internal/config"
)

// ServerPIDFile is where the process SERVING a seat writes its own pid.
//
// THE LEDGER'S PID AND THIS ONE NAME DIFFERENT PROCESSES. A registration
// carries the RUNTIME's pid, because the runtime is what a supervisor
// launched and what "who is at this seat" asks about. The MCP server runs one
// generation removed from that process (cmd/loc/mcp.go), so nothing in the
// registration names it. This file does. The server writes it when it takes
// the seat, and removes it when it gives the seat up.
//
// THE PATH IS SPELLED HERE AND NOWHERE ELSE. Two callers use it: the
// server's claim and the server's release. A third caller, the CLI's test
// for a seat that rings its own bell, was removed when the bell moved into
// the binary.
func ServerPIDFile(endpoint string) string {
	return filepath.Join(config.Home(), "run", endpoint+".mcp.pid")
}

// ReleaseServerPID removes the file. A file that is already gone is a SUCCESS,
// by the rule Remove applies to the ledger: the caller asked for the seat to
// name no server, and the seat names none.
func ReleaseServerPID(endpoint string) error {
	if err := os.Remove(ServerPIDFile(endpoint)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// DaemonPIDFile is where the Locutorium's own daemon writes its pid.
//
// IT IS NOT A SEAT'S FILE. ServerPIDFile names one process per endpoint; this
// names the single process that runs the broker and the heartbeat for the
// whole deployment, so it carries no endpoint in its name. The shape is the
// same — two lines, the pid and the start time — so the same liveness question
// can be asked of it.
func DaemonPIDFile() string {
	return filepath.Join(config.Home(), "run", "loc.pid")
}
