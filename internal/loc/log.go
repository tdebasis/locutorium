package loc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/tdebasis/locutorium/internal/config"
)

// LogEvent appends one event to the day's message log.
//
// event is marshalled exactly as it stands, so THE CALLER'S STRUCT IS THE
// LINE. The field order a caller declares is the order on disk, and a caller
// leaves out a field that does not apply rather than sending an empty one.
// The file holds two families of line — message events keyed by `uid`, seat
// events keyed by `seat` — and a reader tells them apart by which key is
// present. An empty `uid` on a seat event would put every seat event in the
// message family, under a key that matches no message.
//
// The line is built in memory and handed to Write ONCE. Many processes append
// to this same file — every `loc send` and every `loc read` on the deployment,
// on whatever day it is — and a line written across two Write calls can have
// another writer's line land between the two halves, corrupting both records
// at once. One call is what makes append-only safe without a lock.
//
// at names the day file AND is the event's own clock, and the two must agree.
// A line stamped 00:00:01 that sits in the previous day's file is a record
// that whoever reads it has to know a rule to find. A caller that is handed a
// clock hands this one the same clock it stamped the line with.
//
// Every error here is swallowed: this log is an aid to whoever looks, not a
// dependency that send or read has, so a full disk or an unwritable directory
// must not turn a delivered message into a failed one, or a message that was
// shown into one that is handed over twice.
func LogEvent(at time.Time, event any) {
	dir := filepath.Join(config.Home(), "run", "log")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, at.UTC().Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()

	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	line = append(line, '\n')
	_, _ = f.Write(line)
}

// LogSent records a message that reached the medium.
//
// The day file comes from the machine's clock rather than from the envelope's
// own stamp, which is what this call has always done. The two are the same
// call to time.Now a few microseconds apart.
func LogSent(e Envelope) {
	LogEvent(time.Now().UTC(), struct {
		UID    string `json:"uid"`
		Status string `json:"status"`
		TS     string `json:"ts"`
		From   string `json:"from"`
		To     string `json:"to"`
		Body   string `json:"body"`
	}{
		UID:    e.ID,
		Status: "sent",
		TS:     e.TS,
		From:   e.From,
		To:     e.To,
		Body:   e.Body,
	})
}

// LogFailed records a message that send refused.
//
// IT CARRIES NO BODY, and that is the one place this line differs from the
// `sent` line beside it. A refused message never reached the medium, so the
// record is about the attempt rather than about the content; the sender still
// holds what it tried to say. Keeping the body here would put text on disk
// for every mistyped endpoint, which is the opposite of what entry 10 of
// docs/DECISIONS.md was willing to pay for.
//
// `from` is written even when it is empty, because the empty sender IS the
// reason on a `no identity` refusal and a missing key would hide it.
func LogFailed(e Envelope, reason string) {
	LogEvent(time.Now().UTC(), struct {
		UID    string `json:"uid"`
		Status string `json:"status"`
		TS     string `json:"ts"`
		From   string `json:"from"`
		To     string `json:"to"`
		Reason string `json:"reason"`
	}{
		UID:    e.ID,
		Status: "failed",
		TS:     e.TS,
		From:   e.From,
		To:     e.To,
		Reason: reason,
	})
}

// LogRead records that a reader took one message off its own queue.
//
// IT REPEATS NOTHING. Entry 10 of docs/DECISIONS.md had every line repeat
// `from`, `to` and `body` so that one line means something alone; this line
// cannot do that, because the reader holds the envelope and not the send. It
// carries the two facts the `sent` line does not have — when it was taken and
// by whom — and the `uid` that joins the pair.
//
// at is the clock the caller stamped the event with, so the line and the file
// it lands in name the same day.
func LogRead(uid, by string, at time.Time) {
	LogEvent(at, struct {
		UID    string `json:"uid"`
		Status string `json:"status"`
		TS     string `json:"ts"`
		By     string `json:"by"`
	}{
		UID:    uid,
		Status: "read",
		TS:     at.UTC().Format(tsLayout),
		By:     by,
	})
}

// LogBellFailed records a bell that the seat's notifier could not ring.
//
// It carries no `uid`. The server learns that mail arrived, not which message
// arrived: the watch hands it an arrival and the count comes from Unread, so
// a per-message bell failure is not a thing this code is in a position to
// report. Saying so here is cheaper than a reader inferring it from an absent
// key.
func LogBellFailed(seat, reason string, at time.Time) {
	logSeat(seat, "bell-failed", reason, at)
}

// LogQueueDeleted records a queue that some process destroyed, and why.
//
// The reason is one of `left`, `displaced`, `expired` or `orphan`. A queue
// going away is how mail stops arriving for a seat, so the word matters: an
// agent that left took its own queue, and an agent that was displaced or
// reaped did not.
func LogQueueDeleted(seat, reason string, at time.Time) {
	logSeat(seat, "queue-deleted", reason, at)
}

// logSeat writes the seat family's one shape. Both of its callers name a
// fixed status, so a status is never a string a call site can misspell.
func logSeat(seat, status, reason string, at time.Time) {
	LogEvent(at, struct {
		Seat   string `json:"seat"`
		Status string `json:"status"`
		TS     string `json:"ts"`
		Reason string `json:"reason"`
	}{
		Seat:   seat,
		Status: status,
		TS:     at.UTC().Format(tsLayout),
		Reason: reason,
	})
}
