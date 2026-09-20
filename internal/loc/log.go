package loc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
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
// The line is built in memory and handed to Write ONCE. (One Go call, not
// one syscall: os.File.Write retries a short write, and a short write on a
// local regular file needs ENOSPC or EINTR — Assayer N1. Precision, not a
// live defect.) Many processes append
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
	dir := LogDir()
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
// THE DAY FILE COMES FROM THE ENVELOPE'S OWN STAMP, not from the clock at the
// moment of writing. The first cut of this call took the file day from
// time.Now and the line's ts from the envelope, and called the two "a few
// microseconds apart". They are not: send() runs the attendance check and the
// publish between stamping the envelope and reaching this line, and across
// midnight that put a line stamped yesterday into today's file (Assayer, F1,
// 2026-09-16). The line and its file now name the same day, as LogRead and
// logSeat already did.
func LogSent(e Envelope) {
	LogEvent(stampedAt(e), struct {
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
	LogEvent(stampedAt(e), struct {
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

// LogBellTry records ONE TRY of a seat's bell, with what came of it.
//
// EVERY TRY IS WRITTEN DOWN, and the result never changes the counting. `try`
// and `of` say where this try sits in the streak; `result` is one of `rang`,
// `refused`, `suppressed` and `failed`. A reader that wants to know whether a
// bell was rung again after one that did not get through reads it here: before
// this, a ring that worked left no line at all, so "it was tried again and
// worked" and "nobody tried and the seat looked by itself" were the same
// record (#144).
//
// `reason` is left out for `rang`, which has none. It is the notifier's own
// text for the other three, because the repair differs by what it says.
//
// It carries no `uid`. The server learns that mail arrived, not which message
// arrived: the watch hands it an arrival and the count comes from Unread, so
// a per-message bell event is not a thing this code is in a position to
// report. Saying so here is cheaper than a reader inferring it from an absent
// key.
//
// THE OLD LINE IS GONE. `bell-failed`, written once per busy streak, is what
// this replaces. No build writes one any more. Old day files hold them, so
// every reader still parses one.
func LogBellTry(seat string, try, of int, result, reason string, at time.Time) {
	LogEvent(at, struct {
		Seat   string `json:"seat"`
		Status string `json:"status"`
		TS     string `json:"ts"`
		Try    int    `json:"try"`
		Of     int    `json:"of"`
		Result string `json:"result"`
		Reason string `json:"reason,omitempty"`
	}{
		Seat:   seat,
		Status: "bell-try",
		TS:     at.UTC().Format(tsLayout),
		Try:    try,
		Of:     of,
		Result: result,
		Reason: reason,
	})
}

// LogBellGaveUp records the end of a streak that used all its tries.
//
// It is written after the last try's own line, so the record holds the tries
// and then the stopping. `after` is how many tries there were, which is the
// same number the last try's `of` carries; a reader that has only this line
// still knows.
//
// IT SAYS WHETHER THE SEAT WAS EVER TOLD. `rang` is how many of the streak's
// tries reached the pane, from 0 to `after`. `last` is the final try's result.
// A bell that rang twice and was not answered is a different fact from a bell
// that never rang, and this line said neither before: every stopped streak
// read as a bell that never got through.
//
// `rang` is written even when it is 0. That 0 is the whole of the second fact,
// and an absent key would read as a line from before these two existed.
func LogBellGaveUp(seat string, after, rang int, last string, at time.Time) {
	LogEvent(at, struct {
		Seat   string `json:"seat"`
		Status string `json:"status"`
		TS     string `json:"ts"`
		After  int    `json:"after"`
		Rang   int    `json:"rang"`
		Last   string `json:"last"`
	}{
		Seat:   seat,
		Status: "bell-gave-up",
		TS:     at.UTC().Format(tsLayout),
		After:  after,
		Rang:   rang,
		Last:   last,
	})
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

// logSeat writes the seat family's plainest shape. Its caller names a fixed
// status, so a status is never a string a call site can misspell.
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

// stampedAt is the envelope's own stamp as a time, and the day its line
// belongs in. An envelope that carries an unreadable stamp still gets a line:
// this log loses no record over a malformed field, so the fallback is the
// clock, which is what the caller would have used before F1.
func stampedAt(e Envelope) time.Time {
	if at, err := time.Parse(tsLayout, e.TS); err == nil {
		return at.UTC()
	}
	return time.Now().UTC()
}
