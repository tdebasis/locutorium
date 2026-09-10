package main

import (
	"fmt"
	"os"
	"path/filepath"
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

	// A SCRATCH HOME IS NOT A SCRATCH PORT. The key table defaults nats_url to
	// nats://127.0.0.1:4222, which is a live deployment's address on a
	// developer's machine. A case that takes this home and writes no port of
	// its own resolves to that default, listenAddr passes it because 127.0.0.1
	// IS loopback, and the daemon binds the live broker's port. So the floor's
	// own home names a port no unprivileged process can bind: a case that
	// forgot to write its own fails at the bind, loudly, instead of taking a
	// port that belongs to somebody.
	if err := os.WriteFile(filepath.Join(home, "config"),
		[]byte("provider = nats\nnats_url = nats://127.0.0.1:1\n"), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "cannot write the scratch config: %v\n", err)
		os.Exit(1)
	}
	if refusal := unsafePort(); refusal != "" {
		fmt.Fprintln(os.Stderr, refusal)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
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

// unsafePort is the refusal a scratch deployment earns when its nats_url
// resolves to the product's default, and "" when it names a port of its own.
//
// It is a function rather than four lines inside TestMain so a case can put a
// home in front of it and read the refusal, instead of the floor being the one
// thing in the package that nothing checks.
func unsafePort() string {
	got := config.Get(config.NATSURL, config.Default(config.NATSURL))
	if got != config.Default(config.NATSURL) {
		return ""
	}
	return fmt.Sprintf("refusing to run: nats_url resolves to %s, the product's default. "+
		"A test must write its own ephemeral port into its scratch config; binding the default "+
		"port would take a live deployment's broker on this machine.", got)
}
