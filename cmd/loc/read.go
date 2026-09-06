package main

import (
	"bytes"
	"fmt"
	"io"
	"time"

	"github.com/tdebasis/locutorium/internal/config"
	"github.com/tdebasis/locutorium/internal/loc"
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

// peekTrailer is what stands where the rooms would be. A peek that printed a
// bare topics heading would say the rooms are empty; they are simply not
// looked at, because looking at one currently consumes from it.
const peekTrailer = "── topics ── (not shown: --peek never consumes, " +
	"and topic reads cannot yet be non-consuming)\n"

// readVerb reads the arguments and hands the medium to read.
//
// EXACTLY --peek OR NOTHING. The shell tool silently ignores anything else and
// performs a normal, consuming read, so a misspelt `--pekk` destroys the very
// backlog the flag was typed to leave alone. That trap is documented for the
// shell tool; this build refuses instead.
func readVerb(w io.Writer, args []string) error {
	peek := false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--peek":
		peek = true
	default:
		return errUsage
	}
	return withProvider(func(p provider.Provider) error { return read(p, w, peek) })
}

// read presents the caller's own queue and then the rooms.
func read(p provider.Provider, w io.Writer, peek bool) error {
	me, err := loc.Identity()
	if err != nil {
		return err
	}
	r, ok := p.(provider.Reader)
	if !ok {
		return fmt.Errorf("provider '%s' cannot hand a reader its messages", config.Get("provider", ""))
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

	if _, err := io.WriteString(w, "── queue."+me+" ──\n"); err != nil {
		return err
	}

	if peek {
		// At most one, and nothing acknowledged: the message stays where it is
		// and the next ordinary read hands it over.
		if got {
			if err := present(w, m.Data()); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, peekTrailer)
		return err
	}

	if err := drain(w, m, got, func() (provider.Message, bool, error) {
		return r.NextQueued(me, fetchWait)
	}); err != nil {
		return err
	}

	if _, err := io.WriteString(w, "── topics ──\n"); err != nil {
		return err
	}
	m, got, err = r.NextTopic(me, fetchWait)
	if err != nil {
		return err
	}
	return drain(w, m, got, func() (provider.Message, bool, error) {
		return r.NextTopic(me, fetchWait)
	})
}

// drain shows and then forgets, in that order, until there is nothing left.
func drain(w io.Writer, m provider.Message, got bool, next func() (provider.Message, bool, error)) error {
	var err error
	for got {
		// The write, then the ack that deletes what it carried — and a write
		// that failed returns here at once, with nothing acknowledged, so the
		// message it never showed is still owed to this reader.
		if err = present(w, m.Data()); err != nil {
			return err
		}
		if err = m.Ack(); err != nil {
			return err
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
