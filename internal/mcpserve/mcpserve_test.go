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
		Nudge: func(_, line string) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.nudged = append(f.nudged, line)
		},
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

// home lays a scratch deployment and points LOC_HOME at it.
func home(t *testing.T, config string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("LOC_HOME", dir)
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

// ── registration ────────────────────────────────────────────────────────────

func TestServe_RegistersTheRuntimesPidAndTheRuntimesName(t *testing.T) {
	dir := home(t, "provider = none\n")
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
		"--type " + clientName,
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
	if delivery(t, dir) != "" {
		t.Errorf("an ordinary start wrote to the delivery log: %q", delivery(t, dir))
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

// THE BREAKER CAPS WAKES AND SAYS SO ONCE. A suppressed wake loses nothing —
// the messages are in the queue, and the next read finds all of them — which
// is what makes a cap safe to have; a breaker that shouted on every
// suppression would be the very noise it exists to stop.
func TestBell_TheBreakerCapsWakesAndTripsOnce(t *testing.T) {
	dir := home(t, "provider = none\nwake_window_seconds = 1\nwake_breaker_per_minute = 1\n")
	f := &fake{}
	sess, done := serve(t, f.deps())
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

// ── the small pieces ────────────────────────────────────────────────────────

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
	if d.Nudge == nil || d.Now == nil || d.Ppid == nil || d.Wd == nil {
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
