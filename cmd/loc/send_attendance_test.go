package main

// The say-semantics gate on a presence medium.
//
// `send_requires_attendance = yes` asks one question — is the peer there? — but
// the shipping gate answers it by reading a listener pidfile, which only the
// shell tool's deployment shape ever writes. A seat that joined through the
// presence model (`subscribe`) runs no such listener and leaves no pidfile, so
// on a presence provider the gate refuses EVERY send to an attending peer, one
// line after the same send's QueueExists check confirmed that peer's live
// queue. Observed on a live deployment during the first Go-bus test:
//
//	loc: not attending: '<endpoint>' has no live listener
//	(say-semantics: a send expects an attending peer; ...)
//
// On a medium that carries presence, attendance IS the live queue
// (docs/PRESENCE.md §Queue lifetime; §Inner and outer parlors), so the question
// the config key asks is already answered — and answered from a live fact
// rather than a file. These two cases pin both halves of that: the attending
// peer is reached, and the absent one is still refused for ABSENCE.
//
// The gate's behaviour on a provider WITHOUT presence is unchanged and stays
// pinned by TestSendRequiresAttendanceOnlyWhenConfigured in main_test.go.

import (
	"path/filepath"
	"testing"

	"github.com/tdebasis/locutorium/internal/loctest"
)

// gateOn rewrites the presence deployment's config with the say-semantics key
// turned on — same file newPresence writes, one line added.
func (p *presence) gateOn(t *testing.T) {
	t.Helper()
	loctest.Write(t, filepath.Join(p.home, "config"),
		"provider = nats\nnats_url = "+p.url+"\nmonitor_url = "+p.monitorURL+
			"\ntopic_window = 7d\nsend_requires_attendance = yes\n")
}

// A subscribed peer IS attending: its queue is live, which is the whole of what
// the key asks for on this medium. The send must go through.
func TestPresence_SendRequiresAttendance_SubscribedPeerIsAttending(t *testing.T) {
	p := newPresence(t)
	p.gateOn(t)
	p.as(t, "host")

	exec("subscribe", e1, "--pid", pidStr(livePid(t)), "--type", "acme-cli", "--version", "3.2.0")
	if !p.qexists(q1) {
		t.Fatalf("precondition: subscribe must leave a live queue %s", q1)
	}

	code, out, errOut := exec("send", e1, "to-an-attending-peer")
	assertResult(t, code, out, errOut, 0, "sent → queue."+e1+"\n", "")
	if p.qcount(q1) != 1 {
		t.Errorf("the accepted send must be in %s; have %d", q1, p.qcount(q1))
	}
}

// With the key on, an UNSUBSCRIBED peer is still refused — and refused for
// absence, on standard error only, with nothing on standard out. This is the
// existing behaviour, pinned so the fix cannot turn the gate into a no-op.
func TestPresence_SendRequiresAttendance_UnsubscribedPeerIsRefusedForAbsence(t *testing.T) {
	p := newPresence(t)
	p.gateOn(t)
	p.as(t, "host")

	if p.qexists(q2) {
		t.Fatalf("precondition: an unsubscribed endpoint must have no backing queue")
	}
	checkOnStderr(t, "absent|no live|no subscription|no queue|nobody|not attending",
		"send", e2, "into the void")
	if p.qexists(q2) {
		t.Errorf("a refused send created %s; nothing may accumulate in the inner parlor", q2)
	}
}
