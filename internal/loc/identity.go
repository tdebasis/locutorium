// Package loc holds the Locutorium's semantics: who is speaking, what an
// envelope is, and how one is shown to a reader. It knows nothing about what
// carries a message.
package loc

import (
	"fmt"
	"os"
)

// Identity resolves who is speaking. It is LOC_IDENTITY and nothing else.
//
// An unattributable caller is REFUSED. There is no 'unknown' and no anonymous
// sender: a message whose origin is a guess is worse than no message, because
// the reader cannot tell the difference.
//
// R29 of 2026-09-09 removed the second answer. The deployment used to be able
// to supply an identity hook, and a hook is a shell script that does not cross
// to Windows and that CI runs nothing of. One variable is one thing to set and
// one thing to name in the refusal.
func Identity() (string, error) {
	if id := os.Getenv("LOC_IDENTITY"); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("cannot determine sender identity: set LOC_IDENTITY")
}
