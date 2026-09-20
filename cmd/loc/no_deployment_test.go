package main

// A HOME WITH NO CONFIG FILE IS NOT A DEPLOYMENT.
//
// Every key in internal/config/keys.go has a default, so a process whose home
// has no file still reads a whole configuration that nobody chose. The default
// nats_url is the address a real deployment on this machine listens on. A
// daemon that lost its home dialled it and its sweep deleted the queues there.
//
// listenAddr refuses that state for `loc stop`, `loc stop --force` and a
// hand-typed `loc start --serve`. Bare `loc start` still works, because it
// writes the file before it asks where to listen, and that is also the way
// back for a deployment whose file was deleted.
//
// NO CASE HERE RUNS ON A DEVELOPER'S MACHINE. They belong to the CI suite for
// this package; see AGENTS.md on what `go test ./cmd/loc/...` can reach.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/config"
	"github.com/tdebasis/locutorium/internal/loctest"
	model "github.com/tdebasis/locutorium/internal/presence"
)

// noDeploymentMarks are the three things the refusal has to carry: what is
// wrong, where the file was looked for, and the one command that fixes it.
func noDeploymentMarks(home string) []string {
	return []string{"no config file", filepath.Join(home, "config"), "loc start"}
}

// assertRefusedInPlainWords checks the refusal a person reads.
func assertRefusedInPlainWords(t *testing.T, what, errOut, home string) {
	t.Helper()
	for _, want := range noDeploymentMarks(home) {
		if !strings.Contains(errOut, want) {
			t.Errorf("%s said %q, which does not carry %q", what, errOut, want)
		}
	}
}

// ---------------------------------------------------------- loc start --serve

// A hand-typed `loc start --serve` in a home with no config file is refused,
// and it boots nothing.
//
// THE TWO ARTEFACTS ARE THE PROOF THAT NOTHING BOOTED. JetStream makes
// store/ itself when the server starts, and runDaemon writes the pidfile once
// the broker answers. Neither exists after a refusal.
//
// THE PLANT THAT TURNS THIS RED: delete the config.RequireFile call from
// listenAddr. The case then boots a broker at the default address.
func TestServeRefusesAHomeWithNoConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", "house.keeper")

	code, out, errOut := exec("start", serveFlag)
	if code == 0 {
		t.Fatalf("`loc start --serve` exited 0 in a home with no config file (stdout %q)", out)
	}
	assertRefusedInPlainWords(t, "`loc start --serve`", errOut, home)

	if _, err := os.Stat(filepath.Join(home, "store")); !os.IsNotExist(err) {
		t.Errorf("a store directory exists, so the broker booted: %v", err)
	}
	if _, err := os.Stat(model.DaemonPIDFile()); !os.IsNotExist(err) {
		t.Errorf("a daemon pidfile exists, so the daemon recorded itself: %v", err)
	}
	// `--serve` IS NOT THE WRITER. Only bare `loc start` writes the file, and
	// a `--serve` that wrote one would hide the very state this refuses.
	if _, err := os.Stat(config.File()); !os.IsNotExist(err) {
		t.Errorf("`loc start --serve` wrote the config file: %v", err)
	}
}

// BARE `loc start` STILL WORKS WITH NO FILE, AND THIS IS THE ORDER THAT MAKES
// IT WORK: startVerb calls surfaceConfig, which writes the defaults, and only
// then asks listenAddr where to listen. It is also the recovery path for a
// deployment whose file was deleted under a running daemon.
//
// The failure asserted here is the test floor's (testmain_test.go), because a
// freshly written file names the default port and the floor refuses to bind
// it. What matters is that the failure is NOT the missing deployment: the file
// is on disk by then.
//
// THE PLANT THAT TURNS THIS RED: move the config.RequireFile call from
// listenAddr up into startVerb, ahead of surfaceConfig.
func TestBareStartWritesTheFileBeforeItAsksWhereToListen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", "house.keeper")

	code, out, errOut := exec("start")
	if code == 0 {
		t.Fatalf("`loc start` exited 0; the floor was meant to refuse the default port (stdout %q)", out)
	}
	if strings.Contains(errOut, "no config file") {
		t.Errorf("`loc start` was refused for a file it is supposed to write: %q", errOut)
	}
	if !strings.Contains(out, "wrote "+config.File()) {
		t.Errorf("stdout %q does not say the file was written", out)
	}
	if !config.Exists() {
		t.Errorf("no config file at %s after `loc start`", config.File())
	}
}

// ------------------------------------------------------------------ loc stop

// `loc stop` and `loc stop --force` in a home with no config file are refused,
// and nothing is signalled.
//
// THE STAND-IN IS THE FALSIFIER. It is a process this case spawned, holding a
// port, and the pidfile names it. A stop that got past the refusal would find
// that pid alive and signal it, so the port going quiet is how this case sees
// a regression.
//
// THE PLANT THAT TURNS THIS RED: delete the config.RequireFile call from
// listenAddr. Both invocations then reach stopPID and kill the stand-in.
func TestStopRefusesAHomeWithNoConfigFileAndSignalsNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", "house.keeper")

	port := freePort(t)
	held := standIn(t, port)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	writeFile(t, model.DaemonPIDFile(),
		fmt.Sprintf("%d\n%s\n", held.Pid, model.StartedAt(held.Pid)))

	for _, args := range [][]string{{"stop"}, {"stop", forceFlag}} {
		name := strings.Join(args, " ")
		t.Run(name, func(t *testing.T) {
			code, out, errOut := exec(args...)
			if code == 0 {
				t.Fatalf("`loc %s` exited 0 in a home with no config file (stdout %q)", name, out)
			}
			assertRefusedInPlainWords(t, "`loc "+name+"`", errOut, home)
			if !portAnswers(addr) {
				t.Fatalf("`loc %s` signalled the stand-in on %s", name, addr)
			}
		})
	}
}

// ------------------------------------------------------------ the replay

// THE INCIDENT, REPLAYED. A daemon is running with a broker of its own. Its
// config file is rewritten underneath it to name a SECOND broker, which is
// what a restored backup or a second deployment's installer does. Every later
// beat must still sweep the broker this daemon runs.
//
// NEITHER #140 NOR #141 SAVES THIS. #140's rule limited deletion to instances
// the ledger holds a row for, and the planted queue is in instance `house`,
// which the ledger holds for the whole case. #141 removed deletion on an
// absent row altogether, so that queue is now safe from deletion on either
// broker. The daemon's writes still have to land on the right one: the sweep
// REAPS a dead row's queue and CREATES a live row's missing queue, and both of
// those act on whatever broker the provider reaches.
//
// The dead row is there so the sweep has work to do on its own broker, and the
// live row's recreated queue is how this case knows the sweep ran at all. A
// case that asserted only the survivor would pass just as well if every beat
// had failed.
//
// THE PLANT THAT TURNS THIS RED: make nats.brokerURL return
// config.Value(config.NATSURL) unconditionally, so the pin is ignored. The
// repair pass then creates the live row's queue on the SECOND broker, and the
// assertion that the second broker never gains that stream fails.
//
// THE FALSIFIER IS THE REPAIR PASS, AND THE ASSERTION HAD TO MOVE TO IT. Two
// earlier assertions caught the plant and neither catches it now:
//
//   - The planted queue being deleted on the second broker. #141 removed the
//     pass that deleted a queue no row claims, so a mis-aimed sweep no longer
//     destroys it.
//   - The live row's queue being ABSENT from this daemon's own broker. The
//     first beat runs BEFORE the rewrite, on the pinned address either way, and
//     it creates that queue. After the rewrite there is nothing left on the own
//     broker for a mis-aimed sweep to fail to do, so that assertion passes under
//     the plant.
//
// What a mis-aimed sweep still does, every beat after the rewrite, is look for
// the live row's queue on the second broker, fail to find it — that broker
// never had it — and make it. So the second broker gaining a stream named for
// this deployment's live seat is the one effect the plant cannot avoid, and it
// is what this case now asserts.
func TestTheDaemonSweepsItsOwnBrokerAfterTheConfigIsRewritten(t *testing.T) {
	home, port := scratchHouse(t)

	// The other deployment's broker, and the queue a sweep on it would destroy.
	other := loctest.Boot(t)
	otherNC, otherJS := other.Admin(t, "admin", "scratch")
	defer otherNC.Close()
	const orphan = "house.orphan"
	orphanStream := model.StreamName(orphan)
	if _, err := otherJS.AddStream(&natsgo.StreamConfig{
		Name:      orphanStream,
		Subjects:  []string{"queue." + orphan},
		Retention: natsgo.WorkQueuePolicy,
		Storage:   natsgo.MemoryStorage,
		Replicas:  1,
	}); err != nil {
		t.Fatalf("plant %s on the other broker: %v", orphanStream, err)
	}

	// The ledger: one live row, which holds instance `house` for every beat,
	// and one row whose process was never alive.
	const live, gone = "house.keeper", "house.gone"
	pid := os.Getpid()
	writeFile(t, filepath.Join(home, "run", "presence", live+".json"), fmt.Sprintf(
		`{"endpoint":%q,"instance":%q,"process":{"pid":%d,"started":%q},"registered":"2026-01-14T09:12:04.318Z"}`,
		live, model.Instance(live), pid, model.StartedAt(pid)))
	writeFile(t, filepath.Join(home, "run", "presence", gone+".json"), fmt.Sprintf(
		`{"endpoint":%q,"instance":%q,"process":{"pid":4242,"started":"1970-01-01T00:00:00.000Z"},"registered":"2026-01-14T09:12:04.318Z"}`,
		gone, model.Instance(gone)))

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
	defer func() {
		sigs <- syscall.SIGTERM
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Error("the daemon did not end when it was signalled")
		}
	}()

	beatFile := filepath.Join(home, "run", "heartbeat", time.Now().Format("2006-01-02")+".log")
	waitUntil(t, "the first beat", func() bool { return len(beatLines(t, beatFile)) >= 1 })

	// THE REWRITE. The file now names the other broker, exactly as a restored
	// backup would. WriteDefault refuses an existing file, so this is a plain
	// overwrite, which is what an installer or an editor does.
	writeFile(t, filepath.Join(home, "config"),
		"provider = nats\nnats_url = "+other.URL+"\n")
	if got := config.Value(config.NATSURL); got != other.URL {
		t.Fatalf("the rewritten config reads %q, want %q; the case never armed", got, other.URL)
	}

	before := len(beatLines(t, beatFile))
	waitUntil(t, "two beats after the rewrite", func() bool {
		return len(beatLines(t, beatFile)) >= before+2
	})

	// The queue on the other broker survived both beats.
	if _, err := otherJS.StreamInfo(orphanStream); err != nil {
		t.Errorf("%s is gone from the other broker: %v; the sweep followed the rewritten config", orphanStream, err)
	}

	// THE SECOND BROKER NEVER GAINS THIS DEPLOYMENT'S QUEUE. A sweep that
	// followed the rewritten config would find the live row with no queue
	// there and create one, on every beat. A pinned sweep never writes to
	// that broker at all.
	//
	// THE INSTRUMENT GRIPPED: the StreamInfo call above reached this same
	// connection and answered, so a failure here is the stream's absence and
	// not a connection that went away. ErrStreamNotFound is required for the
	// same reason — any other error means the question was not asked.
	if _, err := otherJS.StreamInfo(model.StreamName(live)); !errors.Is(err, natsgo.ErrStreamNotFound) {
		t.Errorf("asking the other broker for %s gave %v, want ErrStreamNotFound; "+
			"the sweep wrote to the broker the rewritten config names",
			model.StreamName(live), err)
	}

	// THE SWEEP RAN AT ALL. On this daemon's own broker the live row's queue
	// was missing and the sweep made it. This fires on a daemon whose every
	// beat failed; it does NOT fire on the plant above, because the first beat
	// makes this queue before the rewrite lands.
	ownNC, ownJS := adminAt(t, fmt.Sprintf("nats://127.0.0.1:%d", port))
	defer ownNC.Close()
	if _, err := ownJS.StreamInfo(model.StreamName(live)); err != nil {
		t.Errorf("%s is not on this daemon's own broker: %v; the sweep did not run there",
			model.StreamName(live), err)
	}
	// And the dead row's own queue was reaped there rather than anywhere else.
	if _, err := os.Stat(filepath.Join(home, "run", "presence", gone+".json")); !os.IsNotExist(err) {
		t.Errorf("the dead row survived every beat: %v", err)
	}
}

// adminAt opens an independent connection to one broker, for a case that
// asserts against a server loctest did not boot.
func adminAt(t *testing.T, url string) (*natsgo.Conn, natsgo.JetStreamContext) {
	t.Helper()
	nc, err := natsgo.Connect(url, natsgo.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("cannot reach %s: %v", url, err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		t.Fatalf("jetstream at %s: %v", url, err)
	}
	return nc, js
}
