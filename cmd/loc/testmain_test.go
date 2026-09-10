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
