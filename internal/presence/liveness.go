package presence

import (
	"strings"
	"syscall"
	"time"

	"github.com/tdebasis/locutorium/internal/loc"
)

// Liveness is asked of the OPERATING SYSTEM, and of nothing else.
//
// Asking whether a process exists works identically whatever that process is,
// which makes it the one presence signal needing no per-agent knowledge. Every
// alternative — matching a process name, watching a file the agent writes,
// waiting for it to answer — requires knowing something about that kind of
// agent, and grows a special case for each new one.
//
// It is a SAME-MACHINE question. A process id means nothing anywhere else, so
// whatever checks one runs beside the process it is checking.

// lstartLayout is the ctime-like line `ps -o lstart=` prints, on BSD ps and on
// procps alike:
//
//	Sat Sep  6 15:49:58 2026
//
// The day is space-padded, which is what `_2` is for. There is no zone in it,
// so it is read as local time — which is what ps meant by it.
const lstartLayout = "Mon Jan _2 15:04:05 2006"

// StartedAt is when the operating system says a pid started, in the schema's
// stamp — or "" when it cannot be read, which is what a process that is
// already gone looks like.
func StartedAt(pid int) string { return normalizeStart(loc.PidStart(pid)) }

// normalizeStart converts one ps start-time line into the schema's stamp.
//
// The recorded value is a NORMALISED stamp rather than the raw ps line,
// because it goes into an event other machines read, and a BSD ctime string is
// not a timestamp anyone else can parse. Comparison stays exact all the same:
// the same conversion is applied to what ps says now, so the two are compared
// as the same kind of thing.
func normalizeStart(lstart string) string {
	lstart = strings.TrimSpace(lstart)
	if lstart == "" {
		return ""
	}
	t, err := time.ParseInLocation(lstartLayout, lstart, time.Local)
	if err != nil {
		return ""
	}
	return t.UTC().Format(TSLayout)
}

// Alive reports whether the recorded (pid, start time) PAIR is still running.
//
// The pair is the identity. A number alone would go on looking alive the
// moment the operating system handed it to something else, which is exactly
// the case a restart produces.
func Alive(pid int, started string) bool {
	if pid <= 0 || started == "" {
		return false
	}
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	return StartedAt(pid) == started
}

// Alive asks the same question about a registration. A registration whose
// start time was never readable — the process was already gone when it was
// registered — is never alive, and that is the honest answer rather than an
// error: it is exactly what a sweep exists to find.
func (r *Registration) Alive() bool { return Alive(r.Process.PID, r.Process.Started) }
