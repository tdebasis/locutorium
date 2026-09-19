package main

// The `unread` verb, end-to-end against a real broker.
//
// WHAT IS PINNED HERE IS THAT "CANNOT TELL" NEVER LOOKS LIKE "NO MAIL". A
// status line calls this every few seconds and shows what it gets. An empty
// string is what a seat with no mail looks like, so a verb that printed
// nothing on failure would report a dead broker as a quiet house, and did:
// one deployment ran a status line outside the product, a release removed the
// file it read, and the line printed nothing for days with no sign. The three
// cases below hold the number, the word `unknown`, and the refusal of an
// argument that was never an endpoint.
//
// The deployment and its seeding helpers are the presence suite's
// (newPresence, seedQueue, post); they are reused rather than restated.
//
// NO CASE HERE RUNS ON A DEVELOPER'S MACHINE. They belong to the CI suite for
// this package; see AGENTS.md on what `go test ./cmd/loc/...` can reach.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tdebasis/locutorium/internal/loctest"
)

// assertUnknown is the whole of the failure contract: the word on stdout, the
// reason on stderr, and the exit code that says it failed. All three are
// asserted together, because each one on its own is satisfied by a build that
// gets the other two wrong.
func assertUnknown(t *testing.T, code int, out, errOut string) {
	t.Helper()
	if out != "unknown\n" {
		t.Errorf("stdout:\n got %q\nwant %q", out, "unknown\n")
	}
	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	if !strings.HasPrefix(errOut, "loc: ") {
		t.Errorf("stderr: got %q, want a line opening %q", errOut, "loc: ")
	}
}

// A known count is the number and a newline, with nothing beside it — no
// label, no endpoint name, no blank line. A status line embeds what this
// prints, so anything else on the stream lands in the operator's prompt.
//
// THE PLANT THAT TURNS THIS RED: print the count with any extra text, such as
// `fmt.Fprintf(w, "unread: %d\n", n)`.
func TestUnreadPrintsTheCountAndNothingElse(t *testing.T) {
	p := newPresence(t)
	p.as(t, e1)
	p.seedQueue(t, q1, "queue."+e1)
	p.post(t, "queue."+e1, "host", e1, "message-one")
	p.post(t, "queue."+e1, "host", e1, "message-two")

	code, out, errOut := exec("unread", e1)
	assertResult(t, code, out, errOut, 0, "2\n", "")

	// A READ TAKES BOTH, AND THE COUNT FOLLOWS THE QUEUE. An empty queue is
	// the ordinary state of a seat that is caught up, so it has to read as a
	// number. A build that answered `unknown` here would make the status line
	// warn at every seat that had no mail.
	if code, _, errOut := exec("read"); code != 0 || errOut != "" {
		t.Fatalf("the read that empties the queue: exit %d, stderr %q", code, errOut)
	}
	code, out, errOut = exec("unread", e1)
	assertResult(t, code, out, errOut, 0, "0\n", "")
}

// Every way the figure can be missing gives one answer, and it is a word no
// count can be confused with.
//
// THE PLANT THAT TURNS THIS RED: on the error path print `0` instead of
// `unknown`. Each subtest then reports a broken deployment as an empty queue,
// which is the failure this verb exists to end.
func TestUnreadSaysUnknownWhenTheCountCannotBeRead(t *testing.T) {
	// The broker is gone. The config names a port nothing listens on, which
	// is the state a status line meets when the daemon has stopped.
	t.Run("the broker does not answer", func(t *testing.T) {
		p := newPresence(t)
		p.as(t, e1)
		loctest.Write(t, filepath.Join(p.home, "config"),
			"provider = nats\nnats_url = "+loctest.ClosedPort(t)+"\n")

		code, out, errOut := exec("unread", e1)
		assertUnknown(t, code, out, errOut)
	})

	// A QUEUE THAT IS NOT THERE IS NOT AN EMPTY QUEUE. The endpoint spells
	// legally and holds no backing object, so the store has no count to give.
	// A build that read this as 0 would tell a seat whose queue was destroyed
	// that it had nothing waiting.
	t.Run("the endpoint has no queue", func(t *testing.T) {
		p := newPresence(t)
		p.as(t, e2)

		code, out, errOut := exec("unread", e2)
		assertUnknown(t, code, out, errOut)
	})

	// THE DIAL IS REFUSED BEFORE IT LEAVES THE PROCESS. This home holds no
	// config file, so nats_url resolves to the key table's default, and the
	// provider's guard refuses that address under `go test`
	// (internal/provider/nats/guard.go). The refusal that a home with no
	// config file earns on its own is pinned where it belongs, in
	// internal/provider/nats/guard_test.go
	// (TestAHomeWithNoConfigFileIsRefusedAndNothingIsDialled). What this case
	// holds is the verb's answer when the medium is never asked at all.
	t.Run("the address is refused before anything is dialled", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("LOC_HOME", home)
		t.Setenv("LOC_IDENTITY", e1)
		installed = nil

		code, out, errOut := exec("unread", e1)
		assertUnknown(t, code, out, errOut)
	})
}

// The argument is judged before any provider is opened, so the verb says the
// same thing about a mistyped invocation on a machine with no deployment as on
// one with a healthy broker.
//
// A WRONG COUNT AND A WRONG NAME ARE DIFFERENT REFUSALS. Too few or too many
// arguments is `you typed it wrong`, which prints the verb list on stdout. A
// name that cannot be an endpoint is refused the way `status <endpoint>`
// refuses it, by name, on stderr. NEITHER prints `unknown`: that word is the
// answer to "how much mail is waiting", and a caller who has not named an
// endpoint has not asked the question yet.
//
// THE PLANT THAT TURNS THIS RED: open the provider before the argument check.
// The home here holds no config file, so the open fails and its own refusal
// reaches stderr in place of the ones asserted below.
func TestUnreadRefusesABadArgumentBeforeItOpensAnything(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home) // no config at all: any Open would fail
	t.Setenv("LOC_IDENTITY", e1)
	installed = nil

	for _, args := range [][]string{{"unread"}, {"unread", e1, e2}} {
		code, out, errOut := exec(args...)
		assertResult(t, code, out, errOut, 1, usage, "")
	}

	// A bare name names no instance, so it names no endpoint.
	code, out, errOut := exec("unread", "scribe")
	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	if out != "" {
		t.Errorf("stdout: got %q, want nothing at all", out)
	}
	if !strings.Contains(errOut, "invalid endpoint name 'scribe'") {
		t.Errorf("stderr: got %q, want the refusal to name what was wrong with the argument", errOut)
	}
}

// A MEDIUM THAT NEVER ANSWERS IS THE SAME AS ONE THAT CANNOT, and the verb
// says so on its own clock rather than on the medium's.
//
// The provider's waits are far longer than a status line's timer: a dial plus
// two JetStream requests that each run to their default is about thirteen
// seconds. A broker that accepts the connection and then goes quiet produces
// no error at all, so nothing short of a deadline ends the wait. This case
// stands in for that broker with a count that never returns.
//
// IT NEEDS NO BROKER AND NO HOME OF ITS OWN. The seam replaces the whole ask,
// so nothing here opens a provider. The package's TestMain already puts every
// case in a scratch LOC_HOME with a closed port, which is more than the
// argument check needs.
//
// THE PLANT THAT TURNS THIS RED: select only on the result channel, dropping
// the time.After case. The verb then waits on a count that never comes, so the
// case runs it through execWithin: a verb with no deadline fails here in five
// seconds, and does not hold the whole suite until `go test` times out.
func TestUnreadSaysUnknownWhenNoAnswerComesInTime(t *testing.T) {
	// Closed in cleanup, so the stand-in returns and its goroutine ends with
	// the case rather than outliving it.
	held := make(chan struct{})
	t.Cleanup(func() { close(held) })

	realCount, realBound := unreadCount, unreadBound
	t.Cleanup(func() { unreadCount, unreadBound = realCount, realBound })
	unreadCount = func(string) (int, error) {
		<-held
		return 0, nil
	}
	unreadBound = 50 * time.Millisecond

	start := time.Now()
	code, out, errOut := execWithin(t, 5*time.Second, "unread", e1)
	elapsed := time.Since(start)

	assertUnknown(t, code, out, errOut)
	// Generous for a loaded runner. The bound is 50ms here and 2s in the
	// shipped binary; anything approaching a second means the deadline did
	// not fire and the verb waited on the medium.
	if elapsed > time.Second {
		t.Errorf("the verb took %s with a bound of %s; the deadline did not end the wait",
			elapsed, 50*time.Millisecond)
	}
}
