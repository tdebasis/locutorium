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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tdebasis/locutorium/internal/config"
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
