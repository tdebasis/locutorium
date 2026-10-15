package mcpserve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// recorder collects the delivery-log lines a notifier writes.
type recorder struct{ lines []string }

func (r *recorder) log(line string) { r.lines = append(r.lines, line) }

func (r *recorder) joined() string { return strings.Join(r.lines, "\n") }

// ---------------------------------------------------------------- tmux

// scratchPane opens a tmux session of this test's own and returns its pane id
// and the file the pane appends every submitted line to.
//
// THE SESSION IS THIS TEST'S OWN AND IT IS KILLED BY NAME. A developer running
// this suite has a real tmux server with real work in it; nothing here touches
// a session it did not create.
func scratchPane(t *testing.T, prompt bool) (pane, typed string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux on this machine")
	}
	typed = filepath.Join(t.TempDir(), "typed")
	name := fmt.Sprintf("loc-bell-%d-%d", os.Getpid(), time.Now().UnixNano())

	// The pane reads its own standard input and appends every line to a file,
	// so a submitted line is observable and NOTHING TYPED IS EXECUTED.
	body := "while IFS= read -r l; do printf '%s\\n' \"$l\" >> " + typed + "; done"
	if prompt {
		body = "printf '⏵⏵ accept edits mode on\\n❯ \\n'; " + body
	}
	out, err := exec.Command("tmux", "new-session", "-d", "-s", name, "-P", "-F", "#{pane_id}",
		"sh", "-c", body).Output()
	if err != nil {
		t.Skipf("cannot open a scratch tmux session: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })
	pane = strings.TrimSpace(string(out))
	time.Sleep(300 * time.Millisecond) // let the pane paint its prompt
	return pane, typed
}

func TestNotify_TmuxTypesTheBellIntoAPromptAndSubmitsIt(t *testing.T) {
	pane, typed := scratchPane(t, true)
	r := &recorder{}
	n := &tmuxNotifier{log: r.log}

	if err := n.Ring("workshop.scribe", pane, "🔔 1 new → read", ""); err != nil {
		t.Fatalf("a pane at a prompt refused the bell: %v", err)
	}
	var got string
	for i := 0; i < 40; i++ {
		b, _ := os.ReadFile(typed)
		got = string(b)
		if strings.Contains(got, "🔔") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(got, "🔔 1 new → read") {
		t.Errorf("the pane received %q; the bell was never typed and submitted", got)
	}
	if want := "nudge workshop.scribe rang '🔔 1 new → read'"; r.joined() != want {
		t.Errorf("delivery log said %q; want %q", r.joined(), want)
	}
}

func TestNotify_TmuxRefusesAPaneInCopyMode(t *testing.T) {
	pane, typed := scratchPane(t, true)
	if err := exec.Command("tmux", "copy-mode", "-t", pane).Run(); err != nil {
		t.Skipf("cannot put the scratch pane in copy mode: %v", err)
	}
	r := &recorder{}
	n := &tmuxNotifier{log: r.log}

	if err := n.Ring("workshop.scribe", pane, "🔔 1 new → read", ""); err == nil {
		t.Fatal("a pane in copy mode took the bell")
	}
	assertNothingTyped(t, typed)
	if want := "nudge workshop.scribe refused: pane_in_mode"; r.joined() != want {
		t.Errorf("delivery log said %q; want %q", r.joined(), want)
	}
}

// A PANE THAT IS A PLAIN SHELL EXECUTES WHAT IS TYPED INTO IT. This is the one
// arm that is about damage rather than about delivery.
func TestNotify_TmuxRefusesAPaneThatIsNotAPrompt(t *testing.T) {
	pane, typed := scratchPane(t, false)
	r := &recorder{}
	n := &tmuxNotifier{log: r.log}

	if err := n.Ring("workshop.scribe", pane, "🔔 1 new → read", ""); err == nil {
		t.Fatal("a pane with no prompt marker took the bell")
	}
	assertNothingTyped(t, typed)
	if want := "nudge workshop.scribe refused: not a prompt"; r.joined() != want {
		t.Errorf("delivery log said %q; want %q", r.joined(), want)
	}
}

// ONLY A TRANSIENT REFUSAL IS BUSY. The bell asks a busy pane again and never
// asks a broken one, so the two must be told apart at the one place that knows
// which is which. The refusal TEXT does not change with the marker: the day's
// record carries that text and `loc transcript` prints it.
//
// PLANT: in Ring, replace `busyRefusal{n.refuse(endpoint, "pane_in_mode")}`
// with `n.refuse(endpoint, "pane_in_mode")`.
func TestNotify_OnlyATransientRefusalIsBusy(t *testing.T) {
	// NO PANE ADDRESS NEEDS NO TMUX. A seat with no address is broken, and
	// asking it again repairs nothing.
	r := &recorder{}
	n := &tmuxNotifier{log: r.log}
	err := n.Ring("workshop.scribe", "", "🔔 1 new → read", "")
	if err == nil {
		t.Fatal("a seat with no pane address took the bell")
	}
	if errors.Is(err, ErrBusy) {
		t.Errorf("no pane address reads as busy: %v", err)
	}

	// COPY MODE IS BUSY. A person is reading the pane right now.
	pane, _ := scratchPane(t, true)
	if err := exec.Command("tmux", "copy-mode", "-t", pane).Run(); err != nil {
		t.Skipf("cannot put the scratch pane in copy mode: %v", err)
	}
	err = n.Ring("workshop.scribe", pane, "🔔 1 new → read", "")
	if !errors.Is(err, ErrBusy) {
		t.Errorf("a pane in copy mode gave %v; it is busy and the bell asks again", err)
	}
	if err.Error() != "refused: pane_in_mode" {
		t.Errorf("the refusal reads %q; the day's record carries this text", err.Error())
	}

	// A PANE THAT IS NOT AT A PROMPT IS BUSY TOO: the seat is mid-turn.
	other, _ := scratchPane(t, false)
	err = n.Ring("workshop.scribe", other, "🔔 1 new → read", "")
	if !errors.Is(err, ErrBusy) {
		t.Errorf("a pane that is not a prompt gave %v; it is busy and the bell asks again", err)
	}
	if err.Error() != "refused: not a prompt" {
		t.Errorf("the refusal reads %q; the day's record carries this text", err.Error())
	}
}

func assertNothingTyped(t *testing.T, typed string) {
	t.Helper()
	time.Sleep(600 * time.Millisecond)
	if b, err := os.ReadFile(typed); err == nil && strings.TrimSpace(string(b)) != "" {
		t.Errorf("the pane received %q; a refusal must type nothing", string(b))
	}
}

// ---------------------------------------------------------------- none

func TestNotify_NoneRingsNothingAndSaysNothing(t *testing.T) {
	r := &recorder{}
	n, err := newNotifier(ListenerNone, r.log, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Ring("workshop.scribe", "%1", "🔔 1 new → read", ""); err != nil {
		t.Fatalf("the silent notifier failed: %v", err)
	}
	if r.joined() != "" {
		t.Errorf("the silent notifier wrote %q to the delivery log", r.joined())
	}
}

// ---------------------------------------------------------------- claude

// fakeClaude puts a `claude` first on PATH that records its argv and the two
// environment guards, prints a courier's stream that proves a send to the
// session on pane %42, and exits with the code given. THE REAL BINARY IS NEVER
// INVOKED BY A TEST.
func fakeClaude(t *testing.T, exitCode int) string {
	t.Helper()
	return fakeClaudeSays(t, exitCode, courierStream(courierSent))
}

// fakeClaudeSays is fakeClaude with the courier's standard output given.
func fakeClaudeSays(t *testing.T, exitCode int, stdout string) string {
	t.Helper()
	dir := t.TempDir()
	rec := filepath.Join(dir, "argv")
	out := filepath.Join(dir, "stdout")
	if err := os.WriteFile(out, []byte(stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"{ for a in \"$@\"; do printf '%s\\n' \"$a\"; done\n" +
		"  printf 'LOC_COURIER=%s\\n' \"$LOC_COURIER\"\n" +
		"  printf 'LOC_IDENTITY=[%s]\\n' \"$LOC_IDENTITY\"\n" +
		"  printf -- '--END--\\n'; } >> " + rec + "\n" +
		"cat " + out + "\n" +
		fmt.Sprintf("exit %d\n", exitCode)
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return rec
}

// THE CANNED COURIER STREAMS. Each line has the shape of a line the runtime
// stores in a courier's session transcript (#216): the ListAgents listing, the
// SendMessage call, and its result, whose failure is "success":false with no
// is_error flag. The system line stands for the init event and is ignored.
const (
	courierInit    = `{"type":"system","subtype":"init","session_id":"s1","tools":["ListAgents","SendMessage"]}`
	courierListing = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_list","name":"ListAgents","input":{}}]}}
{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_list","type":"tool_result","content":"This session is loc-bell [d877eb].\n\nPeer sessions (2):\n  scribe [57c49f]  ·  interactive  ·  idle  ·  tmux workshop:@2.%42  ·  started 1h ago\n  clerk [9b368a]  ·  interactive  ·  idle  ·  tmux workshop:@0.%4  ·  started 1h ago"}]}}`
	courierCall   = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_send","name":"SendMessage","input":{"to":"%s","message":"[LOC] 🔔 1 new → read — run /read-loc now."}}]}}`
	courierOK     = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_send","type":"tool_result","content":[{"type":"text","text":"{\"success\":true,\"message\":\"sent to scribe\",\"msg_id\":\"m1\"}"}]}]}}`
	courierNoOne  = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_send","type":"tool_result","content":[{"type":"text","text":"{\"success\":false,\"message\":\"No agent named 'scribe [57c49f]' is reachable.\\nUse ListAgents to see everyone you can message.\"}"}]}]}}`
	courierDenied = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"<tool_use_error>permission denied</tool_use_error>","is_error":true,"tool_use_id":"toolu_send"}]}}`
	courierProse  = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Sent the message to scribe."}]}}`
	courierDone   = `{"type":"result","subtype":"success","is_error":false,"result":"done"}`
)

var (
	courierSent = []string{courierInit, courierListing, fmt.Sprintf(courierCall, "scribe"), courierOK, courierDone}
	// The incident in #216: no session on the pane, nothing sent, exit 0.
	// The closing prose is the model's own and claims a send, which is
	// exactly what the reading must not believe.
	courierNoMatch = []string{courierInit, courierListing, courierProse, courierDone}
)

func courierStream(lines []string) string { return strings.Join(lines, "\n") + "\n" }

func TestNotify_ClaudeSpawnsTheCourierWithItsPromptAndItsTwoGuards(t *testing.T) {
	rec := fakeClaude(t, 0)
	r := &recorder{}
	n, err := newNotifier(ListenerClaude, r.log, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Ring("workshop.scribe", "%42", "🔔 1 new → read", ""); err != nil {
		t.Fatalf("the courier failed: %v", err)
	}
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatalf("the courier was never spawned: %v", err)
	}
	got := string(b)
	for _, want := range []string{
		"-p\n",
		"tmux pane id is '%42'",
		"[LOC] 🔔 1 new → read — run /read-loc now.",
		"--allowedTools\nListAgents,SendMessage\n",
		"--output-format\nstream-json\n--verbose\n",
		"LOC_COURIER=1\n",
		"LOC_IDENTITY=[]\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the courier's call is missing %q; it was:\n%s", want, got)
		}
	}
	if want := "nudge workshop.scribe rang '🔔 1 new → read'"; r.joined() != want {
		t.Errorf("delivery log said %q; want %q", r.joined(), want)
	}
}

// THE COURIER CARRIES THE SENDER'S NAME (#124). The assertion is on the argv
// the notifier hands the runtime, recorded by the spawned process itself: a
// name that never reached `claude -n` is not a label on anything.
//
// The name is computed by courierName, the same function the bell calls, so
// this joins the bell's rule to the flag rather than restating the rule here.
func TestNotify_ClaudeNamesTheCourierAfterTheSender(t *testing.T) {
	cases := []struct {
		name    string
		senders []string
		want    string
	}{
		{"one sender is that seat, house and all", []string{"workshop.scribe"}, "workshop.scribe"},
		{"two senders are the first and a count", []string{"workshop.binder", "workshop.warden"}, "workshop.binder+1"},
		{"no sender is the fallback", nil, "loc-bell"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := fakeClaude(t, 0)
			r := &recorder{}
			n, err := newNotifier(ListenerClaude, r.log, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			if err := n.Ring("workshop.scribe", "%42", "🔔 1 new → read", courierName(c.senders)); err != nil {
				t.Fatalf("the courier failed: %v", err)
			}
			b, err := os.ReadFile(rec)
			if err != nil {
				t.Fatalf("the courier was never spawned: %v", err)
			}
			got := string(b)
			if want := "-n\n" + c.want + "\n"; !strings.Contains(got, want) {
				t.Errorf("the courier's call is missing %q; it was:\n%s", want, got)
			}
			// THE BELL TEXT DOES NOT CHANGE. The whole prompt is asserted as
			// one argv entry, so a sender that leaked into the line fails here.
			if want := fmt.Sprintf(courierPrompt, "%42", "🔔 1 new → read") + "\n"; !strings.Contains(got, want) {
				t.Errorf("the bell text changed; the call was:\n%s", got)
			}
			// The two guards are load-bearing and this change does not touch
			// them; a spawn that lost one consumes the message it announces.
			for _, want := range []string{"LOC_COURIER=1\n", "LOC_IDENTITY=[]\n"} {
				if !strings.Contains(got, want) {
					t.Errorf("the courier's call is missing the guard %q; it was:\n%s", want, got)
				}
			}
		})
	}
}

func TestNotify_ClaudeReportsTheCouriersExitCode(t *testing.T) {
	fakeClaude(t, 3)
	r := &recorder{}
	n, err := newNotifier(ListenerClaude, r.log, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Ring("workshop.scribe", "%42", "🔔 1 new → read", ""); err == nil {
		t.Fatal("a courier that exited 3 was recorded as a ring")
	}
	if want := "nudge workshop.scribe BELL FAILED: courier exit 3"; r.joined() != want {
		t.Errorf("delivery log said %q; want %q", r.joined(), want)
	}
}

// #216: THE BELL RINGS ONLY ON A SEND THE STREAM PROVES. A courier exits 0
// whatever it did, so each case below exits 0 and differs only in what it
// printed.
func TestNotify_ClaudeRingsOnlyOnAProvenSend(t *testing.T) {
	for _, c := range []struct {
		name   string
		stream string
		want   string // "" means the bell rang
	}{
		{"a send the runtime confirmed", courierStream(courierSent), ""},
		{"the session named by its id", courierStream([]string{courierListing,
			fmt.Sprintf(courierCall, "scribe [57c49f]"), courierOK, courierDone}), ""},
		{"no session on the pane, and prose that claims a send", courierStream(courierNoMatch),
			"courier found no matching session"},
		{"a send that reached no one", courierStream([]string{courierListing,
			fmt.Sprintf(courierCall, "scribe [57c49f]"), courierNoOne, courierDone}),
			"courier send failed: No agent named 'scribe [57c49f]' is reachable."},
		{"a send the runtime refused", courierStream([]string{courierListing,
			fmt.Sprintf(courierCall, "scribe"), courierDenied, courierDone}),
			"courier send failed: permission denied"},
		{"a send to a session on another pane", courierStream([]string{courierListing,
			fmt.Sprintf(courierCall, "clerk"), courierOK, courierDone}),
			"courier sent to 'clerk', not the session on pane %42"},
		{"output that is not a stream", "Sent the message to scribe.\n", reasonNoResult},
		{"no output at all", "", reasonNoResult},
		{"a stream in a shape this does not read", `{"type":"assistant","message":{"content":"SendMessage to scribe: success"}}` + "\n" + courierDone + "\n",
			"courier found no matching session"},
		{"a stream that stops before the run ends", courierStream([]string{courierInit, courierListing}),
			reasonNoResult},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeClaudeSays(t, 0, c.stream)
			r := &recorder{}
			n, err := newNotifier(ListenerClaude, r.log, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			err = n.Ring("workshop.scribe", "%42", "🔔 1 new → read", "")
			if c.want == "" {
				if err != nil {
					t.Fatalf("a proven send failed: %v", err)
				}
				if want := "nudge workshop.scribe rang '🔔 1 new → read'"; r.joined() != want {
					t.Errorf("delivery log said %q; want %q", r.joined(), want)
				}
				return
			}
			if err == nil {
				t.Fatalf("recorded as a ring; the delivery log said %q", r.joined())
			}
			if errors.Is(err, ErrBusy) || errors.Is(err, ErrHeld) {
				t.Errorf("the failure is marked busy or held: %v", err)
			}
			if err.Error() != c.want {
				t.Errorf("the failure says %q; want %q", err.Error(), c.want)
			}
			if want := "nudge workshop.scribe BELL FAILED: " + c.want; r.joined() != want {
				t.Errorf("delivery log said %q; want %q", r.joined(), want)
			}
		})
	}
}

// N1: AN ADDRESS THAT IS A SESSION NAME is matched exactly, as the name
// alone or as ListAgents' "name [id]". A PANE TARGET is the known gap:
// ListAgents does not print targets, so the runtime's confirmation is the
// whole of the evidence there.
func TestNotify_ClaudeMatchesTheAddresseeByAddressForm(t *testing.T) {
	sentTo := func(to string) string {
		return courierStream([]string{courierListing, fmt.Sprintf(courierCall, to), courierOK, courierDone})
	}
	for _, c := range []struct {
		name, address, stream, want string
	}{
		{"a name, sent to that name", "scribe", sentTo("scribe"), ""},
		{"a name, sent to name [id]", "scribe", sentTo("scribe [57c49f]"), ""},
		{"a name, sent to another session", "scribe", sentTo("clerk"),
			"courier sent to 'clerk', not the session named scribe"},
		{"a name, sent to a longer name", "scribe", sentTo("house.scribe [5b2d3e]"),
			"courier sent to 'house.scribe [5b2d3e]', not the session named scribe"},
		{"a name, sent to a numbered twin", "scribe", sentTo("scribe (2) [abc123]"),
			"courier sent to 'scribe (2) [abc123]', not the session named scribe"},
		{"a dotted name, sent to that name [id]", "house.scribe", sentTo("house.scribe [5b2d3e]"), ""},
		{"a dotted name, sent to another session", "house.scribe", sentTo("clerk"),
			"courier sent to 'clerk', not the session named house.scribe"},
		{"a pane target, any confirmed send", "workshop:2.1", sentTo("clerk"), ""},
		{"a pane target with a window id and pane id", "workshop:@2.%4", sentTo("clerk"), ""},
		{"a pane target, no send", "workshop:2.1", courierStream(courierNoMatch),
			"courier found no matching session"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeClaudeSays(t, 0, c.stream)
			r := &recorder{}
			n, err := newNotifier(ListenerClaude, r.log, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			err = n.Ring("workshop.scribe", c.address, "🔔 1 new → read", "")
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != c.want {
				t.Errorf("the ring came to %q; want %q (delivery log: %q)", got, c.want, r.joined())
			}
		})
	}
}

// N2: A STRAY SEND IS NAMED. The courier wrote to another session before the
// right one. The bell rang — the seat was told — and the delivery log says
// who else was written to, delivered or not.
func TestNotify_ClaudeNamesAStraySendBeforeTheRightOne(t *testing.T) {
	stray := strings.ReplaceAll(fmt.Sprintf(courierCall, "clerk"), "toolu_send", "toolu_stray")
	strayOK := strings.ReplaceAll(courierOK, "toolu_send", "toolu_stray")
	missed := strings.ReplaceAll(fmt.Sprintf(courierCall, "nobody [000000]"), "toolu_send", "toolu_miss")
	missedNo := strings.ReplaceAll(courierNoOne, "toolu_send", "toolu_miss")
	fakeClaudeSays(t, 0, courierStream([]string{courierListing, stray, strayOK, missed, missedNo,
		fmt.Sprintf(courierCall, "scribe"), courierOK, courierDone}))
	r := &recorder{}
	n, err := newNotifier(ListenerClaude, r.log, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Ring("workshop.scribe", "%42", "🔔 1 new → read", ""); err != nil {
		t.Fatalf("the right send was made and the ring failed: %v", err)
	}
	want := "nudge workshop.scribe courier also sent to 'clerk' (delivered), not the session on pane %42\n" +
		"nudge workshop.scribe courier also sent to 'nobody [000000]' (not delivered: No agent named 'scribe [57c49f]' is reachable.), not the session on pane %42\n" +
		"nudge workshop.scribe rang '🔔 1 new → read'"
	if r.joined() != want {
		t.Errorf("delivery log said\n%s\nwant\n%s", r.joined(), want)
	}
}

// A LIVE STREAM. testdata/courier-stream-sent.jsonl is the standard output of
// one real `claude -p --output-format stream-json --verbose` courier run, cut
// to the lines after init, with the session names replaced and the request
// ids redacted. It holds the event types this does not read (rate limits,
// thinking-token counts, a ToolSearch) between the ones it does, and its send
// went to "scribe [acefd1]" on pane %4.
func TestNotify_ClaudeReadsARealCourierStream(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "courier-stream-sent.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ address, want string }{
		{"%4", ""},
		{"scribe", ""},
		{"%5", "courier sent to 'scribe [acefd1]', not the session on pane %5"},
	} {
		reason, strays := courierOutcome(b, c.address)
		if reason != c.want || len(strays) != 0 {
			t.Errorf("address %s: reason %q, strays %q; want %q and none", c.address, reason, strays, c.want)
		}
	}
}

// A PANE TARGET IS NOT IN THE LISTING. ListAgents prints pane ids, so for an
// address such as workshop:2.1 the addressee cannot be matched mechanically,
// and the runtime's own confirmation of the send is the whole of the evidence.
func TestNotify_ClaudeTrustsAConfirmedSendForAPaneTarget(t *testing.T) {
	fakeClaudeSays(t, 0, courierStream(courierSent))
	r := &recorder{}
	n, err := newNotifier(ListenerClaude, r.log, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Ring("workshop.scribe", "workshop:2.1", "🔔 1 new → read", ""); err != nil {
		t.Fatalf("a confirmed send to a pane target failed: %v", err)
	}
}

// R35c. A courier is a whole Claude session, so three arrivals inside the
// window spawn ONE of them. The two that are dropped lose nothing: the
// messages are in the queue and the next read finds them.
func TestNotify_ClaudeSpawnsAtMostOneCourierPerSeatPerWindow(t *testing.T) {
	rec := fakeClaude(t, 0)
	r := &recorder{}
	now := time.Now()
	n, err := newNotifier(ListenerClaude, r.log, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := n.Ring("workshop.scribe", "%42", "🔔 1 new → read", ""); err != nil {
			t.Fatalf("ring %d failed: %v", i+1, err)
		}
	}
	b, _ := os.ReadFile(rec)
	if got := strings.Count(string(b), "--END--"); got != 1 {
		t.Errorf("three rings inside the window spawned %d couriers; want 1", got)
	}
	if got := strings.Count(r.joined(), "rate-limited"); got != 2 {
		t.Errorf("delivery log has %d rate-limited lines; want 2. It said:\n%s", got, r.joined())
	}

	// AND THE WINDOW OPENS AGAIN. A rate limit that never lifted would be a
	// silenced seat rather than a slowed one.
	now = now.Add(courierWindow + time.Second)
	if err := n.Ring("workshop.scribe", "%42", "🔔 1 new → read", ""); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(rec)
	if got := strings.Count(string(b), "--END--"); got != 2 {
		t.Errorf("the window never reopened: %d couriers spawned in all", got)
	}
}

// ---------------------------------------------------------------- the type

func TestNotify_AnUnknownListenerTypeIsRefusedByName(t *testing.T) {
	err := ValidListenerType("claude-courier")
	if err == nil {
		t.Fatal("the retired name claude-courier was accepted")
	}
	for _, want := range []string{"claude-courier", "tmux", "herdr", "claude", "webhook", "none"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %s", want, err)
		}
	}
	for _, ok := range ListenerTypes {
		if err := ValidListenerType(ok); err != nil {
			t.Errorf("the valid type %q was refused: %v", ok, err)
		}
	}
}

// EVERY TYPE BUT `none` HAS A NOTIFIER OF ITS OWN. newNotifier ends in a
// default that returns the silent one, so a type added to ListenerTypes and
// not to the switch would register seats that read as reachable and ring
// nothing. This is the case that fails when that happens.
func TestNotify_EveryTypeButNoneHasItsOwnNotifier(t *testing.T) {
	for _, typ := range ListenerTypes {
		n, err := newNotifier(typ, func(string) {}, time.Now)
		if err != nil {
			t.Fatalf("newNotifier(%q): %v", typ, err)
		}
		_, silent := n.(silentNotifier)
		if silent != (typ == ListenerNone) {
			t.Errorf("type %q: silent notifier = %v", typ, silent)
		}
	}
}

// ---------------------------------------------------------------- webhook

// receiver is a loopback HTTP server of this test's own, on a port the kernel
// picks. It answers every request with one status and one body, and keeps
// what it was sent.
type receiver struct {
	*httptest.Server
	hits        atomic.Int32
	method      string
	contentType string
	body        []byte
}

func newReceiver(t *testing.T, status int, answer string) *receiver {
	t.Helper()
	r := &receiver{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits.Add(1)
		r.method = req.Method
		r.contentType = req.Header.Get("Content-Type")
		r.body, _ = io.ReadAll(req.Body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(r.Close)
	return r
}

const webhookBellLine = "🔔 1 new from alice → read"

func TestWebhook_A2xxAnswerRingsAndTheBodyIsTheBell(t *testing.T) {
	r := newReceiver(t, http.StatusAccepted, "")
	rec := &recorder{}
	n := newWebhookNotifier(rec.log)

	if err := n.Ring("workshop.scribe", r.URL+"/hooks/bell", webhookBellLine, "alice"); err != nil {
		t.Fatalf("Ring: %v", err)
	}
	if got := r.hits.Load(); got != 1 {
		t.Fatalf("the receiver got %d requests, want 1", got)
	}
	if r.method != http.MethodPost {
		t.Errorf("method = %q, want POST", r.method)
	}
	if r.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", r.contentType)
	}
	var got map[string]string
	if err := json.Unmarshal(r.body, &got); err != nil {
		t.Fatalf("the body is not JSON: %v: %q", err, r.body)
	}
	want := map[string]string{"event_type": "loc.bell", "endpoint": "workshop.scribe", "bell": webhookBellLine}
	if len(got) != len(want) {
		t.Errorf("the body has %d fields, want %d: %q", len(got), len(want), r.body)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("body[%q] = %q, want %q", k, got[k], v)
		}
	}
	if !strings.Contains(rec.joined(), "nudge workshop.scribe rang") {
		t.Errorf("the ring was not written to the delivery log: %q", rec.joined())
	}
}

// 429 AND 503 ARE BUSY, and nothing else is. The mark is what makes the bell
// refund its breaker and ask again, so a receiver that is broken must not
// carry it.
func TestWebhook_ABusyReceiverIsMarkedBusy(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		r := newReceiver(t, status, "")
		rec := &recorder{}
		err := newWebhookNotifier(rec.log).Ring("workshop.scribe", r.URL, webhookBellLine, "")
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("status %d: err = %v, want ErrBusy", status, err)
		}
		want := fmt.Sprintf("refused: webhook status %d", status)
		if err.Error() != want {
			t.Errorf("status %d: reason = %q, want %q", status, err, want)
		}
		if !strings.Contains(rec.joined(), "nudge workshop.scribe "+want) {
			t.Errorf("status %d: the refusal was not logged: %q", status, rec.joined())
		}
	}
}

// THE RECEIVER'S TEXT STAYS OUT OF THE RECORD. The reason reaches the day's
// record and `loc transcript` prints it; the status code is all that is kept.
func TestWebhook_AnErrorStatusFailsAndItsBodyIsNotRecorded(t *testing.T) {
	const page = "RECEIVER-ERROR-PAGE-TEXT"
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusInternalServerError} {
		r := newReceiver(t, status, page)
		rec := &recorder{}
		err := newWebhookNotifier(rec.log).Ring("workshop.scribe", r.URL, webhookBellLine, "")
		if err == nil {
			t.Fatalf("status %d rang", status)
		}
		if errors.Is(err, ErrBusy) {
			t.Errorf("status %d was marked busy: %v", status, err)
		}
		if want := fmt.Sprintf("webhook status %d", status); err.Error() != want {
			t.Errorf("status %d: reason = %q, want %q", status, err, want)
		}
		if strings.Contains(err.Error(), page) || strings.Contains(rec.joined(), page) {
			t.Errorf("status %d: the response body was recorded: %q / %q", status, err, rec.joined())
		}
		if !strings.Contains(rec.joined(), "BELL FAILED") {
			t.Errorf("status %d: the failure was not logged: %q", status, rec.joined())
		}
	}
}

func TestWebhook_AReceiverThatIsDownFails(t *testing.T) {
	r := newReceiver(t, http.StatusOK, "")
	address := r.URL
	r.Close() // the port is free again and nothing listens on it
	rec := &recorder{}
	err := newWebhookNotifier(rec.log).Ring("workshop.scribe", address, webhookBellLine, "")
	if err == nil {
		t.Fatal("a bell to a closed port rang")
	}
	if errors.Is(err, ErrBusy) {
		t.Errorf("a receiver that is down was marked busy: %v", err)
	}
	if want := "webhook: the receiver could not be reached"; err.Error() != want {
		t.Errorf("reason = %q, want %q", err, want)
	}
}

func TestWebhook_ASlowReceiverFailsAtTheTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // runs first: lets the handler return so Close can
	n := newWebhookNotifier(func(string) {})
	n.client.Timeout = 50 * time.Millisecond
	err := n.Ring("workshop.scribe", srv.URL, webhookBellLine, "")
	if err == nil {
		t.Fatal("a receiver that never answered rang")
	}
	if errors.Is(err, ErrBusy) {
		t.Errorf("a timeout was marked busy: %v", err)
	}
	if want := "webhook: no answer in 50ms"; err.Error() != want {
		t.Errorf("reason = %q, want %q", err, want)
	}
}

// A REDIRECT IS NOT FOLLOWED. It can name another machine, and the loopback
// rule was applied to the address the seat registered and to nothing else.
func TestWebhook_ARedirectIsNotFollowed(t *testing.T) {
	target := newReceiver(t, http.StatusOK, "")
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, target.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)
	err := newWebhookNotifier(func(string) {}).Ring("workshop.scribe", redirector.URL, webhookBellLine, "")
	if err == nil || err.Error() != "webhook status 302" {
		t.Errorf("err = %v, want a failed bell with status 302", err)
	}
	if got := target.hits.Load(); got != 0 {
		t.Errorf("the redirect was followed: the target got %d requests", got)
	}
}

// THE RING ASKS THE RULE ITSELF. The receiver is on this machine and answers
// 200, so a ring that skipped the rule would reach it and would ring. The case
// below cannot show that: a ring to an address off the machine fails whether
// or not the rule was asked.
func TestWebhook_TheRingRefusesAnAddressTheRuleRefuses(t *testing.T) {
	r := newReceiver(t, http.StatusOK, "")
	withUser := strings.Replace(r.URL, "http://", "http://alice:hunter2@", 1)
	err := newWebhookNotifier(func(string) {}).Ring("workshop.scribe", withUser, webhookBellLine, "")
	if err == nil {
		t.Errorf("a ring to an address with user information rang")
	}
	if got := r.hits.Load(); got != 0 {
		t.Errorf("the receiver got %d requests, want 0", got)
	}
}

// THE DIAL ASKS ABOUT THE ADDRESS, NOT THE NAME. `localhost` is a name a
// resolver answers, so the client refuses a peer that is not loopback after
// the name was resolved.
func TestWebhook_TheDialRefusesAPeerThatIsNotLoopback(t *testing.T) {
	for _, peer := range []string{"127.0.0.1:8644", "127.255.255.254:1", "[::1]:8644"} {
		if err := loopbackPeer("tcp", peer, nil); err != nil {
			t.Errorf("the loopback peer %q was refused: %v", peer, err)
		}
	}
	for _, peer := range []string{"192.168.1.10:8644", "93.184.216.34:80", "0.0.0.0:80", "[::]:80", "[fe80::1%lo0]:80", "example.com:80", "nonsense"} {
		if err := loopbackPeer("tcp", peer, nil); err == nil {
			t.Errorf("the peer %q was accepted", peer)
		}
	}
	// The notifier's own client carries the check.
	tr, ok := newWebhookNotifier(func(string) {}).client.Transport.(*http.Transport)
	if !ok || tr.DialContext == nil {
		t.Fatal("the webhook client has no dial of its own")
	}
	if _, err := tr.DialContext(context.Background(), "tcp", "192.0.2.1:9"); err == nil ||
		!strings.Contains(err.Error(), "not a loopback address") {
		t.Errorf("a dial off this machine: err = %v, want the loopback refusal", err)
	}
}

// THE ADDRESS MUST BE A LOOPBACK http URL. This case asks the rule as
// `subscribe` and `loc mcp` ask it, before the seat registers. The ring asks
// it again, and the case above holds that.
func TestWebhook_AnAddressOffThisMachineIsRefused(t *testing.T) {
	const password = "hunter2"
	refused := map[string]string{
		"":                               "empty",
		"127.0.0.1:8644":                 "not a URL",
		"workshop:1.2":                   "must be http",
		"https://127.0.0.1:8644/hooks":   "must be http",
		"http:///hooks/bell":             "no host",
		"http://example.com/hooks/bell":  "not loopback",
		"http://192.168.1.10:8644/hooks": "not loopback",
		"http://0.0.0.0:8644/hooks":      "not loopback",
		"http://alice:" + password + "@127.0.0.1:8644/hooks": "user information",
	}
	for address, want := range refused {
		err := ValidListenerAddress(ListenerWebhook, address)
		if err == nil {
			t.Errorf("address %q was accepted", address)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("address %q: the refusal does not say %q: %v", address, want, err)
		}
		if strings.Contains(err.Error(), password) {
			t.Errorf("the refusal repeats the password: %v", err)
		}
		rec := &recorder{}
		ringErr := newWebhookNotifier(rec.log).Ring("workshop.scribe", address, webhookBellLine, "")
		if ringErr == nil || errors.Is(ringErr, ErrBusy) {
			t.Errorf("address %q: Ring = %v, want a failed bell", address, ringErr)
		}
		if strings.Contains(rec.joined(), password) {
			t.Errorf("the delivery log repeats the password: %q", rec.joined())
		}
	}
	for _, address := range []string{
		"http://127.0.0.1:8644/hooks/bell", "http://localhost:8644/hooks/bell", "http://[::1]:8644/hooks/bell",
	} {
		if err := ValidListenerAddress(ListenerWebhook, address); err != nil {
			t.Errorf("the loopback address %q was refused: %v", address, err)
		}
	}
	// The other types have no rule here: their addresses are opaque.
	for _, typ := range []string{ListenerTmux, ListenerClaude, ListenerNone} {
		if err := ValidListenerAddress(typ, "workshop:1.2"); err != nil {
			t.Errorf("type %q refused an address: %v", typ, err)
		}
	}
}

// newAnsweringReceiver is a receiver that sets one reply header. The name is
// written to the wire exactly as given, so a case can send a name in a case
// the server would not have chosen.
func newAnsweringReceiver(t *testing.T, status int, name, value string) *receiver {
	t.Helper()
	r := &receiver{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits.Add(1)
		_, _ = io.ReadAll(req.Body)
		if name != "" {
			w.Header()[name] = []string{value}
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(r.Close)
	return r
}

// A 202 THAT SAYS `Loc-Bell: held` IS HELD (#228). The receiver has the bell
// and will deliver it when it is safe, so it is neither a ring nor a refusal.
// The header's NAME is matched as HTTP matches names, so a receiver that
// writes it in lower case is read the same.
//
// PLANT: in Ring, delete the held arm, and the answer reads as rang.
func TestWebhook_A202WithTheHeldHeaderIsHeld(t *testing.T) {
	for _, name := range []string{"Loc-Bell", "loc-bell"} {
		r := newAnsweringReceiver(t, http.StatusAccepted, name, "held")
		rec := &recorder{}
		err := newWebhookNotifier(rec.log).Ring("workshop.scribe", r.URL, webhookBellLine, "")
		if !errors.Is(err, ErrHeld) {
			t.Fatalf("header %q: err = %v, want ErrHeld", name, err)
		}
		if errors.Is(err, ErrBusy) {
			t.Errorf("header %q: a held bell was marked busy: %v", name, err)
		}
		if want := "webhook status 202, Loc-Bell: held"; err.Error() != want {
			t.Errorf("header %q: reason = %q, want %q", name, err, want)
		}
		if !strings.Contains(rec.joined(), "nudge workshop.scribe held") {
			t.Errorf("header %q: the held answer was not logged: %q", name, rec.joined())
		}
		if strings.Contains(rec.joined(), "rang") {
			t.Errorf("header %q: a held bell was logged as rung: %q", name, rec.joined())
		}
	}
}

// ONLY THE EXACT ANSWER IS HELD. A plain 202 is what every receiver written
// before this answer sends, and it must still read as rang; so must a header
// with a misspelled name, another value, or the right header on another 2xx.
//
// PLANT: in Ring, match the held arm on `code == http.StatusAccepted` alone,
// and the plain 202 reads as held.
// PLANT: in Ring, compare the value with strings.EqualFold, and `Held` reads
// as held.
func TestWebhook_OnlyTheExactHeldAnswerIsHeld(t *testing.T) {
	for _, c := range []struct {
		name        string
		status      int
		header, val string
	}{
		{"a plain 202", http.StatusAccepted, "", ""},
		{"a misspelled header name", http.StatusAccepted, "Loc-Bel", "held"},
		{"another value", http.StatusAccepted, "Loc-Bell", "busy"},
		{"the value in another case", http.StatusAccepted, "Loc-Bell", "Held"},
		{"the header on a 200", http.StatusOK, "Loc-Bell", "held"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newAnsweringReceiver(t, c.status, c.header, c.val)
			rec := &recorder{}
			err := newWebhookNotifier(rec.log).Ring("workshop.scribe", r.URL, webhookBellLine, "")
			if err != nil {
				t.Fatalf("err = %v; want a bell that rang", err)
			}
			if !strings.Contains(rec.joined(), "nudge workshop.scribe rang") {
				t.Errorf("the ring was not logged as rung: %q", rec.joined())
			}
		})
	}
}
