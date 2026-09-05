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
// page, so a change to a space or a backtick is a change to the product.
//
// Both ends are NAMED, always. "you" was written for someone reading their own
// queue at a terminal, where the recipient is obvious because they typed the
// command; delivery now puts this line in front of whoever is watching the
// pane, and "ada -> you" tells that reader nothing about who "you" is.
//
// Backticks are the ONLY colour lever available. Measured against a real pane:
// markdown renders (bold, italic, fences), inline code renders COLOURED, and
// ANSI escapes are stripped in transit and arrive as literal "[34m" junk. So
// the endpoints are marked as inline code to colour them, and the timestamp is
// left plain so it recedes behind the names. Bold goes AROUND the code span,
// not inside it: markdown does not parse emphasis within a span, so **text**
// inside backticks arrives as literal asterisks.
//
// The arrow in the header is ASCII "->", not the U+2192 used by the status
// lines. Inside a code span the ASCII form is what the pane renders cleanly.
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
	out.WriteString("**`" + field(e.From) + " -> " + field(e.To) + "`**   " + field(e.TS) + "\n")
	out.WriteString("\n")

	// Each line is its own INLINE CODE SPAN, which is what renders blue.
	// Per LINE, not one span for the whole body: a code span does not cross a
	// line break, so a single pair of backticks would colour the first line
	// and leak raw backticks into the rest.
	//
	// THE COST, because it must not be rediscovered as a bug: markdown does
	// NOT render inside a code span. Bold, headings, lists and tables arrive
	// as literal characters. Blue body and rendered formatting are mutually
	// exclusive, and this picks blue deliberately.
	body := ""
	if e.Body != nil {
		body = *e.Body
	}
	for _, b := range strings.Split(body, "\n") {
		switch {
		case b == "":
			out.WriteString("\n")
		case strings.Contains(b, "`"):
			// A body containing a backtick would close the span early and
			// spill the remainder unstyled. Markdown allows a longer fence,
			// and the padding spaces are required so a leading or trailing
			// backtick in the text is not read as part of the delimiter.
			out.WriteString("`` " + b + " ``\n")
		default:
			out.WriteString("`" + b + "`\n")
		}
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
