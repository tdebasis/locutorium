package mcpserve

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// WHAT THE COURIER DID, READ FROM ITS OWN EVENTS (#216).
//
// `claude -p --output-format stream-json --verbose` prints one JSON object per
// line. The lines this reads have the shape the runtime also stores in its
// session transcripts:
//
//	{"type":"assistant","message":{"content":[{"type":"tool_use","id":"…","name":"SendMessage","input":{"to":"…",…}}]}}
//	{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"…","content":[{"type":"text","text":"{\"success\":true,…}"}]}]}}
//	{"type":"result","is_error":false,…}
//
// A SEND IS PROVEN BY ITS RESULT AND NOTHING ELSE. The runtime reports a
// SendMessage that reached no one as an ordinary tool result whose text is
// {"success":false,"message":"No agent named '…' is reachable. …"}, with no
// is_error flag, so a result counts as a send only when it is not an error
// AND its text says "success":true.
//
// THE ADDRESSEE IS CHECKED WHEREVER IT CAN BE CHECKED MECHANICALLY. Three
// address forms reach this:
//
//   - A pane id such as %2. ListAgents prints it on the session's own line
//     ("tmux house:@1.%2"), so the send must name a session on a line that
//     carries that id.
//   - A session name such as scribe. The send must name exactly that session:
//     "scribe", or "scribe [id]" as ListAgents writes it.
//   - A pane target such as house:3.1 (it holds ':' or '.'). ListAgents does
//     not print targets, so nothing in the stream ties a session to one, and
//     the runtime's confirmation of the send is the whole of the evidence.
//     THIS IS A KNOWN GAP: a courier that confirms a send to the wrong session
//     at a pane target is recorded as rang.
//
// A CHANGE IN THE RUNTIME'S OUTPUT FAILS EVERY BELL; IT NEVER RINGS ONE. Each
// fact here has to be found before it counts — a SendMessage call, its
// result, the "success":true in it, the addressee — so a stream whose shape
// this no longer reads finds none of them, and the bell is written down as
// failed, mostly with reasonNoResult. A run of those across every claude seat
// is the sign to look at the format, not at the seats.

// courierEvent is the part of one stream-json line this reads.
type courierEvent struct {
	Type    string `json:"type"`
	IsError bool   `json:"is_error"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// courierBlock is one content block of an assistant or user message.
type courierBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// paneID is the address form ListAgents prints: tmux's own %N pane id.
var paneID = regexp.MustCompile(`^%[0-9]+$`)

// reasonNoResult is the failure for a stream that proves nothing either way.
// It names the format, because a changed format is how every bell would read.
const reasonNoResult = "courier gave no result (output unreadable, or the runtime's format changed)"

// courierReasonMax caps the runtime's text where it reaches a reason. The
// reason lands in the day's record and in `loc status`, one line each.
const courierReasonMax = 100

// courierOutcome reads the courier's stdout. It returns reason "" when the
// stream proves a bell was sent to the session at address, or the reason the
// bell failed. strays names the other sends the courier made, each with
// whether it was delivered, so a ring that also woke someone else says so.
func courierOutcome(out []byte, address string) (reason string, strays []string) {
	var (
		events   int
		finished bool                  // a result event that is not an error
		uses     = map[string]string{} // SendMessage tool_use id -> addressee
		order    []string              // SendMessage ids in the order called
		results  = map[string]courierBlock{}
		listings []string // ListAgents results, as text
		listing  = map[string]bool{}
	)
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var ev courierEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Type == "" {
			continue
		}
		events++
		if ev.Type == "result" {
			finished = !ev.IsError
			continue
		}
		var blocks []courierBlock
		if json.Unmarshal(ev.Message.Content, &blocks) != nil {
			continue // a plain-string content holds no tool call
		}
		for _, b := range blocks {
			switch {
			case b.Type == "tool_use" && b.Name == "SendMessage":
				uses[b.ID] = addressee(b.Input)
				order = append(order, b.ID)
			case b.Type == "tool_use" && b.Name == "ListAgents":
				listing[b.ID] = true
			case b.Type == "tool_result" && listing[b.ToolUseID]:
				listings = append(listings, resultText(b.Content))
			case b.Type == "tool_result":
				if _, ok := uses[b.ToolUseID]; ok {
					results[b.ToolUseID] = b
				}
			}
		}
	}
	if events == 0 {
		return reasonNoResult, nil
	}
	if len(order) == 0 {
		if !finished {
			return reasonNoResult, nil
		}
		return "courier found no matching session", nil
	}

	var wrong, failed string
	rang := false
	for _, id := range order {
		r, ok := results[id]
		if !ok {
			continue
		}
		to := uses[id]
		why, sent := sendResult(r)
		if !isAddressee(listings, address, to) {
			// A send to another session is a stray whether or not it was
			// delivered. A delivered one woke the wrong seat and says so; an
			// undelivered one is a send that failed, and says why.
			if sent {
				wrong = fmt.Sprintf("courier sent to '%s', %s", short(to), notAt(address))
				strays = append(strays, fmt.Sprintf("'%s' (delivered)", short(to)))
			} else {
				failed = why
				strays = append(strays, fmt.Sprintf("'%s' (not delivered: %s)", short(to), why))
			}
			continue
		}
		if !sent {
			failed = why
			continue
		}
		rang = true
	}
	switch {
	case rang:
		return "", strays
	case wrong != "":
		return wrong, nil
	case failed != "":
		return "courier send failed: " + failed, nil
	default:
		return reasonNoResult, nil
	}
}

// paneTarget reports whether an address is a tmux pane target such as
// house:3.1, the form ListAgents does not print.
//
// THE TARGET'S OWN SHAPE IS MATCHED, not any colon or dot: a session, a
// colon, a window, and optionally a dot and a pane. A dotted session name
// such as house.scribe is a name, and the exact-name rule applies to it.
func paneTarget(address string) bool { return paneTargetForm.MatchString(address) }

var paneTargetForm = regexp.MustCompile(`^[^:\s]+:@?\d+(\.%?\d+)?$`)

// namedID is a session as ListAgents names it: "name [id]".
var namedID = regexp.MustCompile(`^(.*) \[[^\]]+\]$`)

// isAddressee reports whether a send to `to` is a send to the session at
// address, under the three rules in the comment at the top of this file.
func isAddressee(listings []string, address, to string) bool {
	switch {
	case paneID.MatchString(address):
		return onPane(listings, address, to)
	case paneTarget(address):
		return true // the known gap: nothing in the stream can say
	default:
		if to == address {
			return true
		}
		m := namedID.FindStringSubmatch(to)
		return m != nil && m[1] == address
	}
}

// notAt is the rest of a wrong-addressee reason.
func notAt(address string) string {
	if paneID.MatchString(address) {
		return "not the session on pane " + address
	}
	return "not the session named " + address
}

// addressee is the session a SendMessage call names.
func addressee(input json.RawMessage) string {
	var in struct {
		To        string `json:"to"`
		Recipient string `json:"recipient"`
	}
	_ = json.Unmarshal(input, &in)
	if in.To != "" {
		return in.To
	}
	return in.Recipient
}

// sendResult reports whether a SendMessage result is a send, and why not.
func sendResult(r courierBlock) (why string, sent bool) {
	text := resultText(r.Content)
	var body struct {
		Success *bool  `json:"success"`
		Message string `json:"message"`
	}
	parsed := json.Unmarshal([]byte(text), &body) == nil
	if !r.IsError && parsed && body.Success != nil && *body.Success {
		return "", true
	}
	if parsed && body.Message != "" {
		text = body.Message
	}
	text = strings.TrimSpace(strings.NewReplacer("<tool_use_error>", "", "</tool_use_error>", "").Replace(text))
	if text == "" {
		return "no reason given", false
	}
	return short(text), false
}

// resultText is a tool result's text, whether the runtime gave it as a string
// or as a list of text blocks.
func resultText(content json.RawMessage) string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// onPane reports whether a ListAgents listing shows the session `to` on the
// pane. A listing line reads "  name [id]  ·  …  ·  tmux sess:@1.%2  ·  …", and
// a send names the session as "name", as "name [id]", or by its id.
func onPane(listings []string, pane, to string) bool {
	if to == "" {
		return false
	}
	carries := regexp.MustCompile(`(^|[\s.:])` + regexp.QuoteMeta(pane) + `($|[^0-9])`)
	for _, l := range listings {
		for _, line := range strings.Split(l, "\n") {
			if !carries.MatchString(line) {
				continue
			}
			name := strings.TrimSpace(strings.SplitN(line, "·", 2)[0])
			if name == to || strings.HasPrefix(name, to+" [") || strings.HasSuffix(name, "["+to+"]") {
				return true
			}
		}
	}
	return false
}

// short is the first line of s, cut to courierReasonMax characters.
func short(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if utf8.RuneCountInString(s) <= courierReasonMax {
		return s
	}
	r := []rune(s)
	return string(r[:courierReasonMax]) + "…"
}
