package presence

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

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

// ReleaseServerPIDIfOwn removes the file only when it names the process given
// by pid and started, the pair the server wrote when it claimed the seat. It
// returns 0 when the file was removed or was already gone, and otherwise the
// pid the file names, which is left in place.
//
// A SERVER RELEASES ONLY ITS OWN CLAIM. A later server of the same seat writes
// its own pid over the earlier one's, and the earlier one, leaving, would
// otherwise remove the file that names its successor: a served seat would then
// name no server. The pair is compared rather than the pid, because a pid
// alone names a stranger once the kernel reuses it. A file that cannot be
// parsed names nobody this process can prove is itself, so it is left too, and
// reported as pid -1.
//
// The read and the remove are two steps. A server that claims the seat
// between them loses its file; the gap is one read of a two-line file.
func ReleaseServerPIDIfOwn(endpoint string, pid int, started string) (other int, err error) {
	b, err := os.ReadFile(ServerPIDFile(endpoint))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		return -1, nil
	}
	named, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || named <= 0 {
		return -1, nil
	}
	if named != pid || strings.TrimSpace(lines[1]) != started {
		return named, nil
	}
	return 0, ReleaseServerPID(endpoint)
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
