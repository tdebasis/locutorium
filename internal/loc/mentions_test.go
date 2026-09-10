package loc

import (
	"reflect"
	"testing"
)

func TestMentions(t *testing.T) {
	cases := []struct {
		body string
		want []string
	}{
		{"@bob please look at the restore proof", []string{"bob"}},
		{"nothing here", nil},
		// Deduplicated and sorted, like `sort -u`.
		{"@zed @ada @zed", []string{"ada", "zed"}},
		{"@a-b and @c_d", []string{"a-b", "c_d"}},
		// The grammar is lowercase: an @Name is not an endpoint reference,
		// and the match stops at the first character outside it.
		{"@Bob", nil},
		{"mail@example.com", []string{"example"}},
		{"@bob, @carol.", []string{"bob", "carol"}},
	}
	for _, c := range cases {
		if got := Mentions(c.body); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Mentions(%q) = %v, want %v", c.body, got, c.want)
		}
	}
}
