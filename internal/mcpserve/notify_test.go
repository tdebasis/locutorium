package mcpserve

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
// environment guards, and exits with the code given. THE REAL BINARY IS NEVER
// INVOKED BY A TEST.
func fakeClaude(t *testing.T, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	rec := filepath.Join(dir, "argv")
	script := "#!/bin/sh\n" +
		"{ for a in \"$@\"; do printf '%s\\n' \"$a\"; done\n" +
		"  printf 'LOC_COURIER=%s\\n' \"$LOC_COURIER\"\n" +
		"  printf 'LOC_IDENTITY=[%s]\\n' \"$LOC_IDENTITY\"\n" +
		"  printf -- '--END--\\n'; } >> " + rec + "\n" +
		fmt.Sprintf("exit %d\n", exitCode)
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return rec
}

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
		{"one sender is that seat", []string{"workshop.scribe"}, "scribe"},
		{"two senders are the first and a count", []string{"workshop.binder", "workshop.warden"}, "binder+1"},
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
	for _, want := range []string{"claude-courier", "tmux", "claude", "none"} {
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
