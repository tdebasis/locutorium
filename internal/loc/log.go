package loc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/tdebasis/locutorium/internal/config"
)

// LogSent appends one line to the day's message log.
//
// The line is built in memory and handed to Write ONCE. Many processes append
// to this same file — every `loc send` on the deployment, on whatever day it
// is — and a line written across two Write calls can have another writer's
// line land between the two halves, corrupting both records at once. One call
// is what makes append-only safe without a lock.
//
// Every error here is swallowed: this log is an aid to whoever looks, not a
// dependency `send` has, so a full disk or an unwritable directory must not
// turn a delivered message into a failed one.
func LogSent(e Envelope) {
	dir := filepath.Join(config.Home(), "run", "log")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, time.Now().UTC().Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()

	line, err := json.Marshal(struct {
		UID    string `json:"uid"`
		Status string `json:"status"`
		TS     string `json:"ts"`
		From   string `json:"from"`
		To     string `json:"to"`
		Body   string `json:"body"`
	}{
		UID:    e.ID,
		Status: "sent",
		TS:     e.TS,
		From:   e.From,
		To:     e.To,
		Body:   e.Body,
	})
	if err != nil {
		return
	}
	line = append(line, '\n')
	_, _ = f.Write(line)
}
