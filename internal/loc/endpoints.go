package loc

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tdebasis/locutorium/internal/config"
)

// Endpoints reads the deployment's registry: $LOC_HOME/endpoints, one name
// per line. The file is written by the deployment, never by this tool, so a
// missing one means "no endpoints", not an error.
func Endpoints() []string {
	b, err := os.ReadFile(filepath.Join(config.Home(), "endpoints"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		// The shell iterates this list unquoted, so blank lines fall out and
		// a name never contains whitespace. Same result, said out loud.
		for _, f := range strings.Fields(line) {
			out = append(out, f)
		}
	}
	return out
}

// EndpointExists reports whether name appears as an exact whole line in the
// registry — `grep -qxF`, not a substring match.
func EndpointExists(name string) bool {
	// An empty name is never an endpoint. `grep -xF ""` matches a BLANK LINE,
	// so a registry with a stray one would let `loc send "" ...` through and
	// address a subject with nothing after the dot. Said here so it does not
	// depend on how tidy the file is.
	if name == "" {
		return false
	}
	b, err := os.ReadFile(filepath.Join(config.Home(), "endpoints"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if line == name {
			return true
		}
	}
	return false
}
