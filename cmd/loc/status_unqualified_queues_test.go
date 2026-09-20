package main

// The fourth thing the report says: a queue object whose name is not an
// endpoint at all (#136).
//
// The sweep reads one listing and this reads two. What the second listing
// holds is a queue made before an endpoint carried an instance name — the
// sweep cannot see one, and must not remove one, because a bare name does not
// say which deployment made it. A report is the whole remedy.

import (
	"strings"
	"testing"
)

// The fixed sentence. It is quoted here whole, because the wording is the
// product: an operator reads it and goes to the broker's own tool.
const unqualifiedQueueLine = "QUEUE_scribe: queue with an unqualified name; the sweep cannot see it; remove it with the broker's own tool"

// A stray is named, and it is named after the seats.
//
// FIRE CONTROL: the same deployment with nothing in the second listing prints
// no such line, so the line cannot come from anywhere but that listing.
func TestStatusReportsAQueueWithAnUnqualifiedName(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")

	var control strings.Builder
	if err := seatLines(&control, d.spy); err != nil {
		t.Fatalf("seatLines: %v", err)
	}
	if strings.Contains(control.String(), "unqualified") {
		t.Fatalf("control: with no such queue the report is:\n%s", control.String())
	}

	d.spy.unqualified = []string{"QUEUE_scribe"}
	var out strings.Builder
	if err := seatLines(&out, d.spy); err != nil {
		t.Fatalf("seatLines: %v", err)
	}
	if !strings.Contains(out.String(), unqualifiedQueueLine+"\n") {
		t.Errorf("the report omits %q; whole report:\n%s", unqualifiedQueueLine, out.String())
	}
	if !strings.HasPrefix(out.String(), "workshop.scribe: row ok, queue ok\n") {
		t.Errorf("a seat's row no longer comes first:\n%s", out.String())
	}
}

// The strays come last, after the queues with no row, in name order. The
// report reads top to bottom as what the next beat repairs, then what it
// leaves standing, then what it cannot see at all.
func TestStatusPutsTheUnqualifiedQueuesLast(t *testing.T) {
	d := newPresenceDeployment(t)
	wholeSeat(t, d, "workshop.scribe")
	d.spy.exists["workshop.stray"] = true
	d.spy.unqualified = []string{"QUEUE_two", "QUEUE_one"}

	var out strings.Builder
	if err := seatLines(&out, d.spy); err != nil {
		t.Fatalf("seatLines: %v", err)
	}
	report := out.String()
	order := []string{
		"workshop.scribe: row ok, queue ok\n",
		"workshop.stray: queue with no row; nothing removes it; remove it with loc unsubscribe workshop.stray\n",
		"QUEUE_one: queue with an unqualified name;",
		"QUEUE_two: queue with an unqualified name;",
	}
	at := -1
	for _, want := range order {
		i := strings.Index(report, want)
		if i < 0 {
			t.Fatalf("the report omits %q; whole report:\n%s", want, report)
		}
		if i < at {
			t.Errorf("%q came too early; whole report:\n%s", want, report)
		}
		at = i
	}
}

// THE SWEEP NEVER REMOVES ONE. This is the premise the whole change rests on:
// a cleanup touches only its own house, and an unqualified name says nothing
// about whose house the queue is in. The sweep does not even read the listing
// the strays are in, and this asserts the consequence rather than the reason.
func TestSweepNeverRemovesAQueueWithAnUnqualifiedName(t *testing.T) {
	d := newPresenceDeployment(t)
	liveRow(t, d, "workshop.scribe")
	d.spy.exists["workshop.scribe"] = true
	d.spy.unqualified = []string{"QUEUE_scribe"}
	d.spy.created, d.spy.deleted = nil, nil

	code, out, errOut := exec("sweep")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
	if len(d.spy.deleted) != 0 {
		t.Errorf("the sweep deleted %v; a queue whose name is not an endpoint is not this deployment's to remove", d.spy.deleted)
	}
	if strings.Contains(out, "QUEUE_scribe") {
		t.Errorf("the sweep spoke about a queue it cannot see:\n%s", out)
	}
}
