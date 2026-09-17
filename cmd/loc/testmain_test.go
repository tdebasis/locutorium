package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tdebasis/locutorium/internal/config"
)

// TestMain puts a floor under every test in this package: no test in it can
// reach the machine's live deployment.
//
// WHY THE FLOOR IS HERE AND NOT IN THE CASES. config.Home falls back to
// $HOME/.locutorium when LOC_HOME is unset (internal/config/config.go). This
// package now holds a daemon that boots a broker with no authentication and a
// sweep that deletes queues on a ticker. One case that forgets its own home
// would reap a person's live mail. Each case still makes its own scratch home;
// this is the backstop under them, because discipline is not one.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "loc-test-home-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot make a scratch LOC_HOME: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("LOC_HOME", home); err != nil {
		fmt.Fprintf(os.Stderr, "cannot set LOC_HOME: %v\n", err)
		os.Exit(1)
	}

	// THE CHECK IS LOUD AND IT STOPS THE RUN. A silent fallback is what the
	// floor exists to catch.
	live := filepath.Join(os.Getenv("HOME"), ".locutorium")
	if under(config.Home(), live) {
		fmt.Fprintf(os.Stderr, "refusing to run: config.Home() is %s, which is the live deployment at %s\n",
			config.Home(), live)
		os.Exit(1)
	}

	// THE SCRATCH HOME CARRIES A CLOSED PORT. A case that makes no config of
	// its own resolves nats_url to the table's default, nats://127.0.0.1:4222,
	// and that is a live deployment's address on a developer's machine. On
	// 2026-09-16 two cases in this package reached the live broker that way.
	// One of them swept every queue off it. The floor writes a config into the
	// scratch home, so a case that forgets one dials nothing.
	//
	// THE CLIENT-SIDE GUARD IS THE REAL FLOOR, NOT THIS. The nats provider
	// refuses the default address under `go test` (internal/provider/nats/
	// guard.go). This line only means a forgetful case gets a closed port
	// rather than a refusal it did not expect.
	//
	// A case that needs a home with NO config at all still makes its own with
	// t.TempDir. Two do, and neither reads this one.
	closed, err := closedLoopbackURL()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot reserve a closed port: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(home, "config"), []byte("nats_url = "+closed+"\n"), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "cannot write the scratch config: %v\n", err)
		os.Exit(1)
	}

	// A SCRATCH HOME IS NOT A SCRATCH PORT. The key table defaults nats_url to
	// nats://127.0.0.1:4222, which is a live deployment's address on a
	// developer's machine. A case that makes a home of its own and writes no
	// port resolves to that default, and listenAddr passes it because
	// 127.0.0.1 IS loopback. The floor refuses that address at the one seam
	// every broker boot in this package goes through.
	def := config.Default(config.NATSURL)
	u, err := url.Parse(def)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot read the default %s: %v\n", config.NATSURL, err)
		os.Exit(1)
	}
	refuseListen = func(host string, port int) error {
		if host != u.Hostname() || strconv.Itoa(port) != u.Port() {
			return nil
		}
		return fmt.Errorf("refusing to run: nats_url resolves to %s, the product's default. "+
			"A test must write its own ephemeral port into its scratch config; binding the default "+
			"port would take a live deployment's broker on this machine", def)
	}

	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// closedLoopbackURL asks the kernel for a loopback port and gives it straight
// back, then names it as a nats URL. Nothing listens there.
//
// It is loctest.ClosedPort without the *testing.T, because TestMain has none.
func closedLoopbackURL() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := l.Addr().String()
	return "nats://" + addr, l.Close()
}

// under reports whether path is dir or sits inside it.
func under(path, dir string) bool {
	if dir == filepath.Clean(string(filepath.Separator)) || strings.TrimSpace(dir) == "" {
		return false
	}
	path = filepath.Clean(path)
	dir = filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}
