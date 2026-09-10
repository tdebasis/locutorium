package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tdebasis/locutorium/internal/loc"
	"github.com/tdebasis/locutorium/internal/provider"
)

// Nothing in this file reaches a medium. The verbs that need a provider get a
// spy, selected the same way a real one is — by name, out of the deployment's
// config — so the dispatch under test is the dispatch that ships.

type call struct {
	target   string
	envelope []byte
}

type spy struct {
	sends     []call
	publishes []call
	topics    int
	status    int
	closed    int

	// err, when set, is what every operation returns.
	err error
	// out is what Topics and Status write.
	out string
}

func (s *spy) SendQueue(endpoint string, envelope []byte) error {
	s.sends = append(s.sends, call{endpoint, envelope})
	return s.err
}

func (s *spy) PublishTopic(topic string, envelope []byte) error {
	s.publishes = append(s.publishes, call{topic, envelope})
	return s.err
}

func (s *spy) Topics(w io.Writer) error {
	s.topics++
	if s.err != nil {
		return s.err
	}
	_, err := io.WriteString(w, s.out)
	return err
}

func (s *spy) Status(w io.Writer) error {
	s.status++
	if s.err != nil {
		return s.err
	}
	_, err := io.WriteString(w, s.out)
	return err
}

func (s *spy) Close() { s.closed++ }

// installed is the spy the "spy" factory hands out. One registration for the
// whole test binary, because a duplicate is a panic by design.
var installed *spy

func init() {
	provider.Register("spy", func() (provider.Provider, error) {
		if installed == nil {
			return nil, fmt.Errorf("no spy installed for this test")
		}
		return installed, nil
	})
}

// deployment is a scratch $LOC_HOME: a config naming the spy, a registry, and
// a nudge hook that records instead of ringing.
type deployment struct {
	home string
	spy  *spy
}

func newDeployment(t *testing.T, endpoints ...string) *deployment {
	t.Helper()
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "config"), "provider = spy\n")
	writeFile(t, filepath.Join(home, "endpoints"), strings.Join(endpoints, "\n")+"\n")
	writeFile(t, filepath.Join(home, "hooks", "nudge"),
		"#!/bin/sh\necho \"$1 $2\" >> \""+home+"/nudges.log\"\n")
	if err := os.Chmod(filepath.Join(home, "hooks", "nudge"), 0o700); err != nil {
		t.Fatalf("chmod nudge hook: %v", err)
	}

	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", "ada")

	d := &deployment{home: home, spy: &spy{}}
	installed = d.spy
	t.Cleanup(func() { installed = nil })
	return d
}

func (d *deployment) nudges(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(d.home, "nudges.log"))
	if err != nil {
		return ""
	}
	return string(b)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// exec drives the tool exactly as main does, minus the process boundary.
func exec(args ...string) (code int, stdout, stderr string) {
	var out, errOut strings.Builder
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// assertResult is the whole contract of one invocation: the code, and the
// exact bytes on each stream.
func assertResult(t *testing.T, gotCode int, gotOut, gotErr string, wantCode int, wantOut, wantErr string) {
	t.Helper()
	if gotCode != wantCode {
		t.Errorf("exit code %d, want %d", gotCode, wantCode)
	}
	if gotOut != wantOut {
		t.Errorf("stdout:\n got %q\nwant %q", gotOut, wantOut)
	}
	if gotErr != wantErr {
		t.Errorf("stderr:\n got %q\nwant %q", gotErr, wantErr)
	}
}

// ------------------------------------------------------------------- usage

// Everything that means "you typed it wrong" prints the verb list on STDOUT
// and exits 1, and says nothing on stderr.
func TestUsagePaths(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no verb", nil},
		{"empty argument list", []string{}},
		{"unknown verb", []string{"frobnicate"}},
		{"a verb that is nearly right", []string{"sends", "ada", "hi"}},
		{"send with no arguments", []string{"send"}},
		{"send with only an endpoint", []string{"send", "ada"}},
		{"publish with no arguments", []string{"publish"}},
		{"publish with only a topic", []string{"publish", "standup"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeployment(t, "ada", "bob")
			code, out, errOut := exec(tc.args...)
			assertResult(t, code, out, errOut, 1, usage, "")
			if len(d.spy.sends)+len(d.spy.publishes) != 0 {
				t.Error("a mistyped invocation reached the medium")
			}
		})
	}
}

// A too-short send or publish is refused BEFORE the provider is opened: the
// argument check is not allowed to depend on a working deployment.
func TestArgumentCheckHappensBeforeTheProviderIsOpened(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home) // no config at all: any Open would fail
	installed = nil

	for _, args := range [][]string{{"send", "ada"}, {"publish", "standup"}} {
		code, out, errOut := exec(args...)
		assertResult(t, code, out, errOut, 1, usage, "")
	}
}

// ----------------------------------------------------------------- version

// version answers from the VERSION file and touches no medium, so it must work
// on a machine that has just cloned this and configured nothing. Found by CI,
// and only CI could have found it: every machine that runs this by hand
// already has a deployment on it.
func TestVersionNeedsNoDeployment(t *testing.T) {
	t.Setenv("LOC_HOME", filepath.Join(t.TempDir(), "does-not-exist"))
	t.Setenv("LOC_IDENTITY", "")
	installed = nil
	stampVersion(t, "1.4.2")

	code, out, errOut := exec("version")
	assertResult(t, code, out, errOut, 0, "loc 1.4.2\n", "")
}

func TestVersionIgnoresTrailingArguments(t *testing.T) {
	newDeployment(t, "ada")
	stampVersion(t, "1.4.2")

	code, out, errOut := exec("version", "--porcelain")
	assertResult(t, code, out, errOut, 0, "loc 1.4.2\n", "")
}

// Unstamped, the number is read out of the tree the binary lives in — so a
// plain `go build` still tells the truth. The file is planted beside this test
// binary, which is exactly the walk the released binary does from build/bin.
func TestVersionReadsTheTreeWhenNotStamped(t *testing.T) {
	stampVersion(t, "")

	exe, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate the test binary: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	if versionFileAbove(dir) {
		t.Skip("a VERSION already sits above this test binary; the walk cannot be pinned here")
	}

	// With nothing above it, the refusal names where it looked — and it
	// reaches the user in the tool's one error shape.
	refusal := "no VERSION above " + dir + " — this loc is a copy, not a build in its tree"
	if _, err := version(); err == nil {
		t.Error("version() invented a number with no VERSION anywhere above it")
	} else if err.Error() != refusal {
		t.Errorf("got %q, want %q", err.Error(), refusal)
	}
	code, out, errOut := exec("version")
	assertResult(t, code, out, errOut, 1, "", "loc: "+refusal+"\n")

	// Whitespace of every kind is stripped, the same way `tr -d '[:space:]'`
	// does it for the shell.
	path := filepath.Join(dir, "VERSION")
	writeFile(t, path, "  1.4.2\n")
	t.Cleanup(func() { os.Remove(path) })

	got, err := version()
	if err != nil {
		t.Fatalf("version(): %v", err)
	}
	if got != "1.4.2" {
		t.Errorf("got %q, want %q", got, "1.4.2")
	}

	// And it reaches the stream through the verb, not just the function.
	newDeployment(t, "ada")
	code, out, errOut = exec("version")
	assertResult(t, code, out, errOut, 0, "loc 1.4.2\n", "")
}

// A VERSION file that is only whitespace is not a version, and the walk keeps
// going rather than reporting an empty one.
func TestVersionRejectsABlankVERSION(t *testing.T) {
	stampVersion(t, "")

	exe, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate the test binary: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	if versionFileAbove(dir) {
		t.Skip("a VERSION already sits above this test binary; the walk cannot be pinned here")
	}
	path := filepath.Join(dir, "VERSION")
	writeFile(t, path, "\n \t\n")
	t.Cleanup(func() { os.Remove(path) })

	if v, err := version(); err == nil {
		t.Errorf("got %q, want a refusal for a blank VERSION", v)
	}
}

// stampVersion sets the link-time number for one test and puts it back after.
func stampVersion(t *testing.T, v string) {
	t.Helper()
	prev := buildVersion
	buildVersion = v
	t.Cleanup(func() { buildVersion = prev })
}

func versionFileAbove(dir string) bool {
	for i := 0; i < 40; i++ {
		if _, err := os.Stat(filepath.Join(dir, "VERSION")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
	return false
}

// -------------------------------------------------------------- dispatching

// Every working verb reaches its own operation, exactly once, and closes the
// provider on the way out.
func TestVerbsDispatch(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		out    string
		stdout string
		check  func(t *testing.T, s *spy)
	}{
		{
			name:   "send",
			args:   []string{"send", "bob", "message-one"},
			stdout: "sent → queue.bob\n",
			check: func(t *testing.T, s *spy) {
				if len(s.sends) != 1 || s.sends[0].target != "bob" {
					t.Fatalf("SendQueue calls: %v", s.sends)
				}
				if len(s.publishes) != 0 {
					t.Errorf("send also published: %v", s.publishes)
				}
			},
		},
		{
			name:   "publish",
			args:   []string{"publish", "standup", "message-one"},
			stdout: "published → #standup\n",
			check: func(t *testing.T, s *spy) {
				if len(s.publishes) != 1 || s.publishes[0].target != "standup" {
					t.Fatalf("PublishTopic calls: %v", s.publishes)
				}
				if len(s.sends) != 0 {
					t.Errorf("publish also sent: %v", s.sends)
				}
			},
		},
		{
			name:   "topics",
			args:   []string{"topics"},
			out:    "#standup  (1 in window)\n",
			stdout: "#standup  (1 in window)\n",
			check: func(t *testing.T, s *spy) {
				if s.topics != 1 || s.status != 0 {
					t.Errorf("topics=%d status=%d, want 1 and 0", s.topics, s.status)
				}
			},
		},
		{
			name: "status",
			args: []string{"status"},
			out:  "ada          unread: 0\n",
			// The daemon's own state is the first line of the report (#27).
			// The spy carries mail without carrying presence, so the per-seat
			// findings are absent here and the unread report follows directly.
			stdout: "daemon: not running\nada          unread: 0\n",
			check: func(t *testing.T, s *spy) {
				if s.status != 1 || s.topics != 0 {
					t.Errorf("status=%d topics=%d, want 1 and 0", s.status, s.topics)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeployment(t, "ada", "bob")
			d.spy.out = tc.out

			code, out, errOut := exec(tc.args...)
			assertResult(t, code, out, errOut, 0, tc.stdout, "")
			tc.check(t, d.spy)
			if d.spy.closed != 1 {
				t.Errorf("provider closed %d times, want 1", d.spy.closed)
			}
		})
	}
}

// The bytes handed to the medium are an envelope, addressed as the verb says.
func TestSendPutsARealEnvelopeOnTheMedium(t *testing.T) {
	d := newDeployment(t, "ada", "bob")

	if code, _, errOut := exec("send", "bob", "message-one"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	env := parseEnvelope(t, d.spy.sends[0].envelope)
	if env.From != "ada" || env.To != "bob" || env.Kind != "msg" || env.Body != "message-one" {
		t.Errorf("envelope = %+v", env)
	}
}

func TestPublishAddressesTheTopicWithItsHash(t *testing.T) {
	d := newDeployment(t, "ada", "bob")

	if code, _, errOut := exec("publish", "standup", "morning"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	env := parseEnvelope(t, d.spy.publishes[0].envelope)
	if env.To != "#standup" {
		t.Errorf("envelope addressed to %q, want %q", env.To, "#standup")
	}
}

func parseEnvelope(t *testing.T, b []byte) loc.Envelope {
	t.Helper()
	var env loc.Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("the medium was handed something that is not an envelope (%v): %s", err, b)
	}
	return env
}

// ------------------------------------------------------------------ nudges

func TestSendRingsTheRecipient(t *testing.T) {
	d := newDeployment(t, "ada", "bob")

	if code, _, errOut := exec("send", "bob", "message-one"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if got, want := d.nudges(t), "bob [LOC] 1 new → loc read\n"; got != want {
		t.Errorf("nudges: got %q, want %q", got, want)
	}
}

func TestPublishRingsOnlyMentionedEndpointsThatExist(t *testing.T) {
	d := newDeployment(t, "ada", "bob")

	if code, _, errOut := exec("publish", "standup", "@bob and @mallory, and @bob again"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if got, want := d.nudges(t), "bob [LOC] 1 new in #standup → loc read\n"; got != want {
		t.Errorf("nudges: got %q, want %q", got, want)
	}
}

// The nudge is advisory and fires only once the message is on the medium: a
// refused send rings nobody.
func TestARefusedSendRingsNobody(t *testing.T) {
	d := newDeployment(t, "ada", "bob")
	d.spy.err = fmt.Errorf("send failed: no acknowledgement from the store")

	code, out, errOut := exec("send", "bob", "message-one")
	assertResult(t, code, out, errOut, 1, "", "loc: send failed: no acknowledgement from the store\n")
	if got := d.nudges(t); got != "" {
		t.Errorf("a refused send rang: %q", got)
	}
}

// ------------------------------------------------------- refusals, verbatim

func TestRefusals(t *testing.T) {
	long := strings.Repeat("a", loc.MaxBodyChars+1)
	tooLong := fmt.Sprintf("loc: message too long: %d characters, limit %d. "+
		"This channel carries conversation, not documents. "+
		"Put the content in a file or the archive and send a pointer to it.\n",
		loc.MaxBodyChars+1, loc.MaxBodyChars)

	cases := []struct {
		name     string
		identity string
		args     []string
		wantErr  string
	}{
		{
			name:    "send to an endpoint the registry does not have",
			args:    []string{"send", "mallory", "hi"},
			wantErr: "loc: unknown endpoint 'mallory' (not in this deployment's registry)\n",
		},
		{
			name:    "send to the empty endpoint",
			args:    []string{"send", "", "hi"},
			wantErr: "loc: unknown endpoint '' (not in this deployment's registry)\n",
		},
		{
			name:    "send a body over the limit",
			args:    []string{"send", "bob", long},
			wantErr: tooLong,
		},
		{
			name:    "publish a body over the limit",
			args:    []string{"publish", "standup", long},
			wantErr: tooLong,
		},
		{
			name:    "publish to a topic with a capital in it",
			args:    []string{"publish", "Standup", "hi"},
			wantErr: "loc: invalid topic name 'Standup'\n",
		},
		{
			name:    "publish to a topic starting with punctuation",
			args:    []string{"publish", "-standup", "hi"},
			wantErr: "loc: invalid topic name '-standup'\n",
		},
		{
			name:    "publish to a topic with a slash in it",
			args:    []string{"publish", "stand/up", "hi"},
			wantErr: "loc: invalid topic name 'stand/up'\n",
		},
		{
			name:    "publish to the empty topic",
			args:    []string{"publish", "", "hi"},
			wantErr: "loc: invalid topic name ''\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeployment(t, "ada", "bob")
			if tc.identity != "" {
				t.Setenv("LOC_IDENTITY", tc.identity)
			}
			code, out, errOut := exec(tc.args...)
			assertResult(t, code, out, errOut, 1, "", tc.wantErr)
			if len(d.spy.sends)+len(d.spy.publishes) != 0 {
				t.Error("a refused message reached the medium anyway")
			}
		})
	}
}

// A topic name that is legal must not be caught by the grammar. The refusal
// list above is only half the statement.
func TestLegalTopicNamesArePublished(t *testing.T) {
	for _, topic := range []string{"standup", "s", "0", "release-1.4", "a.b_c-d"} {
		t.Run(topic, func(t *testing.T) {
			d := newDeployment(t, "ada")
			code, out, errOut := exec("publish", topic, "hi")
			assertResult(t, code, out, errOut, 0, "published → #"+topic+"\n", "")
			if len(d.spy.publishes) != 1 {
				t.Fatalf("PublishTopic calls: %v", d.spy.publishes)
			}
		})
	}
}

// A body exactly at the limit is fine; the refusal is for one character more.
func TestABodyAtTheLimitIsAccepted(t *testing.T) {
	d := newDeployment(t, "ada", "bob")
	code, out, errOut := exec("send", "bob", strings.Repeat("a", loc.MaxBodyChars))
	assertResult(t, code, out, errOut, 0, "sent → queue.bob\n", "")
	if len(d.spy.sends) != 1 {
		t.Errorf("SendQueue calls: %v", d.spy.sends)
	}
}

// An unattributable caller is refused. There is no 'unknown' sender.
func TestUnattributableCallerIsRefused(t *testing.T) {
	for _, args := range [][]string{
		{"send", "bob", "hi"},
		{"publish", "standup", "hi"},
	} {
		t.Run(args[0], func(t *testing.T) {
			d := newDeployment(t, "ada", "bob")
			t.Setenv("LOC_IDENTITY", "")

			code, out, errOut := exec(args...)
			if code != 1 || out != "" {
				t.Errorf("exit %d, stdout %q; want 1 and nothing", code, out)
			}
			want := "loc: cannot determine sender identity: set LOC_IDENTITY\n"
			if errOut != want {
				t.Errorf("stderr:\n got %q\nwant %q", errOut, want)
			}
			if len(d.spy.sends)+len(d.spy.publishes) != 0 {
				t.Error("an unattributable message reached the medium")
			}
		})
	}
}

// The order of send's checks is load-bearing: identity first, so a caller who
// is nobody is told that rather than being told about the body they sent.
func TestSendChecksIdentityBeforeTheBody(t *testing.T) {
	newDeployment(t, "ada", "bob")
	t.Setenv("LOC_IDENTITY", "")

	_, _, errOut := exec("send", "bob", strings.Repeat("a", loc.MaxBodyChars+1))
	if !strings.Contains(errOut, "cannot determine sender identity") {
		t.Errorf("got %q, want the identity refusal first", errOut)
	}
}

// And the body before the registry, so an over-long message to a name that
// does not exist reports the fixable thing.
func TestSendChecksTheBodyBeforeTheRegistry(t *testing.T) {
	newDeployment(t, "ada", "bob")

	_, _, errOut := exec("send", "mallory", strings.Repeat("a", loc.MaxBodyChars+1))
	if !strings.Contains(errOut, "message too long") {
		t.Errorf("got %q, want the length refusal first", errOut)
	}
}

// ------------------------------------------------------------ say-semantics

// The attendance requirement is config-gated and off by default, so a send to
// a dormant endpoint succeeds unless the deployment asked otherwise.
func TestSendRequiresAttendanceOnlyWhenConfigured(t *testing.T) {
	d := newDeployment(t, "ada", "bob")
	code, out, errOut := exec("send", "bob", "hi")
	assertResult(t, code, out, errOut, 0, "sent → queue.bob\n", "")

	writeFile(t, filepath.Join(d.home, "config"), "provider = spy\nsend_requires_attendance = yes\n")
	code, out, errOut = exec("send", "bob", "hi")
	want := "loc: not attending: 'bob' has no live listener " +
		"(say-semantics: a send expects an attending peer; " +
		"use a durable channel for messages meant to wait)\n"
	assertResult(t, code, out, errOut, 1, "", want)
	if len(d.spy.sends) != 1 {
		t.Errorf("SendQueue calls: %v, want only the first", d.spy.sends)
	}
}

// ------------------------------------------------------- the provider seam

// A deployment with no config file still has a provider: the key table's
// default (internal/config/keys.go). The refusal a caller then sees names the
// next thing that is actually missing, rather than a provider that is not.
//
// THIS PINS A CHANGE. The call sites used to spell their own default, "", and
// an empty name was refused as "no provider configured". The defaults live in
// one table now, and the table says nats, so no caller can produce an empty
// name and that refusal is unreachable from here.
func TestVerbsTakeTheTableDefaultProviderWhenTheFileIsSilent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", "ada")
	installed = nil

	creds := "loc: no credentials for 'ada' at " + home + "/creds/ada\n"
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"publish", "standup", "hi"}, creds},
		{[]string{"topics"}, creds},
		{[]string{"status"}, creds},
		// `send` never reaches the medium: the endpoint's form is refused
		// first, by the presence model the nats provider carries.
		{[]string{"send", "bob", "hi"}, "loc: invalid endpoint name 'bob': an endpoint must match " +
			"<instance>.<agent>, each segment [a-z0-9-]+, with exactly one dot and the underscore barred\n"},
	} {
		t.Run(c.args[0], func(t *testing.T) {
			code, out, errOut := exec(c.args...)
			assertResult(t, code, out, errOut, 1, "", c.want)
		})
	}
}

func TestAProviderThisBuildDoesNotCarry(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "config"), "provider = carrier-pigeon\n")
	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", "ada")
	installed = nil

	code, out, errOut := exec("topics")
	assertResult(t, code, out, errOut, 1, "",
		"loc: unknown provider 'carrier-pigeon' (not built into this loc)\n")
}

// A factory that cannot build its provider fails the verb, and Close is never
// called on a provider that was never opened.
func TestAProviderThatWillNotOpen(t *testing.T) {
	newDeployment(t, "ada")
	installed = nil // the spy factory refuses when nothing is installed

	code, out, errOut := exec("topics")
	assertResult(t, code, out, errOut, 1, "", "loc: no spy installed for this test\n")
}

// Whatever the medium says goes out in the tool's one error shape, unchanged.
func TestAFailureFromTheMediumIsReportedVerbatim(t *testing.T) {
	for _, args := range [][]string{
		{"send", "bob", "hi"},
		{"publish", "standup", "hi"},
		{"topics"},
		{"status"},
	} {
		t.Run(args[0], func(t *testing.T) {
			d := newDeployment(t, "ada", "bob")
			d.spy.err = fmt.Errorf("cannot reach the medium")

			code, out, errOut := exec(args...)
			assertResult(t, code, out, errOut, 1, "", "loc: cannot reach the medium\n")
			if d.spy.closed != 1 {
				t.Errorf("provider closed %d times, want 1 even on failure", d.spy.closed)
			}
		})
	}
}
