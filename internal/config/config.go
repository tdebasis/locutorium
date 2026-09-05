// Package config reads the deployment's flat key=value file.
//
// The format is deliberately not YAML and not TOML: a deployment file a
// person edits by hand should have no parser to own and no way to be subtly
// wrong. One key, one equals sign, one value that runs to the end of the
// line.
package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Home is the deployment root: $LOC_HOME, or ~/.locutorium.
//
// Resolved on every call rather than cached, because a test harness and the
// conformance suite both move it out from under a running process.
func Home() string {
	if h := os.Getenv("LOC_HOME"); h != "" {
		return h
	}
	return filepath.Join(os.Getenv("HOME"), ".locutorium")
}

// Get returns the value for key in $LOC_HOME/config, or def when the file is
// missing, the key is absent, or the value is empty.
//
// The shell reads this file with
//
//	sed -n "s/^[[:space:]]*<key>[[:space:]]*=[[:space:]]*//p" | tail -1
//
// and this reproduces it exactly: leading whitespace and the whitespace
// around the equals sign are optional and of any width, everything after it
// is the value verbatim (trailing whitespace included), and the LAST matching
// line wins rather than the first.
func Get(key, def string) string {
	f, err := os.Open(filepath.Join(Home(), "config"))
	if err != nil {
		return def
	}
	defer f.Close()

	val := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if v, ok := matchKey(sc.Text(), key); ok {
			val = v
		}
	}
	if val == "" {
		return def
	}
	return val
}

// matchKey reports whether line assigns key, and with what value.
func matchKey(line, key string) (string, bool) {
	rest := strings.TrimLeft(line, " \t\v\f\r\n")
	if !strings.HasPrefix(rest, key) {
		return "", false
	}
	rest = strings.TrimLeft(rest[len(key):], " \t\v\f\r\n")
	if !strings.HasPrefix(rest, "=") {
		return "", false
	}
	return strings.TrimLeft(rest[1:], " \t\v\f\r\n"), true
}
