package main

// Presence integration suite — the Go equivalent of conformance/presence.sh,
// driving the CLI's run() end-to-end against a real embedded broker.
//
// This is commit A of a test-first change: it defines docs/PRESENCE.md's
// three-question presence model as the CLI must come to implement it, and it
// is RED by construction against the build that exists today. The presence
// verbs — subscribe / unsubscribe / sweep / emit / registry / watch and the
// per-endpoint `status` — are not dispatched yet: they return errUsage or
// errNotImplemented, or (for `status`) the old unread-count report. So every
// case here compiles against the shipping run() and FAILS at runtime, with no
// stubs required. `go build ./...` stays green because these are _test.go.
//
// The red is MEANINGFUL, not incidental. The current build fails every unbuilt
// verb the same generic way — the verb list on stdout for a usage error, or
// "loc: not implemented in this build" on stderr — so a case that merely
// demanded a non-zero exit would pass on that generic failure and prove
// nothing. checkRefusal and checkOnStderr (below) are the Go equivalents of the
// shell suite's load-bearing helpers: a refusal counts only when the exit is
// non-zero AND standard error carries the SPECIFIC reason the spec's wording
// implies (and, for checkOnStderr, standard out is empty). For the
// destroy/--force cases the backing stream is stood up by hand first, so a red
// line reads "the verb failed to destroy it", never the vacuous "there was
// nothing there".
//
// Effects are asserted through an INDEPENDENT admin observer (stream existence
// and message counts via JetStream; events via a live core `presence.>` witness),
// never through the code under test. Events are historyless — the bus stores
// nothing — so they are caught as they fly, exactly as a real consumer would.
//
// Everything here is deployment-neutral: generic instances (workshop/atelier),
// a generic agent type (acme-cli), and the generic bus roles host / watch /
// admin. No real names, credentials, or paths appear.
//
// Harness sharing: the embedded-server + $LOC_HOME + admin-observer primitives
// live in internal/loctest, shared with internal/provider/nats's own tests;
// the presence-shaped deployment (the ACL user set, the event witness, the
// seed helpers) is built on those primitives here, next to the cases that use
// it. See conformance/PRESENCE-GOTEST-NOTES.md.

import (
	"encoding/json"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/loctest"
)

// The presence deployment's endpoints and their injective backing objects.
// An endpoint is namespaced <instance>.<agent>; the backing object substitutes
// dots for underscores (PRESENCE.md §Subjects, endpoints and queues).
const (
	e1 = "workshop.scribe" // instance: workshop
	q1 = "QUEUE_workshop_scribe"
	e2 = "workshop.clerk"
	q2 = "QUEUE_workshop_clerk"
	e3 = "atelier.scribe" // instance: atelier — same agent, other instance
	q3 = "QUEUE_atelier_scribe"
	// A SEAT WHOSE GRANTS PREDATE THE EVENTS PLANE. Same instance as e1/e2,
	// but its access control is the one a live deployment generated before
	// events moved off `topic.>` — so the server refuses its join event and
	// the suite can hold what a refused event does to the verb that emitted it.
	eOld = "workshop.legacy"
	qOld = "QUEUE_workshop_legacy"

	presencePassword = "presence-scratch"
)

// ── the presence deployment ─────────────────────────────────────────────────

// presence is one scratch presence-model deployment: a broker with the ACL user
// set the model needs, a $LOC_HOME configured for `provider = nats`, and an
// independent admin connection the cases assert through.
type presence struct {
	home       string
	url        string
	monitorURL string
	admin      *natsgo.Conn
	adminJS    natsgo.JetStreamContext
}

// presenceUsers is the access-control block, adapted from
// providers/nats/bootstrap.sh with the model's differences: endpoints are
// namespaced (queue subjects have two tokens after `queue`, ACLs use `queue.>`),
// backing-object names substitute dots for underscores, a `host` supervisor
// exists alongside the read-only `watch`, and each endpoint may create and
// delete its own queue stream because subscribe creates the queue and
// unsubscribe destroys it.
func presenceUsers() []*natsserver.User {
	users := []*natsserver.User{
		// admin: full rights, and the identity every assertion observes through.
		{Username: "admin", Password: presencePassword},
		// host: the supervisor. It launches agents (so it holds their pids),
		// subscribes and unsubscribes them, sweeps, answers the registry and
		// sends on their behalf. Broad rights, because it is trusted
		// (PRESENCE.md §Trust, in the inner parlor).
		{Username: "host", Password: presencePassword, Permissions: &natsserver.Permissions{
			Publish:   &natsserver.SubjectPermission{Allow: []string{"queue.>", "topic.>", "presence.>", "registry.>", "$JS.>"}},
			Subscribe: &natsserver.SubjectPermission{Allow: []string{"queue.>", "topic.>", "presence.>", "registry.>", "_INBOX.>"}},
		}},
		// watch: read-only. It may follow every queue and event stream and may
		// publish nothing at all (PRESENCE.md §Command-line operations → watch,
		// §Trust — the watch role publishes nothing).
		{Username: "watch", Password: presencePassword, Permissions: &natsserver.Permissions{
			Subscribe: &natsserver.SubjectPermission{Allow: []string{"queue.>", "topic.>", "presence.>", "registry.>", "_INBOX.>"}},
			Publish:   &natsserver.SubjectPermission{Deny: []string{">"}},
		}},
	}
	for _, e := range []string{e1, e2, e3, eOld} {
		s := strings.ReplaceAll(e, ".", "_") // workshop.scribe -> workshop_scribe
		inst := strings.SplitN(e, ".", 2)[0] // workshop.scribe -> workshop
		// THE EVENTS PLANE, AND ONE SEAT THAT NEVER GOT IT. A seat emits its
		// own events, so bootstrap.sh grants it its instance's events subject.
		// eOld is left with the pre-split grant alone, which is what makes the
		// refused-publish case a real refusal rather than a mock.
		events := "presence." + inst
		if e == eOld {
			events = "topic." + inst
		}
		users = append(users, &natsserver.User{Username: e, Password: presencePassword, Permissions: &natsserver.Permissions{
			Publish: &natsserver.SubjectPermission{Allow: []string{
				"queue.>", "topic.>", "registry.>", events,
				"$JS.API.INFO",
				"$JS.API.STREAM.CREATE.QUEUE_" + s, "$JS.API.STREAM.DELETE.QUEUE_" + s,
				"$JS.API.STREAM.INFO.QUEUE_" + s, "$JS.API.STREAM.NAMES", "$JS.API.STREAM.LIST",
				"$JS.API.CONSUMER.DURABLE.CREATE.QUEUE_" + s + "." + s,
				"$JS.API.CONSUMER.CREATE.QUEUE_" + s, "$JS.API.CONSUMER.CREATE.QUEUE_" + s + ".>",
				"$JS.API.CONSUMER.INFO.QUEUE_" + s + "." + s,
				"$JS.API.CONSUMER.MSG.NEXT.QUEUE_" + s + "." + s,
				"$JS.ACK.QUEUE_" + s + ".>",
			}},
			Subscribe: &natsserver.SubjectPermission{Allow: []string{"queue." + e, "topic." + inst, events, "registry." + inst, "_INBOX.>"}},
		}})
	}
	return users
}

// newPresence boots a presence deployment for one test and returns it wired up.
// No host process is run: the registry is host-held (PRESENCE.md §Who is here
// right now), and its absence is one of the things under test.
func newPresence(t *testing.T) *presence {
	t.Helper()
	home := t.TempDir()
	users := presenceUsers()
	srv := loctest.Boot(t, users, true /* monitor: registry/status read connection state from it */)

	loctest.Write(t, filepath.Join(home, "config"),
		"provider = nats\nnats_url = "+srv.URL+"\nmonitor_url = "+srv.MonitorURL+"\ntopic_window = 7d\n")
	// The static registry file is a message-plane artifact, not part of the
	// presence model; it is written only so the shipping `send` reaches the
	// medium at all. A presence-model send derives attendance from live
	// subscriptions, not from this file.
	loctest.Write(t, filepath.Join(home, "endpoints"), strings.Join([]string{e1, e2, e3}, "\n")+"\n")
	for _, u := range users {
		loctest.Write(t, filepath.Join(home, "creds", u.Username), presencePassword)
	}
	t.Setenv("LOC_HOME", home)
	// The spy provider from main_test.go is registered under "spy"; this
	// deployment names "nats", so it is never selected. Cleared defensively.
	installed = nil

	nc, js := srv.Admin(t, "admin", presencePassword)
	t.Cleanup(nc.Close)
	return &presence{home: home, url: srv.URL, monitorURL: srv.MonitorURL, admin: nc, adminJS: js}
}

// as sets the identity the next exec() speaks as — the endpoint the semantics
// layer authenticates. Host for supervisor verbs, watch for the reader.
func (p *presence) as(t *testing.T, id string) { t.Helper(); t.Setenv("LOC_IDENTITY", id) }

// witness opens a live core subscription so events can be caught as they fly:
// the bus keeps no history, so nothing can be inspected after the fact. A core
// subscription sees a message whether it was published plainly or through
// JetStream, so it does not presume how the implementation publishes.
func (p *presence) witness(t *testing.T, subject string) *natsgo.Subscription {
	t.Helper()
	sub, err := p.admin.SubscribeSync(subject)
	if err != nil {
		t.Fatalf("witness subscribe %s: %v", subject, err)
	}
	if err := p.admin.Flush(); err != nil {
		t.Fatalf("witness flush: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return sub
}

// collect drains a witness for up to d, returning the raw payloads seen.
func collect(sub *natsgo.Subscription, d time.Duration) []string {
	var out []string
	deadline := time.Now().Add(d)
	for {
		remain := time.Until(deadline)
		if remain <= 0 {
			break
		}
		m, err := sub.NextMsg(remain)
		if err != nil {
			break
		}
		out = append(out, string(m.Data))
	}
	return out
}

// seedQueue stands a backing queue up by hand, as a real subscribe would have.
// The destroy/--force cases need a queue that EXISTS before the verb runs, so a
// red line reads "the verb failed to destroy it", not "there was nothing to
// destroy" — which would pass on any build that does nothing at all.
func (p *presence) seedQueue(t *testing.T, stream, subject string) {
	t.Helper()
	if _, err := p.adminJS.AddStream(&natsgo.StreamConfig{
		Name:      stream,
		Subjects:  []string{subject},
		Retention: natsgo.WorkQueuePolicy,
		Storage:   natsgo.MemoryStorage,
		Replicas:  1,
	}); err != nil {
		t.Fatalf("seed stream %s: %v", stream, err)
	}
}

func (p *presence) qexists(stream string) bool {
	_, err := p.adminJS.StreamInfo(stream)
	return err == nil
}

// qcount is the message count of a backing stream, or -1 when the stream does
// not exist — the difference between "a queue is here holding one" and "there
// is no queue", which is the whole ephemeral-queue distinction.
func (p *presence) qcount(stream string) int64 {
	info, err := p.adminJS.StreamInfo(stream)
	if err != nil {
		return -1
	}
	return int64(info.State.Msgs)
}

// ── meaningful-red helpers (Go equivalents of check_refusal / check_on_stderr)

// checkRefusal asserts the invocation FAILED for the specific spec reason: a
// non-zero exit AND standard error matching pat (case-insensitive). A generic
// usage error (its reason is on stdout, not stderr) or the build's
// "not implemented" line cannot satisfy it — which is exactly what keeps the
// current build's failures from spuriously passing a refusal case.
func checkRefusal(t *testing.T, pat string, args ...string) {
	t.Helper()
	code, _, errOut := exec(args...)
	if code == 0 || !regexp.MustCompile("(?i)"+pat).MatchString(errOut) {
		t.Errorf("want a refusal naming its reason (non-zero exit AND stderr ~ /%s/); got exit=%d stderr=%q", pat, code, errOut)
	}
}

// checkOnStderr adds the channel discipline of §Exit codes: the reason belongs
// on standard error and NOTHING is printed on standard out — the opposite of a
// usage banner, which is stdout for someone still learning the invocation.
func checkOnStderr(t *testing.T, pat string, args ...string) {
	t.Helper()
	code, out, errOut := exec(args...)
	if code == 0 || out != "" || !regexp.MustCompile("(?i)"+pat).MatchString(errOut) {
		t.Errorf("want a stderr-only refusal (non-zero, empty stdout, stderr ~ /%s/); got exit=%d stdout=%q stderr=%q",
			pat, code, out, errOut)
	}
}

// checkRefusalNamingIncumbent asserts a refusal that names the incumbent
// CONCRETELY: a non-zero exit, the incumbent's pid digits on stderr, AND a
// start-time-shaped token. A bare "already held" with no pid cannot satisfy it —
// which is the whole point, since the spec says the refusal names the incumbent's
// pid AND start time (PRESENCE.md §One agent per endpoint; PR #12/#16 text). The
// exact start-time rendering is the implementation's to choose, so only its shape
// is asserted, not a byte-exact value.
var startTimeShape = regexp.MustCompile(`\b(19|20)\d\d\b|\d{4}-\d{2}-\d{2}T`)

func checkRefusalNamingIncumbent(t *testing.T, pid int, args ...string) {
	t.Helper()
	code, _, errOut := exec(args...)
	if code == 0 || !strings.Contains(errOut, pidStr(pid)) || !startTimeShape.MatchString(errOut) {
		t.Errorf("want a refusal naming the incumbent's pid %d AND its start time; got exit=%d stderr=%q",
			pid, code, errOut)
	}
}

// execWithin runs exec() under a deadline. `emit` is the model's sole
// non-blocking verb (PRESENCE.md §Failure and degradation): it must return
// immediately even against a dead broker, so a blocking or retrying impl has to
// go RED here rather than hang the suite.
func execWithin(t *testing.T, d time.Duration, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	type res struct {
		code           int
		stdout, stderr string
	}
	ch := make(chan res, 1)
	go func() {
		c, o, e := exec(args...)
		ch <- res{c, o, e}
	}()
	select {
	case r := <-ch:
		return r.code, r.stdout, r.stderr
	case <-time.After(d):
		t.Fatalf("emit did not return within %s — a blocking impl must go red, not hang", d)
		return 0, "", ""
	}
}

// waitFor polls cond until it holds or d elapses.
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

// safeBuffer is a writer safe for the run()-in-a-goroutine reader verbs.
type safeBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *safeBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}
func (w *safeBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// errAfterWriter is a reader that goes away after `after` writes — the shape of
// a `loc read | head -c 1` dying mid-render.
type errAfterWriter struct{ after int }

func (w *errAfterWriter) Write(p []byte) (int, error) {
	if w.after <= 0 {
		return 0, io.ErrClosedPipe
	}
	w.after--
	return len(p), nil
}

// livePid is a process certainly alive for the test's duration: this one.
func livePid(t *testing.T) int { t.Helper(); return os.Getpid() }

// deadPid is a process id that is certainly gone: a child started and reaped.
func deadPid(t *testing.T) int {
	t.Helper()
	c := osexec.Command("sh", "-c", "exit 0")
	if err := c.Start(); err != nil {
		t.Fatalf("start throwaway process: %v", err)
	}
	pid := c.Process.Pid
	_ = c.Wait() // reaped; the number is now free
	return pid
}

// findEvent returns the first captured event of the given kind.
func findEvent(t *testing.T, events []string, kind string) (map[string]any, bool) {
	t.Helper()
	for _, e := range events {
		var m map[string]any
		if json.Unmarshal([]byte(e), &m) == nil && m["kind"] == kind {
			return m, true
		}
	}
	return nil, false
}

// itoa renders a pid the way an event or a message body would carry it.
func pidStr(pid int) string { return strconv.Itoa(pid) }

// ════════════════════════════════════════════════════════════════════════════
// 1. Namespaced endpoints; injective backing name; no collision
//    PRESENCE.md §Subjects, endpoints and queues → "Endpoint names are
//    namespaced by instance" and "A note for implementers".
// ════════════════════════════════════════════════════════════════════════════

// subscribe is what brings a queue into being; the same agent in two instances
// must back two DISTINCT objects, a collision made impossible by construction.
func TestPresence_Subscribe_CreatesInjectiveBackingObjects(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")

	exec("subscribe", e1, "--pid", pidStr(livePid(t)), "--type", "acme-cli", "--version", "3.2.0")
	exec("subscribe", e3, "--pid", pidStr(livePid(t)), "--type", "acme-cli", "--version", "3.2.0")

	if !p.qexists(q1) {
		t.Errorf("subscribe did not create %s (dots→underscores backing object)", q1)
	}
	if !(p.qexists(q1) && p.qexists(q3) && q1 != q3) {
		t.Errorf("the same agent in two instances must back two distinct objects; have %s=%v %s=%v",
			q1, p.qexists(q1), q3, p.qexists(q3))
	}
}

// Mail for one instance's scribe must never fall into the other's queue —
// namespacing makes the collision impossible by construction. PRESENCE.md
// §Subjects, endpoints and queues → "Endpoint names are namespaced by instance".
func TestPresence_Send_ReachesOnlyItsOwnQueue(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")

	exec("subscribe", e1, "--pid", pidStr(livePid(t)), "--type", "acme-cli", "--version", "3.2.0")
	exec("subscribe", e3, "--pid", pidStr(livePid(t)), "--type", "acme-cli", "--version", "3.2.0")
	exec("send", e1, "for-workshop-scribe-only")

	if p.qcount(q1) != 1 || p.qcount(q3) != 0 {
		t.Errorf("a send to %s must reach only its queue; have %s=%d %s=%d",
			e1, q1, p.qcount(q1), q3, p.qcount(q3))
	}
}

// Character set (refinement): an endpoint carrying the barred `_` is refused
// non-zero, with a reason — it is what keeps the dots→underscores substitution
// injective. PRESENCE.md §Subjects, endpoints and queues ("the underscore
// barred … injective by construction").
func TestPresence_Subscribe_RefusesUnderscoreInEndpoint(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	checkRefusal(t, "invalid|character|underscore|endpoint name|namespac|barred|must match",
		"subscribe", "workshop.scr_ibe", "--pid", pidStr(livePid(t)), "--type", "acme-cli", "--version", "3.2.0")
}

// A FLAG GIVEN WITH AN EMPTY VALUE IS REFUSED, so that absent and empty stay
// distinguishable. Optional fields are `omitempty` strings: `--address ""` and
// no `--address` produce identical JSON and identical values after unmarshal,
// so without this a consumer cannot tell "this deployment predates addressing"
// from "somebody set it to nothing". An empty cwd is only a missing detail; an
// empty delivery address is an endpoint nothing can reach, written down as
// though it were configured — and a registration outlives the process that
// wrote it, so a later reader inherits the ambiguity.
//
// The last two arms are the control, and without them this test cannot fail in
// the direction that matters: a parser that refused every flag would pass the
// refusal arms alone, so "refuses everything" and "refuses correctly" would be
// the same green.
func TestPresence_Subscribe_RefusesAFlagGivenWithNoValue(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	pid := pidStr(livePid(t))

	// EACH ARM STANDS ALONE. A refusal arm must not register anything — that
	// is the whole point — but if the refusal regresses it WILL register, and
	// then every later arm fails with "endpoint is held" instead of its own
	// reason. Clearing between arms keeps a failure pointing at the arm that
	// actually failed. (Observed: without this, reverting the fix produced
	// three failures, two of them noise.)
	clear := func() { exec("unsubscribe", e2, "--force") }

	// The field this was found on: an address that cannot be delivered to.
	checkRefusal(t, "no value|empty|omit the flag",
		"subscribe", e2, "--pid", pid, "--type", "acme-cli", "--version", "3.2.0", "--address", "")
	clear()

	// The same defect on a field where it had always been latent and harmless.
	checkRefusal(t, "no value|empty|omit the flag",
		"subscribe", e2, "--pid", pid, "--type", "acme-cli", "--version", "3.2.0", "--cwd", "")
	clear()

	// CONTROL 1: a real value is still accepted and still lands.
	if code, _, errOut := exec("subscribe", e2, "--pid", pid, "--type", "acme-cli",
		"--version", "3.2.0", "--address", "workshop:1.2"); code != 0 {
		t.Fatalf("a non-empty --address must still be accepted; got exit=%d stderr=%q", code, errOut)
	}
	exec("unsubscribe", e2, "--force")

	// CONTROL 2: OMITTING the flag is not the same as passing it empty, and
	// must stay legal. This is the arm that would catch a fix which refused
	// absence along with emptiness.
	if code, _, errOut := exec("subscribe", e2, "--pid", pid, "--type", "acme-cli",
		"--version", "3.2.0"); code != 0 {
		t.Fatalf("omitting --address must remain legal; got exit=%d stderr=%q", code, errOut)
	}
	exec("unsubscribe", e2, "--force")
}

// ════════════════════════════════════════════════════════════════════════════
// 2. subscribe registers, creates the queue, publishes agent.subscribe
//    PRESENCE.md §How an agent joins; §Command-line operations → subscribe;
//    §Events → agent.subscribe carries the full registration.
// ════════════════════════════════════════════════════════════════════════════

// The join event is the only heavy event. It must carry the FULLY-QUALIFIED
// endpoint (workshop.clerk, never bare clerk) and the registration: type,
// display, cwd and pid. PRESENCE.md §Events → agent.subscribe payload.
func TestPresence_Subscribe_PublishesFullyQualifiedJoinEvent(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	pid := livePid(t)
	sub := p.witness(t, "presence.workshop")

	exec("subscribe", e2, "--pid", pidStr(pid), "--type", "acme-cli", "--version", "3.2.0",
		"--display", "The Clerk", "--cwd", "/workspaces/clerk", "--address", "workshop:1.2")

	events := collect(sub, 2*time.Second)
	m, ok := findEvent(t, events, "agent.subscribe")
	if !ok {
		t.Fatalf("subscribe published no agent.subscribe event on presence.workshop; saw %v", events)
	}
	if m["endpoint"] != e2 {
		t.Errorf("join event endpoint = %v, want the fully-qualified %q", m["endpoint"], e2)
	}
	agent, _ := m["agent"].(map[string]any)
	if agent == nil || agent["type"] != "acme-cli" || agent["version"] != "3.2.0" {
		t.Errorf("join event carries the wrong agent.type/version; got %v, want type acme-cli version 3.2.0", m["agent"])
	}
	proc, _ := m["process"].(map[string]any)
	if proc == nil || proc["pid"] != float64(pid) {
		t.Errorf("join event carries no process.pid=%d; got %v", pid, m["process"])
	}
	if started, _ := proc["started"].(string); started == "" {
		t.Errorf("join event carries no non-empty process.started; got %v", m["process"])
	}
	// PR #16: the convenience `instance` field must equal the endpoint prefix.
	if m["instance"] != "workshop" {
		t.Errorf("join event instance = %v, want the endpoint prefix %q", m["instance"], "workshop")
	}
	// The display and cwd VALUES passed on the command line must survive, not
	// merely the keys. display may be a string or a {name,...} object, so the
	// value is sought within it rather than pinned to a shape the spec leaves open.
	if dj, _ := json.Marshal(m["display"]); !strings.Contains(string(dj), "The Clerk") {
		t.Errorf("join event display does not carry the value passed (%q); got %v", "The Clerk", m["display"])
	}
	if m["cwd"] != "/workspaces/clerk" {
		t.Errorf("join event cwd = %v, want the value passed %q", m["cwd"], "/workspaces/clerk")
	}
	if m["address"] != "workshop:1.2" {
		t.Errorf("join event address = %v, want the value passed %q", m["address"], "workshop:1.2")
	}
}

// subscribe on a free endpoint exits 0 and creates the queue.
func TestPresence_Subscribe_ExitsZeroAndCreatesQueue(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")

	code, _, errOut := exec("subscribe", e2, "--pid", pidStr(livePid(t)), "--type", "acme-cli", "--version", "3.2.0")
	if code != 0 {
		t.Errorf("subscribe on a free endpoint exited %d (stderr %q), want 0", code, errOut)
	}
	if !p.qexists(q2) {
		t.Errorf("subscribe did not create the clerk's queue %s", q2)
	}
}

// ════════════════════════════════════════════════════════════════════════════
// 3. unsubscribe destroys the queue and publishes agent.unsubscribe(clean)
//    PRESENCE.md §How an agent leaves (reason: clean); §Queue lifetime; §CLI →
//    unsubscribe defaults to clean.
// ════════════════════════════════════════════════════════════════════════════

func TestPresence_Unsubscribe_DestroysQueueAndEmitsClean(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	// Stand the queue up first, so "destroyed" measures the verb's effect and
	// not the accident that it was never there.
	p.seedQueue(t, q2, "queue."+e2)
	sub := p.witness(t, "presence.workshop")

	code, _, errOut := exec("unsubscribe", e2, "--force")
	if code != 0 {
		t.Errorf("unsubscribe --force on a held endpoint exited %d (stderr %q), want 0", code, errOut)
	}
	if p.qexists(q2) {
		t.Errorf("unsubscribe did not destroy %s (nothing may outlive the subscription)", q2)
	}
	m, ok := findEvent(t, collect(sub, 2*time.Second), "agent.unsubscribe")
	if !ok {
		t.Fatalf("unsubscribe published no agent.unsubscribe event")
	}
	if m["reason"] != "clean" {
		t.Errorf("agent.unsubscribe reason = %v, want the default %q", m["reason"], "clean")
	}
	if m["endpoint"] != e2 {
		t.Errorf("agent.unsubscribe endpoint = %v, want the qualified %q", m["endpoint"], e2)
	}
}

// ════════════════════════════════════════════════════════════════════════════
// 4. sweep unsubscribes dead pids with reason=expiry, and is idempotent
//    PRESENCE.md §How an agent leaves (reason: expiry); §CLI → sweep. Same-
//    machine is a precondition the harness cannot violate on one host; see the
//    notes.
// ════════════════════════════════════════════════════════════════════════════

func TestPresence_Sweep_UnsubscribesDeadPidWithExpiry(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	// A registration whose process is already gone is what sweep exists to reap.
	exec("subscribe", e2, "--pid", pidStr(deadPid(t)), "--type", "acme-cli", "--version", "3.2.0")
	sub := p.witness(t, "presence.workshop")

	code, _, errOut := exec("sweep", "workshop")
	if code != 0 {
		t.Errorf("sweep exited %d (stderr %q), want 0", code, errOut)
	}
	m, ok := findEvent(t, collect(sub, 2*time.Second), "agent.unsubscribe")
	if !ok {
		t.Fatalf("sweep published no agent.unsubscribe for the dead-pid registration")
	}
	if m["reason"] != "expiry" {
		t.Errorf("sweep's agent.unsubscribe reason = %v, want %q", m["reason"], "expiry")
	}
	if m["endpoint"] != e2 {
		t.Errorf("sweep's agent.unsubscribe endpoint = %v, want the qualified %q", m["endpoint"], e2)
	}
}

// Idempotent (refinement): a second sweep with nothing dead left produces NO
// second unsubscribe event — not merely exit 0. The event's absence is the
// real proof it did nothing, safe to run on a timer.
func TestPresence_Sweep_SecondSweepEmitsNoUnsubscribe(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	exec("subscribe", e2, "--pid", pidStr(deadPid(t)), "--type", "acme-cli", "--version", "3.2.0")
	exec("sweep", "workshop") // first sweep reaps the dead pid

	sub := p.witness(t, "presence.workshop")
	code, _, errOut := exec("sweep", "workshop") // second sweep: nothing dead left
	if code != 0 {
		t.Errorf("second sweep exited %d (stderr %q), want a clean no-op 0", code, errOut)
	}
	if m, ok := findEvent(t, collect(sub, 1*time.Second), "agent.unsubscribe"); ok {
		t.Errorf("a second sweep emitted a second agent.unsubscribe (%v); it must be a silent no-op", m["endpoint"])
	}
}

// ════════════════════════════════════════════════════════════════════════════
// 5. Ephemeral queues; a send to an absent endpoint is REFUSED for ABSENCE
//    PRESENCE.md §Queue lifetime; §Inner and outer parlors ("Send is refused —
//    nowhere to deliver"); §Properties ("a refused send says so").
// ════════════════════════════════════════════════════════════════════════════

func TestPresence_Send_ToAbsentEndpoint_RefusedForAbsence(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	// atelier.clerk was never subscribed: there is no backing queue.
	if p.qexists("QUEUE_atelier_clerk") {
		t.Fatalf("precondition: an unsubscribed endpoint must have no backing queue")
	}
	// The refusal must be about ABSENCE — not a generic "unknown endpoint" —
	// and nothing may reach the medium. The absence vocabulary deliberately
	// excludes "registry", so the shipping "unknown endpoint … registry" error
	// cannot satisfy it.
	checkOnStderr(t, "absent|no live|no subscription|no queue|nobody|no one is|not attending|no endpoint holding",
		"send", "atelier.clerk", "into the void")
	// A refused send never brings a queue into being.
	if p.qexists("QUEUE_atelier_clerk") {
		t.Errorf("a refused send created a queue; nothing may accumulate in the inner parlor")
	}
}

// ════════════════════════════════════════════════════════════════════════════
// 6. emit: non-blocking, emitter-stamped ts, tool.post refs INSIDE a refs array
//    PRESENCE.md §Failure and degradation ("drops the event and returns
//    immediately"); §Events → common envelope (ts emitter-stamped), tool.post
//    references tool.pre.
// ════════════════════════════════════════════════════════════════════════════

// Non-blocking on a dead broker, under a deadline: emit is the sole verb that
// must exit 0 rather than error when the medium is unreachable, because it runs
// inside the agent's hook and must never make the agent wait.
func TestPresence_Emit_NonBlockingOnDeadBroker(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	// Point the deployment at a port nothing is listening on.
	loctest.Write(t, filepath.Join(p.home, "config"),
		"provider = nats\nnats_url = "+loctest.ClosedPort(t)+"\n")

	code, _, errOut := execWithin(t, 3*time.Second, "emit", "activity.start", e1)
	if code != 0 {
		t.Errorf("emit against a dead broker exited %d (stderr %q); it must drop the event and exit 0", code, errOut)
	}
}

// The emitter's timestamp — the moment the thing happened — must survive
// verbatim, not be replaced by the publish time.
func TestPresence_Emit_StampsCallerSuppliedTs(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	const stamp = "2026-01-14T09:31:20.114Z"
	sub := p.witness(t, "presence.workshop")

	exec("emit", "activity.start", e1, "--ts", stamp)

	m, ok := findEvent(t, collect(sub, 2*time.Second), "activity.start")
	if !ok {
		t.Fatalf("emit published no activity.start event")
	}
	if m["ts"] != stamp {
		t.Errorf("emit stamped ts=%v, want the caller-supplied %q", m["ts"], stamp)
	}
	if m["endpoint"] != e1 {
		t.Errorf("emit event endpoint = %v, want the qualified %q", m["endpoint"], e1)
	}
}

// tool.post references the tool.pre it closes, and the reference sits INSIDE a
// refs ARRAY (refinement) — parallel tool calls are told apart by it.
func TestPresence_Emit_ToolPostRefsToolPreInRefsArray(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	sub := p.witness(t, "presence.workshop")

	exec("emit", "tool.pre", e1, "--tool", "shell")
	pre, ok := findEvent(t, collect(sub, 2*time.Second), "tool.pre")
	if !ok {
		t.Fatalf("emit published no tool.pre event")
	}
	preID, _ := pre["id"].(string)
	if preID == "" {
		t.Fatalf("tool.pre event carried no id to reference")
	}
	if pre["endpoint"] != e1 {
		t.Errorf("tool.pre endpoint = %v, want the fully-qualified %q", pre["endpoint"], e1)
	}

	sub2 := p.witness(t, "presence.workshop")
	exec("emit", "tool.post", e1, "--tool", "shell", "--refs", preID)
	post, ok := findEvent(t, collect(sub2, 2*time.Second), "tool.post")
	if !ok {
		t.Fatalf("emit published no tool.post event")
	}
	if post["endpoint"] != e1 {
		t.Errorf("tool.post endpoint = %v, want the fully-qualified %q", post["endpoint"], e1)
	}
	refs, isArray := post["refs"].([]any)
	if !isArray {
		t.Fatalf("tool.post refs is %T, want a JSON array", post["refs"])
	}
	found := false
	for _, r := range refs {
		if r == preID {
			found = true
		}
	}
	if !found {
		t.Errorf("tool.post refs array %v does not contain the tool.pre id %q", refs, preID)
	}
}

// ════════════════════════════════════════════════════════════════════════════
// 7. registry: no host running is a failure, not an empty success
//    PRESENCE.md §Who is here right now; §Exit codes. Only the design-agnostic
//    no-host refusal is tested; listing registrations (the happy path) is a
//    documented GAP — see the notes.
// ════════════════════════════════════════════════════════════════════════════

func TestPresence_Registry_NoHostRunning_Fails(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	// This deployment runs no host process, so the request goes unanswered: the
	// verb must FAIL saying so, never print an empty roster and exit 0.
	checkRefusal(t, "no host|unanswered|no reply|timed out|timeout|no registry|no answer",
		"registry", "workshop")
}

// ════════════════════════════════════════════════════════════════════════════
// 8. status reports registered / process-alive / activity+reason SEPARATELY
//    PRESENCE.md §Command-line operations → status; §Three questions.
// ════════════════════════════════════════════════════════════════════════════

// status must report its inputs as three separate facts, in the ops-table
// vocabulary (registered-or-not, process alive-or-not, activity state with its
// reason) — NOT collapsed, and NOT in the "Reachable" Three-Questions wording.
// A start-time VALUE must be present, not merely a label. (Refinement.)
func TestPresence_Status_ReportsThreeInputsSeparately(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	pid := livePid(t)
	exec("subscribe", e1, "--pid", pidStr(pid), "--type", "acme-cli", "--version", "3.2.0")

	_, out, _ := exec("status", e1)

	if !regexp.MustCompile(`(?i)register`).MatchString(out) {
		t.Errorf("status does not report the registration fact; got %q", out)
	}
	if !regexp.MustCompile(`(?i)alive|process`).MatchString(out) {
		t.Errorf("status does not report the process-alive fact; got %q", out)
	}
	if !regexp.MustCompile(`(?i)idle|active|working|activity`).MatchString(out) {
		t.Errorf("status does not report the activity state; got %q", out)
	}
	// The alive fact is the recorded (pid, start-time) PAIR: both the number
	// and a start-time value, not just a "since" label.
	if !strings.Contains(out, pidStr(pid)) {
		t.Errorf("status does not carry the recorded pid %d; got %q", pid, out)
	}
	if !regexp.MustCompile(`\d{4}-\d{2}-\d{2}|\d{1,2}:\d{2}|\bago\b`).MatchString(out) {
		t.Errorf("status names a start-time label but no start-time VALUE; got %q", out)
	}
}

// Stale-event ordering (refinement, the most load-bearing derivation rule):
// an OLDER activity.start arriving AFTER a NEWER activity.end must be discarded,
// so status still reports idle rather than flipping back to active.
// PRESENCE.md §Deriving state from events ("Consumers discard stale events").
func TestPresence_Status_StaleActivityStartAfterNewerEndIsIdle(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	exec("subscribe", e1, "--pid", pidStr(livePid(t)), "--type", "acme-cli", "--version", "3.2.0")

	// The end is newer; the start that follows it is older, and out of order.
	exec("emit", "activity.end", e1, "--ts", "2026-01-14T10:04:30.000Z")
	exec("emit", "activity.start", e1, "--ts", "2026-01-14T10:04:25.000Z")

	_, out, _ := exec("status", e1)
	if regexp.MustCompile(`(?i)\bactive\b`).MatchString(out) || !regexp.MustCompile(`(?i)idle`).MatchString(out) {
		t.Errorf("a stale activity.start must leave status idle (last-timestamp-wins); got %q", out)
	}
}

// ════════════════════════════════════════════════════════════════════════════
// 9. watch is a read-only follow of the event stream
//    PRESENCE.md §Command-line operations → watch ("Follows the event stream …
//    Read-only").
// ════════════════════════════════════════════════════════════════════════════

// watch follows the stream and presents an event published after it starts.
// run() blocks on a real follow, so it is driven in a goroutine and abandoned;
// the deployment's server is torn down at test end, which ends it.
func TestPresence_Watch_FollowsEventStream(t *testing.T) {
	p := newPresence(t)
	p.as(t, "watch")

	var out safeBuffer
	go func() { _ = run([]string{"watch", "workshop"}, &out, io.Discard) }()
	time.Sleep(300 * time.Millisecond) // let a real watch subscribe first

	if err := p.admin.Publish("presence.workshop",
		[]byte(`{"id":"ev_watchme","ts":"2026-01-14T10:05:00.000Z","kind":"activity.end","endpoint":"workshop.scribe"}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	_ = p.admin.Flush()

	if !waitFor(2*time.Second, func() bool { return strings.Contains(out.String(), "ev_watchme") }) {
		t.Errorf("watch did not present an event published after it started; saw %q", out.String())
	}
}

// An event carrying a field the reader has never heard of must still be
// presented, not rejected. PRESENCE.md §Notes on the fields ("Unknown fields
// are ignored, not rejected").
func TestPresence_Watch_ToleratesUnknownField(t *testing.T) {
	p := newPresence(t)
	p.as(t, "watch")

	var out safeBuffer
	go func() { _ = run([]string{"watch", "workshop"}, &out, io.Discard) }()
	time.Sleep(300 * time.Millisecond)

	if err := p.admin.Publish("presence.workshop",
		[]byte(`{"id":"ev_unknownfield","ts":"2026-01-14T10:00:00.000Z","kind":"activity.start","endpoint":"workshop.scribe","future_field":"a consumer must not choke on this"}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	_ = p.admin.Flush()

	if !waitFor(2*time.Second, func() bool { return strings.Contains(out.String(), "ev_unknownfield") }) {
		t.Errorf("watch dropped an event carrying an unknown field; saw %q", out.String())
	}
}

// ════════════════════════════════════════════════════════════════════════════
// 11. One agent per endpoint: refuse-held, unsub-refuses-live, --force, empty,
//     dead. PRESENCE.md §One agent per endpoint.
// ════════════════════════════════════════════════════════════════════════════

// Subscribing a held endpoint is refused, and the refusal NAMES the incumbent's
// pid — so the caller can tell a crashed predecessor from a live agent. A bare
// non-zero exit does not carry that.
func TestPresence_Subscribe_OnHeldEndpoint_RefusedNamingIncumbentPid(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	pid := livePid(t)
	exec("subscribe", e1, "--pid", pidStr(pid), "--type", "acme-cli", "--version", "3.2.0") // holds e1

	checkRefusalNamingIncumbent(t, pid,
		"subscribe", e1, "--pid", pidStr(pid), "--type", "acme-cli", "--version", "3.2.0")
}

// Plain unsubscribe must REFUSE a live incumbent, naming the live process — so
// a restart can never displace a running agent.
func TestPresence_Unsubscribe_RefusesLiveIncumbent(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	pid := livePid(t)
	exec("subscribe", e1, "--pid", pidStr(pid), "--type", "acme-cli", "--version", "3.2.0")

	checkRefusalNamingIncumbent(t, pid,
		"unsubscribe", e1)
}

// --force is the deliberate displacement: it removes a live incumbent. The
// queue is stood up first, so "freed" measures the removal and is not vacuously
// true because the queue was never there.
func TestPresence_Unsubscribe_ForceRemovesLiveIncumbent(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	exec("subscribe", e1, "--pid", pidStr(livePid(t)), "--type", "acme-cli", "--version", "3.2.0")
	p.seedQueue(t, q1, "queue."+e1)

	code, _, errOut := exec("unsubscribe", e1, "--force")
	if code != 0 {
		t.Errorf("unsubscribe --force on a live incumbent exited %d (stderr %q), want 0", code, errOut)
	}
	if p.qexists(q1) {
		t.Errorf("--force did not free the endpoint; %s still exists", q1)
	}
}

// On an EMPTY endpoint plain unsubscribe is a no-op success — so an
// unconditional unsubscribe-then-subscribe restart is always safe.
func TestPresence_Unsubscribe_EmptyEndpointIsNoOp(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	code, _, errOut := exec("unsubscribe", "atelier.clerk")
	if code != 0 {
		t.Errorf("unsubscribe on an empty endpoint exited %d (stderr %q), want a no-op 0", code, errOut)
	}
}

// On a DEAD incumbent plain unsubscribe clears it — the other half of the safe
// restart.
func TestPresence_Unsubscribe_ClearsDeadIncumbent(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	exec("subscribe", e2, "--pid", pidStr(deadPid(t)), "--type", "acme-cli", "--version", "3.2.0")
	// Stand the queue up so "cleared" measures the verb's effect, not a queue that
	// was never created; witness the departure event, as the clean/force cases do.
	p.seedQueue(t, q2, "queue."+e2)
	sub := p.witness(t, "presence.workshop")

	code, _, errOut := exec("unsubscribe", e2)
	if code != 0 {
		t.Errorf("plain unsubscribe on a dead incumbent exited %d (stderr %q), want 0 (restart needs no pre-check)", code, errOut)
	}
	if p.qexists(q2) {
		t.Errorf("plain unsubscribe did not clear the dead incumbent's queue %s", q2)
	}
	if _, ok := findEvent(t, collect(sub, 2*time.Second), "agent.unsubscribe"); !ok {
		t.Errorf("plain unsubscribe on a dead incumbent published no agent.unsubscribe event")
	}
}

// ════════════════════════════════════════════════════════════════════════════
// 12. Exit-code discipline: failures are non-zero + stderr, never empty stdout
//     PRESENCE.md §Exit codes. The reason belongs on STDERR and the refusal
//     prints NOTHING on stdout — the opposite of a usage banner.
// ════════════════════════════════════════════════════════════════════════════

func TestPresence_RefusedSubscribe_SpeaksOnStderrOnly(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	exec("subscribe", e3, "--pid", pidStr(livePid(t)), "--type", "acme-cli", "--version", "3.2.0") // holds e3

	checkOnStderr(t, "held|already|incumbent|in use|"+pidStr(livePid(t)),
		"subscribe", e3, "--pid", "1", "--type", "acme-cli", "--version", "3.2.0")
}

func TestPresence_RefusedSend_SpeaksOnStderrOnly(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	checkOnStderr(t, "absent|no live|no subscription|no queue|nobody|no one is|not attending|no endpoint holding",
		"send", "atelier.clerk", "into the void")
}

func TestPresence_RegistryFailure_SpeaksOnStderrOnly(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	checkOnStderr(t, "no host|unanswered|no reply|timed out|timeout|no registry|no answer",
		"registry", "workshop")
}

// ════════════════════════════════════════════════════════════════════════════
// SEPARATE — a bootstrap invariant, NOT counted among the presence cases.
// The watch credential cannot publish: the server itself denies it, below the
// tool. This holds for any conformant deployment, so it passes even on the
// current build. PRESENCE.md §Command-line operations → watch ("Follows the
// event stream … Read-only") and the scratch ACL design — NOT §Trust, which
// concerns accidental collision between instances, not watch's publish rights.
// ════════════════════════════════════════════════════════════════════════════

func TestBootstrap_WatchCredentialIsDeniedPublish(t *testing.T) {
	p := newPresence(t)
	sub := p.witness(t, "presence.workshop")

	violations := make(chan error, 4)
	nc, err := natsgo.Connect(p.url,
		natsgo.UserInfo("watch", presencePassword),
		natsgo.ErrorHandler(func(_ *natsgo.Conn, _ *natsgo.Subscription, e error) {
			select {
			case violations <- e:
			default:
			}
		}),
	)
	if err != nil {
		t.Fatalf("connect as watch: %v", err)
	}
	defer nc.Close()

	_ = nc.Publish("presence.workshop", []byte("forbidden"))
	_ = nc.Flush()
	// A control publish from admin proves the witness path works.
	_ = p.admin.Publish("presence.workshop", []byte(`{"id":"ev_control"}`))
	_ = p.admin.Flush()

	seen := collect(sub, 1*time.Second)
	sawControl := false
	for _, m := range seen {
		if strings.Contains(m, "forbidden") {
			t.Errorf("the watch credential's publish was delivered; the ACL is not enforced")
		}
		if strings.Contains(m, "ev_control") {
			sawControl = true
		}
	}
	if !sawControl {
		t.Errorf("the witness did not see the admin control publish; the path is broken, not the ACL")
	}
	select {
	case e := <-violations:
		if !strings.Contains(strings.ToLower(e.Error()), "permission") {
			t.Errorf("watch got an async error that was not a permissions violation: %v", e)
		}
	case <-time.After(1 * time.Second):
		t.Errorf("no permissions violation surfaced to the watch credential")
	}
}

// keysOf lists a decoded event's top-level keys, for a legible failure message.
func keysOf(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
