package presence

import (
	"fmt"
	"strings"
	"time"
)

// Attendance is the answer to one question: does this endpoint hold a queue?
// It is the question the send path asks, and a seat whose queue is gone is a
// seat that cannot receive mail.
//
// IT HAS THREE STATES AND NOT TWO. "The broker says there is no queue" and
// "the broker could not be asked" are different facts, and a bool reports the
// second as the first. The zero value is the third state, so a caller that
// forgets to fill this in says it did not ask rather than reporting an absent
// queue.
type Attendance struct {
	asked  bool
	exists bool
	reason string
}

// Asked records what the medium answered.
func Asked(exists bool) Attendance {
	return Attendance{asked: true, exists: exists}
}

// NotAsked records that the question could not be put, and why. The reason
// belongs to the medium. It says nothing about the endpoint.
func NotAsked(reason string) Attendance {
	return Attendance{reason: reason}
}

// Derive renders the four facts an endpoint's status reports, EACH WITH THE
// REASON FOR IT.
//
// They are not collapsed into one word on purpose. Away because there is no
// process, idle because nothing has happened lately, and idle because the
// window elapsed with events missing are different situations, and a report
// that prints only the conclusion hides which one it is looking at.
//
// The attending fact is the only one that needs the medium, and the caller
// asks the medium and hands the answer in. A medium that cannot answer costs
// this report one line. It does not cost the report the other three.
func Derive(endpoint string, reg *Registration, att Attendance, act *Activity, now time.Time, window time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", endpoint)
	fmt.Fprintf(&b, "  registered: %s\n", registeredLine(reg))
	fmt.Fprintf(&b, "  attending:  %s\n", attendingLine(att))
	fmt.Fprintf(&b, "  process:    %s\n", processLine(reg))
	fmt.Fprintf(&b, "  activity:   %s\n", activityLine(reg, act, now, window))
	return b.String()
}

// attendingLine derives the reachable fact from the medium's answer.
//
// Each branch avoids the words the other branches use, for the reason
// activityLine gives below. A reader, or a check, that reads this report for
// `yes` must not find it in a line that means the opposite.
func attendingLine(att Attendance) string {
	switch {
	case !att.asked:
		reason := att.reason
		if reason == "" {
			reason = "the question was never put"
		}
		return fmt.Sprintf("unknown (could not ask: %s)", reason)
	case att.exists:
		return "yes (queue exists)"
	default:
		return "no (no queue: a send to this endpoint is refused)"
	}
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
