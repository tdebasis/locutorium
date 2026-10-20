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
// "work/scribe". herdr's own pane ids name a pane inside one session, and
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
		return fmt.Errorf("invalid herdr address '%s': it must be <session>/<agent name>, such as 'work/scribe'", address)
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
// marked ErrBusy and the bell asks again. An idle or done agent is typed into.
//
// IT READS NO SCREEN, FOR ANY AGENT KIND. herdr reports the state for every
// kind it runs, so the bell asks that and nothing else. Reading an agent's
// screen to guess whether its box holds a draft was dropped as brittle: it
// would come back only if herdr itself could report the box's contents.
//
// WHAT THE GUARD DOES NOT ESTABLISH. herdr does not protect a half-typed
// draft: `herdr agent prompt` appends to it and submits both (measured
// 2026-10-13 in the deployment), so a bell can be sent with a draft. herdr can
// also report an agent idle while it shows a menu (measured on Codex,
// 2026-10-19), so a bell can land in the menu.
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

	status := a.Result.Agent.Status
	switch status {
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

	if err := exec.Command("herdr", "--session", session, "agent", "prompt", name, bell).Run(); err != nil {
		return n.fail(endpoint, fmt.Sprintf("herdr agent prompt: %v", err))
	}
	// The line names the state herdr reported, so a ring at the edge of a
	// turn can be read afterwards as herdr's answer and not a guess.
	n.log(fmt.Sprintf("nudge %s rang '%s' (herdr state: %s)", endpoint, bell, status))
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
