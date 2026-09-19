package loc

// The message log READ BACK. LogEvent and its callers write the day files;
// this is the one reader of them, and `loc status` is its first caller.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tdebasis/locutorium/internal/config"
)

// LogDir is where the day files live.
func LogDir() string { return filepath.Join(config.Home(), "run", "log") }

// Event is ONE LINE OF EITHER FAMILY, with every field the writers emit.
//
// The two families stay apart by which key is set, exactly as they do on disk:
// a message event carries `uid`, a seat event carries `seat`, and a caller
// tests the key its family is keyed by. A struct is safe here where it is not
// in the writers, because a reader that finds an empty `uid` on a seat event
// also finds a non-empty `seat` beside it.
type Event struct {
	UID    string `json:"uid,omitempty"`
	Seat   string `json:"seat,omitempty"`
	Status string `json:"status,omitempty"`
	TS     string `json:"ts,omitempty"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Body   string `json:"body,omitempty"`
	By     string `json:"by,omitempty"`
	Reason string `json:"reason,omitempty"`

	// The bell's fields. `try` and `of` place one try in its streak, `result`
	// says what came of it, and `after` is on the give-up line alone.
	//
	// AN OLD `bell-failed` LINE STILL PARSES. No build writes one, and every
	// field it carried — `seat`, `status`, `ts`, `reason` — is above, so a day
	// file written before #144 reads the same as it always did.
	Try    int    `json:"try,omitempty"`
	Of     int    `json:"of,omitempty"`
	Result string `json:"result,omitempty"`
	After  int    `json:"after,omitempty"`
}

// At is the event's own stamp. The second return is false when the line
// carries no stamp this code can parse, and a caller that orders events drops
// such a line rather than sorting it to the epoch.
func (e Event) At() (time.Time, bool) {
	at, err := time.Parse(time.RFC3339, e.TS)
	if err != nil {
		return time.Time{}, false
	}
	return at.UTC(), true
}

// ReadEvents returns every event the day files in dir hold, IN APPEND ORDER:
// the files by name, which is the date, and the lines inside each file as
// they were written.
//
// A line that does not parse is SKIPPED and the rest of the file is still
// read. Many processes append to one file, so a torn line can happen, and
// losing a whole day over one of them would make the log less useful the more
// it is used. The error is for the directory, not for its contents: an absent
// directory is no events and no error, because a deployment that has sent no
// message has no log.
func ReadEvents(dir string) ([]Event, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var out []Event
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			// One unreadable day file does not hide the other days.
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var ev Event
			if json.Unmarshal([]byte(line), &ev) != nil {
				continue
			}
			out = append(out, ev)
		}
	}
	return out, nil
}
