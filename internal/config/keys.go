package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Key is one setting: its name, the value loc uses when the file does not say,
// and one line a person can read.
//
// THE TABLE IS THE ONLY PLACE A DEFAULT LIVES. The defaults were spelled at
// each call site, in four files, and no list of them existed. A default that
// is written twice can be changed once. The daemon also writes the file on
// first run, so the comment in the file and the value in the code come from
// this one table and cannot drift apart.
type Key struct {
	Name    string
	Default string
	Comment string
}

// The key names. They are constants so a caller cannot ask for a key that the
// table does not hold.
const (
	Provider                  = "provider"
	NATSURL                   = "nats_url"
	MonitorURL                = "monitor_url"
	TopicWindow               = "topic_window"
	SendRequiresAttendance    = "send_requires_attendance"
	IdleTimeout               = "idle_timeout"
	HeartbeatLogRetentionDays = "heartbeat_log_retention_days"
	WakeRetrySeconds          = "wake_retry_seconds"
	WakeTries                 = "wake_tries"
)

// Keys is the deployment's whole configuration, in the order the file writes
// them.
var Keys = []Key{
	{Provider, "nats", "which transport loc talks to; only nats exists"},
	{NATSURL, "nats://127.0.0.1:4222", "where the broker listens and clients connect"},
	{MonitorURL, "", "the broker's HTTP monitoring endpoint; no verb reads it, and `loc start` opens no such port"},
	{TopicWindow, "7d", "how long a topic's messages live before retention removes them"},
	{SendRequiresAttendance, "no", "whether send refuses when nobody is listening at the target"},
	{IdleTimeout, "10m", "how long without activity before a seat reads as idle in status"},
	{HeartbeatLogRetentionDays, "7", "heartbeat log files older than this are deleted when loc starts"},
	{WakeRetrySeconds, "60", "the fixed gap between one try of a seat's bell and the next"},
	{WakeTries, "3", "how many times a bell tries while mail is unread, before it gives up"},
}

// Default returns the table's value for name, or "" when the table has no such
// key.
func Default(name string) string {
	for _, k := range Keys {
		if k.Name == name {
			return k.Default
		}
	}
	return ""
}

// Value returns what the deployment says for name, or the table's default.
// Every caller in the tree asks through this function, so no call site carries
// a default of its own.
func Value(name string) string {
	warnOnce.Do(func() { WarnRenamedKeys(os.Stderr) })
	return Get(name, Default(name))
}

// renamed is one key that this release reads under a new name.
type renamed struct {
	Old string
	New string
}

// Renames lists every key whose name changed. A deployment file is written
// once and hand-edited after that: WriteDefault refuses a file that exists, so
// no release can add the new name to an old file. The old line therefore stays
// and does nothing, and the new key takes the table default without saying so.
// Each entry here turns that silence into one line on stderr.
var Renames = []renamed{
	{Old: "idle_window", New: IdleTimeout},
}

// warnOnce keeps the warning to one line per process. Value is the one path
// every caller in the tree reads through, and a tool that reads four keys must
// not say the same thing four times.
var warnOnce sync.Once

// WarnRenamedKeys writes one line to w for each renamed key that the
// deployment's config still carries. The line names the old key, the new key,
// and the value that is in force, which comes from the new key and never from
// the old line. It reads through Get, not Value: Value is what calls this
// function, and a second entry to that call would deadlock on warnOnce.
func WarnRenamedKeys(w io.Writer) {
	for _, r := range Renames {
		if Get(r.Old, "") == "" {
			continue
		}
		fmt.Fprintf(w, "loc: config key %s is now %s; the %s line is ignored, and %s = %s is in force\n",
			r.Old, r.New, r.Old, r.New, Get(r.New, Default(r.New)))
	}
}

// Int returns Value(name) as a number, or the table's default when the file
// holds something that is not one. A bad line in a hand-edited file must not
// stop the daemon.
func Int(name string) int {
	if n, err := strconv.Atoi(strings.TrimSpace(Value(name))); err == nil {
		return n
	}
	n, _ := strconv.Atoi(strings.TrimSpace(Default(name)))
	return n
}

// File is the path to the deployment's config file.
func File() string { return filepath.Join(Home(), "config") }

// ErrNoDeployment says that this home holds no config file.
//
// THE DEFAULTS ARE NOT A DEPLOYMENT. Every key above has a default, so a
// process with no file reads a whole configuration that nobody chose. The
// default nats_url is the address a real deployment on this machine listens
// on, so a daemon that lost its home dialled a live broker and swept it. A
// caller that acts on the medium asks RequireFile first and is refused.
//
// The text carries no path. This variable is built once, and LOC_HOME moves
// under a running process, so the path is added by RequireFile at the moment
// the question is asked.
var ErrNoDeployment = errors.New("this deployment has no config file; run `loc start` to write one")

// Exists reports whether File is there and is a regular file.
//
// A DIRECTORY AT THAT PATH IS NOT A CONFIG FILE. Get opens the path and falls
// back to the defaults when the open fails, so a directory reads exactly like
// an absent file, and that is the state this predicate must call absent.
func Exists() bool {
	fi, err := os.Stat(File())
	return err == nil && fi.Mode().IsRegular()
}

// RequireFile answers nil when the deployment has a config file, and an error
// naming the path when it does not.
//
// Get and Value are UNCHANGED by this. A file that exists and says nothing
// about a key still takes the table's default, which is what a hand-edited
// file relies on.
func RequireFile() error {
	if Exists() {
		return nil
	}
	return fmt.Errorf("%w (looked for %s)", ErrNoDeployment, File())
}

// WriteDefault writes the table to path, with each key's comment above it.
//
// It refuses to overwrite a file that is there. A person edits this file, and
// a start that rewrote it would discard those edits every boot.
func WriteDefault(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	var b strings.Builder
	b.WriteString("# The Locutorium's settings. One key, one equals sign, one value.\n")
	for _, k := range Keys {
		fmt.Fprintf(&b, "\n# %s\n%s = %s\n", k.Comment, k.Name, k.Default)
	}
	if _, err := f.WriteString(b.String()); err != nil {
		return err
	}
	return f.Close()
}
