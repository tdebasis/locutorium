package mcpserve

// The seat's server, driven against FAKE VERBS.
//
// The verbs themselves are pinned in cmd/loc, against a real broker: what is
// under test here is everything the server decides ON TOP of them — whose pid
// it registers, whom it refuses, what reaches the pane and how often, what
// goes in the delivery log, and what the read tool puts in front of the mail.
// Handing in functions instead of a medium is what lets a case say "the store
// cannot answer" or "the previous occupant is dead" without arranging a broker
// to be in that state.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tdebasis/locutorium/internal/loc"
	model "github.com/tdebasis/locutorium/internal/presence"
)

const (
	seat       = "workshop.scribe"
	clientName = "acme-runtime"
	clientVer  = "9.9.9"
)

// ── the fake tool around the server ─────────────────────────────────────────

type fake struct {
	mu sync.Mutex

	subscribed   [][]string
	unsubscribed [][]string
	emitted      []string
	nudged       []string

	// The callbacks Watch was handed, so a case can make an arrival happen.
	arrived, reconnected func()

	unread    int
	unreadErr error
	nudgeErr  error
	sendErr   error
	slowLeave time.Duration
}

func (f *fake) deps() Deps {
	return Deps{
		Endpoint: seat,
		Version:  "1.4.2",
		Subscribe: func(args []string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.subscribed = append(f.subscribed, args)
			return nil
		},
		Unsubscribe: func(args []string) error {
			time.Sleep(f.slowLeave)
			f.mu.Lock()
			defer f.mu.Unlock()
			f.unsubscribed = append(f.unsubscribed, args)
			return nil
		},
		Send: func(w io.Writer, to, body string) error {
			if f.sendErr != nil {
				return f.sendErr
			}
			_, err := fmt.Fprintf(w, "sent → queue.%s\n", to)
			return err
		},
		Read: func(w io.Writer, peek bool) error {
			_, err := io.WriteString(w, "── queue."+seat+" ──\n")
			if err != nil {
				return err
			}
			// Two envelopes, each written in ONE call, as read.go promises.
			for _, line := range []string{"  `a -> b` first\n", "  `a -> b` second\n"} {
				if _, err := io.WriteString(w, line); err != nil {
					return err
				}
			}
			_, err = io.WriteString(w, "── topics ──\n")
			return err
		},
		Status: func(w io.Writer, endpoint string) error {
			_, err := fmt.Fprintf(w, "status of %q\n", endpoint)
			return err
		},
		Topics: func(w io.Writer) error {
			_, err := io.WriteString(w, "(no active topics)\n")
			return err
		},
		Emit: func(kind, endpoint, tool string) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.emitted = append(f.emitted, kind+" "+endpoint+" "+tool)
		},
		Watch: func(_ string, arrived, reconnected func()) (func(), error) {
			f.mu.Lock()
			f.arrived, f.reconnected = arrived, reconnected
			f.mu.Unlock()
			return func() {}, nil
		},
		Unread: func(string) (int, error) { return f.unread, f.unreadErr },
		Notify: notifierFunc(func(_, _, bell string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.nudged = append(f.nudged, bell)
			return f.nudgeErr
		}),
		// A pid that is alive and is not this process, so "our parent" is a
		// fact a case can arrange rather than inherit.
		Ppid: os.Getppid,
		Wd:   func() (string, error) { return "/scratch", nil },
	}
}

func (f *fake) bells() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.nudged...)
}

// ready waits until the server has wired its listener up: a client's Connect
// returns as soon as the pipe is up, which is BEFORE the seat has been
// registered and the bell started.
func (f *fake) ready(t *testing.T) {
	t.Helper()
	if !waitFor(5*time.Second, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.arrived != nil
	}) {
		t.Fatal("the server never started listening on the seat's queue")
	}
}

// ring is one arrival, as the medium would report it.
func (f *fake) ring() {
	f.mu.Lock()
	a := f.arrived
	f.mu.Unlock()
	a()
}

// reconnect is the connection coming back after a break.
func (f *fake) reconnect() {
	f.mu.Lock()
	r := f.reconnected
	f.mu.Unlock()
	r()
}

func (f *fake) events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.emitted...)
}

// home lays a scratch deployment and points LOC_HOME at it. It also fills in
// the two listener facts every case needs to get past the deployment's own
// preflight (LOC_LISTENER_TYPE, LOC_LISTENER_ADDRESS) — a case about their
// absence overrides them itself.
func home(t *testing.T, config string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("LOC_HOME", dir)
	t.Setenv("LOC_LISTENER_TYPE", "tmux")
	t.Setenv("LOC_LISTENER_ADDRESS", "workshop:1.2")
	return dir
}

// hold writes a registration for the seat, as a predecessor would have left.
func hold(t *testing.T, pid int, started string) {
	t.Helper()
	if err := os.MkdirAll(model.Dir(), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	b, err := json.Marshal(&model.Registration{
		Endpoint: seat, Instance: "workshop",
		Agent:      model.Agent{Type: "acme-cli", Version: "1.0.0"},
		Process:    model.Process{PID: pid, Started: started},
		Registered: model.Now(),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(model.Dir(), seat+".json"), b, 0o600); err != nil {
		t.Fatalf("write registration: %v", err)
	}
}

// delivery is the seat's delivery log.
func delivery(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "run", seat+".delivery.log"))
	if err != nil {
		return ""
	}
	return string(b)
}

// serve starts the server on an in-memory pair and connects a client to it.
func serve(t *testing.T, d Deps) (*mcp.ClientSession, chan error) {
	t.Helper()
	serverT, clientT := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), d, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: clientVer}, nil)
	sess, err := client.Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return sess, done
}

// stop closes the client and waits for the server to finish leaving.
func stop(t *testing.T, sess *mcp.ClientSession, done chan error) {
	t.Helper()
	_ = sess.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the server returned %v; an ordinary ending is a clean one", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not return after the runtime let go")
	}
}

func toolText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		tc, ok := c.(*mcp.TextContent)
		if !ok {
			t.Fatalf("a tool returned %T, not text", c)
		}
		b.WriteString(tc.Text)
	}
	return b.String()
}

// ── registration ────────────────────────────────────────────────────────────

func TestServe_RegistersTheRuntimesPidAndTheRuntimesName(t *testing.T) {
	dir := home(t, "provider = none\n")
	t.Setenv("LOC_LISTENER_TYPE", "tmux")
	t.Setenv("LOC_LISTENER_ADDRESS", "workshop:1.2")
	f := &fake{}
	sess, done := serve(t, f.deps())
	stop(t, sess, done)

	if len(f.subscribed) != 1 {
		t.Fatalf("subscribed %d times; a seat is taken once", len(f.subscribed))
	}
	got := strings.Join(f.subscribed[0], " ")
	want := []string{
		seat,
		"--pid " + fmt.Sprint(os.Getppid()),
		"--type tmux",
		"--address workshop:1.2",
		"--version " + clientVer,
		"--display scribe",
		"--cwd /scratch",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("the registration is missing %q; it was: %s", w, got)
		}
	}
	if len(f.unsubscribed) != 1 || !strings.Contains(strings.Join(f.unsubscribed[0], " "), "--reason clean") {
		t.Errorf("the seat was not freed cleanly on the way out: %v", f.unsubscribed)
	}
	// An ordinary session should log goodbye and left lines, with no other entries.
	log := delivery(t, dir)
	if !strings.Contains(log, "goodbye "+seat+": eof") || !strings.Contains(log, "left "+seat) {
		t.Errorf("an ordinary session should log goodbye and left; got: %q", log)
	}
}

// A DEAD PREDECESSOR IS DISPLACED, AND SAID SO. A registration whose process
// has no start time was already gone when it was recorded, which is exactly
// what a crashed runtime leaves behind.
func TestServe_DisplacesADeadIncumbentAndNamesItsPid(t *testing.T) {
	dir := home(t, "provider = none\n")
	hold(t, 999999, "")
	f := &fake{}
	sess, done := serve(t, f.deps())
	stop(t, sess, done)

	if len(f.subscribed) != 1 {
		t.Fatalf("a dead incumbent was not displaced: subscribed %d times", len(f.subscribed))
	}
	if len(f.unsubscribed) < 2 || !strings.Contains(strings.Join(f.unsubscribed[0], " "), "--force") {
		t.Errorf("the displacement was not forced: %v", f.unsubscribed)
	}
	if !strings.Contains(delivery(t, dir), "displaced "+seat+": pid 999999 is gone") {
		t.Errorf("the displacement left no trace; the log said %q", delivery(t, dir))
	}
}

// A LIVE SEAT IS NOT TAKEN, and the refusal names the pid — the only thing
// that lets an operator tell a crashed predecessor from a running agent.
func TestServe_RefusesASeatHeldByAnotherLiveProcess(t *testing.T) {
	home(t, "provider = none\n")
	incumbent := exec.Command("sleep", "30")
	if err := incumbent.Start(); err != nil {
		t.Fatalf("start the stand-in incumbent: %v", err)
	}
	pid := incumbent.Process.Pid
	t.Cleanup(func() { _ = incumbent.Process.Kill(); _, _ = incumbent.Process.Wait() })
	hold(t, pid, model.StartedAt(pid))

	f := &fake{}
	serverT, _ := mcp.NewInMemoryTransports()
	err := Serve(context.Background(), f.deps(), serverT)
	if err == nil {
		t.Fatal("a second server took a live seat")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("pid %d", pid)) {
		t.Errorf("the refusal did not name the incumbent's pid %d: %v", pid, err)
	}
	if len(f.subscribed) != 0 {
		t.Errorf("a refused server registered anyway: %v", f.subscribed)
	}
}

// NO LISTENER FACTS, NO SEAT. The deployment sets LOC_LISTENER_TYPE and
// LOC_LISTENER_ADDRESS; a server with neither has no default to fall back on
// and nothing to register the seat as reachable through, so it refuses before
// it ever touches the handshake or the registry.
func TestServe_RefusesWithoutListenerEnv(t *testing.T) {
	dir := home(t, "provider = none\n")
	t.Setenv("LOC_LISTENER_TYPE", "") // unset, as home's default is overridden
	f := &fake{}
	serverT, _ := mcp.NewInMemoryTransports()
	// Serve must RETURN, with an error, before any client arrives. A Serve that
	// blocks here has started serving — the exact thing this case exists to
	// refuse — and on the code before the check it blocked until go test's own
	// ten-minute timeout. So the wait is bounded, and a timeout is a failure
	// with its own name rather than a hang that looks like a slow test.
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), f.deps(), serverT) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the server started with no LOC_LISTENER_TYPE")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return within 3s: it is serving instead of refusing")
	}
	if len(f.subscribed) != 0 {
		t.Errorf("a refused server registered anyway: %v", f.subscribed)
	}
	if !strings.Contains(delivery(t, dir), "LOC_LISTENER_TYPE") {
		t.Errorf("the refusal was not written to the delivery log: %q", delivery(t, dir))
	}
}

// The seat is freed even when the medium has gone with the runtime. Bounded,
// because nothing is waiting for us by then and a hang would be the worst of
// the endings available.
func TestServe_LeavingIsBounded(t *testing.T) {
	home(t, "provider = none\n")
	f := &fake{slowLeave: 10 * time.Second}
	sess, done := serve(t, f.deps())
	_ = sess.Close()

	start := time.Now()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("a medium that will not answer turned the exit into a hang")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("leaving took %s; it is bounded to %s", elapsed, departureWait)
	}
}

// ── the tools ───────────────────────────────────────────────────────────────

func TestTools_EachIsTheVerbAndTheEventsBracketIt(t *testing.T) {
	dir := home(t, "provider = none\n")
	f := &fake{}
	sess, done := serve(t, f.deps())
	ctx := context.Background()

	call := func(name string, args map[string]any) *mcp.CallToolResult {
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("call %s: %v", name, err)
		}
		return res
	}

	if got := toolText(t, call("topics", nil)); got != "(no active topics)\n" {
		t.Errorf("topics said %q", got)
	}
	if got := toolText(t, call("status", nil)); got != "status of \"\"\n" {
		t.Errorf("bare status said %q", got)
	}
	if got := toolText(t, call("status", map[string]any{"endpoint": "workshop.clerk"})); got != "status of \"workshop.clerk\"\n" {
		t.Errorf("status of an endpoint said %q", got)
	}
	if got := toolText(t, call("send", map[string]any{"to": "workshop.clerk", "body": "hello"})); got != "sent → queue.workshop.clerk\n" {
		t.Errorf("send said %q", got)
	}

	got := toolText(t, call("read", nil))
	if !strings.HasPrefix(got, ReadReminder+"\n\n") {
		t.Fatalf("read did not open with the reminder: %q", got)
	}
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Errorf("read did not hand the messages over: %q", got)
	}
	// TWO handed over, and the log says so — the only remaining evidence that
	// a message was delivered and read, if the agent never prints it.
	if !strings.Contains(delivery(t, dir), "read "+seat+" handed=2") {
		t.Errorf("the delivery log did not record what was handed over: %q", delivery(t, dir))
	}

	events := f.events()
	for _, want := range []string{
		"tool.pre " + seat + " topics", "tool.post " + seat + " topics",
		"tool.pre " + seat + " read", "tool.post " + seat + " read",
	} {
		if !strings.Contains(strings.Join(events, "\n"), want) {
			t.Errorf("no %q in the event stream: %v", want, events)
		}
	}
	stop(t, sess, done)
}

// A verb's refusal comes back in the shape the command line prints it in, so
// an agent reading a tool error and an operator reading a terminal are reading
// the same sentence.
func TestTools_ARefusalKeepsTheToolsOneErrorShape(t *testing.T) {
	home(t, "provider = none\n")
	f := &fake{sendErr: fmt.Errorf("nobody is attending 'workshop.clerk'")}
	sess, done := serve(t, f.deps())

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "send", Arguments: map[string]any{"to": "workshop.clerk", "body": "x"}})
	if err != nil {
		t.Fatalf("call send: %v", err)
	}
	if !res.IsError {
		t.Fatal("a refused send came back as a success")
	}
	if got := toolText(t, res); !strings.HasPrefix(got, "loc: ") {
		t.Errorf("the refusal was %q; the command line prints 'loc: <what>'", got)
	}
	stop(t, sess, done)
}

// ── the bell ────────────────────────────────────────────────────────────────

func TestBell_ArrivalsInsideTheWindowRingOnce(t *testing.T) {
	dir := home(t, "provider = none\nwake_window_seconds = 1\n")
	f := &fake{}
	sess, done := serve(t, f.deps())
	defer stop(t, sess, done)

	f.ready(t)
	for i := 0; i < 3; i++ {
		f.ring()
	}
	time.Sleep(2 * time.Second)

	if got := f.bells(); len(got) != 1 || got[0] != "🔔 3 new → read" {
		t.Fatalf("three arrivals in one window rang %v; want one bell, %q", got, "🔔 3 new → read")
	}
	if !strings.Contains(delivery(t, dir), "wake "+seat+" count=3") {
		t.Errorf("the wake was not written down: %q", delivery(t, dir))
	}
}

// A BELL THAT COULD NOT RING SAYS SO.
//
// The nudge hook is the one part of a wake that lives outside this binary, and
// it is the part most likely to be missing or broken — an unset deployment, a
// file without its execute bit, a script that exits non-zero. A wake lost that
// way is exactly as invisible as a wake that was never due, so the failure is
// written where the deployment already looks for what was delivered, and said
// once on the runtime's own log.
//
// THE REAL NOTIFIER, not the fake seam: the case is about what happens when
// the bell cannot ring, so the seam is left unfilled and the seat's registered
// type is tmux with a pane address that names no pane.
func TestBell_ABellThatCouldNotRingSaysSo(t *testing.T) {
	dir := home(t, "provider = none\nwake_window_seconds = 1\n")
	f := &fake{}
	d := f.deps()
	d.Notify = nil // the real notifier, and its pane does not exist
	var stderr bytes.Buffer
	d.Stderr = &stderr
	sess, done := serve(t, d)
	defer stop(t, sess, done)

	f.ready(t)
	f.ring()
	time.Sleep(2 * time.Second)

	log := delivery(t, dir)
	if !strings.Contains(log, "bell failed "+seat+":") {
		t.Errorf("the delivery log does not say the bell failed: %q\n"+
			"a wake that could not ring is indistinguishable from one that was never due "+
			"unless the failure is written down", log)
	}
	if !strings.Contains(stderr.String(), "bell failed "+seat+":") {
		t.Errorf("nothing reached the runtime's log: %q", stderr.String())
	}
	if got := f.bells(); len(got) != 0 {
		t.Errorf("the fake seam recorded %v; this case runs the real hook", got)
	}
}

// The other way a bell fails: the hook is there and exits non-zero. Same
// treatment, because the seat's occupant is equally untold either way, and the
// wake is NOT recorded — a line saying the pane was woken when it was not is
// worse than no line at all.
func TestBell_AHookThatExitsNonZeroIsAFailedBell(t *testing.T) {
	dir := home(t, "provider = none\nwake_window_seconds = 1\n")
	f := &fake{nudgeErr: fmt.Errorf("exit status 3")}
	d := f.deps()
	var stderr bytes.Buffer
	d.Stderr = &stderr
	sess, done := serve(t, d)
	defer stop(t, sess, done)

	f.ready(t)
	f.ring()
	time.Sleep(2 * time.Second)

	log := delivery(t, dir)
	if !strings.Contains(log, "bell failed "+seat+": exit status 3") {
		t.Errorf("the delivery log does not carry the hook's own reason: %q", log)
	}
	if strings.Contains(log, "wake "+seat) {
		t.Errorf("a failed bell was written down as a wake: %q", log)
	}
	if !strings.Contains(stderr.String(), "bell failed "+seat) {
		t.Errorf("nothing reached the runtime's log: %q", stderr.String())
	}
}

// WAKE ON BACKLOG, at start and after every reconnect: those are the two
// moments an arrival can have been seen by nobody.
func TestBell_ABacklogRingsAtStartAndAfterAReconnect(t *testing.T) {
	home(t, "provider = none\nwake_window_seconds = 1\n")
	f := &fake{unread: 4}
	sess, done := serve(t, f.deps())
	defer stop(t, sess, done)

	if !waitFor(3*time.Second, func() bool { return len(f.bells()) == 1 }) {
		t.Fatalf("a seat that came up to a full queue was told %v", f.bells())
	}
	if got := f.bells()[0]; got != "🔔 4 new → read" {
		t.Errorf("the backlog bell said %q; four were waiting", got)
	}

	f.reconnect()
	if !waitFor(3*time.Second, func() bool { return len(f.bells()) == 2 }) {
		t.Errorf("a reconnect did not ask after the backlog again: %v", f.bells())
	}
}

// A store that cannot say how much is waiting rings nothing: a bell with no
// number in it would be a bell for a queue that may well be empty.
func TestBell_AnUnanswerableBacklogRingsNothing(t *testing.T) {
	home(t, "provider = none\n")
	f := &fake{unreadErr: fmt.Errorf("cannot reach the medium")}
	sess, done := serve(t, f.deps())
	defer stop(t, sess, done)

	time.Sleep(300 * time.Millisecond)
	if got := f.bells(); len(got) != 0 {
		t.Errorf("an unanswerable backlog rang %v", got)
	}
}

// THE BACKLOG FIGURE IS THE BROKER'S, NOT A SECOND COUNT.
//
// Unread is the consumer's NumPending, and it ALREADY INCLUDES every arrival
// the watcher has counted since the last ring. Adding the two together counted
// one message twice: a seat that received a single message in the stretch
// between the watcher going live and the backlog sample was rung for two, and
// the second of them does not exist. The sample SETS the figure instead, and
// keeps whichever is larger — a figure the broker holds is the truth, and the
// watcher's tally is only ever a lower bound on it.
func TestBell_TheBacklogSampleDoesNotCountAnArrivalTwice(t *testing.T) {
	home(t, "provider = none\nwake_window_seconds = 30\n")
	f := &fake{unread: 1}
	d := f.deps()
	d.Watch = arrivalsDuringWatch(d.Watch, 1)
	sess, done := serve(t, d)
	defer stop(t, sess, done)

	if !waitFor(3*time.Second, func() bool { return len(f.bells()) == 1 }) {
		t.Fatalf("the backlog sample rang %v; one message was waiting and it is one bell", f.bells())
	}
	if got := f.bells()[0]; got != "🔔 1 new → read" {
		t.Errorf("the bell said %q; the watcher's arrival and the broker's count "+
			"are the SAME message, and the seat has one thing to read", got)
	}
	// The coalescing window is thirty seconds and nothing else arrives, so a
	// second bell could only come from a phantom the sample left pending.
	time.Sleep(300 * time.Millisecond)
	if got := f.bells(); len(got) != 1 {
		t.Errorf("rang %v; the sample must leave nothing behind", got)
	}
}

// THE LARGER FIGURE WINS, AND IT IS THE BROKER'S. A watcher that counted two
// while the queue holds five has simply not been listening for the whole of
// the queue's life, which is the ordinary case at start.
func TestBell_TheBacklogSampleTakesTheLargerFigure(t *testing.T) {
	home(t, "provider = none\nwake_window_seconds = 30\n")
	f := &fake{unread: 5}
	d := f.deps()
	d.Watch = arrivalsDuringWatch(d.Watch, 2)
	sess, done := serve(t, d)
	defer stop(t, sess, done)

	if !waitFor(3*time.Second, func() bool { return len(f.bells()) == 1 }) {
		t.Fatalf("rang %v; want one bell", f.bells())
	}
	if got := f.bells()[0]; got != "🔔 5 new → read" {
		t.Errorf("the bell said %q; the broker holds five and the watcher saw two of them", got)
	}
}

// arrivalsDuringWatch wraps the fake's Watch so that n arrivals land in the one
// stretch a case cannot otherwise reach: after the watcher is live and before
// startBell takes its backlog sample.
func arrivalsDuringWatch(watch func(string, func(), func()) (func(), error), n int) func(string, func(), func()) (func(), error) {
	return func(endpoint string, arrived, reconnected func()) (func(), error) {
		stop, err := watch(endpoint, arrived, reconnected)
		if err != nil {
			return stop, err
		}
		for i := 0; i < n; i++ {
			arrived()
		}
		return stop, nil
	}
}

// THE BREAKER CAPS WAKES AND SAYS SO ONCE. A suppressed wake loses nothing —
// the messages are in the queue, and the next read finds all of them — which
// is what makes a cap safe to have; a breaker that shouted on every
// suppression would be the very noise it exists to stop.
func TestBell_TheBreakerCapsWakesAndTripsOnce(t *testing.T) {
	dir := home(t, "provider = none\nwake_window_seconds = 1\nwake_breaker_per_minute = 1\n")
	f := &fake{}
	d := f.deps()

	// Inject a clock pinned to the start of a minute so the test outcome does not
	// depend on where a minute boundary happens to fall. The case is about the
	// breaker's cap, not about calendar timing: a wall clock spanning 7.5s could
	// cross a minute boundary and reset the counter, making the test decision
	// non-deterministic. Base time is truncated to the start of its minute; Now
	// returns base + elapsed time since the case started, so 7.5s of test time
	// stays within one calendar minute by construction.
	startCaseTime := time.Now()
	base := startCaseTime.Truncate(time.Minute)
	d.Now = func() time.Time {
		return base.Add(time.Since(startCaseTime))
	}

	sess, done := serve(t, d)
	defer stop(t, sess, done)

	f.ready(t)
	for round := 0; round < 3; round++ {
		f.ring()
		// Comfortably past the one-second window: each ring must reach the
		// breaker as its OWN wake, or the case would be testing coalescing.
		time.Sleep(2500 * time.Millisecond)
	}

	if got := f.bells(); len(got) != 1 {
		t.Errorf("a cap of one per minute let %d bells through: %v", len(got), got)
	}
	if n := strings.Count(delivery(t, dir), "BREAKER TRIPPED"); n != 1 {
		t.Errorf("the breaker announced itself %d times; once per trip", n)
	}
}

// A medium that cannot be watched costs the WAKE and nothing else: the seat
// stays registered, the tools keep working, and the agent can still read.
func TestBell_AnUnwatchableMediumStillServesTheSeat(t *testing.T) {
	home(t, "provider = none\n")
	f := &fake{}
	d := f.deps()
	d.Watch = func(string, func(), func()) (func(), error) { return nil, fmt.Errorf("cannot reach the medium") }
	sess, done := serve(t, d)
	defer stop(t, sess, done)

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "topics"})
	if err != nil {
		t.Fatalf("call topics on a seat with no listener: %v", err)
	}
	if got := toolText(t, res); got != "(no active topics)\n" {
		t.Errorf("topics said %q", got)
	}
}

// ── the small pieces ────────────────────────────────────────────────────────

// queueCount counts what the QUEUE handed over and nothing else, and passes
// every byte through untouched.
func TestQueueCount_CountsQueueEnvelopesOnly(t *testing.T) {
	var out strings.Builder
	c := &queueCount{w: &out}
	for _, s := range []string{
		"── queue.workshop.scribe ──\n",
		"  `a -> b` one\n",
		"  `a -> b` two\n",
		"── topics ──\n",
		"  `a -> #room` not mail\n",
	} {
		if _, err := io.WriteString(c, s); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if c.n != 2 {
		t.Errorf("counted %d handed over; two came out of the queue", c.n)
	}
	if !strings.HasSuffix(out.String(), "not mail\n") {
		t.Errorf("the counter changed what was written: %q", out.String())
	}
}

// A peek's trailer stands where the rooms would be, and begins with the same
// heading — so it closes the queue exactly as the plain heading does.
func TestQueueCount_ThePeekTrailerClosesTheQueue(t *testing.T) {
	c := &queueCount{w: io.Discard}
	_, _ = io.WriteString(c, "── queue.workshop.scribe ──\n")
	_, _ = io.WriteString(c, "  `a -> b` one\n")
	_, _ = io.WriteString(c, "── topics ── (not shown: --peek never consumes)\n")
	_, _ = io.WriteString(c, "trailing noise\n")
	if c.n != 1 {
		t.Errorf("counted %d; one was handed over", c.n)
	}
}

// A sender picks its own identity, and the identity is the first bytes of the
// envelope a reader sees. A seat named like the topics heading must not be able
// to close the queue early and take every envelope after it out of the count.
func TestQueueCount_ASenderNamedLikeTheHeadingCannotSilenceTheCount(t *testing.T) {
	render := func(from string) string {
		var b strings.Builder
		err := loc.RenderOne(&b, loc.Envelope{
			ID:   "0bb1d1ab-0000-4000-8000-000000000001",
			TS:   "2026-09-12T00:00:00Z",
			From: from,
			To:   "workshop.scribe",
			Kind: "mail",
			Body: "one line",
		})
		if err != nil {
			t.Fatalf("render an envelope from %q: %v", from, err)
		}
		return b.String()
	}
	c := &queueCount{w: io.Discard}
	for _, s := range []string{
		"── queue.workshop.scribe ──\n",
		render("── topics ──x"),
		render("a"),
		"── topics ──\n",
	} {
		if _, err := io.WriteString(c, s); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if c.n != 2 {
		t.Errorf("counted %d handed over; two envelopes came out of the queue", c.n)
	}
}

func TestConfigInt_ATypoIsNotNoCapAtAll(t *testing.T) {
	home(t, "provider = none\nwake_breaker_per_minute = six\nwake_breaker_per_hour = 0\n")
	if got := configInt("wake_breaker_per_minute", defaultBreakerMinute); got != defaultBreakerMinute {
		t.Errorf("a non-numeric cap read as %d; want the default %d", got, defaultBreakerMinute)
	}
	if got := configInt("wake_breaker_per_hour", defaultBreakerHour); got != defaultBreakerHour {
		t.Errorf("a zero cap read as %d; want the default %d", got, defaultBreakerHour)
	}
	if got := configInt("wake_window_seconds", defaultWakeWindow); got != defaultWakeWindow {
		t.Errorf("an absent key read as %d; want the default %d", got, defaultWakeWindow)
	}
}

func TestAgentToken(t *testing.T) {
	if got := agentToken("workshop.scribe"); got != "scribe" {
		t.Errorf("agentToken = %q", got)
	}
	// Bare names are refused long before here; the token is still the whole
	// of what there is, rather than an empty display name.
	if got := agentToken("scribe"); got != "scribe" {
		t.Errorf("agentToken of a bare name = %q", got)
	}
}

func TestWithDefaults_FillsTheSeamsWithTheRealThing(t *testing.T) {
	d := Deps{}.withDefaults()
	if d.Now == nil || d.Ppid == nil || d.Wd == nil || d.Stderr == nil {
		t.Fatal("a zero Deps left a seam unfilled")
	}
	if d.Ppid() != os.Getppid() {
		t.Errorf("the default parent is %d; this process's is %d", d.Ppid(), os.Getppid())
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

// notifierFunc adapts a function to the Notifier interface, so a case that
// only wants to see the bell line does not have to declare a type for it.
type notifierFunc func(endpoint, address, bell string) error

func (f notifierFunc) Ring(endpoint, address, bell string) error {
	return f(endpoint, address, bell)
}
