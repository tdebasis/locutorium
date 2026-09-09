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

	model "github.com/tdebasis/locutorium/internal/presence"
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
	emittedOn        []string
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

func (s *presenceSpy) QueueExists(endpoint string) (bool, error) {
	if s.existsErr != nil {
		return false, s.existsErr
	}
	return s.exists[endpoint], nil
}

// Queues answers from the same map QueueExists answers from, filtered to one
// instance and sorted, so the spy cannot report a listing that disagrees with
// what it reports one endpoint at a time. An empty instance is every queue the
// spy has, which is what the real provider's widened subject filter gives.
func (s *presenceSpy) Queues(instance string) ([]string, error) {
	if s.queuesErr != nil {
		return nil, s.queuesErr
	}
	var out []string
	for e, ok := range s.exists {
		if ok && (instance == "" || model.Instance(e) == instance) {
			out = append(out, e)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Emit records the instance it was asked to speak in alongside the event, so a
// case can hold that an event went out on ITS OWN instance's subject and not
// on the one the verb was called about.
func (s *presenceSpy) Emit(instance string, event []byte) error {
	s.emitted = append(s.emitted, event)
	s.emittedOn = append(s.emittedOn, instance)
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
		{"subscribe missing --version", []string{"subscribe", "workshop.scribe", "--pid", "1", "--type", "acme-cli"}},
		{"subscribe missing --type", []string{"subscribe", "workshop.scribe", "--pid", "1", "--version", "3.2.0"}},
		{"subscribe missing --pid", []string{"subscribe", "workshop.scribe", "--type", "acme-cli", "--version", "3.2.0"}},
		{"unsubscribe with no endpoint", []string{"unsubscribe"}},
		{"unsubscribe with a dangling flag", []string{"unsubscribe", "workshop.scribe", "--reason"}},
		{"sweep with a second argument", []string{"sweep", "workshop", "atelier"}},
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
			args: []string{"subscribe", "workshop.scr_ibe", "--pid", "1", "--type", "acme-cli", "--version", "3.2.0"},
			wantErr: "loc: invalid endpoint name 'workshop.scr_ibe': an endpoint must match <instance>.<agent>, " +
				"each segment [a-z0-9-]+, with exactly one dot and the underscore barred\n",
		},
		{
			name: "subscribe to a bare agent name",
			args: []string{"subscribe", "scribe", "--pid", "1", "--type", "acme-cli", "--version", "3.2.0"},
			wantErr: "loc: invalid endpoint name 'scribe': an endpoint must match <instance>.<agent>, " +
				"each segment [a-z0-9-]+, with exactly one dot and the underscore barred\n",
		},
		{
			name:    "subscribe with a pid that is not a number",
			args:    []string{"subscribe", "workshop.scribe", "--pid", "later", "--type", "acme-cli", "--version", "3.2.0"},
			wantErr: "loc: invalid --pid 'later': a process id is a positive number\n",
		},
		{
			name:    "subscribe with a pid of zero",
			args:    []string{"subscribe", "workshop.scribe", "--pid", "0", "--type", "acme-cli", "--version", "3.2.0"},
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
			name:    "sweep of an instance with a dot in it",
			args:    []string{"sweep", "workshop.scribe"},
			wantErr: "loc: invalid instance name 'workshop.scribe': an instance must match [a-z0-9-]+\n",
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

// With the instance left out, registry and watch mean the caller's own — and a
// caller whose identity is not an endpoint has no instance to take one from. It
// is refused rather than guessed: each instance is a separate subject with its
// own host, so asking one question of all of them is undefined.
//
// SWEEP IS NOT IN THIS LIST. A sweep reads the ledger and the broker rather
// than asking one instance's host a question, so its bare form covers every
// instance it finds and takes nothing from the caller's identity.
func TestAnInstanceOmittedComesFromTheCallersIdentity(t *testing.T) {
	d := newPresenceDeployment(t)
	d.spy.reply = []byte(`{"agents":[]}`)

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
		{"subscribe", "workshop.scribe", "--pid", alivePid(), "--type", "acme-cli", "--version", "3.2.0"},
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
		"--type", "acme-cli", "--version", "3.2.0", "--display", "The Scribe", "--cwd", "/workspaces/scribe"); code != 0 {
		t.Fatalf("first subscribe exited %d (stderr %q)", code, errOut)
	}
	if len(d.spy.created) != 1 || d.spy.created[0] != "workshop.scribe" {
		t.Errorf("CreateQueue calls: %v", d.spy.created)
	}

	code, out, errOut := exec("subscribe", "workshop.scribe", "--pid", "1", "--type", "acme-cli", "--version", "3.2.0")
	if code != 1 || out != "" {
		t.Errorf("exit %d, stdout %q; want 1 and nothing", code, out)
	}
	for _, want := range []string{
		"is held by", "pid " + pid,
		"loc unsubscribe workshop.scribe",
		"loc subscribe workshop.scribe --force",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("refusal %q does not carry %q", errOut, want)
		}
	}
	if len(d.spy.created) != 1 {
		t.Errorf("a refused subscribe created a queue: %v", d.spy.created)
	}
}

// --force takes a held endpoint deliberately, the way unsubscribe --force
// frees one. The row afterwards names the new process, so a caller can tell
// the take happened.
func TestSubscribeForceTakesAHeldEndpoint(t *testing.T) {
	d := newPresenceDeployment(t)

	if code, _, errOut := exec("subscribe", "workshop.scribe", "--pid", alivePid(),
		"--type", "acme-cli", "--version", "3.2.0"); code != 0 {
		t.Fatalf("first subscribe exited %d (stderr %q)", code, errOut)
	}

	code, out, errOut := exec("subscribe", "workshop.scribe", "--pid", "4242",
		"--type", "acme-cli", "--version", "3.2.0", "--force")
	assertResult(t, code, out, errOut, 0, "", "")

	var reg model.Registration
	b, err := os.ReadFile(d.ledger("workshop.scribe.json"))
	if err != nil {
		t.Fatalf("read the row: %v", err)
	}
	if err := json.Unmarshal(b, &reg); err != nil {
		t.Fatalf("unmarshal the row: %v", err)
	}
	if reg.Process.PID != 4242 {
		t.Errorf("the row names pid %d, want the taking process 4242", reg.Process.PID)
	}
	ev := decodeEvent(t, d.spy.emitted, "agent.subscribe")
	if ev["endpoint"] != "workshop.scribe" {
		t.Errorf("the join event names %v, want workshop.scribe", ev["endpoint"])
	}
}

// A predecessor already gone when it was registered has no start time of its
// own, so the refusal dates it by the registration instead — the caller still
// gets a time, which is what tells them how stale this is.
func TestSubscribeRefusalDatesAnIncumbentWithNoStartTime(t *testing.T) {
	d := newPresenceDeployment(t)
	writeFile(t, d.ledger("workshop.scribe.json"),
		`{"endpoint":"workshop.scribe","instance":"workshop","agent":{"type":"acme-cli","version":"3.2.0"},`+
			`"process":{"pid":4242,"started":""},"registered":"2026-01-14T09:12:04.318Z"}`)

	_, _, errOut := exec("subscribe", "workshop.scribe", "--pid", alivePid(), "--type", "acme-cli", "--version", "3.2.0")
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
		{"subscribe", "workshop.scribe", "--pid", alivePid(), "--type", "acme-cli", "--version", "3.2.0"},
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

// A queue the medium refuses to create leaves no registration behind: the
// ledger is written after the queue exists, so a half-made subscription is not
// a state this can end in.
func TestSubscribeWritesNoRegistrationWhenTheQueueCannotBeMade(t *testing.T) {
	d := newPresenceDeployment(t)
	d.spy.createErr = fmt.Errorf("cannot reach the medium")

	code, out, errOut := exec("subscribe", "workshop.scribe", "--pid", alivePid(), "--type", "acme-cli", "--version", "3.2.0")
	assertResult(t, code, out, errOut, 1, "", "loc: cannot reach the medium\n")
	if _, err := os.Stat(d.ledger("workshop.scribe.json")); !os.IsNotExist(err) {
		t.Error("a subscribe that could not create its queue registered anyway")
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

	code, out, _ := exec("subscribe", "workshop.scribe", "--pid", alivePid(), "--type", "acme-cli", "--version", "3.2.0")
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
func TestUnsubscribeKeepsTheRegistrationWhenTheQueueSurvives(t *testing.T) {
	d := newPresenceDeployment(t)
	d.spy.exists["workshop.scribe"] = true
	d.spy.deleteErr = fmt.Errorf("cannot reach the medium")
	writeFile(t, d.ledger("workshop.scribe.json"),
		`{"endpoint":"workshop.scribe","instance":"workshop","process":{"pid":4242,"started":""},"registered":"2026-01-14T09:12:04.318Z"}`)

	code, out, errOut := exec("unsubscribe", "workshop.scribe")
	assertResult(t, code, out, errOut, 1, "", "loc: cannot reach the medium\n")
	if _, err := os.Stat(d.ledger("workshop.scribe.json")); err != nil {
		t.Error("the registration was cleared although its queue survived")
	}
}

// ---------------------------------------------------------------------- sweep

// The sweep reaps a registration whose process is gone, with reason expiry,
// and leaves a live one exactly where it is.
func TestSweepReapsOnlyTheDead(t *testing.T) {
	d := newPresenceDeployment(t)
	writeFile(t, d.ledger("workshop.scribe.json"),
		`{"endpoint":"workshop.scribe","instance":"workshop","process":{"pid":4242,"started":""},"registered":"2026-01-14T09:12:04.318Z"}`)
	// A live one, and one belonging to another instance entirely.
	if code, _, errOut := exec("subscribe", "workshop.clerk", "--pid", alivePid(), "--type", "acme-cli", "--version", "3.2.0"); code != 0 {
		t.Fatalf("subscribe exited %d (stderr %q)", code, errOut)
	}
	if code, _, errOut := exec("subscribe", "atelier.scribe", "--pid", alivePid(), "--type", "acme-cli", "--version", "3.2.0"); code != 0 {
		t.Fatalf("subscribe exited %d (stderr %q)", code, errOut)
	}
	d.spy.emitted = nil

	if code, _, errOut := exec("sweep", "workshop"); code != 0 {
		t.Fatalf("sweep exited %d (stderr %q)", code, errOut)
	}
	if len(d.spy.deleted) != 1 || d.spy.deleted[0] != "workshop.scribe" {
		t.Errorf("sweep destroyed %v, want only the dead endpoint's queue", d.spy.deleted)
	}
	ev := decodeEvent(t, d.spy.emitted, "agent.unsubscribe")
	if ev["reason"] != "expiry" || ev["endpoint"] != "workshop.scribe" {
		t.Errorf("sweep's departure = %v, want workshop.scribe by expiry", ev)
	}
	if _, err := os.Stat(d.ledger("workshop.clerk.json")); err != nil {
		t.Error("the sweep cleared a live registration")
	}
	if _, err := os.Stat(d.ledger("atelier.scribe.json")); err != nil {
		t.Error("the sweep reached into another instance")
	}
}

// With nothing to reconcile the sweep changes nothing and says nothing. It
// still asks the broker, because a reconcile cannot know there is nothing to do
// until it has compared the two pictures. What makes it safe on a timer is that
// it creates nothing, destroys nothing and publishes nothing when the two
// pictures already agree.
func TestSweepWithNothingToReconcileChangesNothing(t *testing.T) {
	d := newPresenceDeployment(t)

	code, out, errOut := exec("sweep", "workshop")
	assertResult(t, code, out, errOut, 0, "", "")
	if len(d.spy.created)+len(d.spy.deleted)+len(d.spy.emitted) != 0 {
		t.Errorf("a sweep with nothing to do acted: created=%v deleted=%v emitted=%d",
			d.spy.created, d.spy.deleted, len(d.spy.emitted))
	}
}

// ------------------------------------------------------- sweep as a reconcile

// deadRow writes a registration whose process is gone, without going through
// subscribe: the pid is the file's dead-pid literal.
func (d *presenceDeployment) deadRow(t *testing.T, endpoint string) {
	t.Helper()
	writeFile(t, d.ledger(endpoint+".json"),
		`{"endpoint":"`+endpoint+`","instance":"`+model.Instance(endpoint)+`",`+
			`"process":{"pid":4242,"started":""},"registered":"2026-01-14T09:12:04.318Z"}`)
}

// serverPIDFile is where the process serving a seat writes its own pid, spelled
// here as internal/presence spells it.
func (d *presenceDeployment) serverPIDFile(endpoint string) string {
	return filepath.Join(d.home, "run", endpoint+".mcp.pid")
}

// incidentsFile is the day's incident record under this deployment's home.
func (d *presenceDeployment) incidentsFile() string {
	return filepath.Join(d.home, "run", "incidents", time.Now().UTC().Format("2006-01-02")+".jsonl")
}

// Pass 1. A dead row loses all three things that named it: the queue, the
// registration, and the server pidfile. One departure goes out, by expiry.
func TestSweepPass1ClearsEverythingADeadRowNamed(t *testing.T) {
	d := newPresenceDeployment(t)
	d.deadRow(t, "workshop.scribe")
	d.spy.exists["workshop.scribe"] = true
	writeFile(t, d.serverPIDFile("workshop.scribe"), "4242\n")

	code, out, errOut := exec("sweep", "workshop")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if want := "reaped workshop.scribe: pid 4242 is gone\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	if d.spy.exists["workshop.scribe"] {
		t.Error("the dead seat's queue is still there")
	}
	if _, err := os.Stat(d.ledger("workshop.scribe.json")); !os.IsNotExist(err) {
		t.Error("the dead seat's registration is still there")
	}
	if _, err := os.Stat(d.serverPIDFile("workshop.scribe")); !os.IsNotExist(err) {
		t.Error("the dead seat's server pidfile is still there")
	}
	if len(d.spy.emitted) != 1 {
		t.Fatalf("%d events emitted, want exactly one", len(d.spy.emitted))
	}
	ev := decodeEvent(t, d.spy.emitted, "agent.unsubscribe")
	if ev["reason"] != "expiry" || ev["endpoint"] != "workshop.scribe" {
		t.Errorf("departure = %v, want workshop.scribe by expiry", ev)
	}
}

// Pass 2. A queue no registration holds is destroyed, and its departure is
// announced: something is draining mail that nobody is answering.
func TestSweepPass2DestroysAQueueNoRowHolds(t *testing.T) {
	d := newPresenceDeployment(t)
	d.spy.exists["workshop.ghost"] = true

	code, out, errOut := exec("sweep", "workshop")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if want := "destroyed the queue for workshop.ghost: no registration holds it\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	if len(d.spy.deleted) != 1 || d.spy.deleted[0] != "workshop.ghost" {
		t.Errorf("DeleteQueue calls: %v, want only the orphan", d.spy.deleted)
	}
	if len(d.spy.emitted) != 1 {
		t.Fatalf("%d events emitted, want exactly one", len(d.spy.emitted))
	}
	decodeEvent(t, d.spy.emitted, "agent.unsubscribe")
}

// Pass 3. A live row whose queue is gone gets its queue back, and the join is
// announced again. THE ROW IS NOT REWRITTEN: the agent has been registered
// since it subscribed, and a reconcile that restamped Registered would make
// every sweep look like a new arrival.
func TestSweepPass3RemakesAQueueAndLeavesTheRowAlone(t *testing.T) {
	d := newPresenceDeployment(t)
	if code, _, errOut := exec("subscribe", "workshop.scribe", "--pid", alivePid(),
		"--type", "acme-cli", "--version", "3.2.0"); code != 0 {
		t.Fatalf("subscribe exited %d (stderr %q)", code, errOut)
	}
	before, err := os.ReadFile(d.ledger("workshop.scribe.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The broker lost the queue; the ledger still holds the seat.
	delete(d.spy.exists, "workshop.scribe")
	d.spy.created, d.spy.deleted, d.spy.emitted = nil, nil, nil

	code, out, errOut := exec("sweep", "workshop")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if want := "remade the queue for workshop.scribe\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	if len(d.spy.created) != 1 || d.spy.created[0] != "workshop.scribe" {
		t.Errorf("CreateQueue calls: %v, want the one live seat", d.spy.created)
	}
	if len(d.spy.emitted) != 1 {
		t.Fatalf("%d events emitted, want exactly one", len(d.spy.emitted))
	}
	decodeEvent(t, d.spy.emitted, "agent.subscribe")

	after, err := os.ReadFile(d.ledger("workshop.scribe.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("the registration was rewritten:\n before %s\n  after %s", before, after)
	}
}

// THE ORDER OF THE PASSES. A dead row with no queue must end with no queue.
// Pass 1 removes the row, so pass 3 never sees it. Run pass 3 first and the
// sweep makes a queue for a seat that is not running.
func TestSweepMakesNoQueueForADeadRow(t *testing.T) {
	d := newPresenceDeployment(t)
	d.deadRow(t, "workshop.scribe")

	code, _, errOut := exec("sweep", "workshop")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if len(d.spy.created) != 0 {
		t.Errorf("the sweep made a queue for a dead seat: %v", d.spy.created)
	}
	if _, err := os.Stat(d.ledger("workshop.scribe.json")); !os.IsNotExist(err) {
		t.Error("the dead row is still there")
	}
}

// A LISTING THAT FAILED IS NOT AN EMPTY LISTING. The broker could not say which
// queues it has, so the sweep changes nothing at all: acting on ignorance here
// destroys every live seat's queue.
func TestSweepActsOnNothingWhenTheQueuesCannotBeListed(t *testing.T) {
	d := newPresenceDeployment(t)
	d.deadRow(t, "workshop.scribe")
	d.spy.exists["workshop.ghost"] = true
	d.spy.queuesErr = fmt.Errorf("cannot reach the medium")

	code, out, _ := exec("sweep", "workshop")
	if code == 0 || out != "" {
		t.Errorf("exit %d, stdout %q; want non-zero and nothing", code, out)
	}
	if len(d.spy.created)+len(d.spy.deleted)+len(d.spy.emitted) != 0 {
		t.Errorf("the sweep acted on a listing it never got: created=%v deleted=%v emitted=%d",
			d.spy.created, d.spy.deleted, len(d.spy.emitted))
	}
	if _, err := os.Stat(d.ledger("workshop.scribe.json")); err != nil {
		t.Error("the sweep reaped a row although it could not list the queues")
	}
}

// A QUEUE LISTING THAT FAILED IS A FAULT OF THE HOUSE, and the house writes it
// down. The sweep stops at that point, so without a line on disk a timer sees a
// non-zero exit and never learns what refused it.
//
// THE INCIDENT IS RECORDED AND NOT PUBLISHED. The medium is the thing that just
// failed, so an event about the failure would fail the same way.
func TestSweepRecordsAnIncidentWhenTheQueuesCannotBeListed(t *testing.T) {
	d := newPresenceDeployment(t)
	d.deadRow(t, "workshop.scribe")
	d.spy.queuesErr = fmt.Errorf("cannot list the queues of 'workshop': permissions violation")

	code, out, _ := exec("sweep", "workshop")
	if code == 0 || out != "" {
		t.Errorf("exit %d, stdout %q; want non-zero and nothing", code, out)
	}

	raw, err := os.ReadFile(d.incidentsFile())
	if err != nil {
		t.Fatalf("a failed listing was not written down: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("%d lines in the incident record, want one: %q", len(lines), string(raw))
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("the line is not one whole event (%v): %q", err, lines[0])
	}
	if got["kind"] != "house.incident" {
		t.Errorf("kind = %q, want house.incident", got["kind"])
	}
	if got["reason"] != model.IncidentEnumerationRefused {
		t.Errorf("reason = %q, want %q", got["reason"], model.IncidentEnumerationRefused)
	}
	if !strings.Contains(got["detail"], "permissions violation") {
		t.Errorf("detail = %q, want the error text from the listing", got["detail"])
	}
	if len(d.spy.emitted) != 0 {
		t.Errorf("the sweep published %d events about a medium that had just failed",
			len(d.spy.emitted))
	}
}

// AN UNREADABLE ROW STOPS PASS 2, AND ONLY PASS 2. The row may hold the
// endpoint whose queue pass 2 is about to call an orphan, so the queue is left
// alone. The fault is reported twice, live and durably, and the verb exits
// non-zero so a timer sees it.
func TestSweepReportsAnUnreadableRowAndSkipsPass2(t *testing.T) {
	d := newPresenceDeployment(t)
	writeFile(t, d.ledger("workshop.bad.json"), "{ not json")
	d.spy.exists["workshop.bad"] = true

	code, out, errOut := exec("sweep", "workshop")
	if code == 0 {
		t.Errorf("exit 0 on an unreadable row; stdout %q stderr %q", out, errOut)
	}
	if len(d.spy.deleted) != 0 {
		t.Errorf("pass 2 ran with an unreadable row in the ledger: %v", d.spy.deleted)
	}
	ev := decodeEvent(t, d.spy.emitted, "house.incident")
	if ev["reason"] != "ledger.unreadable" {
		t.Errorf("incident reason = %v, want ledger.unreadable", ev["reason"])
	}
	if ev["endpoint"] != "workshop.bad" {
		t.Errorf("incident endpoint = %v, want workshop.bad", ev["endpoint"])
	}
	raw, err := os.ReadFile(d.incidentsFile())
	if err != nil {
		t.Fatalf("no incident was written down: %v", err)
	}
	if lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n"); len(lines) != 1 {
		t.Errorf("%d lines in the incident record, want one", len(lines))
	}
	if !strings.Contains(errOut, "workshop.bad") {
		t.Errorf("stderr %q does not name the unreadable row", errOut)
	}
	if !strings.Contains(errOut, "pass 2") {
		t.Errorf("stderr %q does not say which pass was skipped", errOut)
	}
}

// emittedInstance is the instance an event naming endpoint was published in.
// It reads the spy's parallel record, so a case can hold that a departure went
// out on its own instance's subject.
func emittedInstance(t *testing.T, d *presenceDeployment, kind, endpoint string) string {
	t.Helper()
	for i, raw := range d.spy.emitted {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("the medium was handed something that is not an event (%v): %s", err, raw)
		}
		if m["kind"] == kind && m["endpoint"] == endpoint {
			return d.spy.emittedOn[i]
		}
	}
	t.Fatalf("no %s for %s among %d emitted", kind, endpoint, len(d.spy.emitted))
	return ""
}

// THE BARE FORM KNOWS NOTHING IN ADVANCE. It reads every row on this machine
// and reconciles all of them in one run. Each departure goes out on its OWN
// instance's subject, because the subject is derived from the endpoint and not
// from what the verb was called about.
func TestSweepBareReapsTheDeadInEveryInstance(t *testing.T) {
	d := newPresenceDeployment(t)
	d.deadRow(t, "workshop.scribe")
	d.deadRow(t, "atelier.clerk")
	d.spy.exists["workshop.scribe"] = true
	d.spy.exists["atelier.clerk"] = true

	code, out, errOut := exec("sweep")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	want := "reaped atelier.clerk: pid 4242 is gone\n" +
		"reaped workshop.scribe: pid 4242 is gone\n"
	if out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	for _, e := range []string{"workshop.scribe", "atelier.clerk"} {
		if _, err := os.Stat(d.ledger(e + ".json")); !os.IsNotExist(err) {
			t.Errorf("the row for %s is still there", e)
		}
		if d.spy.exists[e] {
			t.Errorf("the queue for %s is still there", e)
		}
	}
	if len(d.spy.emitted) != 2 {
		t.Fatalf("%d events emitted, want one departure per dead seat", len(d.spy.emitted))
	}
	if got := emittedInstance(t, d, "agent.unsubscribe", "workshop.scribe"); got != "workshop" {
		t.Errorf("workshop.scribe's departure went out on %q, want workshop", got)
	}
	if got := emittedInstance(t, d, "agent.unsubscribe", "atelier.clerk"); got != "atelier" {
		t.Errorf("atelier.clerk's departure went out on %q, want atelier", got)
	}
}

// The bare form runs every pass across every instance in one go: an orphan
// queue in one instance is destroyed while a lost queue in another is remade.
func TestSweepBareReconcilesBothWaysAcrossInstances(t *testing.T) {
	d := newPresenceDeployment(t)
	if code, _, errOut := exec("subscribe", "workshop.scribe", "--pid", alivePid(),
		"--type", "acme-cli", "--version", "3.2.0"); code != 0 {
		t.Fatalf("subscribe exited %d (stderr %q)", code, errOut)
	}
	delete(d.spy.exists, "workshop.scribe") // the broker lost it
	d.spy.exists["atelier.ghost"] = true    // and holds one no row claims
	d.spy.created, d.spy.deleted, d.spy.emitted, d.spy.emittedOn = nil, nil, nil, nil

	code, out, errOut := exec("sweep")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	want := "destroyed the queue for atelier.ghost: no registration holds it\n" +
		"remade the queue for workshop.scribe\n"
	if out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	if len(d.spy.deleted) != 1 || d.spy.deleted[0] != "atelier.ghost" {
		t.Errorf("DeleteQueue calls: %v, want only the orphan", d.spy.deleted)
	}
	if len(d.spy.created) != 1 || d.spy.created[0] != "workshop.scribe" {
		t.Errorf("CreateQueue calls: %v, want only the lost queue", d.spy.created)
	}
	if got := emittedInstance(t, d, "agent.unsubscribe", "atelier.ghost"); got != "atelier" {
		t.Errorf("the orphan's departure went out on %q, want atelier", got)
	}
	if got := emittedInstance(t, d, "agent.subscribe", "workshop.scribe"); got != "workshop" {
		t.Errorf("the remade seat's join went out on %q, want workshop", got)
	}
}

// THE BARE FORM DOES NOT NARROW TO THE CALLER'S OWN INSTANCE. The caller here
// speaks as workshop.scribe and the only dead row is in atelier; the row is
// reaped all the same. This is the removed default, held so it cannot return.
func TestSweepBareIgnoresTheCallersIdentity(t *testing.T) {
	d := newPresenceDeployment(t)
	t.Setenv("LOC_IDENTITY", "workshop.scribe")
	d.deadRow(t, "atelier.clerk")

	code, out, errOut := exec("sweep")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if want := "reaped atelier.clerk: pid 4242 is gone\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	if _, err := os.Stat(d.ledger("atelier.clerk.json")); !os.IsNotExist(err) {
		t.Error("the bare sweep narrowed to the caller's own instance and left the atelier row")
	}
}

// ---------------------------------------- a pass that cannot finish stops here

// A queue the medium will not destroy leaves the dead row ON DISK. The ledger
// must never say a seat is free while its queue is still there, which is the
// rule unsubscribe follows and the reason pass 1 stops rather than carries on.
func TestSweepKeepsTheDeadRowWhenItsQueueSurvives(t *testing.T) {
	d := newPresenceDeployment(t)
	d.deadRow(t, "workshop.scribe")
	d.spy.exists["workshop.scribe"] = true
	d.spy.deleteErr = fmt.Errorf("cannot reach the medium")

	code, out, errOut := exec("sweep", "workshop")
	assertResult(t, code, out, errOut, 1, "", "loc: cannot reach the medium\n")
	if _, err := os.Stat(d.ledger("workshop.scribe.json")); err != nil {
		t.Error("the row was cleared although its queue survived")
	}
	if len(d.spy.emitted) != 0 {
		t.Error("a departure was announced for a seat whose queue is still there")
	}
}

// A queue the medium will not make is not announced as a join. Pass 3 stops at
// the create, so nothing hears about a seat that has no queue.
func TestSweepAnnouncesNoJoinWhenTheQueueCannotBeRemade(t *testing.T) {
	d := newPresenceDeployment(t)
	if code, _, errOut := exec("subscribe", "workshop.scribe", "--pid", alivePid(),
		"--type", "acme-cli", "--version", "3.2.0"); code != 0 {
		t.Fatalf("subscribe exited %d (stderr %q)", code, errOut)
	}
	delete(d.spy.exists, "workshop.scribe")
	d.spy.created, d.spy.emitted, d.spy.emittedOn = nil, nil, nil
	d.spy.createErr = fmt.Errorf("cannot reach the medium")

	code, out, errOut := exec("sweep", "workshop")
	assertResult(t, code, out, errOut, 1, "", "loc: cannot reach the medium\n")
	if len(d.spy.emitted) != 0 {
		t.Error("a join was announced for a queue that was never made")
	}
}

// AN INCIDENT THAT CANNOT BE PUBLISHED STOPS THE SWEEP BEFORE PASS 1. The
// medium would not take the report of the fault, so it will not take a
// departure or a join either, and a reconcile that pressed on would act on a
// medium it cannot speak to. Neither pass touched a queue.
func TestSweepStopsWhenTheIncidentCannotBePublished(t *testing.T) {
	d := newPresenceDeployment(t)
	writeFile(t, d.ledger("workshop.bad.json"), "{ not json")
	d.deadRow(t, "workshop.scribe")
	d.spy.exists["workshop.scribe"] = true
	if code, _, errOut := exec("subscribe", "workshop.clerk", "--pid", alivePid(),
		"--type", "acme-cli", "--version", "3.2.0"); code != 0 {
		t.Fatalf("subscribe exited %d (stderr %q)", code, errOut)
	}
	delete(d.spy.exists, "workshop.clerk") // a live row whose queue pass 3 would remake
	d.spy.created, d.spy.deleted, d.spy.emitted, d.spy.emittedOn = nil, nil, nil, nil
	d.spy.emitErr = fmt.Errorf("cannot reach the medium")

	code, out, errOut := exec("sweep", "workshop")
	assertResult(t, code, out, errOut, 1, "", "loc: cannot reach the medium\n")
	if len(d.spy.deleted) != 0 {
		t.Errorf("pass 1 ran: %v", d.spy.deleted)
	}
	if len(d.spy.created) != 0 {
		t.Errorf("pass 3 ran: %v", d.spy.created)
	}
	if _, err := os.Stat(d.ledger("workshop.scribe.json")); err != nil {
		t.Error("the dead row was reaped although the incident could not be published")
	}
}

// TWO UNREADABLE ROWS ARE NAMED TOGETHER, in the plural. One message is what a
// person reads, and a message that names one of two faults sends them back for
// the other.
func TestSweepNamesEveryUnreadableRow(t *testing.T) {
	d := newPresenceDeployment(t)
	writeFile(t, d.ledger("workshop.bad-one.json"), "{ not json")
	writeFile(t, d.ledger("workshop.bad-two.json"), "{ not json either")

	code, out, errOut := exec("sweep", "workshop")
	if code == 0 || out != "" {
		t.Errorf("exit %d, stdout %q; want non-zero and nothing", code, out)
	}
	for _, want := range []string{"the registrations for", "'workshop.bad-one'", "'workshop.bad-two'"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr %q does not carry %q", errOut, want)
		}
	}
	raw, err := os.ReadFile(d.incidentsFile())
	if err != nil {
		t.Fatalf("read incidents: %v", err)
	}
	if lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n"); len(lines) != 2 {
		t.Errorf("%d lines in the incident record, want one per bad row", len(lines))
	}
}

// A SERVER PIDFILE THAT WILL NOT GO STOPS PASS 1. The seat would otherwise have
// no registration and still name a server, which is the half-cleared state pass
// 1 exists to avoid. A directory with something in it is a path os.Remove
// refuses, which is how the failure is arranged here.
func TestSweepStopsWhenTheServerPIDFileWillNotGo(t *testing.T) {
	d := newPresenceDeployment(t)
	d.deadRow(t, "workshop.scribe")
	writeFile(t, filepath.Join(d.serverPIDFile("workshop.scribe"), "inside"), "x")

	code, out, _ := exec("sweep", "workshop")
	if code == 0 || out != "" {
		t.Errorf("exit %d, stdout %q; want non-zero and nothing", code, out)
	}
	if len(d.spy.emitted) != 0 {
		t.Error("a departure was announced although the seat still names a server")
	}
}

// The remaining places a pass can stop. Each one is a medium that took one call
// and refused the next, and in every one the verb reports the refusal rather
// than finishing the pass on a medium that is not answering.
func TestSweepStopsAtEveryFailedCallInAPass(t *testing.T) {
	cases := []struct {
		name  string
		seed  func(d *presenceDeployment, t *testing.T)
		spoil func(s *presenceSpy)
	}{
		{
			name: "pass 1 cannot announce the departure",
			seed: func(d *presenceDeployment, t *testing.T) {
				d.deadRow(t, "workshop.scribe")
				d.spy.exists["workshop.scribe"] = true
			},
			spoil: func(s *presenceSpy) { s.emitErr = fmt.Errorf("cannot reach the medium") },
		},
		{
			name:  "pass 2 cannot destroy the orphan queue",
			seed:  func(d *presenceDeployment, t *testing.T) { d.spy.exists["workshop.ghost"] = true },
			spoil: func(s *presenceSpy) { s.deleteErr = fmt.Errorf("cannot reach the medium") },
		},
		{
			name:  "pass 2 cannot announce the orphan's departure",
			seed:  func(d *presenceDeployment, t *testing.T) { d.spy.exists["workshop.ghost"] = true },
			spoil: func(s *presenceSpy) { s.emitErr = fmt.Errorf("cannot reach the medium") },
		},
		{
			// The row is written by subscribe rather than by hand: a
			// registration is alive only when its recorded start time matches
			// what the operating system says now, so a hand-written start time
			// reads as DEAD and pass 3 would never see the row.
			name: "pass 3 cannot announce the join",
			seed: func(d *presenceDeployment, t *testing.T) {
				if code, _, errOut := exec("subscribe", "workshop.scribe", "--pid", alivePid(),
					"--type", "acme-cli", "--version", "3.2.0"); code != 0 {
					t.Fatalf("subscribe exited %d (stderr %q)", code, errOut)
				}
				delete(d.spy.exists, "workshop.scribe")
				d.spy.created, d.spy.emitted, d.spy.emittedOn = nil, nil, nil
			},
			spoil: func(s *presenceSpy) { s.emitErr = fmt.Errorf("cannot reach the medium") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newPresenceDeployment(t)
			tc.seed(d, t)
			tc.spoil(d.spy)

			code, _, errOut := exec("sweep", "workshop")
			if code != 1 || errOut != "loc: cannot reach the medium\n" {
				t.Errorf("exit %d, stderr %q; want the refusal reported", code, errOut)
			}
		})
	}
}

// THE NAMED FORM IS NARROW. `sweep <instance>` is scoped to that instance, so a
// queue in another one is not its to destroy, however orphaned it looks from
// here. The bare form is the wide one; it is held below.
func TestSweepNamedIsScopedToThatInstanceAlone(t *testing.T) {
	d := newPresenceDeployment(t)
	d.spy.exists["atelier.ghost"] = true

	code, out, errOut := exec("sweep", "workshop")
	assertResult(t, code, out, errOut, 0, "", "")
	if len(d.spy.deleted) != 0 {
		t.Errorf("sweep workshop destroyed %v", d.spy.deleted)
	}
	if !d.spy.exists["atelier.ghost"] {
		t.Error("the other instance's queue is gone")
	}
}

// A ledger that cannot be listed is reported rather than read as empty: an
// unreadable record area and no dead agents look identical otherwise.
func TestSweepReportsALedgerItCannotList(t *testing.T) {
	d := newPresenceDeployment(t)
	d.blockTheLedger(t)

	code, out, _ := exec("sweep", "workshop")
	if code != 1 || out != "" {
		t.Errorf("exit %d, stdout %q; want 1 and nothing", code, out)
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
		`"agent":{"type":"acme-cli","version":"3.2.0"},"process":{"pid":4242,"started":"2026-01-14T09:12:04.006Z"},` +
		`"cwd":"/workspaces/scribe","registered":"2026-01-14T09:12:04.318Z"}]}`

	t.Run("one line per agent", func(t *testing.T) {
		d := newPresenceDeployment(t)
		d.spy.reply = []byte(roster)

		code, out, errOut := exec("registry", "workshop")
		if code != 0 || errOut != "" {
			t.Fatalf("exit %d, stderr %q", code, errOut)
		}
		for _, want := range []string{"workshop.scribe", "acme-cli 3.2.0", "pid 4242", "/workspaces/scribe"} {
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
		"--type", "acme-cli", "--version", "3.2.0"); code != 0 {
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
		"--type", "acme-cli", "--version", "3.2.0"); code != 0 {
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
	writeFile(t, filepath.Join(d.home, "endpoints"), "")
	d.spy.exists["workshop.scribe"] = true

	code, out, errOut := exec("send", "workshop.scribe", "hello")
	assertResult(t, code, out, errOut, 0, "sent → queue.workshop.scribe\n", "")

	// And the file listing a name changes nothing when nobody is attending it.
	writeFile(t, filepath.Join(d.home, "endpoints"), "workshop.clerk\n")
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
