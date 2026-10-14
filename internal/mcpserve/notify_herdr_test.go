package mcpserve

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------- herdr

// fakeHerdr puts a `herdr` first on PATH. `agent get` prints the JSON given,
// `agent read` prints the screen given, and `agent prompt` appends its argv to
// a file and prints nothing. It returns that file. THE REAL HERDR IS NEVER
// INVOKED BY A TEST, so no live session is read or typed into.
func fakeHerdr(t *testing.T, get string, getExit int, screen string) string {
	t.Helper()
	dir := t.TempDir()
	prompts := filepath.Join(dir, "prompts")
	for name, body := range map[string]string{"get": get, "screen": screen} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do case \"$a\" in\n" +
		"  get) cat " + filepath.Join(dir, "get") + "; exit " + strconv.Itoa(getExit) + " ;;\n" +
		"  read) cat " + filepath.Join(dir, "screen") + "; exit 0 ;;\n" +
		"  prompt) for b in \"$@\"; do printf '%s\\n' \"$b\"; done >> " + prompts + "; printf -- '--END--\\n' >> " + prompts + "; exit 0 ;;\n" +
		"esac; done\nexit 9\n"
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return prompts
}

// herdrGet is `herdr agent get` for an agent of the given kind and state, in
// the shape herdr 0.9.3 prints.
func herdrGet(kind, status string) string {
	return `{"id":"cli:agent:get","result":{"agent":{"agent":"` + kind + `","agent_status":"` + status +
		`","name":"scribe","pane_id":"w1:p2"},"type":"agent_info"}}`
}

const herdrNotFound = `{"error":{"code":"agent_not_found","message":"agent target scribe not found"},"id":"cli:agent:get"}`

// Claude Code's input box, as `herdr agent read --format ansi` shows it.
const (
	boxRule       = "\x1b[2m────────────────────────────────────────\x1b[0m"
	screenEmpty   = "output above\n" + boxRule + "\n❯ \n" + boxRule + "\n  ⏵⏵ accept edits on\n"
	screenSuggest = "output above\n" + boxRule + "\n❯ \x1b[2mTry \"fix the tests\"\x1b[0m\n" + boxRule + "\n"
	screenDraft   = "output above\n" + boxRule + "\n❯ half a thought\n" + boxRule + "\n"
	screenDraft2  = "output above\n" + boxRule + "\n❯ \n  and a second line\n" + boxRule + "\n"
	screenNoBox   = "a shell prompt $ \n"
)

// prompted returns the argv of every `agent prompt` the fake saw.
func prompted(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNotify_HerdrTypesTheBellIntoAnIdleEmptyBox(t *testing.T) {
	for _, status := range []string{"idle", "done"} {
		file := fakeHerdr(t, herdrGet("claude", status), 0, screenEmpty)
		r := &recorder{}
		n := &herdrNotifier{log: r.log}
		if err := n.Ring("workshop.scribe", "workshop/scribe", "🔔 1 new → read", ""); err != nil {
			t.Fatalf("%s: an empty box refused the bell: %v", status, err)
		}
		want := "--session\nworkshop\nagent\nprompt\nscribe\n🔔 1 new → read\n--END--\n"
		if got := prompted(t, file); got != want {
			t.Errorf("%s: herdr was asked %q; want %q", status, got, want)
		}
		if want := "nudge workshop.scribe rang '🔔 1 new → read'"; r.joined() != want {
			t.Errorf("%s: delivery log said %q; want %q", status, r.joined(), want)
		}
	}
}

func TestNotify_HerdrTypesOverClaudesOwnSuggestion(t *testing.T) {
	file := fakeHerdr(t, herdrGet("claude", "idle"), 0, screenSuggest)
	n := &herdrNotifier{log: (&recorder{}).log}
	if err := n.Ring("workshop.scribe", "workshop/scribe", "🔔", ""); err != nil {
		t.Fatalf("Claude's faint suggestion was read as a draft: %v", err)
	}
	if prompted(t, file) == "" {
		t.Error("the bell was not typed over Claude's own suggestion")
	}
}

// Every refusal: herdr is never asked to type, and the error says whether the
// bell should ask again (busy) or not.
func TestNotify_HerdrRefuses(t *testing.T) {
	cases := []struct {
		name, address, bell, get string
		getExit                  int
		screen                   string
		busy                     bool
		reason                   string
	}{
		{"working", "workshop/scribe", "🔔", herdrGet("claude", "working"), 0, screenEmpty, true, "agent working"},
		{"dialog", "workshop/scribe", "🔔", herdrGet("claude", "blocked"), 0, screenEmpty, true, "agent at a dialog"},
		{"draft", "workshop/scribe", "🔔", herdrGet("claude", "idle"), 0, screenDraft, true, "draft in the input box"},
		{"draft on line 2", "workshop/scribe", "🔔", herdrGet("claude", "idle"), 0, screenDraft2, true, "draft in the input box"},
		{"no box", "workshop/scribe", "🔔", herdrGet("claude", "idle"), 0, screenNoBox, true, "input box not found"},
		{"other kind", "workshop/scribe", "🔔", herdrGet("codex", "idle"), 0, screenEmpty, false, "no draft check for agent kind 'codex'"},
		{"unknown state", "workshop/scribe", "🔔", herdrGet("claude", "unknown"), 0, screenEmpty, false, "agent state 'unknown'"},
		{"missing agent", "workshop/scribe", "🔔", herdrNotFound, 1, screenEmpty, false, "agent_not_found"},
		{"no answer", "workshop/scribe", "🔔", "", 1, screenEmpty, false, "cannot ask herdr"},
		{"bad address", "scribe", "🔔", herdrGet("claude", "idle"), 0, screenEmpty, false, "bad herdr address"},
		{"empty address", "", "🔔", herdrGet("claude", "idle"), 0, screenEmpty, false, "bad herdr address"},
		{"dash bell", "workshop/scribe", "-x", herdrGet("claude", "idle"), 0, screenEmpty, false, "starts with '-'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file := fakeHerdr(t, c.get, c.getExit, c.screen)
			r := &recorder{}
			n := &herdrNotifier{log: r.log}
			err := n.Ring("workshop.scribe", c.address, c.bell, "")
			if err == nil {
				t.Fatal("the bell rang; want a refusal")
			}
			if got := prompted(t, file); got != "" {
				t.Errorf("herdr was asked to type %q", got)
			}
			if errors.Is(err, ErrBusy) != c.busy {
				t.Errorf("busy = %v; want %v (%v)", errors.Is(err, ErrBusy), c.busy, err)
			}
			if !strings.Contains(r.joined(), c.reason) {
				t.Errorf("delivery log said %q; want it to name %q", r.joined(), c.reason)
			}
		})
	}
}

func TestNotify_HerdrAddressIsCheckedAtSubscribe(t *testing.T) {
	if err := ValidListenerAddress(ListenerHerdr, "conclave/scribe"); err != nil {
		t.Errorf("a good herdr address was refused: %v", err)
	}
	for _, bad := range []string{"", "scribe", "conclave/", "/scribe", "a/b/c", "con clave/scribe"} {
		if err := ValidListenerAddress(ListenerHerdr, bad); err == nil {
			t.Errorf("the herdr address %q was accepted", bad)
		}
	}
	if err := ValidListenerType(ListenerHerdr); err != nil {
		t.Errorf("herdr is not a valid listener type: %v", err)
	}
}
