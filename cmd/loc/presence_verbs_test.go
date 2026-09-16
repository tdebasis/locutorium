package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tdebasis/locutorium/internal/provider"
)

// Nothing in this file reaches a medium either. The presence verbs get a spy
// that carries the presence extension, selected the same way a real provider
// is — by name, out of the deployment's config — so what is under test is the
// dispatch and the semantics, not a broker.
//
// The end-to-end behaviour of these verbs against a real broker is pinned by
// the integration suite next door. What is pinned HERE is everything that
// suite cannot reach without breaking something on purpose: the argument
// grammar, the refusals, and what each verb does when the medium answers
// badly.

// presenceSpy is the message-plane spy plus the presence extension.
type presenceSpy struct {
	spy

	created, deleted []string
	exists           map[string]bool
	emitted          [][]byte
	requested        []string
	watched          string

	createErr, deleteErr, existsErr, emitErr, requestErr, watchErr error
	queuesErr                                                      error
	reply                                                          []byte
	watchOut                                                       string
}

func (s *presenceSpy) CreateQueue(endpoint string) error {
	s.created = append(s.created, endpoint)
	if s.createErr != nil {
		return s.createErr
	}
	s.exists[endpoint] = true
	return nil
}

func (s *presenceSpy) DeleteQueue(endpoint string) error {
	s.deleted = append(s.deleted, endpoint)
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.exists, endpoint)
	return nil
}

// Queues is the enumerator the reconciler reads. It reports what the spy has
// been told exists, and fails whole when queuesErr is set: a short list is
// never one of its answers.
func (s *presenceSpy) Queues() ([]string, error) {
	if s.queuesErr != nil {
		return nil, s.queuesErr
	}
	var out []string
	for e, ok := range s.exists {
		if ok {
			out = append(out, e)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *presenceSpy) QueueExists(endpoint string) (bool, error) {
	if s.existsErr != nil {
		return false, s.existsErr
	}
	return s.exists[endpoint], nil
}

func (s *presenceSpy) Emit(instance string, event []byte) error {
	s.emitted = append(s.emitted, event)
	return s.emitErr
}

func (s *presenceSpy) Request(subject string, _ time.Duration) ([]byte, error) {
	s.requested = append(s.requested, subject)
	if s.requestErr != nil {
		return nil, s.requestErr
	}
	return s.reply, nil
}

func (s *presenceSpy) Watch(instance string, w io.Writer) error {
	s.watched = instance
	if s.watchErr != nil {
		return s.watchErr
	}
	_, err := io.WriteString(w, s.watchOut)
	return err
}

var installedPresence *presenceSpy

func init() {
	provider.Register("presence-spy", func() (provider.Provider, error) {
		if installedPresence == nil {
			return nil, fmt.Errorf("no presence spy installed for this test")
		}
		return installedPresence, nil
	})
}

// presenceDeployment is a scratch $LOC_HOME naming the presence spy, with an
// identity of the namespaced shape the model expects.
type presenceDeployment struct {
	home string
	spy  *presenceSpy
}

func newPresenceDeployment(t *testing.T) *presenceDeployment {
	t.Helper()
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "config"), "provider = presence-spy\n")
	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", "workshop.host")

	d := &presenceDeployment{home: home, spy: &presenceSpy{exists: map[string]bool{}}}
	installed = nil
	installedPresence = d.spy
	t.Cleanup(func() { installedPresence = nil })
	return d
}

// ledger is where a registration lands, so a test can plant or read one
// without going through the verb that writes it.
func (d *presenceDeployment) ledger(name string) string {
	return filepath.Join(d.home, "run", "presence", name)
}

// blockTheLedger makes the registration directory unwritable by putting a FILE
// where the directory belongs — the shape of a deployment whose run area is
// broken.
func (d *presenceDeployment) blockTheLedger(t *testing.T) {
	t.Helper()
	writeFile(t, d.ledger(""), "not a directory")
}

func alivePid() string { return strconv.Itoa(os.Getpid()) }

// --------------------------------------------------------------- the grammar

// A verb typed wrongly prints the verb list on stdout and exits 1 — the same
// answer the message verbs give, because "you typed it wrong" is one answer
// however many verbs there are.
func TestPresenceVerbsUsagePaths(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"subscribe with no endpoint", []string{"subscribe"}},
		{"subscribe with a dangling flag", []string{"subscribe", "workshop.scribe", "--pid"}},
		{"subscribe with a flag it does not know", []string{"subscribe", "workshop.scribe", "--colour", "red"}},
		{"subscribe missing --version", []string{"subscribe", "workshop.scribe", "--pid", "1", "--type", "tmux"}},
		{"subscribe missing --type", []string{"subscribe", "workshop.scribe", "--pid", "1", "--version", "3.2.0"}},
		{"subscribe missing --pid", []string{"subscribe", "workshop.scribe", "--type", "tmux", "--version", "3.2.0"}},
		{"unsubscribe with no endpoint", []string{"unsubscribe"}},
		{"unsubscribe with a dangling flag", []string{"unsubscribe", "workshop.scribe", "--reason"}},
		{"sweep with an argument", []string{"sweep", "workshop"}},
		{"sweep with two arguments", []string{"sweep", "workshop", "atelier"}},
		{"emit with no arguments", []string{"emit"}},
		{"emit with only a kind", []string{"emit", "activity.start"}},
		{"emit of a kind outside the taxonomy", []string{"emit", "activity.middle", "workshop.scribe"}},
		{"emit with a dangling flag", []string{"emit", "tool.pre", "workshop.scribe", "--tool"}},
		{"registry with a flag it does not know", []string{"registry", "workshop", "--verbose"}},
		{"watch with a second argument", []string{"watch", "workshop", "atelier"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newPresenceDeployment(t)
			code, out, errOut := exec(tc.args...)
			assertResult(t, code, out, errOut, 1, usage, "")
			if len(d.spy.created)+len(d.spy.deleted)+len(d.spy.emitted) != 0 {
				t.Error("a mistyped invocation reached the medium")
			}
		})
	}
}

// ------------------------------------------------------------- the refusals

func TestPresenceVerbsRefusals(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name: "subscribe to a name with the barred underscore",
			args: []string{"subscribe", "workshop.scr_ibe", "--pid", "1", "--type", "tmux", "--version", "3.2.0"},
			wantErr: "loc: invalid endpoint name 'workshop.scr_ibe': an endpoint must match <instance>.<agent>, " +
				"each segment [a-z0-9-]+, with exactly one dot and the underscore barred\n",
		},
		{
			name: "subscribe to a bare agent name",
			args: []string{"subscribe", "scribe", "--pid", "1", "--type", "tmux", "--version", "3.2.0"},
			wantErr: "loc: invalid endpoint name 'scribe': an endpoint must match <instance>.<agent>, " +
				"each segment [a-z0-9-]+, with exactly one dot and the underscore barred\n",
		},
		{
			name:    "subscribe with a pid that is not a number",
			args:    []string{"subscribe", "workshop.scribe", "--pid", "later", "--type", "tmux", "--version", "3.2.0"},
			wantErr: "loc: invalid --pid 'later': a process id is a positive number\n",
		},
		{
			name:    "subscribe with a pid of zero",
			args:    []string{"subscribe", "workshop.scribe", "--pid", "0", "--type", "tmux", "--version", "3.2.0"},
			wantErr: "loc: invalid --pid '0': a process id is a positive number\n",
		},
		{
			name:    "unsubscribe with a reason outside the two",
			args:    []string{"unsubscribe", "workshop.scribe", "--reason", "bored"},
			wantErr: "loc: invalid --reason 'bored': a departure is 'clean' or 'expiry'\n",
		},
		{
			name: "unsubscribe an unqualified name",
			args: []string{"unsubscribe", "scribe"},
			wantErr: "loc: invalid endpoint name 'scribe': an endpoint must match <instance>.<agent>, " +
				"each segment [a-z0-9-]+, with exactly one dot and the underscore barred\n",
		},
		{
			name: "emit about an unqualified name",
			args: []string{"emit", "activity.start", "scribe"},
			wantErr: "loc: invalid endpoint name 'scribe': an endpoint must match <instance>.<agent>, " +
				"each segment [a-z0-9-]+, with exactly one dot and the underscore barred\n",
		},
		{
			name: "status of an unqualified name",
			args: []string{"status", "scribe"},
			wantErr: "loc: invalid endpoint name 'scribe': an endpoint must match <instance>.<agent>, " +
				"each segment [a-z0-9-]+, with exactly one dot and the underscore barred\n",
		},
		{
			name:    "registry of an instance with a dot in it",
			args:    []string{"registry", "workshop.scribe"},
			wantErr: "loc: invalid instance name 'workshop.scribe': an instance must match [a-z0-9-]+\n",
		},
		{
			name:    "watch of an instance with a dot in it",
			args:    []string{"watch", "workshop.scribe"},
			wantErr: "loc: invalid instance name 'workshop.scribe': an instance must match [a-z0-9-]+\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newPresenceDeployment(t)
			code, out, errOut := exec(tc.args...)
			assertResult(t, code, out, errOut, 1, "", tc.wantErr)
			if len(d.spy.created)+len(d.spy.deleted)+len(d.spy.emitted) != 0 {
				t.Error("a refused invocation reached the medium")
			}
		})
	}
}

// With the instance left out, these verbs mean the caller's own — and a caller
// whose identity is not an endpoint has no instance to take one from. It is
// refused rather than guessed: there is no "every instance".
func TestAnInstanceOmittedComesFromTheCallersIdentity(t *testing.T) {
	d := newPresenceDeployment(t)
	d.spy.reply = []byte(`{"agents":[]}`)

	// The sweep is not here: it takes no instance, because a process id means
	// something only on the machine holding it, so a sweep is machine-wide by
	// nature and has no caller's-instance form to fall back to.
	for _, verb := range []string{"registry", "watch"} {
		t.Run(verb+" takes the caller's instance", func(t *testing.T) {
			if code, _, errOut := exec(verb); code != 0 {
				t.Errorf("%s exited %d (stderr %q), want the caller's own instance to serve", verb, code, errOut)
			}
		})
	}
	if d.spy.watched != "workshop" {
		t.Errorf("watch followed %q, want the caller's instance %q", d.spy.watched, "workshop")
	}
	if len(d.spy.requested) != 1 || d.spy.requested[0] != "registry.workshop" {
		t.Errorf("registry asked %v, want registry.workshop", d.spy.requested)
	}

	t.Setenv("LOC_IDENTITY", "scribe")
	for _, verb := range []string{"registry", "watch"} {
		t.Run(verb+" with an unqualified identity", func(t *testing.T) {
			code, out, errOut := exec(verb)
			want := "loc: no instance given, and the caller's identity 'scribe' is not " +
				"an endpoint of the form <instance>.<agent> to take one from\n"
			assertResult(t, code, out, errOut, 1, "", want)
		})
	}

	t.Setenv("LOC_IDENTITY", "")
	for _, verb := range []string{"registry", "watch"} {
		t.Run(verb+" with no identity at all", func(t *testing.T) {
			_, _, errOut := exec(verb)
			if !strings.Contains(errOut, "cannot determine sender identity") {
				t.Errorf("got %q, want the identity refusal", errOut)
			}
		})
	}
}

// A medium that carries messages but not presence is named in the refusal.
func TestPresenceVerbsOnAProviderWithoutPresence(t *testing.T) {
	for _, args := range [][]string{
		{"subscribe", "workshop.scribe", "--pid", alivePid(), "--type", "tmux", "--version", "3.2.0"},
		{"unsubscribe", "workshop.scribe"},
		{"registry", "workshop"},
		{"watch", "workshop"},
	} {
		t.Run(args[0], func(t *testing.T) {
			newDeployment(t, "ada")
			t.Setenv("LOC_IDENTITY", "workshop.host")
			code, out, errOut := exec(args...)
			assertResult(t, code, out, errOut, 1, "", "loc: provider 'spy' does not support presence\n")
		})
	}
}

// -------------------------------------------------------------- one endpoint

// A held endpoint is refused, and the refusal names the incumbent concretely
// enough to tell a crashed predecessor from a running agent.
func TestSubscribeRefusesAHeldEndpointNamingTheIncumbent(t *testing.T) {
	d := newPresenceDeployment(t)
	pid := alivePid()

	if code, _, errOut := exec("subscribe", "workshop.scribe", "--pid", pid,
		"--type", "tmux", "--version", "3.2.0", "--display", "The Scribe", "--cwd", "/workspaces/scribe"); code != 0 {
		t.Fatalf("first subscribe exited %d (stderr %q)", code, errOut)
	}
	if len(d.spy.created) != 1 || d.spy.created[0] != "workshop.scribe" {
		t.Errorf("CreateQueue calls: %v", d.spy.created)
	}

	code, out, errOut := exec("subscribe", "workshop.scribe", "--pid", "1", "--type", "tmux", "--version", "3.2.0")
	if code != 1 || out != "" {
		t.Errorf("exit %d, stdout %q; want 1 and nothing", code, out)
	}
	for _, want := range []string{"is held by", "pid " + pid, "loc unsubscribe workshop.scribe"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("refusal %q does not carry %q", errOut, want)
		}
	}
	if len(d.spy.created) != 1 {
		t.Errorf("a refused subscribe created a queue: %v", d.spy.created)
	}
}

// A predecessor already gone when it was registered has no start time of its
// own, so the refusal dates it by the registration instead — the caller still
// gets a time, which is what tells them how stale this is.
func TestSubscribeRefusalDatesAnIncumbentWithNoStartTime(t *testing.T) {
	d := newPresenceDeployment(t)
	writeFile(t, d.ledger("workshop.scribe.json"),
		`{"endpoint":"workshop.scribe","instance":"workshop","agent":{"type":"tmux","version":"3.2.0"},`+
			`"process":{"pid":4242,"started":""},"registered":"2026-01-14T09:12:04.318Z"}`)

	_, _, errOut := exec("subscribe", "workshop.scribe", "--pid", alivePid(), "--type", "tmux", "--version", "3.2.0")
	for _, want := range []string{"pid 4242", "started unknown", "registered 2026-01-14T09:12:04.318Z"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("refusal %q does not carry %q", errOut, want)
		}
	}
}

// An unreadable registration is reported, not stepped over: the endpoint may
// be held, and acting as though it were free is the one outcome the whole
// refuse-rather-than-replace rule exists to prevent.
func TestPresenceVerbsReportAnUnreadableRegistration(t *testing.T) {
	for _, args := range [][]string{
		{"subscribe", "workshop.scribe", "--pid", alivePid(), "--type", "tmux", "--version", "3.2.0"},
		{"unsubscribe", "workshop.scribe"},
		{"status", "workshop.scribe"},
	} {
		t.Run(args[0], func(t *testing.T) {
			d := newPresenceDeployment(t)
			writeFile(t, d.ledger("workshop.scribe.json"), "half a record")

			code, out, errOut := exec(args...)
			if code != 1 || out != "" || !strings.Contains(errOut, "not readable") {
				t.Errorf("exit=%d stdout=%q stderr=%q; want a refusal naming an unreadable record", code, out, errOut)
			}
		})
	}
}

// A queue the medium refuses to create LEAVES THE ROW, and the verb still
// fails. The row is written first on purpose: the gap between the two writes
// is then a row with no queue, which is the sweep's third pass and a repair,
// rather than a queue with no row, which is its second pass and a destruction.
// The caller is told the subscribe failed; the next beat makes the queue.
func TestSubscribeKeepsTheRowWhenTheQueueCannotBeMade(t *testing.T) {
	d := newPresenceDeployment(t)
	d.spy.createErr = fmt.Errorf("cannot reach the medium")

	code, out, errOut := exec("subscribe", "workshop.scribe", "--pid", alivePid(), "--type", "tmux", "--version", "3.2.0")
	assertResult(t, code, out, errOut, 1, "", "loc: cannot reach the medium\n")
	if _, err := os.Stat(d.ledger("workshop.scribe.json")); err != nil {
		t.Error("the row a later beat repairs from was not written")
	}
	if len(d.spy.emitted) != 0 {
		t.Error("a subscribe that could not create its queue announced one")
	}
}

// A ledger that cannot be written fails the verb rather than announcing a
// registration nothing recorded.
func TestSubscribeReportsALedgerItCannotWrite(t *testing.T) {
	d := newPresenceDeployment(t)
	d.blockTheLedger(t)

	code, out, _ := exec("subscribe", "workshop.scribe", "--pid", alivePid(), "--type", "tmux", "--version", "3.2.0")
	if code != 1 || out != "" {
		t.Errorf("exit %d, stdout %q; want 1 and nothing", code, out)
	}
	if len(d.spy.emitted) != 0 {
		t.Error("a subscribe that could not record itself announced anyway")
	}
}

// ---------------------------------------------------------------- departures

// An empty endpoint is a no-op that says nothing and announces nothing: there
// was no departure to report.
func TestUnsubscribeOnAnEmptyEndpointTouchesNothing(t *testing.T) {
	d := newPresenceDeployment(t)

	code, out, errOut := exec("unsubscribe", "atelier.clerk")
	assertResult(t, code, out, errOut, 0, "", "")
	if len(d.spy.deleted)+len(d.spy.emitted) != 0 {
		t.Errorf("a no-op unsubscribe acted: deleted=%v emitted=%d", d.spy.deleted, len(d.spy.emitted))
	}
}

// A medium that cannot say whether an endpoint is attended is an error, not a
// "no": ignorance and absence are different answers.
func TestUnsubscribeReportsAMediumItCannotAsk(t *testing.T) {
	d := newPresenceDeployment(t)
	d.spy.existsErr = fmt.Errorf("cannot reach the medium")

	code, out, errOut := exec("unsubscribe", "atelier.clerk")
	assertResult(t, code, out, errOut, 1, "", "loc: cannot reach the medium\n")
}

// The departure carries the reason it was given, and the queue goes with it.
func TestUnsubscribeCarriesItsReason(t *testing.T) {
	for _, reason := range []string{"clean", "expiry"} {
		t.Run(reason, func(t *testing.T) {
			d := newPresenceDeployment(t)
			d.spy.exists["workshop.scribe"] = true

			args := []string{"unsubscribe", "workshop.scribe"}
			if reason != "clean" {
				args = append(args, "--reason", reason)
			}
			if code, _, errOut := exec(args...); code != 0 {
				t.Fatalf("exit %d, stderr %q", code, errOut)
			}
			if len(d.spy.deleted) != 1 || d.spy.deleted[0] != "workshop.scribe" {
				t.Errorf("DeleteQueue calls: %v", d.spy.deleted)
			}
			ev := decodeEvent(t, d.spy.emitted, "agent.unsubscribe")
			if ev["reason"] != reason {
				t.Errorf("departure reason = %v, want %q", ev["reason"], reason)
			}
		})
	}
}

// A queue the medium will not destroy leaves the registration in place: the
// ledger must never say an endpoint is free while its queue is still there.
func TestUnsubscribeRemovesTheRowBeforeTheQueue(t *testing.T) {
	d := newPresenceDeployment(t)
	d.spy.exists["workshop.scribe"] = true
	d.spy.deleteErr = fmt.Errorf("cannot reach the medium")
	writeFile(t, d.ledger("workshop.scribe.json"),
		`{"endpoint":"workshop.scribe","instance":"workshop","process":{"pid":4242,"started":""},"registered":"2026-01-14T09:12:04.318Z"}`)

	code, out, errOut := exec("unsubscribe", "workshop.scribe")
	assertResult(t, code, out, errOut, 1, "", "loc: cannot reach the medium\n")
	// THE ROW IS GONE AND THE QUEUE IS NOT, which is the gap this order
	// chooses. The sweep's orphan pass finishes what the caller started.
	// Deleting the queue first would leave a row with no queue, and the next
	// beat would rebuild the queue the caller asked to be destroyed.
	if _, err := os.Stat(d.ledger("workshop.scribe.json")); !os.IsNotExist(err) {
		t.Error("the row outlived the queue it was removed before")
	}
}

// ---------------------------------------------------------------------- sweep

// The sweep reaps a registration whose process is gone — its row, its queue
// and its server pidfile — and leaves a live one exactly where it is. It
// reaches every instance, because a machine holds every process on it.
func TestSweepReapsOnlyTheDead(t *testing.T) {
	d := newPresenceDeployment(t)
	writeFile(t, d.ledger("workshop.scribe.json"),
		`{"endpoint":"workshop.scribe","instance":"workshop","process":{"pid":4242,"started":""},"registered":"2026-01-14T09:12:04.318Z"}`)
	// A live one, and one belonging to another instance entirely.
	if code, _, errOut := exec("subscribe", "workshop.clerk", "--pid", alivePid(), "--type", "tmux", "--version", "3.2.0"); code != 0 {
		t.Fatalf("subscribe exited %d (stderr %q)", code, errOut)
	}
	if code, _, errOut := exec("subscribe", "atelier.scribe", "--pid", alivePid(), "--type", "tmux", "--version", "3.2.0"); code != 0 {
		t.Fatalf("subscribe exited %d (stderr %q)", code, errOut)
	}
	d.spy.emitted = nil
	d.spy.deleted = nil

	code, out, errOut := exec("sweep")
	if code != 0 {
		t.Fatalf("sweep exited %d (stderr %q)", code, errOut)
	}
	if len(d.spy.deleted) != 1 || d.spy.deleted[0] != "workshop.scribe" {
		t.Errorf("sweep destroyed %v, want only the dead endpoint's queue", d.spy.deleted)
	}
	if !strings.Contains(out, "workshop.scribe: reaped") {
		t.Errorf("sweep printed %q, want a line naming the reap", out)
	}
	// THE SWEEP PUBLISHES NOTHING. A departure event says an agent left; a
	// reap says a record was wrong, and telling every listener the first when
	// the second happened is a false statement about a seat that may have
	// gone hours ago.
	if len(d.spy.emitted) != 0 {
		t.Errorf("the sweep published %d events, want none", len(d.spy.emitted))
	}
	if _, err := os.Stat(d.ledger("workshop.clerk.json")); err != nil {
		t.Error("the sweep cleared a live registration")
	}
	if _, err := os.Stat(d.ledger("atelier.scribe.json")); err != nil {
		t.Error("the sweep cleared a live registration in another instance")
	}
}

// With the three records in agreement the sweep says nothing and exits 0. It
// is safe on a timer precisely because a quiet run is a silent one.
func TestSweepWithNothingWrongIsSilent(t *testing.T) {
	d := newPresenceDeployment(t)
	if code, _, errOut := exec("subscribe", "workshop.clerk", "--pid", alivePid(), "--type", "tmux", "--version", "3.2.0"); code != 0 {
		t.Fatalf("subscribe exited %d (stderr %q)", code, errOut)
	}
	d.spy.created, d.spy.deleted = nil, nil

	code, out, errOut := exec("sweep")
	assertResult(t, code, out, errOut, 0, "", "")
	// The medium IS opened now: the sweep has to list the queues to know that
	// nothing disagrees. What it does not do is change anything.
	if len(d.spy.created)+len(d.spy.deleted) != 0 {
		t.Errorf("a sweep with nothing wrong acted: created=%v deleted=%v", d.spy.created, d.spy.deleted)
	}
}

// A ledger that cannot be listed is reported rather than read as empty: an
// unreadable record area and no dead agents look identical otherwise.
func TestSweepReportsALedgerItCannotList(t *testing.T) {
	d := newPresenceDeployment(t)
	d.blockTheLedger(t)

	code, out, _ := exec("sweep")
	if code != 1 || out != "" {
		t.Errorf("exit %d, stdout %q; want 1 and nothing", code, out)
	}
}

// A queue enumeration that cannot complete stops the sweep. A short list is
// wrong in the destructive direction on the orphan pass and the duplicating
// direction on the repair pass, so it is never acted on.
func TestSweepStopsWhenTheQueuesCannotBeListed(t *testing.T) {
	d := newPresenceDeployment(t)
	if code, _, errOut := exec("subscribe", "workshop.clerk", "--pid", alivePid(), "--type", "tmux", "--version", "3.2.0"); code != 0 {
		t.Fatalf("subscribe exited %d (stderr %q)", code, errOut)
	}
	d.spy.created, d.spy.deleted = nil, nil
	d.spy.queuesErr = fmt.Errorf("cannot reach the medium")

	code, out, errOut := exec("sweep")
	if code != 1 || out != "" {
		t.Errorf("exit %d, stdout %q; want 1 and nothing", code, out)
	}
	if !strings.Contains(errOut, "cannot reach the medium") {
		t.Errorf("stderr %q does not carry the enumerator's reason", errOut)
	}
	if len(d.spy.created)+len(d.spy.deleted) != 0 {
		t.Errorf("the sweep acted on a listing it never got: created=%v deleted=%v", d.spy.created, d.spy.deleted)
	}
}

// ----------------------------------------------------------------------- emit

// The activity ledger is updated whether or not the medium takes the event,
// and the event that could not be sent is dropped in silence.
func TestEmitRecordsLocallyEvenWhenTheMediumIsGone(t *testing.T) {
	d := newPresenceDeployment(t)
	d.spy.emitErr = fmt.Errorf("cannot reach the medium")

	code, out, errOut := exec("emit", "activity.start", "workshop.scribe", "--ts", "2026-01-14T09:31:20.114Z")
	assertResult(t, code, out, errOut, 0, "", "")

	b, err := os.ReadFile(d.ledger("workshop.scribe.activity.json"))
	if err != nil {
		t.Fatalf("no activity was recorded: %v", err)
	}
	if !strings.Contains(string(b), "2026-01-14T09:31:20.114Z") {
		t.Errorf("activity record %s does not carry the emitter's stamp", b)
	}
}

// A whole provider that cannot be opened is the same non-event: emit runs
// inside an agent's hook and never makes it wait, whatever is wrong.
func TestEmitExitsCleanlyWithNoProviderAtAll(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home) // no config at all: any Open would fail
	t.Setenv("LOC_IDENTITY", "workshop.host")
	installed, installedPresence = nil, nil

	code, out, errOut := exec("emit", "tool.pre", "workshop.scribe", "--tool", "shell")
	assertResult(t, code, out, errOut, 0, "", "")
}

// A membership event is not an activity: leaving is a registration fact, and
// recording it as work would leave a departed agent looking busy.
func TestEmitDoesNotRecordMembershipEventsAsActivity(t *testing.T) {
	d := newPresenceDeployment(t)

	if code, _, errOut := exec("emit", "agent.unsubscribe", "workshop.scribe"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(d.ledger("workshop.scribe.activity.json")); !os.IsNotExist(err) {
		t.Error("a membership event was recorded as activity")
	}
}

// An activity record that cannot be written IS reported: the carve-out that
// keeps emit quiet is about the medium, and a deployment whose own record area
// is broken is not something to hide from its operator.
func TestEmitReportsAnActivityRecordItCannotWrite(t *testing.T) {
	d := newPresenceDeployment(t)
	d.blockTheLedger(t)

	code, out, _ := exec("emit", "activity.start", "workshop.scribe")
	if code != 1 || out != "" {
		t.Errorf("exit %d, stdout %q; want 1 and nothing", code, out)
	}
}

// The references a caller supplies arrive as a list, however many there are.
func TestEmitCarriesEveryReferenceItWasGiven(t *testing.T) {
	d := newPresenceDeployment(t)

	if code, _, errOut := exec("emit", "tool.post", "workshop.scribe",
		"--tool", "shell", "--refs", "ev_1111111111111111,ev_2222222222222222"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	ev := decodeEvent(t, d.spy.emitted, "tool.post")
	refs, ok := ev["refs"].([]any)
	if !ok || len(refs) != 2 || refs[0] != "ev_1111111111111111" || refs[1] != "ev_2222222222222222" {
		t.Errorf("refs = %v, want both ids in a list", ev["refs"])
	}
	if ev["tool"] != "shell" {
		t.Errorf("tool = %v, want shell", ev["tool"])
	}
}

// -------------------------------------------------------------------- registry

func TestRegistryRendering(t *testing.T) {
	roster := `{"agents":[{"endpoint":"workshop.scribe","instance":"workshop",` +
		`"agent":{"type":"tmux","version":"3.2.0"},"process":{"pid":4242,"started":"2026-01-14T09:12:04.006Z"},` +
		`"cwd":"/workspaces/scribe","registered":"2026-01-14T09:12:04.318Z"}]}`

	t.Run("one line per agent", func(t *testing.T) {
		d := newPresenceDeployment(t)
		d.spy.reply = []byte(roster)

		code, out, errOut := exec("registry", "workshop")
		if code != 0 || errOut != "" {
			t.Fatalf("exit %d, stderr %q", code, errOut)
		}
		for _, want := range []string{"workshop.scribe", "tmux 3.2.0", "pid 4242", "/workspaces/scribe"} {
			if !strings.Contains(out, want) {
				t.Errorf("roster %q does not carry %q", out, want)
			}
		}
	})

	t.Run("--json hands back what the host said", func(t *testing.T) {
		d := newPresenceDeployment(t)
		d.spy.reply = []byte(roster)

		code, out, errOut := exec("registry", "workshop", "--json")
		assertResult(t, code, out, errOut, 0, roster+"\n", "")
	})

	t.Run("an empty roster is still an answer", func(t *testing.T) {
		d := newPresenceDeployment(t)
		d.spy.reply = []byte(`{"agents":[]}`)

		code, out, errOut := exec("registry", "workshop")
		assertResult(t, code, out, errOut, 0, "(no agents registered)\n", "")
	})

	t.Run("an answer that is not a registry", func(t *testing.T) {
		d := newPresenceDeployment(t)
		d.spy.reply = []byte("who is asking?")

		code, out, errOut := exec("registry", "workshop")
		if code != 1 || out != "" || !strings.Contains(errOut, "is not a registry") {
			t.Errorf("exit=%d stdout=%q stderr=%q; want a refusal naming the bad answer", code, out, errOut)
		}
	})

	t.Run("nobody answering is a failure, not an empty roster", func(t *testing.T) {
		d := newPresenceDeployment(t)
		d.spy.requestErr = fmt.Errorf("no responders available for request")

		code, out, errOut := exec("registry", "workshop")
		assertResult(t, code, out, errOut, 1, "",
			"loc: no host is answering registry.workshop (the request went unanswered)\n")
	})
}

// ---------------------------------------------------------------------- watch

// A follow that ends badly is reported; a follow that simply ends is not.
func TestWatchReportsAFollowThatFailed(t *testing.T) {
	d := newPresenceDeployment(t)
	d.spy.watchErr = fmt.Errorf("cannot reach the medium")

	code, out, errOut := exec("watch", "workshop")
	assertResult(t, code, out, errOut, 1, "", "loc: cannot reach the medium\n")
	if d.spy.watched != "workshop" {
		t.Errorf("watch followed %q, want workshop", d.spy.watched)
	}
}

func TestWatchWritesWhatTheStreamCarried(t *testing.T) {
	d := newPresenceDeployment(t)
	d.spy.watchOut = "{\"id\":\"ev_1\",\"future_field\":\"unheard of\"}\n"

	code, out, errOut := exec("watch", "workshop")
	assertResult(t, code, out, errOut, 0, d.spy.watchOut, "")
}

// --------------------------------------------------------------------- status

// An endpoint nobody registered is a truthful answer to a fair question, not a
// failure — and it says which of the three facts it has no basis for.
func TestStatusOfAnUnregisteredEndpoint(t *testing.T) {
	newPresenceDeployment(t)

	code, out, errOut := exec("status", "atelier.clerk")
	assertResult(t, code, out, errOut, 0,
		"atelier.clerk\n"+
			"  registered: no\n"+
			"  process:    unknown (no registration)\n"+
			"  activity:   unknown (no registration)\n", "")
}

// The idle window is a deployment's to set, and one it has set wrongly is
// named rather than silently replaced with a default.
func TestStatusReportsAnUnreadableIdleWindow(t *testing.T) {
	d := newPresenceDeployment(t)
	writeFile(t, filepath.Join(d.home, "config"), "provider = presence-spy\nidle_window = a while\n")

	code, out, errOut := exec("status", "workshop.scribe")
	if code != 1 || out != "" || !strings.Contains(errOut, "invalid idle_window") {
		t.Errorf("exit=%d stdout=%q stderr=%q; want a refusal naming the setting", code, out, errOut)
	}
}

// The window is what returns a wrongly-concluded "working" to rest, and the
// deployment's own setting is what sizes it.
func TestStatusHonoursTheConfiguredIdleWindow(t *testing.T) {
	d := newPresenceDeployment(t)
	if code, _, errOut := exec("subscribe", "workshop.scribe", "--pid", alivePid(),
		"--type", "tmux", "--version", "3.2.0"); code != 0 {
		t.Fatalf("subscribe exited %d (stderr %q)", code, errOut)
	}
	stamp := time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05.000Z")
	if code, _, errOut := exec("emit", "tool.pre", "workshop.scribe", "--tool", "shell", "--ts", stamp); code != 0 {
		t.Fatalf("emit exited %d (stderr %q)", code, errOut)
	}

	_, out, _ := exec("status", "workshop.scribe")
	if !strings.Contains(out, "idle window elapsed since "+stamp) {
		t.Errorf("with the default window an hour-old tool call is past it; got %q", out)
	}

	writeFile(t, filepath.Join(d.home, "config"), "provider = presence-spy\nidle_window = 24h\n")
	_, out, _ = exec("status", "workshop.scribe")
	if !strings.Contains(out, "active (tool.pre at "+stamp+")") {
		t.Errorf("with a 24h window an hour-old tool call is inside it; got %q", out)
	}
}

// An activity record that cannot be read is reported: a status that quietly
// reported idle would be inventing the one fact it could not obtain.
func TestStatusReportsAnUnreadableActivityRecord(t *testing.T) {
	d := newPresenceDeployment(t)
	if code, _, errOut := exec("subscribe", "workshop.scribe", "--pid", alivePid(),
		"--type", "tmux", "--version", "3.2.0"); code != 0 {
		t.Fatalf("subscribe exited %d (stderr %q)", code, errOut)
	}
	writeFile(t, d.ledger("workshop.scribe.activity.json"), "half a record")

	code, out, errOut := exec("status", "workshop.scribe")
	if code != 1 || out != "" || !strings.Contains(errOut, "not readable") {
		t.Errorf("exit=%d stdout=%q stderr=%q; want a refusal naming an unreadable record", code, out, errOut)
	}
}

// A reader that goes away mid-report is not a failure of the report.
func TestStatusStopsWhenTheReaderGoesAway(t *testing.T) {
	newPresenceDeployment(t)
	if err := run([]string{"status", "atelier.clerk"}, &errAfterWriter{after: 0}, io.Discard); err != 1 {
		t.Errorf("exit %d, want 1 when the reader has gone", err)
	}
}

// ----------------------------------------------------------------------- send

// On a medium that carries presence, attendance is the live fact and the
// static registry file has nothing to say about it — in either direction.
func TestSendDerivesAttendanceFromTheLiveSubscription(t *testing.T) {
	d := newPresenceDeployment(t)
	// The file lists nobody, and the send still goes: what matters is that a
	// queue is there to deliver to.
	d.spy.exists["workshop.scribe"] = true

	code, out, errOut := exec("send", "workshop.scribe", "hello")
	assertSent(t, code, out, errOut, "workshop.scribe")

	// And the file listing a name changes nothing when nobody is attending it.
	code, out, errOut = exec("send", "workshop.clerk", "hello")
	assertResult(t, code, out, errOut, 1, "",
		"loc: nobody is attending 'workshop.clerk': no live subscription, so no queue to deliver to\n")
	if len(d.spy.sends) != 1 {
		t.Errorf("SendQueue calls: %v, want only the first", d.spy.sends)
	}
}

// A send to something that is not an endpoint at all is refused for what is
// wrong with it, before anyone is asked whether they are attending.
func TestSendRefusesAnUnqualifiedNameOnAPresenceMedium(t *testing.T) {
	newPresenceDeployment(t)

	code, out, errOut := exec("send", "scribe", "hello")
	if code != 1 || out != "" || !strings.Contains(errOut, "invalid endpoint name 'scribe'") {
		t.Errorf("exit=%d stdout=%q stderr=%q; want the name refusal", code, out, errOut)
	}
}

// A medium that cannot say who is attending fails the send rather than
// refusing it for absence: the recipient may well be there.
func TestSendReportsAMediumItCannotAskAboutAttendance(t *testing.T) {
	d := newPresenceDeployment(t)
	d.spy.existsErr = fmt.Errorf("cannot reach the medium")

	code, out, errOut := exec("send", "workshop.scribe", "hello")
	assertResult(t, code, out, errOut, 1, "", "loc: cannot reach the medium\n")
	if len(d.spy.sends) != 0 {
		t.Error("a send went out although attendance could not be established")
	}
}

// decodeEvent finds the first emitted event of a kind.
func decodeEvent(t *testing.T, emitted [][]byte, kind string) map[string]any {
	t.Helper()
	for _, e := range emitted {
		var m map[string]any
		if err := json.Unmarshal(e, &m); err != nil {
			t.Fatalf("the medium was handed something that is not an event (%v): %s", err, e)
		}
		if m["kind"] == kind {
			return m
		}
	}
	t.Fatalf("no %s event among %d emitted", kind, len(emitted))
	return nil
}
