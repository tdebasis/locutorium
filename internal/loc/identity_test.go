package loc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// There is no anonymous sender, ever. A message whose origin is a guess is
// worse than no message, because the reader cannot tell the difference.
func TestIdentityRefusesTheUnattributable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", "")

	_, err := Identity()
	if err == nil {
		t.Fatal("an unattributable caller was accepted")
	}
	want := "cannot determine sender identity: set LOC_IDENTITY or provide an executable " +
		filepath.Join(home, "hooks", "identity")
	if err.Error() != want {
		t.Errorf("refusal text drifted\n got: %s\nwant: %s", err.Error(), want)
	}
}

func TestIdentityPrefersTheEnvironment(t *testing.T) {
	t.Setenv("LOC_HOME", t.TempDir())
	t.Setenv("LOC_IDENTITY", "ada")
	got, err := Identity()
	if err != nil || got != "ada" {
		t.Fatalf("got %q, %v; want ada", got, err)
	}
}

func TestIdentityFallsBackToTheHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", "")
	writeHook(t, home, "#!/bin/sh\necho bob\n", 0o755)

	got, err := Identity()
	if err != nil || got != "bob" {
		t.Fatalf("got %q, %v; want bob", got, err)
	}
}

// A hook that is present but not executable, or that says nothing, is not an
// identity: both fall through to the refusal rather than to a blank name.
func TestIdentityIgnoresAnUnusableHook(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
		mode os.FileMode
	}{
		{"not executable", "#!/bin/sh\necho bob\n", 0o644},
		{"says nothing", "#!/bin/sh\nexit 0\n", 0o755},
		{"fails", "#!/bin/sh\necho bob\nexit 1\n", 0o755},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("LOC_HOME", home)
			t.Setenv("LOC_IDENTITY", "")
			writeHook(t, home, c.body, c.mode)
			if id, err := Identity(); err == nil {
				t.Errorf("accepted %q from an unusable hook", id)
			}
		})
	}
}

func TestIdentityTrimsTheHooksNewline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", "")
	writeHook(t, home, "#!/bin/sh\nprintf 'carol\\n\\n'\n", 0o755)
	got, err := Identity()
	if err != nil {
		t.Fatal(err)
	}
	if got != "carol" || strings.ContainsAny(got, "\n") {
		t.Errorf("got %q, want carol", got)
	}
}

func writeHook(t *testing.T, home, body string, mode os.FileMode) {
	t.Helper()
	dir := filepath.Join(home, "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "identity"), []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}
