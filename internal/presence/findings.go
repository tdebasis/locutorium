package presence

import "sort"

// The three records a seat has, and the one function that says where they
// disagree.
//
// A seat is real when three records agree: a LEDGER ROW says it registered, a
// QUEUE on the medium says mail can reach it, and a LIVE PROCESS says somebody
// is there to take the mail. Any two of them can be true while the third is
// not — a process dies without unsubscribing, a broker restarts and loses a
// memory-backed queue, a subscribe is interrupted between its two writes — and
// each of those is a different repair.
//
// Findings READS. It writes nothing, opens nothing and decides nothing about
// what to do. That is what lets the sweep and `status` share it: one acts on
// the findings and the other prints them, and the two can never disagree about
// what is wrong, because there is one answer and both of them read it.
//
// It reports EVERY disagreement between the rows and the queues, in every
// instance. An instance is only a name here. Findings has no rule about
// instances, because the caller deletes a queue only on the positive evidence
// of a dead row, and a report costs nothing.

// FindingKind is one kind of disagreement.
type FindingKind int

const (
	// DeadPID is a row whose process is gone. Its queue, its row and its
	// server pidfile all outlived the thing they describe.
	DeadPID FindingKind = iota
	// QueueNoRow is a queue that no row claims. It is REPORTED AND NEVER
	// REMOVED. Mail sent to it is stored for a seat the ledger does not hold,
	// and an absent row does not say why it is absent: the agent left without
	// finishing, or the row itself was lost. Those two need opposite
	// treatment, so nothing here acts on either. A later `subscribe` to that
	// endpoint adopts the queue and the mail in it; `loc unsubscribe
	// <endpoint>` removes it by hand.
	QueueNoRow
	// MissingQueue is a live row with no queue. The seat is registered and
	// unreachable.
	MissingQueue
	// UnreadableRow is a row the ledger could not read. It is not a repair;
	// it is a fact about the evidence, and the operator has to fix the file.
	UnreadableRow
)

func (k FindingKind) String() string {
	switch k {
	case DeadPID:
		return "dead pid"
	case QueueNoRow:
		return "queue with no row"
	case MissingQueue:
		return "missing queue"
	case UnreadableRow:
		return "unreadable row"
	}
	return "unknown"
}

// Finding is one disagreement about one endpoint. Err is set only for
// UnreadableRow, which is the only kind that carries a reason.
type Finding struct {
	Kind     FindingKind
	Endpoint string
	Err      error
}

// Findings reports every disagreement between the rows, the queues and the
// processes.
//
// THE ORDER IS THE REPAIR ORDER. A caller that acts on the slice from first to
// last performs #27's passes in #27's sequence: dead pids first, because a reap
// destroys the queue its own row named; queues with no row second, which the
// caller only prints; missing queues last, so a queue recreated in the third
// pass is never described by the second. Unreadable rows come last of all,
// because there is nothing to act on. There is only something to say.
//
// A QUEUE WITH NO ROW IS A FINDING WHATEVER ITS INSTANCE. The caller prints it
// and removes nothing, so there is no danger to bound and no reason to hide a
// queue from the operator who asked what is here. An empty ledger against a
// broker full of queues therefore reports every queue and destroys none.
//
// AN UNREADABLE ROW DOES NOT COUNT AS A ROW, so a queue whose row could not be
// read is reported as a queue with no row, beside the unreadable row itself.
// Both facts are true and the operator needs both: the file is broken, and the
// queue standing beside it is unaccounted for until the file is fixed.
//
// A DEAD ROW IS NEVER ALSO A MISSING QUEUE. MissingQueue is a repair, and
// there is nothing to repair for a seat that is about to be reaped.
func Findings(rows []*Registration, unreadable []Unreadable, queues []string) []Finding {
	hasQueue := make(map[string]bool, len(queues))
	for _, q := range queues {
		hasQueue[q] = true
	}
	hasRow := make(map[string]bool, len(rows))
	for _, r := range rows {
		hasRow[r.Endpoint] = true
	}

	var dead, noRow, missing, unread []Finding
	for _, r := range rows {
		if !r.Alive() {
			dead = append(dead, Finding{Kind: DeadPID, Endpoint: r.Endpoint})
			continue
		}
		if !hasQueue[r.Endpoint] {
			missing = append(missing, Finding{Kind: MissingQueue, Endpoint: r.Endpoint})
		}
	}
	for _, q := range queues {
		if !hasRow[q] {
			noRow = append(noRow, Finding{Kind: QueueNoRow, Endpoint: q})
		}
	}
	for _, u := range unreadable {
		unread = append(unread, Finding{Kind: UnreadableRow, Endpoint: u.Endpoint, Err: u.Err})
	}

	out := make([]Finding, 0, len(dead)+len(noRow)+len(missing)+len(unread))
	for _, group := range [][]Finding{dead, noRow, missing, unread} {
		sort.Slice(group, func(i, j int) bool { return group[i].Endpoint < group[j].Endpoint })
		out = append(out, group...)
	}
	return out
}
