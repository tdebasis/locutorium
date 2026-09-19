package main

import (
	"fmt"
	"io"

	"github.com/tdebasis/locutorium/internal/config"
	model "github.com/tdebasis/locutorium/internal/presence"
	"github.com/tdebasis/locutorium/internal/provider"
)

// unreadVerb prints how much mail one endpoint has not taken.
//
// IT IS THE SECOND SIGNAL, AND IT EXISTS BECAUSE THE FIRST ONE CAN FAIL IN
// SILENCE. The bell is the only thing that tells a seat mail arrived, and a
// broken bell is not retried (docs/OPERATORS.md §The bell). A status line that
// asks this every few seconds is what catches a bell that stopped ringing.
//
// THERE ARE TWO OUTPUTS AND NO THIRD. A count that is known is the decimal
// number and one newline. A count that cannot be had is the word `unknown` and
// one newline. Nothing else ever reaches w.
//
// THE `unknown` LINE GOES TO STDOUT AND THE VERB STILL FAILS. That is the
// point of the verb rather than an inconsistency in it. Printing nothing on
// failure would be indistinguishable from printing an empty mailbox, because
// a status line shows what it is given and a seat with no mail is also shown
// as nothing. One deployment ran a status line outside the product, a release
// removed the file it read, and the line printed nothing for days with no sign
// that it had stopped working. A caller that ignores the exit code reads
// `unknown` and still cannot mistake it for "no mail".
//
// It does not break the two-outcome rule stated above run. The ANSWER is on
// stdout, which is where every answer of this verb goes. The REASON is still
// one `loc:` line on stderr and the code is still 1, both of them picked by
// run and by nothing here.
func unreadVerb(w io.Writer, endpoint string) error {
	// JUDGED BEFORE ANY PROVIDER IS OPENED, and refused the way `status
	// <endpoint>` refuses the same argument. A name that cannot be an endpoint
	// is a typo in the caller's script, not a count the medium withheld, and
	// answering `unknown` to it would send an operator to look at a broker
	// that is working.
	if err := model.ValidEndpoint(endpoint); err != nil {
		return err
	}
	n, err := unreadCount(endpoint)
	if err != nil {
		fmt.Fprintln(w, "unknown")
		return err
	}
	fmt.Fprintf(w, "%d\n", n)
	return nil
}

// unreadCount asks the medium for the figure and derives nothing of its own.
//
// THE NUMBER IS THE PROVIDER'S Unread AND NOTHING ELSE. That method already
// answers with an error rather than 0 when the figure cannot be had, and a
// queue that does not exist is one of those cases: the nats adapter's own
// unread yields "?" for a missing stream or consumer, and Unread refuses to
// read a "?" as a count (internal/provider/nats/nats.go, internal/provider/
// nats/listen.go). A second way of counting here would have to re-invent that
// distinction, and a version of it that read a missing queue as 0 would tell a
// seat whose queue had been destroyed that it was caught up.
func unreadCount(endpoint string) (int, error) {
	name := config.Value(config.Provider)
	p, err := provider.Open(name)
	if err != nil {
		return 0, err
	}
	defer p.Close()
	// A medium that carries mail without being able to say how much is
	// waiting cannot answer this at all, and says so rather than guessing.
	l, ok := p.(provider.Listener)
	if !ok {
		return 0, fmt.Errorf("provider '%s' cannot say how much mail is waiting", name)
	}
	return l.Unread(endpoint)
}
