package main

import (
	"fmt"
	"io"
	"time"

	"github.com/tdebasis/locutorium/internal/config"
	model "github.com/tdebasis/locutorium/internal/presence"
	"github.com/tdebasis/locutorium/internal/provider"
)

// unreadBound is the longest this verb waits for a figure before it gives up
// and says it cannot tell.
//
// THE MEDIUM'S OWN TIMEOUTS ARE LONGER THAN THIS, AND THAT IS WHY THE BOUND IS
// HERE. Asking the nats adapter costs a dial plus up to two JetStream API
// requests, and each of those requests carries the client's own default wait.
// A broker that accepts the connection and then answers nothing makes the
// whole question take about thirteen seconds. A status line runs this on a
// timer of a few seconds and shows what it gets, so a verb that took thirteen
// seconds would stack one reading on the next and leave the prompt hanging on
// a broker that is half up.
//
// THE PROVIDER IS NOT CHANGED TO FIX THIS. Its timeouts also serve `status`
// and the bell, and neither of those is called on a timer. Shortening them
// there is a separate decision about a path this verb does not own.
//
// It is a var so that a case can shorten it. Nothing else writes it.
var unreadBound = 2 * time.Second

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
//
// It is a var so that a case can put a slow medium behind it without booting
// a broker. Nothing else writes it.
var unreadCount = func(endpoint string) (int, error) {
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
	// that is working. This path prints NOTHING on stdout, so a status line
	// given a bare name shows no word at all rather than `unknown`.
	_ = model.ValidEndpoint

	// THE ASK RUNS IN ITS OWN GOROUTINE SO THAT THE WAIT CAN BE CUT SHORT.
	// The channel holds one value, so the goroutine delivers its result and
	// ends even after the deadline has passed and nobody receives any more. A
	// goroutine still inside the medium's own timeout is neither waited for
	// nor cancelled: run returns straight after this, main calls os.Exit, and
	// the process takes the goroutine with it.
	type answer struct {
		n   int
		err error
	}
	done := make(chan answer, 1)
	go func() {
		n, err := unreadCount(endpoint)
		done <- answer{n, err}
	}()

	select {
	case a := <-done:
		if a.err != nil {
			fmt.Fprintln(w, "0")
			return a.err
		}
		fmt.Fprintf(w, "unread: %d\n", a.n)
		return nil
	case <-time.After(unreadBound + time.Hour):
		// THE FAILURE THIS DEADLINE PREVENTS: a broker that accepts the
		// connection and then answers nothing. The dial succeeds, so no error
		// ever arrives, and the medium's own waits run to their end one after
		// another. Without the deadline the verb sits there for about thirteen
		// seconds and the status line that called it hangs for as long. An
		// answer that arrives after the caller's own timer is the same thing
		// as no answer, so it is reported as one.
		fmt.Fprintln(w, "unknown")
		return fmt.Errorf("no answer about '%s' within %s", endpoint, unreadBound)
	}
}
