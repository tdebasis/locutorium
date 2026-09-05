// Package loc holds the Locutorium's semantics: who is speaking, what an
// envelope is, how one is shown to a reader, and what the deployment's hooks
// are called with. It knows nothing about what carries a message.
package loc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tdebasis/locutorium/internal/config"
)

// Identity resolves who is speaking: LOC_IDENTITY, then the deployment's
// identity hook.
//
// An unattributable caller is REFUSED. There is no 'unknown' and no anonymous
// sender: a message whose origin is a guess is worse than no message, because
// the reader cannot tell the difference.
func Identity() (string, error) {
	if id := os.Getenv("LOC_IDENTITY"); id != "" {
		return id, nil
	}
	home := config.Home()
	hook := filepath.Join(home, "hooks", "identity")
	if isExecutable(hook) {
		// The hook's failure is not an error here: it falls through to the
		// same refusal as no hook at all, which names both ways to fix it.
		out, err := exec.Command(hook).Output()
		if err == nil {
			// Command substitution strips trailing newlines; so does this.
			if id := strings.TrimRight(string(out), "\n"); id != "" {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf("cannot determine sender identity: set LOC_IDENTITY or provide an executable %s", hook)
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode().Perm()&0o111 != 0
}
