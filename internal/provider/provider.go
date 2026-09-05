// Package provider is the seam between the Locutorium's semantics and
// whatever carries a message.
//
// Every operation that touches the medium goes through this interface, and
// exactly one implementation is selected by config at startup. Nothing above
// this line knows what a subject or a stream is.
package provider

import (
	"fmt"
	"io"
	"sync"

	"github.com/tdebasis/locutorium/internal/config"
)

// Provider is the medium adapter.
//
// The envelope is passed as bytes, already rendered: a provider transports
// what it is given and never reshapes it.
type Provider interface {
	// SendQueue delivers one envelope to an endpoint's queue and returns only
	// once the store has acknowledged it. A send that is not acknowledged is
	// an error, never a silent drop.
	SendQueue(endpoint string, envelope []byte) error

	// PublishTopic speaks one envelope in a topic, with the same
	// acknowledgement requirement.
	PublishTopic(topic string, envelope []byte) error

	// Topics writes the active topics and their in-window counts.
	Topics(w io.Writer) error

	// Status writes one unread count per endpoint in the registry.
	Status(w io.Writer) error

	// Close releases whatever connection the provider holds.
	Close()
}

// Factory builds a provider. Registration happens in an init function in the
// provider's own package, so selecting one is a matter of importing it.
type Factory func() (Provider, error)

var (
	mu         sync.Mutex
	registered = map[string]Factory{}
)

// Register makes a provider selectable by name. It panics on a duplicate:
// two modules claiming one name is a build mistake, not a runtime condition.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registered[name]; dup {
		panic("provider: duplicate registration for " + name)
	}
	registered[name] = f
}

// Open returns the provider named in config.
func Open(name string) (Provider, error) {
	if name == "" {
		return nil, fmt.Errorf("no provider configured (set 'provider = <name>' in %s/config)", config.Home())
	}
	mu.Lock()
	f, ok := registered[name]
	mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown provider '%s' (not built into this loc)", name)
	}
	return f()
}
