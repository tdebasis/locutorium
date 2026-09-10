package main

// `read --json` end-to-end against a real broker.
//
// A hook that wants to parse what arrived cannot parse markdown headings, so
// `--json` drops them and prints each envelope's raw wire bytes instead — one
// per line, JSON Lines, and otherwise the same consuming read. This pins the
// two things a parser depends on: exactly one line per message, and nothing
// else on standard out.
//
// The deployment and helpers are the presence suite's (newPresence, p.as,
// livePid, pidStr) and read_verbs_test.go's (p.post is not used here — the
// message goes through a real `send` so the envelope on the wire is the one
// `loc send` actually produces).

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/tdebasis/locutorium/internal/loc"
)

// A message sent through a live `send` and read back with `--json` comes out
// as exactly one line of JSON Lines: no headings, the body intact, an id, and
// the message is gone on the next read.
func TestReadJSONShowsOneLineAndConsumes(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	exec("subscribe", e1, "--pid", strconv.Itoa(livePid(t)), "--type", "tmux", "--version", "3.2.0")

	code, out, errOut := exec("send", e1, "json-canary")
	if code != 0 || errOut != "" {
		t.Fatalf("send: exit %d, stderr %q", code, errOut)
	}

	p.as(t, e1)
	code, out, errOut = exec("read", "--json")
	if code != 0 {
		t.Fatalf("read --json: exit %d, stderr %q", code, errOut)
	}

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("stdout is not exactly one line: %q", out)
	}

	var env loc.Envelope
	if err := json.Unmarshal([]byte(lines[0]), &env); err != nil {
		t.Fatalf("the one line does not unmarshal into an Envelope: %v; got %q", err, out)
	}
	if env.Body != "json-canary" {
		t.Errorf("body = %q, want %q", env.Body, "json-canary")
	}
	if env.ID == "" {
		t.Errorf("id is empty")
	}
	if strings.Contains(out, "──") {
		t.Errorf("--json must print no heading lines, got %q", out)
	}

	// The message was consumed: a second --json read is empty.
	code, again, errOut := exec("read", "--json")
	if code != 0 || errOut != "" {
		t.Fatalf("second read --json: exit %d, stderr %q", code, errOut)
	}
	if again != "" {
		t.Errorf("a message was handed over twice: %q", again)
	}
}

// --peek and --json together ask for nothing-consumed and everything-consumed
// at once; the strict parser refuses rather than picking one silently.
func TestReadRefusesPeekAndJSONTogether(t *testing.T) {
	newDeployment(t, "ada")
	code, out, errOut := exec("read", "--peek", "--json")
	assertResult(t, code, out, errOut, 1, usage, "")
}
