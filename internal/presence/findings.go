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

// FindingKind is one kind of disagreement.
type FindingKind int

const (
	// DeadPID is a row whose process is gone. Its queue, its row and its
	// server pidfile all outlived the thing they describe.
	DeadPID FindingKind = iota
	// OrphanQueue is a queue no row claims. Mail sent to it would be stored
	// for a seat that never registered.
	OrphanQueue
	// MissingQueue is a live row with no queue. The seat is registered and
	// unreachable.
	MissingQueue
	// UnreadableRow is a row the ledger could not read. It is not a repair;
	// it is a fact about the evidence, and it makes the orphan pass unsafe.
	UnreadableRow
)

func (k FindingKind) String() string {
	switch k {
	case DeadPID:
		return "dead pid"
	case OrphanQueue:
		return "orphan queue"
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
// last performs #27's three passes in #27's sequence: dead pids first, so the
// queues they held become orphans that the next pass collects; orphan queues
// second; missing queues last, so a queue recreated in the third pass is never
// deleted by the second. Unreadable rows come last of all, because there is
// nothing to act on — only something to say.
//
// AN UNREADABLE ROW DOES NOT COUNT AS A ROW. A queue whose row could not be
// read therefore reports as an orphan, which is exactly why the caller must
// skip the orphan pass entirely when any unreadable row exists: the listing
// cannot tell an abandoned queue from one whose owner it merely could not
// read, and only one of those two should be destroyed.
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

	var dead, orphan, missing, unread []Finding
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
			orphan = append(orphan, Finding{Kind: OrphanQueue, Endpoint: q})
		}
	}
	for _, u := range unreadable {
		unread = append(unread, Finding{Kind: UnreadableRow, Endpoint: u.Endpoint, Err: u.Err})
	}

	out := make([]Finding, 0, len(dead)+len(orphan)+len(missing)+len(unread))
	for _, group := range [][]Finding{dead, orphan, missing, unread} {
		sort.Slice(group, func(i, j int) bool { return group[i].Endpoint < group[j].Endpoint })
		out = append(out, group...)
	}
	return out
}
