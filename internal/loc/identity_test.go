package loc

import (
	"os"
	"path/filepath"
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
	want := "cannot determine sender identity: set LOC_IDENTITY"
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

// R29 of 2026-09-09 removed the identity hook. A deployment that still holds
// the old file gets the refusal, and the file is not run: an executable left
// in a home must never become a second answer to who is speaking.
func TestIdentityNeverRunsAnIdentityHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", "")
	dir := filepath.Join(home, "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ran := filepath.Join(home, "ran")
	body := "#!/bin/sh\ntouch " + ran + "\necho bob\n"
	if err := os.WriteFile(filepath.Join(dir, "identity"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	id, err := Identity()
	if err == nil {
		t.Fatalf("accepted %q from an identity hook", id)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("the identity hook ran")
	}
}
