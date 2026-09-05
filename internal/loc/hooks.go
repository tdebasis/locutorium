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

var mentionRe = regexp.MustCompile(`@[a-z0-9_-]+`)

// Mentions returns the @names in a body, without the '@', deduplicated and
// sorted — the same set, in the same order, as `grep -oE | sed | sort -u`.
func Mentions(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range mentionRe.FindAllString(body, -1) {
		name := strings.TrimPrefix(m, "@")
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
