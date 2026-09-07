package main

// The four tools, driven over the SDK's in-memory transports.
//
// THE CLAIM IS THAT A TOOL IS THE VERB, so each case asks the tool and then
// asks the command line for the same thing and diffs the two texts. A tool
// that merely "worked" would prove nothing: what must not drift is that an
// agent calling `status` and an operator typing `loc status` are told the same
// sentence. The wiring under test is mcpDeps() — the very one the verb uses —
// so the only thing the transport changes is that no process is spawned.

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tdebasis/locutorium/internal/mcpserve"
)

// seat is one server running over an in-memory pair, with the client already
// connected.
type seat struct {
	sess *mcp.ClientSession
	done chan error
}

// serveInMemory starts the seat's server for endpoint and connects a client.
func serveInMemory(t *testing.T, p *presence) *seat {
	t.Helper()
	stampVersion(t, "1.4.2")
	d, release, err := mcpDeps()
	if err != nil {
		t.Fatalf("wire the server: %v", err)
	}
	t.Cleanup(release)

	serverT, clientT := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- mcpserve.Serve(context.Background(), d, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: testClientName, Version: testClientVersion}, nil)
	sess, err := client.Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatalf("connect to the server: %v", err)
	}
	s := &seat{sess: sess, done: done}
	t.Cleanup(func() {
		_ = sess.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the server did not return after the client went away")
		}
	})
	if !waitFor(5*time.Second, func() bool { return registration(t, e1) != nil }) {
		t.Fatalf("the server did not register %s", e1)
	}
	return s
}

// text calls one tool and returns the text it handed back.
func (s *seat) text(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	res, err := s.sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("the %s tool refused: %s", name, toolText(t, res))
	}
	return toolText(t, res)
}

// cli runs the same request on the command line and returns what it printed.
func cli(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if code := run(args, &out, io.Discard); code != 0 {
		t.Fatalf("loc %s exited %d; it printed %q", strings.Join(args, " "), code, out.String())
	}
	return out.String()
}

func TestMCPTools_EachToolIsTheVerbItIsNamedFor(t *testing.T) {
	p := newPresence(t)
	tune(t, p.home, "wake_window_seconds = 1")
	nudgeSpool(t, p.home)

	p.as(t, e1)
	s := serveInMemory(t, p)

	t.Run("topics", func(t *testing.T) {
		if got, want := s.text(t, "topics", nil), cli(t, "topics"); got != want {
			t.Errorf("the topics tool said %q; loc topics says %q", got, want)
		}
	})

	t.Run("status, bare", func(t *testing.T) {
		if got, want := s.text(t, "status", nil), cli(t, "status"); got != want {
			t.Errorf("the status tool said %q; loc status says %q", got, want)
		}
	})

	t.Run("status of an endpoint", func(t *testing.T) {
		if got, want := s.text(t, "status", map[string]any{"endpoint": e3}), cli(t, "status", e3); got != want {
			t.Errorf("the status tool said %q; loc status %s says %q", got, e3, want)
		}
	})

	t.Run("read: the reminder first, then what loc read prints", func(t *testing.T) {
		if err := p.admin.Publish("queue."+e1, []byte(canned("m1", e2, e1, "first"))); err != nil {
			t.Fatalf("seed: %v", err)
		}
		_ = p.admin.Flush()
		got := s.text(t, "read", nil)

		if !strings.HasPrefix(got, mcpserve.ReadReminder+"\n\n") {
			t.Fatalf("the read result did not open with the reminder; it began %q", first(got, 160))
		}
		body := strings.TrimPrefix(got, mcpserve.ReadReminder+"\n\n")

		// The same message again, for the command line to print. A queue read
		// consumes, so the two runs cannot share one message; the envelope is
		// canned so the two renders are identical anyway.
		if err := p.admin.Publish("queue."+e1, []byte(canned("m1", e2, e1, "first"))); err != nil {
			t.Fatalf("seed: %v", err)
		}
		_ = p.admin.Flush()
		if want := cli(t, "read"); body != want {
			t.Errorf("the read tool's block was %q; loc read prints %q", body, want)
		}
	})

	// THE EVENTS PLANE, NOT THE TOPIC PLANE. Events are published on
	// `presence.<instance>` (internal/provider/nats, eventSubjectPrefix); a
	// witness left on `topic.<instance>` hears an empty room and reports it as
	// a server that emitted nothing.
	t.Run("tool.pre and tool.post bracket the call", func(t *testing.T) {
		w := p.witness(t, "presence."+strings.SplitN(e1, ".", 2)[0])
		_ = s.text(t, "topics", nil)
		var pre, post bool
		for _, ev := range collect(w, 2*time.Second) {
			if strings.Contains(ev, `"tool.pre"`) && strings.Contains(ev, `"topics"`) {
				pre = true
			}
			if strings.Contains(ev, `"tool.post"`) && strings.Contains(ev, `"topics"`) {
				post = true
			}
		}
		if !pre || !post {
			t.Errorf("watch saw tool.pre=%v tool.post=%v around a tools call; "+
				"the server is this seat's adapter and is the only party that knows a tool ran", pre, post)
		}
	})

	// LAST, because it leaves mail behind: a queue read consumes, and a send
	// case that ran before the read case would put its own message in front of
	// the one that case seeded. The seat sends to ITSELF — in this deployment
	// an endpoint may ask after its own queue and no other, which is the ACL
	// the presence model gives an agent.
	t.Run("send", func(t *testing.T) {
		got := s.text(t, "send", map[string]any{"to": e1, "body": "one"})
		want := cli(t, "send", e1, "two")
		if got != want {
			t.Errorf("the send tool said %q; loc send says %q", got, want)
		}
	})
}
