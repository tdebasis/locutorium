package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHome(t *testing.T) {
	t.Setenv("LOC_HOME", "/somewhere/else")
	if Home() != "/somewhere/else" {
		t.Errorf("LOC_HOME ignored: %s", Home())
	}
	t.Setenv("LOC_HOME", "")
	t.Setenv("HOME", "/h")
	if Home() != "/h/.locutorium" {
		t.Errorf("default house wrong: %s", Home())
	}
}

// The verified defaults. These are what the tool does when a deployment says
// nothing, and a deployment that says nothing is the common case.
func TestDefaults(t *testing.T) {
	t.Setenv("LOC_HOME", t.TempDir())
	for _, c := range []struct{ key, def string }{
		{"nats_url", "nats://127.0.0.1:4222"},
		{"monitor_url", ""},
		{"topic_window", "7d"},
		{"wake_window_seconds", "5"},
		{"wake_breaker_per_minute", "6"},
		{"wake_breaker_per_hour", "60"},
		{"send_requires_attendance", "no"},
		{"provider", ""},
	} {
		if got := Get(c.key, c.def); got != c.def {
			t.Errorf("Get(%q) = %q with no config, want the default %q", c.key, got, c.def)
		}
	}
}

func TestGet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	body := "" +
		"provider = nats\n" +
		"nats_url=nats://127.0.0.1:4999\n" +
		"   topic_window   =   30d\n" +
		"# commented = no\n" +
		"empty =\n" +
		"nats_urlx = decoy\n" +
		"topic_window = 60d\n"
	if err := os.WriteFile(filepath.Join(home, "config"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ key, def, want string }{
		{"provider", "", "nats"},
		// Whitespace around '=' is optional and of any width.
		{"nats_url", "d", "nats://127.0.0.1:4999"},
		// The LAST assignment wins, not the first.
		{"topic_window", "7d", "60d"},
		// An empty value is not a value.
		{"empty", "fallback", "fallback"},
		{"absent", "fallback", "fallback"},
		// A key is matched whole: 'nats_url' must not read 'nats_urlx'.
		{"nats_urlx", "", "decoy"},
		// A '#' line is not an assignment of 'commented'.
		{"commented", "unset", "unset"},
	}
	for _, c := range cases {
		if got := Get(c.key, c.def); got != c.want {
			t.Errorf("Get(%q, %q) = %q, want %q", c.key, c.def, got, c.want)
		}
	}
}
