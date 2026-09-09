package presence

// An incident is the house reporting its own fault.
//
// It has TWO OUTPUTS, and they answer different questions. The event on
// presence.<instance> is for whoever is watching right now, so a display and
// `loc watch` see the fault as it happens. The line in run/incidents is for
// whoever looks later, because the event plane is not retained and a fault
// nobody was watching would otherwise leave no trace at all.
//
// This file builds and records. It does not decide when a fault has happened,
// and it does not publish: the caller that detected the fault owns both.

import (
	"os"
	"path/filepath"
	"time"

	"github.com/tdebasis/locutorium/internal/config"
)

// NewIncident builds the event for one fault.
//
// THE ENDPOINT MAY BE EMPTY. A medium the house cannot reach is a fault of the
// house, and it belongs to no agent. An unreadable ledger row does name one,
// so the field is there when the caller knows it and empty when it does not.
// The envelope keeps the field either way, because the schema fixes the same
// four fields for every kind.
//
// instance is passed rather than derived, for the same reason: an incident
// with no endpoint has no prefix to derive it from, and the instance is what
// says which house reported the fault.
func NewIncident(instance, endpoint, reason, detail string) *Event {
	ev := NewEvent(KindIncident, endpoint)
	ev.Instance = instance
	ev.Reason = reason
	ev.Detail = detail
	return &ev
}

// LogIncident appends one line to the day's incident record.
//
// The line is built in memory and handed to Write ONCE. Many processes append
// to this same file. Every host and every adapter on the deployment appends to
// it, on whatever day it is. A line written across two Write calls can have
// another writer's line land between the two halves, corrupting both records
// at once. One call is what makes append-only safe without a lock.
//
// Every error here is swallowed, for the reason the event plane is best
// effort: a house that cannot write down a fault must not turn that fault into
// a second one. A full disk loses the record, and the caller still runs.
//
// This is LogSent in internal/loc, written out again rather than called.
// LogSent writes the message envelope's fields, which are uid, status, from,
// to and body. An incident has none of them, so there is nothing to call. The
// import itself would be legal: this package already imports internal/loc, in
// liveness.go. The two functions must keep the same shape, because a reader
// asking how this deployment appends to a daily file should find one answer.
//
// The line is Event.Marshal, the same bytes the event plane carries, so a
// reader can grep the file for a line seen on the bus and find it verbatim.
func LogIncident(ev *Event) {
	dir := filepath.Join(config.Home(), "run", "incidents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, time.Now().UTC().Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()

	line, err := ev.Marshal()
	if err != nil {
		return
	}
	line = append(line, '\n')
	_, _ = f.Write(line)
}
