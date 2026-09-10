package loc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/tdebasis/locutorium/internal/config"
)

// Nudge rings an endpoint's doorbell. Advisory: its failure never loses a
// message, because the message is already in the queue by the time this runs.
func Nudge(endpoint, line string) { _ = NudgeErr(endpoint, line) }

// NudgeErr rings, and SAYS WHY IT COULD NOT.
//
// The hook is the one part of a wake that lives outside this binary, and so
// the part most likely to be absent or broken: an unconfigured deployment, a
// file without its execute bit, a script that exits non-zero. A wake lost that
// way looks exactly like a wake that was never due, which is why the reason is
// returned rather than dropped — a caller that watches a pane for a living has
// somewhere to put it, and Nudge stays for the callers that do not.
func NudgeErr(endpoint, line string) error {
	hook := filepath.Join(config.Home(), "hooks", "nudge")
	if !isExecutable(hook) {
		return fmt.Errorf("no executable nudge hook at %s", hook)
	}
	if err := exec.Command(hook, endpoint, line).Run(); err != nil {
		return fmt.Errorf("%s: %w", hook, err)
	}
	return nil
}

// isExecutable reports whether a path is a file this process may run.
func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode().Perm()&0o111 != 0
}

// mentionRe matches an @name, which under namespaced endpoints may carry one
// dotted segment: @workshop.scribe is one mention, not @workshop followed by
// punctuation.
//
// THE LEADING GROUP IS A LEFT WORD BOUNDARY. RE2 has no lookbehind, so the
// character before the '@' is matched and then dropped, and the dotted reading
// is offered only where the '@' begins a word. The second alternative is the
// bare form for everywhere else, which is what keeps an address reading as it
// always has: in "mail@example.com" the '@' sits inside a word, so the domain
// never becomes an endpoint. A sentence's trailing period closes a mention
// rather than opening a segment, because a segment needs a character after the
// dot.
var mentionRe = regexp.MustCompile(`(?:^|[^a-z0-9_-])@[a-z0-9_-]+(?:\.[a-z0-9_-]+)?|@[a-z0-9_-]+`)

// Mentions returns the @names in a body, without the '@', deduplicated and
// sorted.
func Mentions(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range mentionRe.FindAllString(body, -1) {
		// A match may carry the boundary character the pattern had to consume;
		// the name is what follows the '@'.
		name := m[strings.IndexByte(m, '@')+1:]
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
