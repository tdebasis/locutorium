package mcpserve

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// ---------------------------------------------------------------- herdr

// herdrNotifier types the bell into an agent that herdr runs.
//
// HERDR KNOWS THE AGENT'S STATE, SO THIS FILE DOES NOT GUESS IT. herdr tracks
// each agent it runs as idle, working, blocked (a permission or question
// dialog is open), done or unknown, for every agent kind it supports, and its
// maintainers keep that detection current. The tmux notifier has to read the
// pane to tell a prompt from a shell; this one asks herdr.
//
// THE ADDRESS IS "<herdr session>/<agent name>", for example
// "conclave/scribe". herdr's own pane ids name a pane inside one session, and
// the bell runs outside any session, so the session is part of the address.
type herdrNotifier struct{ log func(string) }

// herdrAddress is the session, a slash, and the agent name. Neither part may
// be empty or hold white space, and the name may not hold a slash.
var herdrAddress = regexp.MustCompile(`^([^/\s]+)/([^/\s]+)$`)

// validHerdrAddress reports whether an address has the herdr form. It is asked
// at `subscribe`, so a seat with an address its bell would refuse never reads
// as reachable.
func validHerdrAddress(address string) error {
	if !herdrAddress.MatchString(address) {
		return fmt.Errorf("a herdr address is '<session>/<agent name>', such as 'conclave/scribe'; got '%s'", address)
	}
	return nil
}

// herdrAgent is the part of `herdr agent get` this notifier reads.
type herdrAgent struct {
	Result struct {
		Agent struct {
			Kind   string `json:"agent"`
			Status string `json:"agent_status"`
		} `json:"agent"`
	} `json:"result"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Ring types one line into the agent and submits it.
//
// THE ORDER OF THE QUESTIONS IS THE GUARD. The state comes first: a dialog
// (blocked) is never typed into, because a keystroke there can answer it, and
// a working agent is never interrupted. Both end on their own, so both are
// marked ErrBusy and the bell asks again. Only an idle or done agent has its
// input box read, and only an empty box is typed into.
//
// THE BOX CHECK IS PER AGENT KIND, AND A KIND WITHOUT ONE IS REFUSED. herdr
// does not protect a half-typed draft: `herdr agent prompt` appends to it and
// submits both (measured 2026-10-13 in the deployment). How an empty box looks
// differs by agent, so a kind this file has no reader for is refused rather
// than typed into blind.
//
// WHAT THE GUARD DOES NOT ESTABLISH. A draft typed in the instant between the
// box read and the prompt is not seen.
func (n *herdrNotifier) Ring(endpoint, address, bell, _ string) error {
	m := herdrAddress.FindStringSubmatch(address)
	if m == nil {
		return n.refuse(endpoint, fmt.Sprintf("bad herdr address '%s'", address))
	}
	session, name := m[1], m[2]
	if strings.HasPrefix(bell, "-") {
		// herdr's argument parser takes a leading dash as an option and has no
		// "--" to stop it, measured on herdr 0.9.3.
		return n.refuse(endpoint, "the bell starts with '-', which herdr would read as an option")
	}

	out, err := exec.Command("herdr", "--session", session, "agent", "get", name).Output()
	var a herdrAgent
	if jerr := json.Unmarshal(out, &a); jerr != nil {
		if err != nil {
			return n.refuse(endpoint, fmt.Sprintf("cannot ask herdr about %s: %v", address, err))
		}
		return n.refuse(endpoint, fmt.Sprintf("cannot read herdr's answer about %s: %v", address, jerr))
	}
	if a.Error != nil {
		return n.refuse(endpoint, fmt.Sprintf("herdr: %s: %s", a.Error.Code, a.Error.Message))
	}
	if err != nil {
		return n.refuse(endpoint, fmt.Sprintf("cannot ask herdr about %s: %v", address, err))
	}

	switch status := a.Result.Agent.Status; status {
	case "working":
		return busyRefusal{n.refuse(endpoint, "agent working")}
	case "blocked":
		return busyRefusal{n.refuse(endpoint, "agent at a dialog")}
	case "unknown":
		// herdr reports unknown while it has not yet placed the agent, at
		// startup and between detections; it ends on its own, so ask again.
		return busyRefusal{n.refuse(endpoint, "agent state 'unknown'")}
	case "idle", "done":
	default:
		return n.refuse(endpoint, fmt.Sprintf("agent state '%s'", status))
	}

	read, ok := herdrBoxReaders[a.Result.Agent.Kind]
	if !ok {
		return n.refuse(endpoint, fmt.Sprintf("no draft check for agent kind '%s'", a.Result.Agent.Kind))
	}
	screen, err := output("herdr", "--session", session, "agent", "read", name, "--source", "visible", "--format", "ansi")
	if err != nil {
		return n.refuse(endpoint, fmt.Sprintf("cannot read %s's screen: %v", address, err))
	}
	switch read(screen) {
	case boxEmpty:
	case boxDraft:
		return busyRefusal{n.refuse(endpoint, "draft in the input box")}
	default:
		// FAILS CLOSED: a box that cannot be found is treated as holding text.
		return busyRefusal{n.refuse(endpoint, "input box not found")}
	}

	if err := exec.Command("herdr", "--session", session, "agent", "prompt", name, bell).Run(); err != nil {
		return n.fail(endpoint, fmt.Sprintf("herdr agent prompt: %v", err))
	}
	n.log(fmt.Sprintf("nudge %s rang '%s'", endpoint, bell))
	return nil
}

func (n *herdrNotifier) refuse(endpoint, reason string) error {
	n.log(fmt.Sprintf("nudge %s refused: %s", endpoint, reason))
	return fmt.Errorf("refused: %s", reason)
}

func (n *herdrNotifier) fail(endpoint, reason string) error {
	n.log(fmt.Sprintf("nudge %s BELL FAILED: %s", endpoint, reason))
	return errors.New(reason)
}

// boxReading is what a box reader makes of an agent's visible screen.
type boxReading int

const (
	boxNotFound boxReading = iota
	boxEmpty
	boxDraft
)

// herdrBoxReaders holds one reader per agent kind, keyed by the kind herdr
// reports. A kind is added here only after it has been tested on the real
// agent.
var herdrBoxReaders = map[string]func(screen string) boxReading{
	"claude": claudeBox,
}

// sgr matches one colour or style sequence.
var sgr = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// leadBlank matches the blanks and style sequences after the prompt mark.
var leadBlank = regexp.MustCompile(`^(?:\s|\x1b\[[0-9;]*m)*`)

// claudeBox reads Claude Code's input box from a visible screen with its
// styles kept. It is the deployment's `bin/seat` herdr_box_clear, ported.
//
// The box is the last line that starts with the prompt mark ❯. It is empty
// when nothing follows the mark, or when what follows is drawn faint (SGR 2),
// which is Claude's own suggestion and not the person's typing. Any text on
// the lines between the prompt line and the rule under it is a draft that
// wrapped, or started on its second line.
func claudeBox(screen string) boxReading {
	lines := strings.Split(screen, "\n")
	plain := func(s string) string { return sgr.ReplaceAllString(s, "") }
	at := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimLeft(plain(l), " \t"), "❯") {
			at = i
		}
	}
	if at < 0 {
		return boxNotFound
	}
	for _, l := range lines[at+1:] {
		p := strings.TrimLeft(plain(l), " \t")
		if strings.HasPrefix(p, "─") {
			break
		}
		if strings.TrimSpace(strings.ReplaceAll(p, " ", " ")) != "" {
			return boxDraft
		}
	}
	line := lines[at]
	rest := strings.ReplaceAll(line[strings.Index(line, "❯")+len("❯"):], " ", " ")
	lead := leadBlank.FindString(rest)
	if strings.TrimSpace(plain(rest[len(lead):])) == "" {
		return boxEmpty
	}
	if strings.Contains(lead, "\x1b[2m") {
		return boxEmpty
	}
	return boxDraft
}
