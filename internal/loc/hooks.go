package loc

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/tdebasis/locutorium/internal/config"
)

// Nudge rings an endpoint's doorbell. Advisory: its failure never loses a
// message, because the message is already in the queue by the time this runs.
func Nudge(endpoint, line string) {
	hook := filepath.Join(config.Home(), "hooks", "nudge")
	if !isExecutable(hook) {
		return
	}
	_ = exec.Command(hook, endpoint, line).Run()
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
