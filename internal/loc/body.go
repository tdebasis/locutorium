package loc

import (
	"fmt"
	"unicode/utf8"
)

// MaxBodyChars is the maximum message body, in CHARACTERS. Deliberately not
// configurable: that a limit exists, and that breaking it is a loud refusal,
// is the product's opinion, and it is what makes the guarantee mean anything.
//
// WHY A LIMIT: this carries conversation, not documents. Long content belongs
// in a file or an archive, and the message points at it.
//
// WHY CHARACTERS AND NOT BYTES: both are deterministic, bytes are unfair. An
// emoji is four bytes, so two messages of visibly equal length would fail
// differently depending on how many status markers they carry.
//
// 4000 is MESSAGE DISCIPLINE, not a technical bound. The transport carries far
// more; the number is a choice about what this channel is for, and raising it
// is a policy conversation, not a physics one.
const MaxBodyChars = 4000

// CheckBody refuses an over-long body BEFORE anything reaches the medium.
//
// It names BOTH numbers: a refusal that only says "too long" makes the sender
// guess how much to cut.
func CheckBody(body string) error {
	n := utf8.RuneCountInString(body)
	if n <= MaxBodyChars {
		return nil
	}
	return fmt.Errorf("message too long: %d characters, limit %d. "+
		"This channel carries conversation, not documents. "+
		"Put the content in a file or the archive and send a pointer to it.",
		n, MaxBodyChars)
}
