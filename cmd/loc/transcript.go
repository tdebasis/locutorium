package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tdebasis/locutorium/internal/config"
	model "github.com/tdebasis/locutorium/internal/presence"
)

// The transcript verb reads the day's record back.
//
// IT DERIVES THE OUTCOME AND STORES NOTHING. internal/loc/log.go appends one
// line per event, and no line says what became of a message: a `sent` line is
// written before anybody has read, and the `read` line that answers it lands
// in whatever day's file the reader was in. The join is the reader's work, so
// it is done here, once, rather than by each person with jq.
//
// THE RECORD IS EVIDENCE AND THIS VERB WRITES NONE OF IT. It opens the log
// files for reading, and it opens nothing else. It touches no medium, so it
// answers on a deployment whose broker is the thing that is wrong.

// tsDay is the length of the date part of an RFC3339 stamp, `2026-09-16`.
// The log writes every stamp at seconds precision in UTC with a literal Z
// (internal/loc/envelope.go, tsLayout), so two stamps compare as strings in
// the same order they compare as times, and a day is a prefix.
const tsDay = len("2006-01-02")

// logEvent is one line of the record, parsed and kept whole.
//
// BOTH FAMILIES ARE ONE STRUCT, and which family a line belongs to is read
// from which key it carries, never from the status word. A message event
// carries `uid`; a seat event carries `seat`. That is the rule the writer
// states (internal/loc/log.go) and the rule docs/OPERATORS.md publishes.
//
// raw is the line as it was on disk. --json hands the events back untouched,
// so a field this struct does not know about still reaches whoever asked.
type logEvent struct {
	raw  json.RawMessage
	file string
	line int
	seq  int // position across every file, in filename then line order

	UID    string `json:"uid"`
	Seat   string `json:"seat"`
	Status string `json:"status"`
	TS     string `json:"ts"`
	From   string `json:"from"`
	To     string `json:"to"`
	Body   string `json:"body"`
	Reason string `json:"reason"`
	By     string `json:"by"`

	// The bell's fields. `try` and `of` place one try in its streak, `result`
	// says what came of it, and `after` is on the give-up line alone. An old
	// `bell-failed` line carries none of them and still reads: no build has
	// written one since #144, and old day files hold them.
	Try    int    `json:"try"`
	Of     int    `json:"of"`
	Result string `json:"result"`
	After  int    `json:"after"`
}

// message is every event that shares one uid, in the order they were written.
type message struct {
	uid    string
	from   string
	to     string
	body   string
	sentTS string
	seq    int
	events []*logEvent
}

// transcriptVerb prints the record as messages rather than as lines.
//
// errw carries the corrupt-line notices. A corrupt line is a fact about the
// record and not a failure of the read, so it goes to the other stream and
// the verb keeps going: one truncated line at the top of a file must not hide
// the day under it.
func transcriptVerb(w, errw io.Writer, args []string) error {
	var seat, uid string
	var pending, asJSON bool
	day, rest := takeDate(args, map[string]bool{"--seat": true, "--uid": true})
	if err := parseFlags(rest,
		map[string]*string{"--seat": &seat, "--uid": &uid},
		map[string]*bool{"--pending": &pending, "--json": &asJSON}); err != nil {
		return err
	}
	if day != "" {
		if _, err := time.Parse("2006-01-02", day); err != nil {
			return fmt.Errorf("invalid date '%s': a day is written YYYY-MM-DD", day)
		}
	}

	events, err := readRecord(errw)
	if err != nil {
		return err
	}
	msgs, seatEvents := join(events)

	// The overlay runs over EVERY message before any filter, so that a seat
	// event counts as belonging to a message the caller did not ask to see.
	// Filtering first would leave it unclaimed and print it as a bare line.
	outcomes, claimed := outcomes(msgs, seatEvents)

	var items []renderable
	for _, m := range msgs {
		if !keepMessage(m, outcomes[m.uid], day, seat, uid, pending) {
			continue
		}
		items = append(items, renderable{ts: m.sentTS, seq: m.seq, msg: m})
	}
	if seat != "" && uid == "" && !pending && !asJSON {
		for _, e := range seatEvents {
			if claimed[e] || e.Seat != seat || !onDay(e.TS, day) {
				continue
			}
			items = append(items, renderable{ts: e.TS, seq: e.seq, seat: e})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].ts != items[j].ts {
			return items[i].ts < items[j].ts
		}
		return items[i].seq < items[j].seq
	})

	out := &lineWriter{w: bufio.NewWriter(w)}
	switch {
	case asJSON:
		for _, it := range items {
			out.line(jsonLine(it.msg, outcomes[it.msg.uid]))
		}
	case len(items) == 0:
		// A single line, because a silent exit 0 and an empty record look the
		// same to whoever ran this and neither says which it was.
		out.printf("(no messages in the record)\n")
	default:
		for _, it := range items {
			if it.seat != nil {
				out.printf("%s %s  %s\n", it.seat.TS, it.seat.Seat, seatWords(it.seat))
				continue
			}
			writeBlock(out, it.msg, outcomes[it.msg.uid], uid != "")
		}
	}
	return out.flush()
}

// renderable is one thing to print, with the key it sorts on. A message sorts
// on the stamp of its first event and a seat line on its own, so the two
// interleave in the order they happened.
type renderable struct {
	ts   string
	seq  int
	msg  *message
	seat *logEvent
}

// takeDate lifts the one bare word out of args, wherever it sits.
//
// The usage writes `[date]` last, after the flags, and a person types it
// first about as often. parseFlags refuses a word it was not expecting, so
// the word is taken off here first and the flags are parsed without it. A
// flag's VALUE is not a bare word, which is why the value flags are named:
// `--seat workshop.scribe` must not read `workshop.scribe` as a date.
func takeDate(args []string, valueFlags map[string]bool) (string, []string) {
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			if valueFlags[args[i]] {
				i++
			}
			continue
		}
		rest := make([]string, 0, len(args)-1)
		rest = append(rest, args[:i]...)
		rest = append(rest, args[i+1:]...)
		return args[i], rest
	}
	return "", args
}

// readRecord reads every day file in the record, in filename order.
//
// Filename order IS time order: the files are named for the UTC day, so they
// sort as days sort. Within a file the lines are appended, so the file order
// is the order the events were written.
func readRecord(errw io.Writer) ([]*logEvent, error) {
	dir := filepath.Join(config.Home(), "run", "log")
	paths, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	var events []*logEvent
	seq := 0
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			// An unreadable file is the operator's problem and not this
			// verb's: name it and read the rest, for the same reason a
			// corrupt line does not stop the read.
			fmt.Fprintf(errw, "loc: cannot read %s: %v\n", path, err)
			continue
		}
		for n, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			e, ok := parseLine(line)
			if !ok {
				// The file AND the line number, because that is what it
				// takes to go and look at the line itself.
				fmt.Fprintf(errw, "loc: %s line %d is not a JSON object; skipped\n", path, n+1)
				continue
			}
			e.file, e.line, e.seq = path, n+1, seq
			seq++
			events = append(events, e)
		}
	}
	return events, nil
}

// parseLine reads one line, and reports whether it is a JSON object.
//
// THE BRACE IS CHECKED BEFORE THE UNMARSHAL. `null` unmarshals into a struct
// without an error and leaves every field empty, so a record line reading
// `null` would otherwise join the message family under an empty uid.
func parseLine(line string) (*logEvent, bool) {
	if line[0] != '{' {
		return nil, false
	}
	var e logEvent
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		return nil, false
	}
	e.raw = json.RawMessage(line)
	return &e, true
}

// join puts each message's events together and keeps the seat events apart.
//
// A line with neither key belongs to neither family and is dropped. It is not
// corrupt — it parsed — so it draws no notice; it simply says nothing this
// verb can place.
func join(events []*logEvent) ([]*message, []*logEvent) {
	var msgs []*message
	var seats []*logEvent
	byUID := map[string]*message{}
	for _, e := range events {
		switch {
		case e.UID != "":
			m, ok := byUID[e.UID]
			if !ok {
				// The FIRST event names the message. A `read` line carries
				// neither `from` nor `to` (internal/loc/log.go: it repeats
				// nothing), so a message whose `sent` line is in a file the
				// operator has pruned shows the uid and empty endpoints
				// rather than nothing at all.
				m = &message{uid: e.UID, from: e.From, to: e.To, body: e.Body, sentTS: e.TS, seq: e.seq}
				byUID[e.UID] = m
				msgs = append(msgs, m)
			}
			m.events = append(m.events, e)
		case e.Seat != "":
			seats = append(seats, e)
		}
	}
	return msgs, seats
}

// outcomes derives one outcome per message, and reports which seat events it
// used to do it.
//
// THE OUTCOME IS THE LAST EVENT, in the order the record was written. Nothing
// else is consulted: a message with a `failed` line and then a `sent` line is
// a message that was sent, because that is what the record says happened
// second.
func outcomes(msgs []*message, seatEvents []*logEvent) (map[string]string, map[*logEvent]bool) {
	words := map[string]string{}
	claimed := map[*logEvent]bool{}
	for _, m := range msgs {
		last := m.events[len(m.events)-1]
		switch last.Status {
		case "read":
			words[m.uid] = fmt.Sprintf("read %s by %s", stamp(m.sentTS, last.TS), last.By)
		case "failed":
			words[m.uid] = fmt.Sprintf("failed: %s", last.Reason)
		case "sent":
			e := lastSeatEvent(m, seatEvents)
			if e == nil {
				words[m.uid] = "pending"
				continue
			}
			claimed[e] = true
			if e.Status == "queue-deleted" {
				words[m.uid] = fmt.Sprintf("lost: queue deleted %s (%s)", stamp(m.sentTS, e.TS), e.Reason)
				continue
			}
			words[m.uid] = "pending, " + bellWords(e, stamp(m.sentTS, e.TS))
		default:
			// A status this build does not know is printed as it stands. The
			// record outlives any one version of this reader, and a word it
			// cannot interpret is still the answer to what happened.
			words[m.uid] = last.Status
		}
	}
	return words, claimed
}

// bellWords says one bell event beside the message it could not be joined to
// by id, with that event's time already rendered.
//
// A TRY, NOT A FAILURE. Since #144 the bell writes every try with its result,
// so this says how far into the streak the seat's bell got and what the last
// try came to. `bell failed <ts>: <reason>` is what an OLD `bell-failed` line
// still reads as; no build writes one.
func bellWords(e *logEvent, ts string) string {
	switch e.Status {
	case "bell-try":
		last := e.Result
		if e.Reason != "" {
			last += fmt.Sprintf(" (%s)", e.Reason)
		}
		return fmt.Sprintf("bell tried %d of %d, last %s %s", e.Try, e.Of, last, ts)
	case "bell-gave-up":
		return fmt.Sprintf("bell gave up %s after %d tries", ts, e.After)
	default:
		return fmt.Sprintf("bell failed %s: %s", ts, e.Reason)
	}
}

// bellStatus reports whether a status word is one of the bell's.
func bellStatus(s string) bool {
	return s == "bell-try" || s == "bell-gave-up" || s == "bell-failed"
}

// lastSeatEvent is the seat event that decides a pending message's fate, or
// nil: the earliest queue deletion after the send, else the latest bell
// event after the send.
//
// THE JOIN IS `seat == to` AND `ts > sent ts`, and the second half is what
// makes it mean anything. A bell that failed an hour before this message
// existed says nothing about this message. The first cut let the later of a
// bell failure and a deletion win; the body below says why that was wrong.
func lastSeatEvent(m *message, seatEvents []*logEvent) *logEvent {
	// A QUEUE DELETION IS TERMINAL. The message was in that queue, and the
	// queue is gone, so nothing after it can bring the message back: a bell
	// that fails an hour later is a fact about the seat, not about mail that
	// no longer exists. So the EARLIEST deletion after the send decides, and
	// a bell failure counts only while no deletion has happened. The first
	// cut took the newest seat event of either kind, and a session end (every
	// one logs `left`) followed by a dead bell read as pending (Assayer F1,
	// 2026-09-16).
	//
	// A seat event stamped at or before the send belongs to whatever was in
	// the queue before this message and never attaches (Assayer F2).
	var deleted, bell *logEvent
	for _, e := range seatEvents {
		if e.Seat != m.to || e.TS <= m.sentTS {
			continue
		}
		switch {
		case e.Status == "queue-deleted":
			if deleted == nil || e.TS < deleted.TS || (e.TS == deleted.TS && e.seq < deleted.seq) {
				deleted = e
			}
		case bellStatus(e.Status):
			// THE LATEST BELL EVENT, which under the flat rule is the last try
			// of the streak or the give-up that closed it. The earlier tries
			// are still in the record and `--seat` prints them one line each.
			if bell == nil || e.TS > bell.TS || (e.TS == bell.TS && e.seq > bell.seq) {
				bell = e
			}
		}
	}
	if deleted != nil {
		return deleted
	}
	return bell
}

// keepMessage applies the filters, all of which narrow and none of which
// widen.
func keepMessage(m *message, outcome, day, seat, uid string, pending bool) bool {
	if uid != "" && m.uid != uid {
		return false
	}
	if !onDay(m.sentTS, day) {
		return false
	}
	if seat != "" && m.from != seat && m.to != seat {
		return false
	}
	if pending {
		// PENDING IS THE LAST EVENT BEING `sent`, which is the three outcomes
		// that start from it: waiting, waiting with no bell, and lost. A
		// message whose queue went is still one nobody read.
		if !strings.HasPrefix(outcome, "pending") && !strings.HasPrefix(outcome, "lost") {
			return false
		}
		// A TOPIC MESSAGE IS EXCLUDED BECAUSE IT NEVER GETS A SECOND EVENT. A
		// room message is read from each attender's own cursor, so there is
		// no one `read` that answers it (internal/loc/log.go: a room read
		// writes nothing). It would sit in this list for ever.
		if model.ValidEndpoint(m.to) != nil {
			return false
		}
	}
	return true
}

// onDay reports whether a stamp falls on the given UTC day. An empty day is
// every day.
func onDay(ts, day string) bool {
	if day == "" {
		return true
	}
	return len(ts) >= tsDay && ts[:tsDay] == day
}

// stamp renders a later event's time beside the send it answers.
//
// SAME DAY, TIME ONLY; a different day, the whole stamp. A read at 14:12:40
// on the day of the send reads as `14:12:40Z`, and the date beside it would
// be the date two words to its left. A read the NEXT day is the case that
// makes the rule: `read 00:00:01Z` under a message sent yesterday evening
// would be read as a read that came before the send.
func stamp(sentTS, ts string) string {
	if len(sentTS) >= tsDay && len(ts) > tsDay && sentTS[:tsDay] == ts[:tsDay] {
		return ts[tsDay+1:]
	}
	return ts
}

// seatWords is one seat event said in words, for a line that belongs to no
// message.
func seatWords(e *logEvent) string {
	switch e.Status {
	case "bell-try":
		last := e.Result
		if e.Reason != "" {
			last += fmt.Sprintf(" (%s)", e.Reason)
		}
		return fmt.Sprintf("bell try %d of %d: %s", e.Try, e.Of, last)
	case "bell-gave-up":
		return fmt.Sprintf("bell gave up after %d tries", e.After)
	case "bell-failed":
		return fmt.Sprintf("bell failed: %s", e.Reason)
	case "queue-deleted":
		return fmt.Sprintf("queue deleted: %s", e.Reason)
	default:
		return fmt.Sprintf("%s: %s", e.Status, e.Reason)
	}
}

// writeBlock prints one message: the header, the body as sent, and under
// --uid the events the header was derived from.
func writeBlock(out *lineWriter, m *message, outcome string, showEvents bool) {
	out.printf("%s %s → %s  %s\n", m.sentTS, m.from, m.to, outcome)
	if m.body != "" {
		// A body is indented WHOLE. It may carry newlines, and a second line
		// left at column 0 would read as the next message's header.
		for _, l := range strings.Split(m.body, "\n") {
			out.printf("  %s\n", l)
		}
	}
	if !showEvents {
		return
	}
	out.printf("  events:\n")
	for _, e := range m.events {
		out.printf("    %s\n", strings.TrimSpace(string(e.raw)))
	}
}

// jsonLine is one message as one line of JSON.
//
// THE EVENTS ARE THE RAW LINES. Re-marshalling them through this file's
// struct would drop every key it does not declare, and the record is written
// by a writer that adds keys without asking this reader first.
func jsonLine(m *message, outcome string) string {
	events := make([]json.RawMessage, 0, len(m.events))
	for _, e := range m.events {
		events = append(events, e.raw)
	}
	b, err := json.Marshal(struct {
		UID     string            `json:"uid"`
		From    string            `json:"from"`
		To      string            `json:"to"`
		SentTS  string            `json:"sent_ts"`
		Body    string            `json:"body"`
		Outcome string            `json:"outcome"`
		Events  []json.RawMessage `json:"events"`
	}{m.uid, m.from, m.to, m.sentTS, m.body, outcome, events})
	if err != nil {
		// Every field is a string or a line that already parsed as JSON, so
		// this cannot fail. Saying so beats printing a half-line.
		return fmt.Sprintf("{\"uid\":%q,\"error\":%q}", m.uid, err.Error())
	}
	return string(b)
}

// lineWriter is a buffered writer that KEEPS ITS FIRST ERROR and stops.
//
// The alternative is a returned error at each of the eight write sites in
// this file, and a reader of that code counts eight checks instead of reading
// what is printed. This shape checks every write — the error is taken from
// each Fprintf — and reports the first one from flush.
type lineWriter struct {
	w   *bufio.Writer
	err error
}

func (l *lineWriter) printf(format string, a ...any) {
	if l.err != nil {
		return
	}
	_, l.err = fmt.Fprintf(l.w, format, a...)
}

func (l *lineWriter) line(s string) { l.printf("%s\n", s) }

func (l *lineWriter) flush() error {
	if l.err != nil {
		return l.err
	}
	return l.w.Flush()
}
