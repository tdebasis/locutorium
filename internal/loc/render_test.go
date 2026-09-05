package loc

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// readmeConsole returns the lines of the one ```console block in README.md.
//
// The page is the authority for what a reader sees. A renderer test that
// carries its own copy of the expected bytes tests only that the copy was
// made; this one fails the moment the tool and the page disagree.
func readmeConsole(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatalf("README.md: %v", err)
	}
	lines := strings.Split(string(b), "\n")
	start := -1
	for i, l := range lines {
		if l == "```console" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatal("README.md has no ```console block")
	}
	for i := start; i < len(lines); i++ {
		if lines[i] == "```" {
			return lines[start:i]
		}
	}
	t.Fatal("README.md console block is not closed")
	return nil
}

// section returns the lines strictly between two markers, plus a trailing
// newline, i.e. exactly the bytes the renderer must produce for that part of
// the transcript.
func section(t *testing.T, lines []string, after, before string) string {
	t.Helper()
	from := -1
	for i, l := range lines {
		if l == after {
			from = i + 1
			break
		}
	}
	if from < 0 {
		t.Fatalf("console block has no line %q", after)
	}
	for i := from; i < len(lines); i++ {
		if lines[i] == before {
			return strings.Join(lines[from:i], "\n") + "\n"
		}
	}
	t.Fatalf("console block has no line %q after %q", before, after)
	return ""
}

// TestRenderMatchesREADME is the load-bearing test of this package: the two
// envelopes in the published transcript, rendered, byte for byte.
func TestRenderMatchesREADME(t *testing.T) {
	console := readmeConsole(t)

	const ts = "2026-08-26T03:54:39Z"
	cases := []struct {
		name          string
		env           Envelope
		after, before string
	}{
		{
			name:   "queue",
			env:    Envelope{ID: "x", TS: ts, From: "ada", To: "bob", Kind: "msg", Body: "the build is green; the tag is yours", Refs: []string{}},
			after:  "── queue.bob ──",
			before: "── topics ──",
		},
		{
			name:   "topic",
			env:    Envelope{ID: "x", TS: ts, From: "ada", To: "#standup", Kind: "msg", Body: "@bob please look at the restore proof", Refs: []string{}},
			after:  "── topics ──",
			before: "$ LOC_IDENTITY=bob loc topics",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want := section(t, console, c.after, c.before)
			var got bytes.Buffer
			if err := RenderOne(&got, c.env); err != nil {
				t.Fatalf("RenderOne: %v", err)
			}
			if got.String() != want {
				t.Errorf("render differs from README\n got: %q\nwant: %q", got.String(), want)
			}
		})
	}
}

func TestRenderHeaderUsesASCIIArrow(t *testing.T) {
	var got bytes.Buffer
	if err := RenderOne(&got, Envelope{From: "ada", To: "bob", TS: "T", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.String(), "ada -> bob") {
		t.Errorf("header arrow is not ASCII: %q", got.String())
	}
	if strings.Contains(got.String(), "→") {
		t.Errorf("header must not carry the status-line arrow: %q", got.String())
	}
}

func TestRenderBodyLines(t *testing.T) {
	cases := []struct{ body, want string }{
		{"one", "`one`\n"},
		{"one\ntwo", "`one`\n`two`\n"},
		{"one\n\ntwo", "`one`\n\n`two`\n"},
		{"", "\n"},
		{"a `b` c", "`` a `b` c ``\n"},
		{"`lead", "`` `lead ``\n"},
		{"trail`", "`` trail` ``\n"},
	}
	for _, c := range cases {
		var got bytes.Buffer
		if err := RenderOne(&got, Envelope{From: "a", To: "b", TS: "T", Body: c.body}); err != nil {
			t.Fatal(err)
		}
		want := "**`a -> b`**   T\n\n" + c.want + "\n"
		if got.String() != want {
			t.Errorf("body %q\n got: %q\nwant: %q", c.body, got.String(), want)
		}
	}
}

// A field that is absent renders as "?" — an empty space where a name belongs
// reads as a name.
func TestRenderMissingFields(t *testing.T) {
	var got bytes.Buffer
	if err := Render(strings.NewReader(`{"kind":"msg"}`), &got); err != nil {
		t.Fatal(err)
	}
	if want := "**`? -> ?`**   ?\n\n\n\n"; got.String() != want {
		t.Errorf("got %q, want %q", got.String(), want)
	}
}

func TestRenderUnparseableLineIsShown(t *testing.T) {
	var got bytes.Buffer
	if err := Render(strings.NewReader("not json\n"), &got); err != nil {
		t.Fatal(err)
	}
	if want := "  [unparseable] not json\n"; got.String() != want {
		t.Errorf("got %q, want %q", got.String(), want)
	}
}
