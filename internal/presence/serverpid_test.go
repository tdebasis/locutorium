package presence

import (
	"os"
	"path/filepath"
	"testing"
)

// Releasing the server's pid file is REACHING A STATE, not performing an
// action. A seat that never wrote one and a seat whose file has been removed
// are the same seat as far as the caller is concerned: neither names a server.
func TestReleaseServerPIDOnAnAbsentFileIsSuccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)

	if err := ReleaseServerPID("workshop.scribe"); err != nil {
		t.Fatalf("release of an absent file: %v, want nil", err)
	}
}

// Line 32 of serverpid.go returns any error that is not IsNotExist. Absence
// is the only failure Release treats as success; a non-empty directory at
// the path is a real failure, and it must reach the caller unchanged.
func TestReleaseServerPIDReportsAFailureThatIsNotAbsence(t *testing.T) {
	scratch(t)

	p := ServerPIDFile("workshop.scribe")
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(p, "child"), nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := ReleaseServerPID("workshop.scribe"); err == nil {
		t.Fatalf("release of a non-empty directory: nil, want an error")
	}

	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("the path is no longer a directory")
	}
}

func TestReleaseServerPIDRemovesThePresentFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)

	path := ServerPIDFile("workshop.scribe")
	if want := filepath.Join(home, "run", "workshop.scribe.mcp.pid"); path != want {
		t.Fatalf("ServerPIDFile = %q, want %q", path, want)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("1234\n2026-01-14T09:00:00Z\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := ReleaseServerPID("workshop.scribe"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the file survived the release: %v", err)
	}
}
