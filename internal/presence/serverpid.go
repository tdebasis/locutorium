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
// THE PATH IS SPELLED HERE AND NOWHERE ELSE. It was written out three times —
// the server's claim, the server's release, and the CLI's test for a seat
// that rings its own bell. A path spelled in three places can be changed in
// two.
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
