package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// buildVersion can be stamped at link time:
//
//	go build -ldflags "-X main.buildVersion=$(tr -d '[:space:]' < VERSION)"
//
// When it is empty the number is read from the tree, so a plain `go build`
// still tells the truth.
var buildVersion = ""

// version returns the one number this build claims to be.
//
// VERSION IS THE SOURCE OF TRUTH AND IT IS STATED ONCE. The number is not
// copied into the Go tree: a second copy is a second thing to forget, and the
// release check exists precisely because a version that disagrees with itself
// is not a version. So the file is found by walking up from wherever this
// binary actually lives — which handles both a binary in build/bin and a
// symlink on PATH pointing into a clone.
func version() (string, error) {
	if buildVersion != "" {
		return buildVersion, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot locate this binary to find its VERSION: %v", err)
	}
	// A relative symlink target is relative to the link that named it, not to
	// whoever is calling; EvalSymlinks walks the whole chain for us.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	for i := 0; i < 40; i++ {
		if b, err := os.ReadFile(filepath.Join(dir, "VERSION")); err == nil {
			v := strings.Map(func(r rune) rune {
				if unicode.IsSpace(r) {
					return -1
				}
				return r
			}, string(b))
			if v != "" {
				return v, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("no VERSION above %s — this loc is a copy, not a build in its tree", filepath.Dir(exe))
}
