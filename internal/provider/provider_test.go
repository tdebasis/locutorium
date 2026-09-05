package provider

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// stub is a provider that carries nothing but its own name, so a test can tell
// which factory answered.
type stub struct{ name string }

func (s *stub) SendQueue(string, []byte) error    { return nil }
func (s *stub) PublishTopic(string, []byte) error { return nil }
func (s *stub) Topics(io.Writer) error            { return nil }
func (s *stub) Status(io.Writer) error            { return nil }
func (s *stub) Close()                            {}

func TestRegisterThenOpen(t *testing.T) {
	Register("stub-basic", func() (Provider, error) { return &stub{name: "stub-basic"}, nil })

	p, err := Open("stub-basic")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s, ok := p.(*stub)
	if !ok {
		t.Fatalf("Open returned %T, want *stub", p)
	}
	if s.name != "stub-basic" {
		t.Errorf("Open returned the provider named %q", s.name)
	}
}

func TestOpenCallsTheFactoryEveryTime(t *testing.T) {
	calls := 0
	Register("stub-counted", func() (Provider, error) {
		calls++
		return &stub{name: "stub-counted"}, nil
	})

	for i := 1; i <= 2; i++ {
		if _, err := Open("stub-counted"); err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		if calls != i {
			t.Fatalf("after %d Opens the factory ran %d times", i, calls)
		}
	}
}

func TestOpenSurfacesAFactoryFailure(t *testing.T) {
	boom := fmt.Errorf("no medium here")
	Register("stub-broken", func() (Provider, error) { return nil, boom })

	p, err := Open("stub-broken")
	if err != boom {
		t.Errorf("got %v, want the factory's own error", err)
	}
	if p != nil {
		t.Errorf("got a provider (%T) alongside the failure", p)
	}
}

// A second module claiming one name is a build mistake, and it is refused at
// the moment it happens rather than being resolved by whichever init ran last.
func TestDuplicateRegistrationPanics(t *testing.T) {
	Register("stub-dup", func() (Provider, error) { return &stub{name: "first"}, nil })

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("a duplicate registration was accepted")
		}
		if got, want := fmt.Sprint(r), "provider: duplicate registration for stub-dup"; got != want {
			t.Errorf("panicked with %q, want %q", got, want)
		}
		// The first registration still stands: the panic refuses the newcomer,
		// it does not corrupt the table.
		p, err := Open("stub-dup")
		if err != nil {
			t.Fatalf("Open after the refused duplicate: %v", err)
		}
		if s := p.(*stub); s.name != "first" {
			t.Errorf("the duplicate replaced the original (got %q)", s.name)
		}
	}()

	Register("stub-dup", func() (Provider, error) { return &stub{name: "second"}, nil })
}

func TestOpenUnknownName(t *testing.T) {
	p, err := Open("no-such-medium")
	if err == nil {
		t.Fatal("Open accepted a name nothing registered")
	}
	want := "unknown provider 'no-such-medium' (not built into this loc)"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
	if p != nil {
		t.Errorf("got a provider (%T) alongside the failure", p)
	}
}

// No provider configured is a different problem from an unknown one, and the
// message points at the file that needs the line — the deployment's own,
// wherever LOC_HOME says it is.
func TestOpenWithNoNameNamesTheConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)

	_, err := Open("")
	if err == nil {
		t.Fatal("Open accepted an empty provider name")
	}
	want := "no provider configured (set 'provider = <name>' in " + home + "/config)"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
	// Said again the way a reader would check it, so a change to how the path
	// is joined cannot pass by agreeing with itself.
	if got := filepath.Join(home, "config"); !strings.Contains(err.Error(), got) {
		t.Errorf("%q does not name %q", err.Error(), got)
	}
}
