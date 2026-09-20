package nats

import (
	"path/filepath"
	"testing"

	"github.com/tdebasis/locutorium/internal/loctest"
	"github.com/tdebasis/locutorium/internal/presence"
)

// THE CONTROL FOR THE PIN: two brokers, and the provider must use the one it
// was given rather than the one the file names.
//
// This is the incident in miniature. A daemon runs a broker of its own and
// keeps reading nats_url, so a config file that points somewhere else moves
// every later operation onto a stranger's broker. There the daemon's ledger
// claims nothing, and its sweep reconciles records it did not write.
// A provider from At must never look at the file for its address.
//
// A stands for the daemon's own broker and B for the other deployment's. The
// scratch config names B, which is the only thing that could pull the provider
// off A.
//
// THE ONE-LINE PLANT THAT TURNS THIS RED: make brokerURL return
// config.Value(config.NATSURL) unconditionally, so the pin is ignored. The
// queue then appears on B and not on A, and both assertions below fail.
func TestAPinnedProviderUsesItsOwnBrokerAndNotTheConfiguredOne(t *testing.T) {
	a := loctest.Boot(t)
	b := loctest.Boot(t)
	if a.URL == b.URL {
		t.Fatalf("both brokers booted on %s; the case needs two", a.URL)
	}

	home := t.TempDir()
	write(t, filepath.Join(home, "config"), "provider = nats\nnats_url = "+b.URL+"\n")
	t.Setenv("LOC_HOME", home)

	const endpoint = "workshop.scribe"
	p := At(a.URL)
	t.Cleanup(p.Close)
	if err := p.CreateQueue(endpoint); err != nil {
		t.Fatalf("CreateQueue on the pinned broker: %v", err)
	}

	// Asserted through each server's own admin connection, so nothing here is
	// reported by the provider under test.
	stream := presence.StreamName(endpoint)

	ncA, jsA := a.Admin(t, "admin", testPassword)
	defer ncA.Close()
	if _, err := jsA.StreamInfo(stream); err != nil {
		t.Errorf("%s is not on the pinned broker: %v", stream, err)
	}

	ncB, jsB := b.Admin(t, "admin", testPassword)
	defer ncB.Close()
	if _, err := jsB.StreamInfo(stream); err == nil {
		t.Errorf("%s reached the broker the config names; the pin was ignored", stream)
	}
}

// New() is unchanged by the pin: with no pinned address it reads the
// deployment's nats_url at every use, which is what every CLI verb wants.
func TestAnUnpinnedProviderStillReadsTheConfiguredAddress(t *testing.T) {
	a := loctest.Boot(t)
	b := loctest.Boot(t)

	home := t.TempDir()
	write(t, filepath.Join(home, "config"), "provider = nats\nnats_url = "+b.URL+"\n")
	t.Setenv("LOC_HOME", home)

	const endpoint = "workshop.clerk"
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(p.Close)
	pr, ok := p.(*Provider)
	if !ok {
		t.Fatalf("New returned %T, want *Provider", p)
	}
	if err := pr.CreateQueue(endpoint); err != nil {
		t.Fatalf("CreateQueue: %v", err)
	}

	stream := presence.StreamName(endpoint)

	ncB, jsB := b.Admin(t, "admin", testPassword)
	defer ncB.Close()
	if _, err := jsB.StreamInfo(stream); err != nil {
		t.Errorf("%s is not on the configured broker: %v", stream, err)
	}

	ncA, jsA := a.Admin(t, "admin", testPassword)
	defer ncA.Close()
	if _, err := jsA.StreamInfo(stream); err == nil {
		t.Errorf("%s reached a broker no config names", stream)
	}
}
