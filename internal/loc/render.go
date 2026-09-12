package loc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// Render reads envelope JSON, one per line, and writes the reader's view.
//
// EVERY BYTE HERE IS A CONTRACT. The suite diffs this output against the
// page, so a change to a space or to a prefix is a change to the product.
//
// Both ends are NAMED, always. "you" was written for someone reading their own
// queue at a terminal, where the recipient is obvious because they typed the
// command. Delivery now puts this line in front of whoever watches the pane.
// "ada -> you" tells that reader nothing about who "you" is.
//
// Forge measured four facts in the live panes on 2026-09-11. They set this
// format:
//
//  1. Claude Code removes the ANSI escapes from every tool result. An escape
//     arrives as the literal text "[31m".
//  2. The raw tool result is shown flat. No markdown in it renders.
//  3. The reader's skill copies that result into a fenced block in the reply.
//     A fence with the tag "diff" paints a line that starts with "+" green. A
//     backtick or an asterisk in the fence shows as a literal character.
//  4. Markup in this output is therefore dead weight. The "+" prefix is the
//     only colour lever that reaches the reader.
//
// The arrow in the header is ASCII "->", not the U+2192 of the status lines.
func Render(r io.Reader, w io.Writer) error {
	out := bufio.NewWriter(w)
	defer out.Flush()

	sc := bufio.NewScanner(r)
	// Bodies run to 4000 characters and may be multi-byte throughout; the
	// default 64KiB token limit would truncate a legal envelope.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e renderView
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			// An unparseable line is SHOWN, not swallowed: a reader who sees
			// nothing cannot tell a quiet queue from a broken one.
			trunc := line
			if len(trunc) > 120 {
				trunc = trunc[:120]
			}
			out.WriteString("  [unparseable] " + trunc + "\n")
			continue
		}
		renderEnvelope(out, e)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return out.Flush()
}

// RenderOne renders a single envelope. It goes through the wire form on
// purpose, so there is exactly one rendering path and a test of this is a test
// of what a reader actually sees.
func RenderOne(w io.Writer, e Envelope) error {
	b, err := e.Marshal()
	if err != nil {
		return err
	}
	return Render(bytes.NewReader(b), w)
}

// renderView is the reading side of an envelope. The fields are pointers so
// that ABSENT and PRESENT-BUT-EMPTY stay distinguishable: only an absent field
// renders as "?", which is what the shell's dict lookups do.
type renderView struct {
	From *string `json:"from"`
	To   *string `json:"to"`
	TS   *string `json:"ts"`
	Body *string `json:"body"`
}

func renderEnvelope(out *bufio.Writer, e renderView) {
	// A missing field renders as "?" rather than as nothing: an empty space
	// where a name belongs reads as a name.
	//
	// The header carries no markup. Fact 3 above: an asterisk or a backtick
	// here reaches the reader as a literal character.
	out.WriteString(field(e.From) + " -> " + field(e.To) + "   " + field(e.TS) + "\n")

	// Every body line starts with "+ ". The reader's fence has the tag "diff",
	// which paints such a line green. Facts 1 to 4 above give the reason.
	//
	// An empty line gets "+" and no trailing space. A trailing space is
	// invisible in the pane, and it is still a byte that the README test
	// compares.
	//
	// A backtick in the body gets no special treatment. This output is not
	// markdown any more, so a backtick is an ordinary character.
	body := ""
	if e.Body != nil {
		body = *e.Body
	}
	for _, b := range strings.Split(body, "\n") {
		if b == "" {
			out.WriteString("+\n")
			continue
		}
		out.WriteString("+ " + b + "\n")
	}
	// Between envelopes, so a batch does not run together.
	out.WriteString("\n")
}

func field(s *string) string {
	if s == nil {
		return "?"
	}
	return *s
}
