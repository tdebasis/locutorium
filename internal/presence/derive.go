package presence

import (
	"fmt"
	"strings"
	"time"
)

// Derive renders the three facts an endpoint's status reports, EACH WITH THE
// REASON FOR IT.
//
// They are not collapsed into one word on purpose. Away because there is no
// process, idle because nothing has happened lately, and idle because the
// window elapsed with events missing are different situations, and a report
// that prints only the conclusion hides which one it is looking at.
func Derive(endpoint string, reg *Registration, act *Activity, now time.Time, window time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", endpoint)
	fmt.Fprintf(&b, "  registered: %s\n", registeredLine(reg))
	fmt.Fprintf(&b, "  process:    %s\n", processLine(reg))
	fmt.Fprintf(&b, "  activity:   %s\n", activityLine(reg, act, now, window))
	return b.String()
}

func registeredLine(reg *Registration) string {
	if reg == nil {
		return "no"
	}
	return fmt.Sprintf("yes (since %s; %s %s)", reg.Registered, reg.Agent.Type, reg.Agent.Version)
}

func processLine(reg *Registration) string {
	if reg == nil {
		// Nothing recorded a pid, so there is no process question to answer —
		// which is different from having asked and found nothing.
		return "unknown (no registration)"
	}
	if reg.Alive() {
		return fmt.Sprintf("alive (pid %d, started %s)", reg.Process.PID, startedOrUnknown(reg))
	}
	return fmt.Sprintf("gone (pid %d, started %s)", reg.Process.PID, startedOrUnknown(reg))
}

func startedOrUnknown(reg *Registration) string {
	if reg.Process.Started == "" {
		return "unknown"
	}
	return reg.Process.Started
}

// activityLine derives the working fact from the last event applied.
//
// The wording of every idle branch avoids the word the working state uses, so
// that a reader — or a check — reading for it cannot find it in a line that
// means the opposite.
func activityLine(reg *Registration, act *Activity, now time.Time, window time.Duration) string {
	switch {
	case reg == nil:
		return "unknown (no registration)"
	case !reg.Alive():
		// Away is a process question and only a process question.
		return "away (process gone)"
	case act == nil || act.Kind == "":
		// An agent waiting for instructions emits nothing. That is its resting
		// state, not a fault.
		return "idle (no activity events)"
	case act.Kind == KindActivityEnd:
		// The end event is an optimisation: when it arrives the transition is
		// immediate, and when it is lost the window below covers it.
		return fmt.Sprintf("idle (%s at %s)", act.Kind, act.TS)
	}
	ts, err := time.Parse(time.RFC3339, act.TS)
	if err != nil {
		return fmt.Sprintf("idle (unreadable timestamp %q on the last %s)", act.TS, act.Kind)
	}
	if now.Sub(ts) <= window {
		return fmt.Sprintf("active (%s at %s)", act.Kind, act.TS)
	}
	return fmt.Sprintf("idle (idle window elapsed since %s)", act.TS)
}
