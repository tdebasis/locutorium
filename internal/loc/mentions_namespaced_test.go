package loc

import (
	"reflect"
	"testing"
)

// Mentions under namespaced endpoints.
//
// An endpoint is <instance>.<agent>, so the name a body rings has a dot in it.
// One dotted segment is all the grammar allows, and the '@' has to be at the
// start of a word for the dotted form to be read — otherwise the domain of an
// email address would become an endpoint, which is why the last case is here.
func TestMentionsNamespaced(t *testing.T) {
	cases := []struct {
		body string
		want []string
	}{
		// The namespaced name is one mention, alongside a bare one.
		{"see @workshop.scribe and @clerk", []string{"clerk", "workshop.scribe"}},
		// Sentence punctuation is not part of the name: a trailing period
		// ends the mention rather than starting a second segment.
		{"ping @workshop.scribe.", []string{"workshop.scribe"}},
		// Two instances of the same agent are two different endpoints.
		{"@workshop.scribe and @atelier.scribe", []string{"atelier.scribe", "workshop.scribe"}},
		// An '@' inside a word is not a mention of a namespaced endpoint. The
		// bare reading is what this has always produced, and it stays.
		{"mail@example.com", []string{"example"}},
	}
	for _, c := range cases {
		if got := Mentions(c.body); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Mentions(%q) = %v, want %v", c.body, got, c.want)
		}
	}
}
