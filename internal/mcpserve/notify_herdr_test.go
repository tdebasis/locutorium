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
// `agent read` writes --READ-- into the prompts file (the bell must never read
// a screen), and `agent prompt` appends its argv to the same file. It returns that file. THE REAL HERDR IS NEVER
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
		"  read) printf -- '--READ--\\n' >> " + prompts + "; cat " + filepath.Join(dir, "screen") + "; exit 0 ;;\n" +
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

// A screen with a draft in Claude Code's box: the bell rings over it, since it
// reads no screen.
const screenDraft = "output above\n\x1b[2m────\x1b[0m\n❯ half a thought\n\x1b[2m────\x1b[0m\n"
const screenEmpty = ""

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

func TestNotify_HerdrTypesTheBellIntoAnIdleAgentOfAnyKind(t *testing.T) {
	for _, kind := range []string{"claude", "codex", "aider"} {
		for _, status := range []string{"idle", "done"} {
			file := fakeHerdr(t, herdrGet(kind, status), 0, screenDraft)
			r := &recorder{}
			n := &herdrNotifier{log: r.log}
			if err := n.Ring("workshop.scribe", "workshop/scribe", "🔔 1 new → read", ""); err != nil {
				t.Fatalf("%s %s: refused: %v", kind, status, err)
			}
			want := "--session\nworkshop\nagent\nprompt\nscribe\n🔔 1 new → read\n--END--\n"
			if got := prompted(t, file); got != want {
				t.Errorf("%s %s: herdr was asked %q; want %q (no screen read)", kind, status, got, want)
			}
			if want := "nudge workshop.scribe rang '🔔 1 new → read' (herdr state: " + status + ")"; r.joined() != want {
				t.Errorf("%s %s: delivery log said %q; want %q", kind, status, r.joined(), want)
			}
		}
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
		{"codex working", "workshop/scribe", "🔔", herdrGet("codex", "working"), 0, screenEmpty, true, "agent working"},
		{"codex at a dialog", "workshop/scribe", "🔔", herdrGet("codex", "blocked"), 0, screenEmpty, true, "agent at a dialog"},
		{"codex unknown state", "workshop/scribe", "🔔", herdrGet("codex", "unknown"), 0, screenEmpty, true, "agent state 'unknown'"},
		{"unknown state", "workshop/scribe", "🔔", herdrGet("claude", "unknown"), 0, screenEmpty, true, "agent state 'unknown'"},
		{"strange state", "workshop/scribe", "🔔", herdrGet("claude", "sleeping"), 0, screenEmpty, false, "agent state 'sleeping'"},
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
	if err := ValidListenerAddress(ListenerHerdr, "work/scribe"); err != nil {
		t.Errorf("a good herdr address was refused: %v", err)
	}
	for _, bad := range []string{"", "scribe", "work/", "/scribe", "a/b/c", "my work/scribe"} {
		if err := ValidListenerAddress(ListenerHerdr, bad); err == nil {
			t.Errorf("the herdr address %q was accepted", bad)
		}
	}
	if err := ValidListenerType(ListenerHerdr); err != nil {
		t.Errorf("herdr is not a valid listener type: %v", err)
	}
}
