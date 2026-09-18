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
// The ledger's own instances bound what a sweep may touch. Every endpoint is
// `<instance>.<agent>`, several deployments may share one broker, and only the
// instances this ledger holds a row for are this ledger's business.

// FindingKind is one kind of disagreement.
type FindingKind int

const (
	// DeadPID is a row whose process is gone. Its queue, its row and its
	// server pidfile all outlived the thing they describe.
	DeadPID FindingKind = iota
	// OrphanQueue is a queue no row claims, in an instance this ledger holds
	// a row for. Mail sent to it would be stored for a seat that never
	// registered.
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
// last performs #27's three passes in #27's sequence: dead pids first, because
// a reap destroys the queue its own row named; orphan queues second; missing
// queues last, so a queue recreated in the third pass is never deleted by the
// second. Unreadable rows come last of all, because there is nothing to act
// on. There is only something to say.
//
// AN UNREADABLE ROW DOES NOT COUNT AS A ROW. A queue whose row could not be
// read therefore reports as an orphan, which is exactly why the caller must
// skip the orphan pass entirely when any unreadable row exists: the listing
// cannot tell an abandoned queue from one whose owner it merely could not
// read, and only one of those two should be destroyed.
//
// A QUEUE IN AN INSTANCE THIS LEDGER DOES NOT HOLD IS NOT A FINDING. The
// instances its own rows name are the boundary, and a queue outside them is
// never reported and never deleted. An empty or vanished ledger therefore
// reaps nothing, and that is the case that matters: a process pointed at the
// wrong broker, holding no rows at all, would otherwise delete every queue it
// could see. An unreadable row still makes its instance owned, so the danger
// an unreadable row carries stays visible to the caller.
//
// THE BOUNDARY HAS A COST. When the LAST seat of an instance leaves
// uncleanly, no row is left to hold that instance, so its queue is not reaped.
// `loc unsubscribe <endpoint>` removes such a queue by hand, and a
// memory-backed queue ends with the broker.
//
// A DEAD ROW IS NEVER ALSO A MISSING QUEUE. MissingQueue is a repair, and
// there is nothing to repair for a seat that is about to be reaped.
func Findings(rows []*Registration, unreadable []Unreadable, queues []string) []Finding {
	hasQueue := make(map[string]bool, len(queues))
	for _, q := range queues {
		hasQueue[q] = true
	}
	hasRow := make(map[string]bool, len(rows))
	// owned is the set of instances this ledger holds a row for. The instance
	// is taken from the ENDPOINT and never from the row's stored Instance
	// field: a row file's contents are not validated against its name, so the
	// field could claim an instance the endpoint does not belong to.
	owned := map[string]bool{}
	for _, r := range rows {
		hasRow[r.Endpoint] = true
		if i := Instance(r.Endpoint); i != "" {
			owned[i] = true
		}
	}
	// A row the ledger could not read still holds its instance, so the queue
	// it may claim is still reported and the caller can still see the danger.
	for _, u := range unreadable {
		if i := Instance(u.Endpoint); i != "" {
			owned[i] = true
		}
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
		if !hasRow[q] && owned[Instance(q)] {
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
