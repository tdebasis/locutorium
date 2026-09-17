package main

// The transcript, driven against a seeded record.
//
// EVERY CASE SEEDS THE FILES AND NOT THE VERBS. internal/loc pins what a
// writer puts on disk and cmd/loc/log_events_test.go pins that the verbs call
// it; what is left, and what these cases are for, is the READER. Seeding the
// lines directly is the only way to arrange a queue deleted after a send, a
// read that lands in the next day's file, and a line that is not JSON — three
// states no sequence of verbs produces on demand.
//
// The fixtures are written in the writer's own shape, field for field
// (internal/loc/log.go). A line here that drifted from the writer would make
// these cases agree with each other and with nothing else.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ------------------------------------------------------------------ fixtures

// scratchRecord is a deployment with a record and nothing else. transcript
// opens no medium, so there is no provider, no config and no identity to set.
func scratchRecord(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	return home
}

// seedDay writes one day's file. The lines land in the order they are given,
// which is the order the reader must treat as the order they happened.
func seedDay(t *testing.T, home, day string, lines ...string) {
	t.Helper()
	writeFile(t, filepath.Join(home, "run", "log", day+".jsonl"),
		strings.Join(lines, "\n")+"\n")
}

func sentLine(uid, ts, from, to, body string) string {
	return `{"uid":"` + uid + `","status":"sent","ts":"` + ts +
		`","from":"` + from + `","to":"` + to + `","body":"` + body + `"}`
}

func readEventLine(uid, ts, by string) string {
	return `{"uid":"` + uid + `","status":"read","ts":"` + ts + `","by":"` + by + `"}`
}

func failedLine(uid, ts, from, to, reason string) string {
	return `{"uid":"` + uid + `","status":"failed","ts":"` + ts +
		`","from":"` + from + `","to":"` + to + `","reason":"` + reason + `"}`
}

func seatLine(seat, status, ts, reason string) string {
	return `{"seat":"` + seat + `","status":"` + status + `","ts":"` + ts +
		`","reason":"` + reason + `"}`
}

// transcribe runs the verb and insists it succeeded. A case that asserts on
// the output of a failed run is asserting on an empty string.
func transcribe(t *testing.T, args ...string) (stdout, stderr string) {
	t.Helper()
	code, out, errOut := exec(append([]string{"transcript"}, args...)...)
	if code != 0 {
		t.Fatalf("transcript %v exit %d: %s", args, code, errOut)
	}
	return out, errOut
}

// ------------------------------------------------------------------ outcomes

// EVERY OUTCOME WORD, one arm each. The word is the whole product of this
// verb: nothing on disk says what became of a message, so each of these is
// derived, and a wrong derivation is a false report about mail.
func TestTranscript_EveryOutcomeWord(t *testing.T) {
	const (
		from = "workshop.scribe"
		to   = "workshop.clerk"
		sent = "2026-09-16T14:12:22Z"
	)
	cases := []struct {
		name  string
		after []string
		want  string
	}{
		{
			"read",
			[]string{readEventLine("u1", "2026-09-16T14:12:40Z", to)},
			"read 14:12:40Z by workshop.clerk",
		},
		{
			"pending",
			nil,
			"pending",
		},
		{
			// The bell is the seat's, not the message's: it joins on the
			// recipient's name and on being later than the send.
			"pending with a failed bell",
			[]string{seatLine(to, "bell-failed", "2026-09-16T14:12:30Z", "exit status 3")},
			"pending, bell failed 14:12:30Z: exit status 3",
		},
		{
			"lost to a deleted queue",
			[]string{seatLine(to, "queue-deleted", "2026-09-16T14:20:00Z", "orphan")},
			"lost: queue deleted 14:20:00Z (orphan)",
		},
		{
			// BOTH APPLY AND THE LATER ONE WINS. The queue went after the
			// bell failed, so the message is lost rather than unannounced.
			"both, the later one winning",
			[]string{
				seatLine(to, "bell-failed", "2026-09-16T14:12:30Z", "exit status 3"),
				seatLine(to, "queue-deleted", "2026-09-16T14:20:00Z", "expired"),
			},
			"lost: queue deleted 14:20:00Z (expired)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := scratchRecord(t)
			seedDay(t, home, "2026-09-16",
				append([]string{sentLine("u1", sent, from, to, "the ledger is ready")}, c.after...)...)

			out, errOut := transcribe(t)

			// THE FIRE CONTROL. Every assertion below is a Contains, and a
			// run that found no message at all satisfies none of them for the
			// right reason. The body proves the fixture reached the reader.
			if !strings.Contains(out, "the ledger is ready") {
				t.Fatalf("the message is not in the transcript at all, so this case proves nothing: %q", out)
			}
			if errOut != "" {
				t.Errorf("a clean record wrote %q to stderr", errOut)
			}
			want := sent + " " + from + " → " + to + "  " + c.want + "\n"
			if !strings.Contains(out, want) {
				t.Errorf("transcript:\n%s\nwant a line reading\n%s", out, want)
			}
		})
	}
}

// A MESSAGE WHOSE ONLY EVENT IS `failed` reads as the refusal and carries no
// body. The writer keeps the body off a refused line on purpose (entry 10 of
// docs/DECISIONS.md), so the reader has none to show, and a block that
// printed an empty indented line would look like a message sent blank.
func TestTranscript_AFailedSendReadsAsItsReason(t *testing.T) {
	home := scratchRecord(t)
	seedDay(t, home, "2026-09-16",
		failedLine("u1", "2026-09-16T14:12:22Z", "workshop.scribe", "nobody", "target not registered"))

	out, _ := transcribe(t)
	if !strings.Contains(out, "u1") && !strings.Contains(out, "nobody") {
		t.Fatalf("the refusal is not in the transcript at all: %q", out)
	}
	want := "2026-09-16T14:12:22Z workshop.scribe → nobody  failed: target not registered\n"
	if out != want {
		t.Errorf("transcript = %q, want exactly %q", out, want)
	}
}

// ------------------------------------------------------------------- day roll

// THE DAY ROLL. A message sent before midnight and read after it has its two
// lines in two files, and `date` names the day it was SENT. The read is still
// found, because every file is read whatever day is asked for.
//
// The read's stamp is printed WHOLE here. `read 00:00:01Z` under a message
// sent at 23:59:58 would read as a read that came before the send.
func TestTranscript_ADateShowsAReadFromTheNextDaysFile(t *testing.T) {
	home := scratchRecord(t)
	seedDay(t, home, "2026-09-16",
		sentLine("u1", "2026-09-16T23:59:58Z", "workshop.scribe", "workshop.clerk", "last word"))
	seedDay(t, home, "2026-09-17",
		readEventLine("u1", "2026-09-17T00:00:01Z", "workshop.clerk"))

	out, _ := transcribe(t, "2026-09-16")
	if !strings.Contains(out, "last word") {
		t.Fatalf("the message sent on the 16th is absent under date 2026-09-16: %q", out)
	}
	want := "read 2026-09-17T00:00:01Z by workshop.clerk"
	if !strings.Contains(out, want) {
		t.Errorf("transcript:\n%s\nwant the outcome %q, read out of the next day's file", out, want)
	}

	// THE PAIRED CONTROL. `date` filters on the day the message was SENT, not
	// on the day of its events. Asked for the 17th, the same message must be
	// absent — otherwise the assertion above would hold for a verb that
	// ignored the date altogether.
	later, _ := transcribe(t, "2026-09-17")
	if strings.Contains(later, "last word") {
		t.Errorf("date 2026-09-17 showed a message sent on the 16th:\n%s", later)
	}
	if !strings.Contains(later, "no messages in the record") {
		t.Errorf("a day with no sends printed %q", later)
	}
}

// --------------------------------------------------------------------- --uid

// `--uid` narrows to one message AND SHOWS ITS EVENTS, which is what makes it
// different from a filter. The block says what was derived; the events say
// what it was derived from, so a person can disagree with the derivation.
func TestTranscript_UIDShowsOneMessageAndItsEvents(t *testing.T) {
	home := scratchRecord(t)
	seedDay(t, home, "2026-09-16",
		sentLine("u1", "2026-09-16T14:00:00Z", "workshop.scribe", "workshop.clerk", "wanted"),
		sentLine("u2", "2026-09-16T14:01:00Z", "workshop.scribe", "workshop.clerk", "not wanted"),
		readEventLine("u1", "2026-09-16T14:02:00Z", "workshop.clerk"))

	all, _ := transcribe(t)
	if !strings.Contains(all, "not wanted") {
		t.Fatalf("the fixture is wrong: the second message is absent before any filter: %q", all)
	}

	out, _ := transcribe(t, "--uid", "u1")
	if !strings.Contains(out, "wanted") {
		t.Fatalf("--uid u1 showed nothing: %q", out)
	}
	if strings.Contains(out, "not wanted") {
		t.Errorf("--uid u1 showed the other message:\n%s", out)
	}
	if !strings.Contains(out, "  events:\n") {
		t.Errorf("--uid printed no events section:\n%s", out)
	}
	for _, want := range []string{`"status":"sent"`, `"status":"read"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the events section does not carry the %s line:\n%s", want, out)
		}
	}
}

// -------------------------------------------------------------------- --seat

// `--seat` is the seat's whole day: the messages it sent or was sent, and its
// own events. A SEAT EVENT THAT BELONGS TO NO MESSAGE IS ITS OWN LINE, in
// time order among the blocks — a bell that failed with nothing waiting is
// still something that happened to that seat.
//
// The date is given AFTER the flag, which is where the usage writes it, so
// this case also pins that `workshop.clerk` is read as the flag's value and
// not as the bare word.
func TestTranscript_SeatShowsItsMessagesAndItsOwnEvents(t *testing.T) {
	home := scratchRecord(t)
	seedDay(t, home, "2026-09-16",
		sentLine("u1", "2026-09-16T14:00:00Z", "workshop.scribe", "workshop.clerk", "to the clerk"),
		readEventLine("u1", "2026-09-16T14:00:05Z", "workshop.clerk"),
		// Later than every message to this seat, so it joins to none of them.
		seatLine("workshop.clerk", "bell-failed", "2026-09-16T15:00:00Z", "no pane"),
		sentLine("u2", "2026-09-16T16:00:00Z", "workshop.scribe", "workshop.other", "not the clerk"))

	out, _ := transcribe(t, "--seat", "workshop.clerk", "2026-09-16")
	if !strings.Contains(out, "to the clerk") {
		t.Fatalf("--seat showed none of the seat's messages: %q", out)
	}
	if strings.Contains(out, "not the clerk") {
		t.Errorf("--seat showed a message that is neither from nor to it:\n%s", out)
	}
	want := "2026-09-16T15:00:00Z workshop.clerk  bell failed: no pane\n"
	if !strings.Contains(out, want) {
		t.Errorf("transcript:\n%s\nwant the unattached seat event as its own line:\n%s", out, want)
	}
	// IN TIME ORDER AMONG THE BLOCKS. The seat line is stamped after the
	// message block and must print after it.
	if strings.Index(out, want) < strings.Index(out, "to the clerk") {
		t.Errorf("the seat line printed before the earlier message:\n%s", out)
	}
}

// A SEAT EVENT THAT EXPLAINS A MESSAGE IS NOT ALSO A LINE OF ITS OWN. It is
// already in that message's outcome, and printing it twice would read as two
// failures.
func TestTranscript_AnAttachedSeatEventIsNotRepeated(t *testing.T) {
	home := scratchRecord(t)
	seedDay(t, home, "2026-09-16",
		sentLine("u1", "2026-09-16T14:00:00Z", "workshop.scribe", "workshop.clerk", "waiting"),
		seatLine("workshop.clerk", "bell-failed", "2026-09-16T14:00:05Z", "no pane"))

	out, _ := transcribe(t, "--seat", "workshop.clerk")
	if !strings.Contains(out, "pending, bell failed 14:00:05Z: no pane") {
		t.Fatalf("the seat event did not reach the outcome, so this case proves nothing: %q", out)
	}
	if strings.Contains(out, "workshop.clerk  bell failed") {
		t.Errorf("the seat event printed again as its own line:\n%s", out)
	}
}

// ----------------------------------------------------------------- --pending

// `--pending` is the list of mail nobody has taken, and A TOPIC MESSAGE IS
// NOT ON IT. A room message is read from each attender's own cursor and gets
// no `read` line at all, so it would sit in this list for ever and the list
// would stop being read.
func TestTranscript_PendingExcludesATopicMessage(t *testing.T) {
	home := scratchRecord(t)
	seedDay(t, home, "2026-09-16",
		sentLine("u1", "2026-09-16T14:00:00Z", "workshop.scribe", "workshop.clerk", "still owed"),
		sentLine("u2", "2026-09-16T14:01:00Z", "workshop.scribe", "#standup", "a room word"),
		sentLine("u3", "2026-09-16T14:02:00Z", "workshop.scribe", "workshop.clerk", "collected"),
		readEventLine("u3", "2026-09-16T14:03:00Z", "workshop.clerk"))

	all, _ := transcribe(t)
	if !strings.Contains(all, "a room word") {
		t.Fatalf("the fixture is wrong: the topic message is absent before the filter: %q", all)
	}

	out, _ := transcribe(t, "--pending")
	if !strings.Contains(out, "still owed") {
		t.Fatalf("--pending showed no pending message: %q", out)
	}
	if strings.Contains(out, "a room word") {
		t.Errorf("--pending showed a topic message:\n%s", out)
	}
	if strings.Contains(out, "collected") {
		t.Errorf("--pending showed a message that was read:\n%s", out)
	}
}

// -------------------------------------------------------------------- --json

// `--json` PARSES, and each object carries its events in log order. The
// events are the raw lines, so a key this reader does not know still reaches
// whoever asked for the JSON.
func TestTranscript_JSONCarriesTheEventsInOrder(t *testing.T) {
	home := scratchRecord(t)
	seedDay(t, home, "2026-09-16",
		sentLine("u1", "2026-09-16T14:00:00Z", "workshop.scribe", "workshop.clerk", "the ledger"),
		readEventLine("u1", "2026-09-16T14:00:40Z", "workshop.clerk"))

	out, errOut := transcribe(t, "--json")
	if errOut != "" {
		t.Errorf("stderr = %q", errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("want one JSON object, got %q", out)
	}

	var got struct {
		UID     string            `json:"uid"`
		From    string            `json:"from"`
		To      string            `json:"to"`
		SentTS  string            `json:"sent_ts"`
		Body    string            `json:"body"`
		Outcome string            `json:"outcome"`
		Events  []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("the line is not JSON: %v\n%s", err, lines[0])
	}
	if got.UID != "u1" || got.From != "workshop.scribe" || got.To != "workshop.clerk" {
		t.Errorf("uid/from/to = %q/%q/%q", got.UID, got.From, got.To)
	}
	if got.SentTS != "2026-09-16T14:00:00Z" || got.Body != "the ledger" {
		t.Errorf("sent_ts/body = %q/%q", got.SentTS, got.Body)
	}
	if got.Outcome != "read 14:00:40Z by workshop.clerk" {
		t.Errorf("outcome = %q", got.Outcome)
	}
	if len(got.Events) != 2 {
		t.Fatalf("events = %v, want the two lines", got.Events)
	}
	// IN ORDER: the send, then the read. The order is the claim — a reader
	// that sorted them would put the same two objects in a wrong sequence.
	for i, want := range []string{`"status":"sent"`, `"status":"read"`} {
		if !strings.Contains(string(got.Events[i]), want) {
			t.Errorf("event %d = %s, want the %s line", i, got.Events[i], want)
		}
	}
}

// ---------------------------------------------------------------- bad input

// A CORRUPT LINE IS SKIPPED, NAMED, AND HIDES NOTHING. A truncated write at
// the top of a file must not cost the operator the day under it, and a skip
// that said nothing would be a reader quietly reporting less than the record
// holds.
func TestTranscript_ACorruptLineIsSkippedAndReported(t *testing.T) {
	home := scratchRecord(t)
	seedDay(t, home, "2026-09-16",
		sentLine("u1", "2026-09-16T14:00:00Z", "workshop.scribe", "workshop.clerk", "before"),
		`{"uid":"u2","status":"sent","ts":"2026-09`,
		"null",
		"[1,2,3]",
		sentLine("u3", "2026-09-16T14:02:00Z", "workshop.scribe", "workshop.clerk", "after"))

	code, out, errOut := exec("transcript")
	if code != 0 {
		t.Fatalf("a corrupt line failed the read: exit %d, %s", code, errOut)
	}
	for _, want := range []string{"before", "after"} {
		if !strings.Contains(out, want) {
			t.Errorf("the corrupt line hid %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "u2") {
		t.Errorf("the truncated line was read as a message:\n%s", out)
	}
	// `null` PARSES INTO A STRUCT and leaves every field empty. A reader that
	// only checked the unmarshal error would take it as a message with no
	// uid, so it is named here by its line number.
	for _, want := range []string{"line 2", "line 3", "line 4"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr does not name %s:\n%s", want, errOut)
		}
	}
	if !strings.Contains(errOut, filepath.Join(home, "run", "log", "2026-09-16.jsonl")) {
		t.Errorf("stderr does not name the file:\n%s", errOut)
	}
}

// AN EMPTY RECORD IS A SENTENCE, NOT A SILENCE. A verb that printed nothing
// would leave the caller unable to tell an empty log from a broken read.
func TestTranscript_AnEmptyRecordSaysSo(t *testing.T) {
	home := scratchRecord(t)
	if _, err := os.Stat(filepath.Join(home, "run", "log")); !os.IsNotExist(err) {
		t.Fatalf("the fixture is wrong: this case needs a home with no record")
	}

	code, out, errOut := exec("transcript")
	assertResult(t, code, out, errOut, 0, "(no messages in the record)\n", "")
}

// A DATE THAT IS NOT A DATE IS REFUSED, and named. Read as a filter it would
// match nothing, and the caller would be told the record is empty on a day it
// is not.
func TestTranscript_ARefusedDateIsNamed(t *testing.T) {
	scratchRecord(t)
	code, out, errOut := exec("transcript", "yesterday")
	if code != 1 || out != "" || !strings.Contains(errOut, "invalid date 'yesterday'") {
		t.Errorf("exit=%d stdout=%q stderr=%q; want the date refused by name", code, out, errOut)
	}
}

// AN UNKNOWN FLAG IS THE USAGE FAILURE, the same as it is for every other
// verb here. A flag that changes nothing is the failure a caller does not
// notice.
func TestTranscript_AnUnknownFlagIsAUsageFailure(t *testing.T) {
	scratchRecord(t)
	code, out, errOut := exec("transcript", "--everything")
	assertResult(t, code, out, errOut, 1, usage, "")
}

// A STATUS WORD THIS BUILD DOES NOT KNOW IS PRINTED AS IT STANDS. The record
// outlives any one version of this reader: a line written by a newer `loc` is
// still the answer to what happened, and a reader that dropped it would
// report a message as pending after it was settled.
func TestTranscript_AnUnknownStatusIsPrintedAsItStands(t *testing.T) {
	home := scratchRecord(t)
	seedDay(t, home, "2026-09-16",
		sentLine("u1", "2026-09-16T14:00:00Z", "workshop.scribe", "workshop.clerk", "a message"),
		`{"uid":"u1","status":"returned","ts":"2026-09-16T14:05:00Z","reason":"unknown"}`,
		seatLine("workshop.other", "banished", "2026-09-16T15:00:00Z", "unknown"))

	out, _ := transcribe(t, "--seat", "workshop.other")
	if !strings.Contains(out, "2026-09-16T15:00:00Z workshop.other  banished: unknown\n") {
		t.Errorf("an unknown seat status was dropped:\n%s", out)
	}

	msg, _ := transcribe(t, "--uid", "u1")
	if !strings.Contains(msg, "  returned\n") {
		t.Errorf("an unknown message status was dropped:\n%s", msg)
	}
}

// A QUEUE THAT WENT WITH NOTHING WAITING is still a line. It is why that seat
// stopped receiving mail, which is the question the next unanswered message
// to it will raise.
func TestTranscript_AnUnattachedQueueDeletionIsItsOwnLine(t *testing.T) {
	home := scratchRecord(t)
	seedDay(t, home, "2026-09-16",
		seatLine("workshop.clerk", "queue-deleted", "2026-09-16T18:40:00Z", "orphan"))

	out, _ := transcribe(t, "--seat", "workshop.clerk")
	want := "2026-09-16T18:40:00Z workshop.clerk  queue deleted: orphan\n"
	if out != want {
		t.Errorf("transcript = %q, want %q", out, want)
	}
}

// A BODY IS INDENTED WHOLE. A send carries newlines, and a second line left
// at column 0 would read as the next message's header.
func TestTranscript_AMultiLineBodyIsIndentedWhole(t *testing.T) {
	home := scratchRecord(t)
	seedDay(t, home, "2026-09-16",
		sentLine("u1", "2026-09-16T14:00:00Z", "workshop.scribe", "workshop.clerk", `first\nsecond`))

	out, _ := transcribe(t)
	if !strings.Contains(out, "  first\n  second\n") {
		t.Errorf("the second line of the body is not indented:\n%s", out)
	}
}

// A READER THAT GOES AWAY IS AN ERROR AND NOT A DEATH. The body here is long
// enough that the buffer flushes part-way through, so the failure arrives at
// a write rather than at the final flush — which is the case the accumulated
// error in lineWriter exists for.
func TestTranscript_AReaderThatGoesAwayIsAnError(t *testing.T) {
	home := scratchRecord(t)
	seedDay(t, home, "2026-09-16",
		sentLine("u1", "2026-09-16T14:00:00Z", "workshop.scribe", "workshop.clerk",
			strings.Repeat("a", 9000)))

	var errOut strings.Builder
	err := transcriptVerb(&refusingWriter{}, &errOut, nil)
	if err == nil {
		t.Fatal("a refused write returned no error")
	}
	if !strings.Contains(err.Error(), "the reader went away") {
		t.Errorf("error = %v, want the writer's own", err)
	}
}

// A LOG FILE THAT CANNOT BE READ IS NAMED, AND THE REST IS STILL READ. One
// file with the wrong mode is the operator's problem and not a reason to lose
// every other day.
func TestTranscript_AnUnreadableFileIsNamedAndSkipped(t *testing.T) {
	home := scratchRecord(t)
	seedDay(t, home, "2026-09-16", sentLine("u1", "2026-09-16T14:00:00Z",
		"workshop.scribe", "workshop.clerk", "still readable"))
	seedDay(t, home, "2026-09-17", sentLine("u2", "2026-09-17T14:00:00Z",
		"workshop.scribe", "workshop.clerk", "hidden"))
	shut := filepath.Join(home, "run", "log", "2026-09-17.jsonl")
	if err := os.Chmod(shut, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(shut, 0o600) })
	if _, err := os.ReadFile(shut); err == nil {
		t.Skip("this user reads a 0000 file, so the case cannot be arranged")
	}

	code, out, errOut := exec("transcript")
	if code != 0 {
		t.Fatalf("an unreadable file failed the read: exit %d, %s", code, errOut)
	}
	if !strings.Contains(out, "still readable") {
		t.Errorf("one unreadable file hid the rest of the record:\n%s", out)
	}
	if !strings.Contains(errOut, shut) {
		t.Errorf("stderr does not name the unreadable file:\n%s", errOut)
	}
}

// ------------------------------------------------- seat events: the two bounds

// A QUEUE DELETION IS TERMINAL. A bell that fails after the queue went is a
// fact about the seat, not about mail that no longer exists, so a lost
// message stays lost. The first cut took the newest seat event and read this
// as pending; every session end logs `left`, so a restarted seat with a dead
// bell hit it (Assayer F1, 2026-09-16).
//
// FIRE CONTROL: the same log without the deletion reads as the bell failure,
// so the case is deciding between the two and not missing the bell.
func TestTranscript_ALostMessageStaysLostAfterALaterBellFailure(t *testing.T) {
	home := scratchRecord(t)
	seedDay(t, home, "2026-09-16",
		sentLine("u1", "2026-09-16T10:00:00Z", "workshop.scribe", "workshop.clerk", "waiting"),
		seatLine("workshop.clerk", "queue-deleted", "2026-09-16T10:05:00Z", "left"),
		seatLine("workshop.clerk", "bell-failed", "2026-09-16T11:00:00Z", "no pane"))
	out, _ := transcribe(t)
	if !strings.Contains(out, "lost: queue deleted 10:05:00Z (left)") {
		t.Errorf("a deletion followed by a bell failure did not stay lost:\n%s", out)
	}

	home = scratchRecord(t)
	seedDay(t, home, "2026-09-16",
		sentLine("u1", "2026-09-16T10:00:00Z", "workshop.scribe", "workshop.clerk", "waiting"),
		seatLine("workshop.clerk", "bell-failed", "2026-09-16T11:00:00Z", "no pane"))
	out, _ = transcribe(t)
	if !strings.Contains(out, "pending, bell failed 11:00:00Z: no pane") {
		t.Errorf("control: without the deletion the bell failure should decide:\n%s", out)
	}
}

// A seat event stamped at or before the send belongs to whatever was in the
// queue before this message. It never attaches. No arm held this bound before
// (Assayer F2, 2026-09-16): dropping it left the suite green.
//
// FIRE CONTROL: the same event one second after the send does attach.
func TestTranscript_ASeatEventBeforeTheSendDoesNotAttach(t *testing.T) {
	home := scratchRecord(t)
	seedDay(t, home, "2026-09-16",
		seatLine("workshop.clerk", "queue-deleted", "2026-09-16T09:59:00Z", "orphan"),
		sentLine("u1", "2026-09-16T10:00:00Z", "workshop.scribe", "workshop.clerk", "waiting"),
		seatLine("workshop.clerk", "bell-failed", "2026-09-16T10:00:00Z", "no pane"))
	out, _ := transcribe(t)
	if !strings.Contains(out, "  pending\n") && !strings.Contains(out, " pending\n") {
		t.Errorf("a seat event at or before the send attached; want plain pending:\n%s", out)
	}
	if strings.Contains(out, "lost") || strings.Contains(out, "bell failed") {
		t.Errorf("an older seat event attached to a later message:\n%s", out)
	}

	home = scratchRecord(t)
	seedDay(t, home, "2026-09-16",
		sentLine("u1", "2026-09-16T10:00:00Z", "workshop.scribe", "workshop.clerk", "waiting"),
		seatLine("workshop.clerk", "bell-failed", "2026-09-16T10:00:01Z", "no pane"))
	out, _ = transcribe(t)
	if !strings.Contains(out, "pending, bell failed 10:00:01Z: no pane") {
		t.Errorf("control: the same event one second after the send should attach:\n%s", out)
	}
}
