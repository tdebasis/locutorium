package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every key in the table is reachable by name, and its default is the value a
// caller gets when the file says nothing.
func TestTheTableAnswersForEveryKey(t *testing.T) {
	t.Setenv("LOC_HOME", t.TempDir())
	for _, k := range Keys {
		if got := Default(k.Name); got != k.Default {
			t.Errorf("Default(%q) = %q, want %q", k.Name, got, k.Default)
		}
		if got := Value(k.Name); got != k.Default {
			t.Errorf("Value(%q) = %q, want the default %q", k.Name, got, k.Default)
		}
		if k.Comment == "" {
			t.Errorf("key %q has no comment; the file it writes would be silent about it", k.Name)
		}
	}
	if got := Default("no-such-key"); got != "" {
		t.Errorf(`Default("no-such-key") = %q, want ""`, got)
	}
}

// The file wins over the table, and only for the key it names.
func TestTheFileOverridesOneKeyAndNoOther(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config"), []byte("idle_timeout = 45s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Value(IdleTimeout); got != "45s" {
		t.Errorf("Value(idle_timeout) = %q, want %q", got, "45s")
	}
	if got := Value(TopicWindow); got != Default(TopicWindow) {
		t.Errorf("Value(topic_window) = %q, want the default %q", got, Default(TopicWindow))
	}
}

// A number the file cannot supply falls back to the table's, rather than to
// zero. A hand-edited line must not stop the daemon.
func TestIntFallsBackToTheTable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	for _, c := range []struct {
		line string
		want int
	}{
		{"", 7},
		{"heartbeat_log_retention_days = 3\n", 3},
		{"heartbeat_log_retention_days = every-so-often\n", 7},
		{"heartbeat_log_retention_days =  2 \n", 2},
	} {
		if err := os.WriteFile(filepath.Join(home, "config"), []byte(c.line), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := Int(HeartbeatLogRetentionDays); got != c.want {
			t.Errorf("Int with %q = %d, want %d", c.line, got, c.want)
		}
	}
}

// WriteDefault writes a file a person can read and this package can parse, and
// it never overwrites one that is there.
func TestWriteDefaultWritesTheTableAndKeepsWhatExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	path := File()
	if path != filepath.Join(home, "config") {
		t.Fatalf("File() = %q, want %q", path, filepath.Join(home, "config"))
	}
	if err := WriteDefault(path); err != nil {
		t.Fatalf("WriteDefault: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, k := range Keys {
		if !strings.Contains(body, "# "+k.Comment+"\n"+k.Name+" = "+k.Default+"\n") {
			t.Errorf("the file has no commented %q; whole file:\n%s", k.Name, body)
		}
		if got := Value(k.Name); got != k.Default {
			t.Errorf("after writing, Value(%q) = %q, want %q", k.Name, got, k.Default)
		}
	}

	// A second write is refused, so a person's edits survive every later start.
	if err := WriteDefault(path); err == nil {
		t.Error("WriteDefault overwrote a file that was already there")
	}
}

// A path whose directory cannot be made is an error, not a silent success.
func TestWriteDefaultReportsAPathItCannotUse(t *testing.T) {
	home := t.TempDir()
	blocker := filepath.Join(home, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteDefault(filepath.Join(blocker, "config")); err == nil {
		t.Error("WriteDefault reported success for a path inside a file")
	}
}

// A config that still carries the old name gets one line that names both keys
// and the value in force. The old line does not set anything: the reader takes
// the table default, and the warning is the only thing that says so.
func TestARenamedKeyIsReportedOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config"), []byte("idle_window = 3m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Get(IdleTimeout, Default(IdleTimeout)); got != "10m" {
		t.Errorf("the old line must not set the new key: got %q, want the default %q", got, "10m")
	}
	var b bytes.Buffer
	WarnRenamedKeys(&b)
	got := b.String()
	if n := strings.Count(got, "\n"); n != 1 {
		t.Fatalf("want one line, got %d: %q", n, got)
	}
	for _, want := range []string{"idle_window", "idle_timeout", "10m"} {
		if !strings.Contains(got, want) {
			t.Errorf("the warning does not name %q: %q", want, got)
		}
	}
}

// A config that carries the new name, or neither name, says nothing. A warning
// that fires always reports nothing.
func TestTheRenameWarningIsSilentWithoutTheOldKey(t *testing.T) {
	for _, line := range []string{"", "idle_timeout = 3m\n"} {
		home := t.TempDir()
		t.Setenv("LOC_HOME", home)
		if err := os.WriteFile(filepath.Join(home, "config"), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		WarnRenamedKeys(&b)
		if b.Len() != 0 {
			t.Errorf("config %q warned: %q", line, b.String())
		}
	}
}

// ------------------------------------------------- the deployment predicate

// Exists and RequireFile answer for the three shapes the path can be in: no
// file, a file, and a DIRECTORY at that name. The third is the one a reader
// gets wrong: Get opens the path and falls back to the defaults when the open
// fails, so a directory behaves exactly like an absent file and has to be
// reported as absent.
//
// THE PLANT THAT TURNS THIS RED: drop the IsRegular test in Exists, so that
// os.Stat succeeding is taken for a config file.
func TestExistsAndRequireFileReadThePathItself(t *testing.T) {
	for _, c := range []struct {
		name  string
		build func(t *testing.T, path string)
		want  bool
	}{
		{"no file at all", func(*testing.T, string) {}, false},
		{"a regular file", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("nats_url = nats://127.0.0.1:1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"an empty regular file", func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"a directory at that path", func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("LOC_HOME", home)
			c.build(t, filepath.Join(home, "config"))

			if got := Exists(); got != c.want {
				t.Errorf("Exists() = %v, want %v", got, c.want)
			}
			err := RequireFile()
			switch {
			case c.want && err != nil:
				t.Errorf("RequireFile() = %v, want nil", err)
			case !c.want && err == nil:
				t.Error("RequireFile() accepted a home with no config file")
			case !c.want:
				if !errors.Is(err, ErrNoDeployment) {
					t.Errorf("RequireFile() = %v, want it to wrap ErrNoDeployment", err)
				}
				if !strings.Contains(err.Error(), File()) {
					t.Errorf("RequireFile() = %v, want it to name %s", err, File())
				}
				if !strings.Contains(err.Error(), "loc start") {
					t.Errorf("RequireFile() = %v, want it to name the way out", err)
				}
			}
		})
	}
}

// A FILE THAT EXISTS AND SAYS NOTHING STILL TAKES THE TABLE'S DEFAULT. This is
// the behaviour RequireFile must not change: a hand-edited file carries the
// lines a person cared about and none of the rest.
func TestAFileSilentOnAKeyStillTakesTheDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config"), []byte("provider = nats\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RequireFile(); err != nil {
		t.Fatalf("RequireFile() = %v, want nil", err)
	}
	if got, want := Value(NATSURL), Default(NATSURL); got != want {
		t.Errorf("Value(%s) = %q, want the table default %q", NATSURL, got, want)
	}
}
