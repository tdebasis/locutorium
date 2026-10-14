package presence

import (
	"fmt"
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

// writePID lays a server pid file naming pid and started.
func writePID(t *testing.T, pid int, started string) string {
	t.Helper()
	p := ServerPIDFile("workshop.scribe")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(fmt.Sprintf("%d\n%s\n", pid, started)), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func TestReleaseServerPIDIfOwnRemovesItsOwnClaim(t *testing.T) {
	t.Setenv("LOC_HOME", t.TempDir())
	p := writePID(t, 1234, "2026-01-14T09:00:00.000Z")

	other, err := ReleaseServerPIDIfOwn("workshop.scribe", 1234, "2026-01-14T09:00:00.000Z")
	if err != nil || other != 0 {
		t.Fatalf("release of its own claim: (%d, %v), want (0, nil)", other, err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("its own file survived: %v", err)
	}
}

// The pid alone is not the identity: the same number with another start time
// is a stranger the kernel handed the number to.
func TestReleaseServerPIDIfOwnLeavesAnotherProcesssClaim(t *testing.T) {
	for _, c := range []struct {
		name    string
		pid     int
		started string
	}{
		{"another pid", 5678, "2026-01-14T09:00:00.000Z"},
		{"the same pid, started later", 1234, "2026-01-15T09:00:00.000Z"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("LOC_HOME", t.TempDir())
			p := writePID(t, c.pid, c.started)

			other, err := ReleaseServerPIDIfOwn("workshop.scribe", 1234, "2026-01-14T09:00:00.000Z")
			if err != nil || other != c.pid {
				t.Fatalf("release: (%d, %v), want (%d, nil)", other, err, c.pid)
			}
			if _, err := os.Stat(p); err != nil {
				t.Fatalf("another process's file was removed: %v", err)
			}
		})
	}
}

func TestReleaseServerPIDIfOwnOnAnAbsentFileIsSuccess(t *testing.T) {
	t.Setenv("LOC_HOME", t.TempDir())
	if other, err := ReleaseServerPIDIfOwn("workshop.scribe", 1234, "x"); err != nil || other != 0 {
		t.Fatalf("release of an absent file: (%d, %v), want (0, nil)", other, err)
	}
}

// A file that does not parse names nobody this process can prove is itself.
func TestReleaseServerPIDIfOwnLeavesAFileItCannotRead(t *testing.T) {
	t.Setenv("LOC_HOME", t.TempDir())
	p := ServerPIDFile("workshop.scribe")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte("not a pid\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if other, err := ReleaseServerPIDIfOwn("workshop.scribe", 1234, "x"); err != nil || other != -1 {
		t.Fatalf("release: (%d, %v), want (-1, nil)", other, err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("an unreadable file was removed: %v", err)
	}
}
