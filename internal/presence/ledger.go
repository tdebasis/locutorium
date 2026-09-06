package presence

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tdebasis/locutorium/internal/config"
)

// The registrations live in a LOCAL LEDGER, beside the processes they
// describe, and the bus stores nothing.
//
// Two reasons, and they are the same reason twice. The registry is held by
// whoever created the agents, not by the medium — a request that nobody
// answers is the truthful reply when no supervisor is running. And liveness is
// a process id, which means something only on the machine the process is on,
// so the record that pairs an endpoint with a pid belongs on that machine too.
// A ledger in the medium would be a fact stored where it cannot be checked.

// Registration is what a subscribe recorded: the same shape the join event
// carries, plus when it was written.
type Registration struct {
	Endpoint   string   `json:"endpoint"`
	Instance   string   `json:"instance"`
	Agent      Agent    `json:"agent"`
	Process    Process  `json:"process"`
	Display    *Display `json:"display,omitempty"`
	Cwd        string   `json:"cwd,omitempty"`
	Registered string   `json:"registered"`
}

// Activity is the LAST APPLIED event for an endpoint — a current picture, not
// a history. Keeping only the newest is what lets a stale event be discarded
// rather than ordered, and it is why a one-shot reader can answer without the
// bus having remembered anything.
type Activity struct {
	TS   string `json:"ts"`
	Kind string `json:"kind"`
}

// Dir is where the ledger lives.
func Dir() string { return filepath.Join(config.Home(), "run", "presence") }

func regPath(endpoint string) string { return filepath.Join(Dir(), endpoint+".json") }

func activityPath(endpoint string) string { return filepath.Join(Dir(), endpoint+".activity.json") }

// Load returns the registration an endpoint holds, or nil when it is free.
// A free endpoint is not an error: it is one of the two answers.
func Load(endpoint string) (*Registration, error) {
	b, err := os.ReadFile(regPath(endpoint))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var r Registration
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("the registration for '%s' is not readable: %v", endpoint, err)
	}
	return &r, nil
}

// Save writes a registration. The write is atomic, so a reader never sees half
// a registration and a crash mid-write leaves the previous one intact.
func Save(r *Registration) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writeAtomic(regPath(r.Endpoint), b)
}

// Remove clears everything the ledger holds for an endpoint — the
// registration and the activity it accumulated. Nothing outlives the
// subscription.
func Remove(endpoint string) error {
	for _, p := range []string{regPath(endpoint), activityPath(endpoint)} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// List returns every registration in one instance, ordered by endpoint. An
// unreadable file is skipped rather than failing the whole listing: a sweep
// that stops at the first bad record leaves the rest of the dead in place.
func List(instance string) ([]*Registration, error) {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Registration
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".activity.json") {
			continue
		}
		endpoint := strings.TrimSuffix(name, ".json")
		if Instance(endpoint) != instance {
			continue
		}
		r, err := Load(endpoint)
		if err != nil || r == nil {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Endpoint < out[j].Endpoint })
	return out, nil
}

// LoadActivity returns the last applied event for an endpoint, or nil when
// none ever was. Absence of activity means idle; it never means away.
func LoadActivity(endpoint string) (*Activity, error) {
	b, err := os.ReadFile(activityPath(endpoint))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var a Activity
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, fmt.Errorf("the activity record for '%s' is not readable: %v", endpoint, err)
	}
	return &a, nil
}

// ApplyActivity records ts/kind as the endpoint's newest fact, and reports
// whether it was applied.
//
// An OLDER event offered after a newer one is DROPPED, not ordered. Hooks fire
// as separate short-lived processes, so two of an agent's own events can arrive
// in either order; the timestamp is what says which is newer, and a start from
// 10:04:25 arriving after an end from 10:04:30 must not flip the agent back to
// working. Discarding is safe because this is a picture and not an archive.
func ApplyActivity(endpoint, ts, kind string) (bool, error) {
	cur, err := LoadActivity(endpoint)
	if err != nil {
		return false, err
	}
	if cur != nil && older(ts, cur.TS) {
		return false, nil
	}
	b, err := json.Marshal(Activity{TS: ts, Kind: kind})
	if err != nil {
		return false, err
	}
	if err := writeAtomic(activityPath(endpoint), b); err != nil {
		return false, err
	}
	return true, nil
}

// older compares two stamps. They are normally both the schema's UTC
// millisecond form, where a byte comparison would do; parsing first is what
// makes a caller-supplied stamp in another legal RFC 3339 offset compare
// correctly rather than lexically.
func older(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339, a)
	tb, errB := time.Parse(time.RFC3339, b)
	if errA == nil && errB == nil {
		return ta.Before(tb)
	}
	return a < b
}

// writeAtomic replaces a file in one step: written beside its destination and
// renamed over it, so a reader sees either the old record or the new one.
func writeAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".write-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // a no-op once the rename has taken it
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, path)
}
