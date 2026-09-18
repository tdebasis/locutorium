package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/tdebasis/locutorium/internal/config"
	"github.com/tdebasis/locutorium/internal/loc"
	model "github.com/tdebasis/locutorium/internal/presence"
	"github.com/tdebasis/locutorium/internal/provider"
)

// READING IS THE ONE VERB THAT DESTROYS, and the order of two operations is
// the whole of its safety.
//
// A queue is retained until its endpoint takes what is in it, so the
// acknowledgement is a delete with no recovery. This used to be a pipeline
// that acked at the moment it fetched: a dead terminal or a renderer error
// destroyed whatever had been fetched, and on one occasion five real messages
// went with it. Here the bytes reach the reader FIRST and are forgotten only
// once the write that carried them returned nil. A failure costs a duplicate,
// which PROTOCOL.md §5 names as ordinary and gives `id` as the key for, and
// never a deletion.
//
// The bash listener's spools are NOT presented here. They belong to the
// listener and to whatever deployment hook presents them; this build has
// neither, and reading a file some other process is appending to is not a
// thing to do speculatively.

// fetchWait is how long one fetch waits before calling the queue dry. Short,
// because the loop ends on the first miss and a dry queue is the common case.
const fetchWait = 1 * time.Second

// readNow is the clock a `read` event is stamped with.
//
// IT IS A VARIABLE SO A CASE CAN PUT AN EVENT ON A DAY OF ITS CHOOSING. The
// day roll is a real property of the record — a read at 00:00:01 belongs in
// the new day's file, beside nothing, and not at the foot of yesterday's —
// and the only other way to exercise it is to wait for midnight. The shipped
// binary reads the machine's clock through it and nothing replaces it.
var readNow = func() time.Time { return time.Now().UTC() }

// peekTrailer is what stands where the rooms would be. A peek that printed a
// bare topics heading would say the rooms are empty; they are simply not
// looked at, because looking at one currently consumes from it.
const peekTrailer = "── topics ── (not shown: --peek never consumes, " +
	"and topic reads cannot yet be non-consuming)\n"

// readVerb reads the arguments and hands the medium to read.
//
// EXACTLY --peek, EXACTLY --json, OR NOTHING. The shell tool silently ignores
// anything else and performs a normal, consuming read, so a misspelt `--pekk`
// destroys the very backlog the flag was typed to leave alone. That trap is
// documented for the shell tool; this build refuses instead — and the same
// strictness rules out `--peek --json` together, which would be asked to
// consume nothing and everything at once.
func readVerb(w io.Writer, args []string) error {
	peek := false
	jsonOut := false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--peek":
		peek = true
	case len(args) == 1 && args[0] == "--json":
		jsonOut = true
	default:
		return errUsage
	}
	return withProvider(func(p provider.Provider) error {
		stop := releaseOnSignal(p)
		defer stop()
		return readMode(p, w, peek, jsonOut)
	})
}

// signalGrace is how long the handler waits for the signal it re-sent to end
// the process. The default disposition is restored before it is sent, so the
// kernel ends this process inside that window and the sleep does not finish.
// It is a bound on a gap, not a delay anybody waits out.
const signalGrace = 2 * time.Second

// readSignalExit is what a released read does after it has handed its message
// back. It is a variable so a case can watch the release without the case's own
// process being killed by the re-raise.
var readSignalExit = reRaise

// reRaise restores the default disposition and sends the signal to this process
// again, so the ending is the ending the caller asked for.
func reRaise(s os.Signal) {
	signal.Reset(stopSignals()...)
	if self, err := os.FindProcess(os.Getpid()); err == nil {
		if err := self.Signal(s); err == nil {
			time.Sleep(signalGrace)
		}
	}
	// Windows delivers no signal to a process this way, so the ending is taken
	// here instead. On Unix this line is not reached.
	os.Exit(1)
}

// releaseOnSignal hands the in-flight message back before a killed read exits.
//
// A READ KILLED BY A SIGNAL SKIPS THE CLOSE THAT RETURNS IT. Close negatively
// acknowledges everything the reader was handed and never took, so an ordinary
// exit returns the message at once — an error, an EPIPE, a dry queue. `^C` and
// `kill` do not run it. Without this handler the message the reader was in the
// middle of stays in flight for the consumer's whole ack-wait, the server's
// 30 s default, and the next read shows an empty mailbox: the one state a
// reader cannot tell apart from having lost it (issue #30).
//
// THE ACK-WAIT IS NOT TOUCHED. An explicit ack-wait is a consumer
// configuration, and every existing cursor would have to be migrated to take
// one. The release costs no migration and bounds the window at the signal
// rather than at a timer.
//
// SIGKILL IS NOT COVERED AND CANNOT BE. No process handles it, so a read
// killed that way still strands its message for the server's default
// ack-wait. That case is the reason the 30 s bound stays where it is.
//
// THE SIGNAL IS NOT SWALLOWED. readSignalExit restores the default disposition
// and sends the same signal again, so the exit status still says the tool was
// killed and names the signal. A caller reading `$?` sees no change.
//
// Close runs on this goroutine while the verb may still be inside its own
// fetch or write, and the two race. The race is bounded by the direction this
// whole path is built on: the worst outcome is a message handed back twice, or
// handed back after it was taken, and both cost a DUPLICATE. Nothing here can
// delete a message the reader has not seen.
func releaseOnSignal(p provider.Provider) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, stopSignals()...)
	go func() {
		s, ok := <-ch
		if !ok {
			// stop closed the channel: the read ended on its own and
			// withProvider's own Close is what returns anything outstanding.
			return
		}
		p.Close()
		readSignalExit(s)
	}()
	return func() {
		// Stop returns only once the signal package will send nothing more, so
		// the close that follows cannot race a delivery.
		signal.Stop(ch)
		close(ch)
	}
}

// read presents the caller's own queue and then the rooms, rendered as
// markdown. It is the form the mcp verb's own Read hook (cmd/loc/mcp.go)
// calls, which has no `--json` mode of its own to plumb through.
func read(p provider.Provider, w io.Writer, peek bool) error {
	return readMode(p, w, peek, false)
}

// readMode is read's full form, adding the `--json` output.
//
// jsonOut consumes exactly as a plain read does — same drains, same
// presence-event filter, same per-message acknowledgement — but shows each
// envelope as its raw wire bytes instead of rendered markdown, and omits the
// `── queue.x ──` / `── topics ──` headings, so the output is JSON Lines and
// nothing else. A hook parses that; it cannot parse a heading.
func readMode(p provider.Provider, w io.Writer, peek bool, jsonOut bool) error {
	me, err := loc.Identity()
	if err != nil {
		return err
	}
	r, ok := p.(provider.Reader)
	if !ok {
		return fmt.Errorf("provider '%s' cannot hand a reader its messages", config.Value(config.Provider))
	}

	// THE MEDIUM IS REACHED BEFORE THE FIRST BYTE IS PRINTED. An unreachable
	// broker is a loud failure with nothing on standard out: a heading printed
	// and then abandoned would tell the reader its mailbox is a place that was
	// just looked at, which is the one thing it must not be told falsely.
	var m provider.Message
	var got bool
	if peek {
		m, got, err = r.PeekQueued(me)
	} else {
		m, got, err = r.NextQueued(me, fetchWait)
	}
	if err != nil {
		return err
	}

	show := present
	if jsonOut {
		show = presentJSON
	}

	if !jsonOut {
		if _, err := io.WriteString(w, "── queue."+me+" ──\n"); err != nil {
			return err
		}
	}

	if peek {
		// At most one, and nothing acknowledged: the message stays where it is
		// and the next ordinary read hands it over.
		if got {
			if err := show(w, m.Data()); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, peekTrailer)
		return err
	}

	// A queue is never filtered: it carries only mail.
	//
	// THE QUEUE IS THE ONLY PLANE THAT RECORDS A READ. A queue message is
	// delivered to one endpoint and consumed once, so "this message was read,
	// by this seat" is a fact about the message. A topic message is read from
	// every attending seat's own position, so the same statement about a room
	// would need one line per attender and would still not be complete while
	// one of them has not read yet. Both forms of `read` and the MCP read tool
	// reach this line, because all three come through readMode.
	if err := drain(w, m, got, func() (provider.Message, bool, error) {
		return r.NextQueued(me, fetchWait)
	}, nil, show, func(raw []byte) {
		e, err := loc.ParseEnvelope(raw)
		if err != nil {
			// Not an envelope, so there is no uid to join on. A line with an
			// empty uid would join to every other unparseable payload in the
			// day's file.
			return
		}
		loc.LogRead(e.ID, me, readNow())
	}); err != nil {
		return err
	}

	if !jsonOut {
		if _, err := io.WriteString(w, "── topics ──\n"); err != nil {
			return err
		}
	}
	m, got, err = r.NextTopic(me, fetchWait)
	if err != nil {
		return err
	}
	return drain(w, m, got, func() (provider.Message, bool, error) {
		return r.NextTopic(me, fetchWait)
	}, isPresenceEvent, show, nil)
}

// isPresenceEvent reports whether a topic payload is a presence event rather
// than something somebody said.
//
// THE SEPARATION IS AT THE SUBJECT LEVEL NOW, AND THIS IS STILL HERE. Events
// are published on presence.<instance>, a family no stream captures, so a room
// stream laid today holds conversation and nothing else. This predicate is the
// GUARD FOR READERS ON OLDER DEPLOYMENTS: rooms that were filled while events
// were spoken on topic.<instance> still hold those events, inside the TOPICS
// stream's own subject space, until the window ages them out — and a reader
// meeting one deserves not to be shown a registration as though someone had
// written to them. It costs one JSON probe per room message and it is the only
// thing standing between an existing deployment and that.
//
// A payload is an event IFF it is a JSON OBJECT WHOSE `kind` IS ONE OF THE SIX
// PRESENCE KINDS, and the taxonomy is asked for rather than restated, so a
// seventh kind is filtered the day it is added. Unmarshalling into a struct is
// what makes "object" part of the test: a JSON string, number or array is a
// type error and is therefore mail. The predicate is deliberately narrow —
// everything it does not recognise is mail, which fails towards showing a
// reader something they did not need rather than hiding something they did.
func isPresenceEvent(raw []byte) bool {
	var probe struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return model.ValidKind(probe.Kind)
}

// drain shows and then forgets, in that order, until there is nothing left.
//
// skip, when non-nil, names the payloads this medium hands over that are NOT
// mail. One of those is PASSED OVER: nothing is rendered and the loop goes on
// past it. The acknowledgement still happens, and on a room that is a cursor
// move for this reader alone — TOPICS is limits-retention, so no other
// reader's copy is touched — not a deletion. Leaving it unacknowledged would
// hand the same event to this reader on every read for as long as the room
// keeps it.
//
// show is how one envelope reaches the caller — present's rendered markdown,
// or presentJSON's raw bytes — so the one loop that drains and acknowledges
// serves both forms of `read` identically.
//
// after, when non-nil, is the record of what just happened, and it runs AFTER
// a successful Ack and for a payload that was shown. The order is the same
// order the ack itself is in: a line saying a message was read, written for a
// message that is still in the queue, is a claim the next read disproves. A
// payload the skip predicate passed over was consumed and never handed to
// anybody, so it is not something a reader read. after returns nothing,
// because a record that could fail the read it describes would be a record
// that costs mail.
func drain(w io.Writer, m provider.Message, got bool, next func() (provider.Message, bool, error), skip func([]byte) bool, show func(io.Writer, []byte) error, after func(raw []byte)) error {
	var err error
	for got {
		// The write, then the ack that deletes what it carried — and a write
		// that failed returns here at once, with nothing acknowledged, so the
		// message it never showed is still owed to this reader.
		raw := m.Data()
		shown := skip == nil || !skip(raw)
		if shown {
			if err = show(w, raw); err != nil {
				return err
			}
		}
		if err = m.Ack(); err != nil {
			return err
		}
		if shown && after != nil {
			after(raw)
		}
		if m, got, err = next(); err != nil {
			return err
		}
	}
	return nil
}

// present renders one envelope and writes it in ONE call.
//
// Rendered into a buffer first, deliberately: a render that wrote straight to
// the stream could leave half an envelope in front of the reader and then fail,
// and the message would be neither shown nor kept. Whole, or not at all.
func present(w io.Writer, raw []byte) error {
	var b bytes.Buffer
	if err := loc.Render(bytes.NewReader(raw), &b); err != nil {
		return err
	}
	_, err := w.Write(b.Bytes())
	return err
}

// presentJSON writes one envelope's raw wire bytes followed by a newline, in
// ONE call — the same whole-or-not-at-all reasoning as present, so a write
// that fails partway leaves nothing acknowledged rather than half a line.
// `--json` promises JSON Lines to whatever parses it; this is the one place
// that promise is kept.
func presentJSON(w io.Writer, raw []byte) error {
	line := make([]byte, 0, len(raw)+1)
	line = append(line, raw...)
	line = append(line, '\n')
	_, err := w.Write(line)
	return err
}
