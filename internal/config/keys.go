package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	IdleWindow                = "idle_window"
	HeartbeatLogRetentionDays = "heartbeat_log_retention_days"
)

// Keys is the deployment's whole configuration, in the order the file writes
// them.
var Keys = []Key{
	{Provider, "nats", "which transport loc talks to; only nats exists"},
	{NATSURL, "nats://127.0.0.1:4222", "where the broker listens and clients connect"},
	{MonitorURL, "", "the broker's HTTP monitoring endpoint; no verb reads it, and `loc start` opens no such port"},
	{TopicWindow, "7d", "how long a room's messages live before retention removes them"},
	{SendRequiresAttendance, "no", "whether send refuses when nobody is listening at the target"},
	{IdleWindow, "10m", "how long without activity before a seat reads as idle in status"},
	{HeartbeatLogRetentionDays, "7", "heartbeat log files older than this are deleted when loc starts"},
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
func Value(name string) string { return Get(name, Default(name)) }

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
