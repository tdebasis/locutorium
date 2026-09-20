package main

import (
	"fmt"
	"net"
	"os"
	osexec "os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/config"
	"github.com/tdebasis/locutorium/internal/loctest"
	model "github.com/tdebasis/locutorium/internal/presence"
)

// ------------------------------------------------------------- the fixtures

// freePort asks the kernel for a port and gives it straight back.
//
// NO TEST IN HERE BINDS 4222. That port is the product's default and a real
// deployment on the machine running these tests is listening on it. A test
// that took it would stop, or be stopped by, a person's live house.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("no free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// scratchHouse is a deployment of one: its own LOC_HOME, its own port, and the
// credentials the provider reads. The broker this boots has no authorization
// block, so the secret is a formality the client hands over and the server
// ignores.
func scratchHouse(t *testing.T) (home string, port int) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", "house.keeper")
	port = freePort(t)
	writeFile(t, filepath.Join(home, "config"),
		fmt.Sprintf("provider = nats\nnats_url = nats://127.0.0.1:%d\n", port))
	return home, port
}

// holdPort listens on port until the test ends, as a broker loc did not start.
func holdPort(t *testing.T, port int) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("cannot hold port %d: %v", port, err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return ln
}

// TestHelperHoldsThePort is not a case. It is the stand-in daemon: a separate
// process that takes the port and waits to be signalled, so `loc stop` and
// `loc stop --force` are driven against a real process and a real signal
// rather than a fake.
func TestHelperHoldsThePort(t *testing.T) {
	addr := os.Getenv("LOC_TEST_HOLD_ADDR")
	if addr == "" {
		t.Skip("not the stand-in")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stand-in cannot listen on %s: %v\n", addr, err)
		os.Exit(1)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		c.Close()
	}
}

// TestHelperExitsAtOnce is not a case either. It is the daemon that dies
// before it ever answers.
func TestHelperExitsAtOnce(t *testing.T) {
	if os.Getenv("LOC_TEST_EXIT_AT_ONCE") == "" {
		t.Skip("not the stand-in")
	}
	os.Exit(0)
}

// standIn starts the stand-in daemon on port and waits for it to answer.
func standIn(t *testing.T, port int) *os.Process {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	cmd := osexec.Command(os.Args[0], "-test.run=^TestHelperHoldsThePort$")
	cmd.Env = append(os.Environ(), "LOC_TEST_HOLD_ADDR="+addr)
	if err := cmd.Start(); err != nil {
		t.Fatalf("cannot start the stand-in: %v", err)
	}
	// ONLY PIDS THIS TEST SPAWNED ARE EVER KILLED.
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	waitUntil(t, "the stand-in to answer", func() bool { return portAnswers(addr) })
	return cmd.Process
}

// waitUntil polls until what says yes, or fails the case.
func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ------------------------------------------------------------- the daemon

// The whole of the daemon, in one process: it boots the broker, creates the
// stream, records itself, beats on its period, and gives everything back when
// it is signalled.
func TestTheDaemonBootsBeatsAndStops(t *testing.T) {
	home, port := scratchHouse(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	sigs := make(chan os.Signal, 1)
	ready := make(chan struct{})
	done := make(chan error, 1)
	var out strings.Builder
	go func() { done <- runDaemon(&out, 100*time.Millisecond, sigs, func() { close(ready) }) }()

	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("the daemon ended before it was ready: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("the daemon never became ready")
	}

	if !portAnswers(addr) {
		t.Errorf("the broker does not answer on %s", addr)
	}
	// The server makes its own store; nothing in loc creates it.
	if fi, err := os.Stat(filepath.Join(home, "store")); err != nil || !fi.IsDir() {
		t.Errorf("no store directory: %v", err)
	}
	// The pidfile names this process, in the two-line shape.
	pid, alive := liveDaemon()
	if !alive || pid != os.Getpid() {
		t.Errorf("pidfile says pid %d alive %v, want %d and true", pid, alive, os.Getpid())
	}
	// The stream the product never created until now.
	assertTopicsStream(t, fmt.Sprintf("nats://127.0.0.1:%d", port))

	// THE BEAT LEAVES A LINE WHEN IT FINDS NOTHING. An empty log is the
	// failure this case exists to catch: it cannot tell a quiet house from a
	// heartbeat that never ran.
	beatFile := filepath.Join(home, "run", "heartbeat", time.Now().Format("2006-01-02")+".log")
	waitUntil(t, "two beats", func() bool { return len(beatLines(t, beatFile)) >= 2 })
	for i, line := range beatLines(t, beatFile) {
		if !strings.HasSuffix(line, "swept, 0 changes") {
			t.Errorf("beat %d reads %q, want a line ending 'swept, 0 changes'", i, line)
		}
		if _, err := time.Parse(time.RFC3339, strings.Fields(line)[0]); err != nil {
			t.Errorf("beat %d does not start with an RFC3339 stamp: %q", i, line)
		}
	}

	sigs <- syscall.SIGTERM
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the daemon ended with %v, want a clean end", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the daemon did not end when it was signalled")
	}
	if _, err := os.Stat(model.DaemonPIDFile()); !os.IsNotExist(err) {
		t.Errorf("the pidfile survived the shutdown: %v", err)
	}
	if portAnswers(addr) {
		t.Errorf("%s still answers after the shutdown", addr)
	}
}

// beatLines is the heartbeat log, one entry per line.
func beatLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	body := strings.TrimRight(string(b), "\n")
	if body == "" {
		return nil
	}
	return strings.Split(body, "\n")
}

// assertTopicsStream asks the broker itself, not the code that made the
// stream, what it holds.
func assertTopicsStream(t *testing.T, url string) {
	t.Helper()
	nc, err := natsgo.Connect(url, natsgo.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("cannot reach the broker: %v", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("cannot open JetStream: %v", err)
	}
	info, err := js.StreamInfo(topicsStream)
	if err != nil {
		t.Fatalf("no %s stream: %v", topicsStream, err)
	}
	if got := info.Config.Subjects; len(got) != 1 || got[0] != "topic.>" {
		t.Errorf("%s captures %v, want [topic.>]", topicsStream, got)
	}
	if info.Config.MaxAge != 7*24*time.Hour {
		t.Errorf("%s max age %s, want the 7d window", topicsStream, info.Config.MaxAge)
	}
	if info.Config.Retention != natsgo.LimitsPolicy || info.Config.Storage != natsgo.FileStorage || info.Config.Replicas != 1 {
		t.Errorf("%s is %v/%v/%d, want limits/file/1", topicsStream,
			info.Config.Retention, info.Config.Storage, info.Config.Replicas)
	}
}

// A stream that is already there is left exactly as it is.
func TestTheDaemonLeavesAStreamItFinds(t *testing.T) {
	_, port := scratchHouse(t)
	sigs := make(chan os.Signal, 1)
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runDaemon(&strings.Builder{}, time.Hour, sigs, func() { close(ready) }) }()
	<-ready

	url := fmt.Sprintf("nats://127.0.0.1:%d", port)
	if err := ensureTopics(url); err != nil {
		t.Errorf("a second ensureTopics failed: %v", err)
	}
	assertTopicsStream(t, url)

	sigs <- syscall.SIGTERM
	if err := <-done; err != nil {
		t.Errorf("the daemon ended with %v", err)
	}
}

// The daemon refuses a listen address that is not this machine's own.
func TestTheDaemonRefusesANonLoopbackAddress(t *testing.T) {
	home, _ := scratchHouse(t)
	writeFile(t, filepath.Join(home, "config"), "provider = nats\nnats_url = nats://0.0.0.0:4222\n")
	err := runDaemon(&strings.Builder{}, time.Hour, make(chan os.Signal), nil)
	if err == nil || !strings.Contains(err.Error(), "refusing to listen on '0.0.0.0'") {
		t.Fatalf("runDaemon returned %v, want a refusal naming the address", err)
	}
}

// ------------------------------------------------------- the address it reads

func TestListenAddrReadsTheURLAndRefusesTheRest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	// THE FLOOR IS ASKED TO STAND ASIDE, AND ONLY HERE. It refuses the
	// product's default address (testmain_test.go), which three of the entries
	// below are about. This case reads an address and binds nothing.
	//
	// THIS IS SAFE ONLY WHILE THE PACKAGE RUNS SERIALLY. refuseListen is one
	// variable for the whole package. No case here calls t.Parallel, so the
	// no-op below is live only for the length of this case. A case that calls
	// t.Parallel could run while this one holds the no-op, and it would boot a
	// broker with no floor under it. Do not add t.Parallel to this case, and
	// do not add it to any case that boots a broker.
	restore := refuseListen
	refuseListen = func(string, int) error { return nil }
	t.Cleanup(func() { refuseListen = restore })
	for _, c := range []struct {
		url      string
		wantHost string
		wantPort int
		wantErr  string
	}{
		{"", "127.0.0.1", 4222, ""},
		{"nats://127.0.0.1:4222", "127.0.0.1", 4222, ""},
		{"nats://localhost:1234", "localhost", 1234, ""},
		{"nats://[::1]:4222", "::1", 4222, ""},
		{"nats://127.0.0.1", "127.0.0.1", 4222, ""},
		{"nats://0.0.0.0:4222", "", 0, "refusing to listen on '0.0.0.0'"},
		{"nats://192.168.1.9:4222", "", 0, "refusing to listen on '192.168.1.9'"},
		{"nats://127.0.0.1:99999", "", 0, "cannot read the port"},
		{"nats://127.0.0.1:not-a-port", "", 0, "cannot read nats_url"},
		{"://:::", "", 0, "cannot read nats_url"},
	} {
		line := ""
		if c.url != "" {
			line = "nats_url = " + c.url + "\n"
		}
		writeFile(t, filepath.Join(home, "config"), line)
		host, port, err := listenAddr()
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%q: %v", c.url, err)
		case c.wantErr == "" && (host != c.wantHost || port != c.wantPort):
			t.Errorf("%q gives %s:%d, want %s:%d", c.url, host, port, c.wantHost, c.wantPort)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%q gives %v, want an error containing %q", c.url, err, c.wantErr)
		}
	}
}

func TestParseWindowReadsDaysAndDurations(t *testing.T) {
	for _, c := range []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"7d", 7 * 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{" 30m ", 30 * time.Minute, false},
		{"2h45m", 2*time.Hour + 45*time.Minute, false},
		{"a week", 0, true},
		{"xd", 0, true},
	} {
		got, err := parseWindow(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseWindow(%q) = %s, want an error", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("parseWindow(%q) = %s, %v; want %s", c.in, got, err, c.want)
		}
	}
}

// ------------------------------------------------------------ the heartbeat

func TestPruneDeletesTheLogsPastTheirRetention(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	plant := func(name string, age time.Duration) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		when := now.Add(-age)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
		return path
	}
	old := plant("2026-09-07.log", 48*time.Hour)
	young := plant("2026-09-09.log", time.Hour)
	other := plant("notes.txt", 48*time.Hour)

	pruneHeartbeats(dir, 1, now)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the two-day-old log survived a one-day retention: %v", err)
	}
	for _, keep := range []string{young, other} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s was deleted and should not have been: %v", keep, err)
		}
	}

	// A retention of zero is no retention at all, and deletes nothing.
	again := plant("2026-09-06.log", 96*time.Hour)
	pruneHeartbeats(dir, 0, now)
	if _, err := os.Stat(again); err != nil {
		t.Errorf("a retention of zero deleted a file: %v", err)
	}
	// A directory that is not there is not an error.
	pruneHeartbeats(filepath.Join(dir, "no-such-dir"), 7, now)
}

// A sweep that fails is on the record, and the daemon keeps beating.
func TestABeatRecordsASweepThatFailed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", "house.keeper")
	// THE SWEEP MUST FAIL, AND IT MUST FAIL AGAINST NOTHING. This case wrote
	// no config at all, so the sweep took the table's default nats_url. On
	// 2026-09-16 that address was the live broker on this machine. The sweep
	// ran against it holding this scratch registry, which is empty, and the
	// pass that deleted a queue on the absence of a row destroyed all six live
	// queues. That pass is gone (#141), and this case still points at a dead
	// address: the sweep must FAIL here, which is what the arm reads. The port
	// below is one the kernel handed out and then closed, so there is no
	// broker to reach.
	writeFile(t, filepath.Join(home, "config"), "nats_url = "+loctest.ClosedPort(t)+"\n")
	beatDir := filepath.Join(home, "run", "heartbeat")
	beatOnce(&strings.Builder{}, beatDir, withPresence)
	lines := beatLines(t, filepath.Join(beatDir, time.Now().Format("2006-01-02")+".log"))
	if len(lines) != 1 || !strings.Contains(lines[0], "sweep failed:") {
		t.Errorf("the log reads %v, want one line naming the failure", lines)
	}
}

// ------------------------------------------------------------- loc start

// The first start writes the file and says what is in it. Every later start
// says where it is and nothing else.
func TestStartWritesTheConfigOnceThenNamesIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)

	var first strings.Builder
	if err := surfaceConfig(&first); err != nil {
		t.Fatalf("surfaceConfig: %v", err)
	}
	for _, k := range config.Keys {
		if !strings.Contains(first.String(), k.Name+" = "+k.Default) {
			t.Errorf("the first start did not print %s; it printed:\n%s", k.Name, first.String())
		}
	}

	var second strings.Builder
	if err := surfaceConfig(&second); err != nil {
		t.Fatalf("surfaceConfig: %v", err)
	}
	want := fmt.Sprintf("config: %s — edit it to change settings\n", filepath.Join(home, "config"))
	if second.String() != want {
		t.Errorf("the second start printed %q, want %q", second.String(), want)
	}
}

func TestStartSaysTheDaemonIsAlreadyRunning(t *testing.T) {
	home, port := scratchHouse(t)
	holdPort(t, port)
	pid := os.Getpid()
	writeFile(t, filepath.Join(home, "run", "loc.pid"), fmt.Sprintf("%d\n%s\n", pid, model.StartedAt(pid)))

	code, out, errOut := exec("start")
	assertResult(t, code, out, errOut, 0,
		fmt.Sprintf("config: %s — edit it to change settings\nalready running, pid %d\n",
			filepath.Join(home, "config"), pid), "")
}

func TestStartSaysWhenAnotherProcessHoldsThePort(t *testing.T) {
	home, port := scratchHouse(t)
	holdPort(t, port)
	// A pidfile naming a process that is gone is the same as no pidfile.
	writeFile(t, filepath.Join(home, "run", "loc.pid"), "999999\n1970-01-01T00:00:00.000Z\n")

	code, out, errOut := exec("start")
	assertResult(t, code, out, errOut, 0,
		fmt.Sprintf("config: %s — edit it to change settings\nport %d is held by a process loc did not start\n",
			filepath.Join(home, "config"), port), "")
}

func TestStartRefusesANonLoopbackAddress(t *testing.T) {
	home, _ := scratchHouse(t)
	writeFile(t, filepath.Join(home, "config"), "provider = nats\nnats_url = nats://0.0.0.0:4222\n")

	code, out, errOut := exec("start")
	if code != 1 || !strings.Contains(errOut, "refusing to listen on '0.0.0.0'") {
		t.Errorf("exit %d, stdout %q, stderr %q; want 1 and a refusal", code, out, errOut)
	}
	if !strings.Contains(errOut, "loopback only") {
		t.Errorf("the refusal does not say why: %q", errOut)
	}
}

// The launch itself, driven through the seam: the parent starts something,
// waits for the port, and reports the pid it started.
func TestStartLaunchesTheDaemonAndReportsItsPID(t *testing.T) {
	home, port := scratchHouse(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	var child *os.Process
	restore := daemonCommand
	daemonCommand = func() (*osexec.Cmd, error) {
		cmd := osexec.Command(os.Args[0], "-test.run=^TestHelperHoldsThePort$")
		cmd.Env = append(os.Environ(), "LOC_TEST_HOLD_ADDR="+addr)
		return cmd, nil
	}
	t.Cleanup(func() {
		daemonCommand = restore
		if child != nil {
			_ = child.Kill()
		}
	})

	code, out, errOut := exec("start")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q; want 0 and nothing", code, errOut)
	}
	m := regexp.MustCompile(`started, pid (\d+)\n$`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("stdout %q, want a line naming the pid it started", out)
	}
	pid, _ := strconv.Atoi(m[1])
	child, _ = os.FindProcess(pid)
	if !portAnswers(addr) {
		t.Errorf("%s does not answer, and start said it started", addr)
	}
	// The daemon's own output has somewhere to go.
	if _, err := os.Stat(filepath.Join(home, "run", "loc.log")); err != nil {
		t.Errorf("no daemon log: %v", err)
	}
}

// A daemon that never answers is a failure, and the message names the log to
// read.
func TestStartReportsADaemonThatNeverAnswers(t *testing.T) {
	scratchHouse(t)
	restoreWait := readyWait
	readyWait = 300 * time.Millisecond
	restore := daemonCommand
	daemonCommand = func() (*osexec.Cmd, error) {
		cmd := osexec.Command(os.Args[0], "-test.run=^TestHelperExitsAtOnce$")
		cmd.Env = append(os.Environ(), "LOC_TEST_EXIT_AT_ONCE=1")
		return cmd, nil
	}
	t.Cleanup(func() { daemonCommand = restore; readyWait = restoreWait })

	code, out, errOut := exec("start")
	if code != 1 || !strings.Contains(errOut, "did not answer") || !strings.Contains(errOut, "loc.log") {
		t.Errorf("exit %d, stdout %q, stderr %q; want 1 and a failure naming the log", code, out, errOut)
	}
}

func TestStartRefusesAnArgumentItDoesNotKnow(t *testing.T) {
	scratchHouse(t)
	code, _, _ := exec("start", "--nonsense")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
}

// ------------------------------------------------------------- loc stop

func TestStopSaysNotRunning(t *testing.T) {
	scratchHouse(t)
	code, out, errOut := exec("stop")
	assertResult(t, code, out, errOut, 0, "not running\n", "")
}

func TestStopEndsTheDaemonItStarted(t *testing.T) {
	home, port := scratchHouse(t)
	p := standIn(t, port)
	writeFile(t, filepath.Join(home, "run", "loc.pid"),
		fmt.Sprintf("%d\n%s\n", p.Pid, model.StartedAt(p.Pid)))

	code, out, errOut := exec("stop")
	assertResult(t, code, out, errOut, 0, "stopped\n", "")
	if portAnswers(fmt.Sprintf("127.0.0.1:%d", port)) {
		t.Error("the port still answers")
	}
	if _, err := os.Stat(model.DaemonPIDFile()); !os.IsNotExist(err) {
		t.Errorf("the pidfile survived the stop: %v", err)
	}
}

// A broker loc did not start is refused, and the refusal names the way past
// itself.
func TestStopRefusesABrokerItDidNotStart(t *testing.T) {
	_, port := scratchHouse(t)
	holdPort(t, port)

	code, out, errOut := exec("stop")
	want := fmt.Sprintf("port %d is held by a process loc did not start\n", port) +
		"stopping it could stop a broker another supervisor owns\n" +
		"to stop it anyway: loc stop --force\n"
	assertResult(t, code, out, errOut, 1, want, "")
}

func TestStopForceEndsTheProcessHoldingThePort(t *testing.T) {
	_, port := scratchHouse(t)
	standIn(t, port)

	code, out, errOut := exec("stop", "--force")
	assertResult(t, code, out, errOut, 0, "stopped\n", "")
	if portAnswers(fmt.Sprintf("127.0.0.1:%d", port)) {
		t.Error("the port still answers")
	}
}

func TestStopRefusesAnArgumentItDoesNotKnow(t *testing.T) {
	scratchHouse(t)
	code, _, _ := exec("stop", "--wrong")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
}

// ------------------------------------------------- the endings and the errors

// TestHelperIgnoresTheSignal is not a case. It is the daemon that will not let
// go of the port.
func TestHelperIgnoresTheSignal(t *testing.T) {
	addr := os.Getenv("LOC_TEST_IGNORE_TERM")
	if addr == "" {
		t.Skip("not the stand-in")
	}
	signal.Ignore(syscall.SIGTERM)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		os.Exit(1)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		c.Close()
	}
}

// serveDaemon is the product's own wiring: it listens for the real signals.
func TestServeDaemonEndsOnARealSignal(t *testing.T) {
	_, port := scratchHouse(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	done := make(chan error, 1)
	go func() { done <- serveDaemon(&strings.Builder{}) }()
	waitUntil(t, "the broker to answer", func() bool { return portAnswers(addr) })

	// Sent to this process, which serveDaemon has just asked to be notified of
	// rather than killed by.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("cannot signal this process: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serveDaemon ended with %v, want a clean end", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("serveDaemon did not end on SIGTERM")
	}
}

// A stream the daemon cannot create ends the boot, and the broker does not
// stay up behind the failure.
func TestTheDaemonEndsWhenTheStreamCannotBeMade(t *testing.T) {
	home, port := scratchHouse(t)
	writeFile(t, filepath.Join(home, "config"), fmt.Sprintf(
		"provider = nats\nnats_url = nats://127.0.0.1:%d\ntopic_window = a week\n", port))

	err := runDaemon(&strings.Builder{}, time.Hour, make(chan os.Signal), nil)
	if err == nil || !strings.Contains(err.Error(), "topic_window") {
		t.Fatalf("runDaemon returned %v, want the window it could not read", err)
	}
	if portAnswers(fmt.Sprintf("127.0.0.1:%d", port)) {
		t.Error("the broker is still up after a failed boot")
	}
}

// A deployment whose run directory cannot be made ends the boot at the
// pidfile, not silently.
func TestTheDaemonEndsWhenItCannotRecordItself(t *testing.T) {
	home, port := scratchHouse(t)
	// `run` is a file, so no directory can be made at that name.
	writeFile(t, filepath.Join(home, "run"), "not a directory")

	err := runDaemon(&strings.Builder{}, time.Hour, make(chan os.Signal), nil)
	if err == nil {
		t.Fatal("runDaemon reported success with no pidfile written")
	}
	if portAnswers(fmt.Sprintf("127.0.0.1:%d", port)) {
		t.Error("the broker is still up after a failed boot")
	}
}

// ensureTopics says so when there is no broker to ask.
func TestEnsureTopicsReportsABrokerItCannotReach(t *testing.T) {
	scratchHouse(t)
	err := ensureTopics(fmt.Sprintf("nats://127.0.0.1:%d", freePort(t)))
	if err == nil || !strings.Contains(err.Error(), "cannot reach the broker") {
		t.Errorf("ensureTopics returned %v, want a refusal naming the broker", err)
	}
}

// A heartbeat log that cannot be written is not a crash. The beat is
// best-effort, and the daemon keeps beating.
func TestAppendBeatSurvivesADirectoryItCannotUse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	blocker := filepath.Join(home, "blocker")
	writeFile(t, blocker, "not a directory")
	appendBeat(filepath.Join(blocker, "heartbeat"), "swept, 0 changes")

	// And a directory it can make, holding a name it cannot open.
	dir := filepath.Join(home, "run", "heartbeat")
	if err := os.MkdirAll(filepath.Join(dir, time.Now().Format("2006-01-02")+".log"), 0o700); err != nil {
		t.Fatal(err)
	}
	appendBeat(dir, "swept, 0 changes")
}

// The pidfile's removal is reported when it fails.
func TestReleaseDaemonPIDReportsWhatItCannotRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	// A directory with something in it cannot be removed as a file.
	writeFile(t, filepath.Join(home, "run", "loc.pid", "inside"), "x")
	if err := releaseDaemonPID(); err == nil {
		t.Error("releaseDaemonPID reported success for a removal that failed")
	}
	// And a pidfile that was never there is a success.
	if err := os.RemoveAll(filepath.Join(home, "run", "loc.pid")); err != nil {
		t.Fatal(err)
	}
	if err := releaseDaemonPID(); err != nil {
		t.Errorf("releaseDaemonPID on an absent file returned %v", err)
	}
}

// The pid lookup says so when nothing holds the port.
func TestPidHoldingPortFindsNothingOnAFreePort(t *testing.T) {
	port := freePort(t)
	if pid, err := pidHoldingPort(port); err == nil {
		t.Errorf("pidHoldingPort(%d) = %d, want an error", port, pid)
	}
}

// A start that cannot build its own re-execution says so.
func TestStartReportsACommandItCannotBuild(t *testing.T) {
	scratchHouse(t)
	restore := daemonCommand
	daemonCommand = func() (*osexec.Cmd, error) { return nil, fmt.Errorf("no executable here") }
	t.Cleanup(func() { daemonCommand = restore })

	code, _, errOut := exec("start")
	if code != 1 || !strings.Contains(errOut, "no executable here") {
		t.Errorf("exit %d, stderr %q; want 1 and the failure", code, errOut)
	}
}

// A config file that cannot be written stops the start.
func TestStartReportsAConfigItCannotWrite(t *testing.T) {
	home := t.TempDir()
	blocker := filepath.Join(home, "blocker")
	writeFile(t, blocker, "not a directory")
	t.Setenv("LOC_HOME", filepath.Join(blocker, "home"))

	code, _, errOut := exec("start")
	if code != 1 || errOut == "" {
		t.Errorf("exit %d, stderr %q; want 1 and a failure", code, errOut)
	}
}

// `loc stop` refuses the same address `loc start` refuses.
func TestStopRefusesANonLoopbackAddress(t *testing.T) {
	home, _ := scratchHouse(t)
	writeFile(t, filepath.Join(home, "config"), "provider = nats\nnats_url = nats://10.0.0.4:4222\n")

	code, _, errOut := exec("stop")
	if code != 1 || !strings.Contains(errOut, "refusing to listen on '10.0.0.4'") {
		t.Errorf("exit %d, stderr %q; want 1 and the refusal", code, errOut)
	}
}

// A process that cannot be signalled is reported, and no pidfile is removed
// behind it.
func TestStopReportsAProcessItCannotSignal(t *testing.T) {
	_, port := scratchHouse(t)
	err := stopPID(&strings.Builder{}, 999999, fmt.Sprintf("127.0.0.1:%d", port))
	if err == nil || !strings.Contains(err.Error(), "999999") {
		t.Errorf("stopPID returned %v, want a failure naming the pid", err)
	}
}

// A daemon that takes the signal and keeps the port is a failure, and the
// message says which pid was signalled.
func TestStopReportsAPortThatNeverCloses(t *testing.T) {
	_, port := scratchHouse(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	cmd := osexec.Command(os.Args[0], "-test.run=^TestHelperIgnoresTheSignal$")
	cmd.Env = append(os.Environ(), "LOC_TEST_IGNORE_TERM="+addr)
	if err := cmd.Start(); err != nil {
		t.Fatalf("cannot start the stand-in: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	waitUntil(t, "the stand-in to answer", func() bool { return portAnswers(addr) })

	restore := readyWait
	readyWait = 500 * time.Millisecond
	t.Cleanup(func() { readyWait = restore })

	err := stopPID(&strings.Builder{}, cmd.Process.Pid, addr)
	if err == nil || !strings.Contains(err.Error(), "still answers") {
		t.Errorf("stopPID returned %v, want a failure naming the port", err)
	}
}

// Both platforms' lookup output is read here, whichever platform this runs on.
func TestPidFromLookupReadsBothTools(t *testing.T) {
	for _, c := range []struct {
		name string
		goos string
		out  string
		want int
	}{
		{"lsof names one pid", "darwin", "812\n", 812},
		{"lsof names several", "darwin", "812\n913\n", 812},
		{"lsof says nothing", "darwin", "", 0},
		{"lsof prints something that is not a pid", "darwin", "no\n", 0},
		{"ss names the listener", "linux",
			"LISTEN 0 128 127.0.0.1:14222 0.0.0.0:* users:((\"nats-server\",pid=812,fd=9))\n", 812},
		{"ss lists another port only", "linux",
			"LISTEN 0 128 127.0.0.1:4222 0.0.0.0:* users:((\"nats-server\",pid=812,fd=9))\n", 0},
		{"ss without the process column", "linux",
			"LISTEN 0 128 127.0.0.1:14222 0.0.0.0:*\n", 0},
		{"ss with a pid of zero", "linux",
			"LISTEN 0 128 127.0.0.1:14222 0.0.0.0:* users:((\"x\",pid=0,fd=9))\n", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			pid, ok := pidFromLookup(c.goos, c.out, 14222)
			if c.want == 0 && ok {
				t.Errorf("found pid %d, want nothing", pid)
			}
			if c.want != 0 && (!ok || pid != c.want) {
				t.Errorf("found %d (%v), want %d", pid, ok, c.want)
			}
		})
	}
}

// A daemon log that cannot be opened stops the start before anything is
// launched.
func TestStartReportsALogItCannotOpen(t *testing.T) {
	home, _ := scratchHouse(t)
	writeFile(t, filepath.Join(home, "run"), "not a directory")
	restore := daemonCommand
	daemonCommand = func() (*osexec.Cmd, error) {
		return osexec.Command(os.Args[0], "-test.run=^TestHelperExitsAtOnce$"), nil
	}
	t.Cleanup(func() { daemonCommand = restore })

	code, _, errOut := exec("start")
	if code != 1 || errOut == "" {
		t.Errorf("exit %d, stderr %q; want 1 and a failure", code, errOut)
	}
}

// A daemon that cannot be launched at all is reported as such.
func TestStartReportsADaemonItCannotLaunch(t *testing.T) {
	scratchHouse(t)
	restore := daemonCommand
	daemonCommand = func() (*osexec.Cmd, error) {
		return osexec.Command(filepath.Join(t.TempDir(), "no-such-binary")), nil
	}
	t.Cleanup(func() { daemonCommand = restore })

	code, _, errOut := exec("start")
	if code != 1 || !strings.Contains(errOut, "cannot start the daemon") {
		t.Errorf("exit %d, stderr %q; want 1 and the failure", code, errOut)
	}
}

// A case with a home of its own and no port of its own is refused before it
// binds anything.
//
// THE HOME IS NOT THE WHOLE ISOLATION. This home is scratch and its nats_url
// still resolves to nats://127.0.0.1:4222, a live deployment's address on the
// machine running the tests. The control for the other direction is
// TestTheDaemonBootsBeatsAndStops: a scratch home with a port of its own boots
// and beats.
func TestTheFloorRefusesTheDefaultPort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", "house.keeper")
	// THE FILE IS WRITTEN, AND IT NAMES THE DEFAULT ON PURPOSE. listenAddr
	// refuses a home with no config file before it reads any address, so this
	// case would otherwise stop at that refusal and never reach the floor it
	// is about. The default address is what a forgetful case resolves to, and
	// the floor is what must refuse it.
	writeFile(t, filepath.Join(home, "config"), "nats_url = "+config.Default(config.NATSURL)+"\n")

	ready := false
	err := runDaemon(&strings.Builder{}, time.Hour, make(chan os.Signal), func() { ready = true })
	if err == nil || !strings.Contains(err.Error(), "the product's default") {
		t.Fatalf("runDaemon returned %v, want the floor's refusal", err)
	}
	if ready {
		t.Error("the daemon became ready, so it bound a port before the floor refused")
	}
}
