package loctest

// The harness's own refusal, exercised.
//
// Admin dials without going through the provider's Dial, so it carries its own
// refusal of the deployment's address. A guard whose decision has never been
// failed is a guard nobody has tested, so the comparison is split out and every
// arm below drives it in a named direction.

import (
	"testing"

	"github.com/tdebasis/locutorium/internal/config"
)

func TestNamesDefaultPortRefusesTheDeploymentAndPassesAScratchServer(t *testing.T) {
	for _, arm := range []struct {
		name string
		addr string
		want bool
	}{
		// THE CASE THIS EXISTS FOR. A helper pinning the port to the default
		// is what would send the whole suite at a live broker.
		{"the default address", "nats://127.0.0.1:4222", true},
		{"the same listener by another loopback name", "nats://localhost:4222", true},
		{"the same listener as ::1", "nats://[::1]:4222", true},
		{"no scheme, which a hand-edited value looks like", "127.0.0.1:4222", true},
		{"no port at all, which the client reads as the default", "nats://127.0.0.1", true},

		// THE COMMON CASE, and the control that proves the guard is not simply
		// refusing everything. Boot hands Admin an address of this shape on
		// every run in the suite.
		{"a kernel-picked port, which is what Boot produces", "nats://127.0.0.1:55123", false},
		{"another kernel-picked port", "nats://127.0.0.1:1", false},

		// AN ADDRESS THAT PARSES AND NAMES NO PORT IS THE DEFAULT PORT, even
		// where the host is nonsense: "::::" parses to the host ":::" with no
		// port, and the client would read that as 4222. Refusing it is failing
		// CLOSED, and it cannot fire on a real run, because Boot always hands
		// Admin an explicit kernel-picked port. This arm was written expecting
		// false and the measurement said otherwise; the behaviour is right and
		// the expectation was wrong.
		{"a parseable host naming no port, which reads as the default", "::::", true},

		// Genuinely unreadable input is NOT a refusal. Saying no to a value it
		// cannot read would fail runs for a reason the message could not
		// explain, and a space is not a legal host.
		{"empty", "", false},
		{"not parseable as an address at all", "not an address", false},
	} {
		t.Run(arm.name, func(t *testing.T) {
			if got := namesDefaultPort(arm.addr); got != arm.want {
				t.Errorf("namesDefaultPort(%q) = %v, want %v", arm.addr, got, arm.want)
			}
		})
	}
}

// TestTheRefusalIsAgainstTheCONFIGUREDDefault pins that the comparison reads
// the product's own key rather than a number written here. A literal 4222 in
// the guard would keep passing if the default ever moved, and the guard would
// then protect an address nobody uses.
func TestTheRefusalIsAgainstTheCONFIGUREDDefault(t *testing.T) {
	def := config.Default(config.NATSURL)
	if def == "" {
		t.Fatal("the product has no default broker address; the guard has nothing to compare against")
	}
	if !namesDefaultPort(def) {
		t.Errorf("namesDefaultPort(%q) is false for the product's OWN default; "+
			"the guard cannot refuse the one address it exists to refuse", def)
	}
}

// TestBootDoesNotProduceTheDefaultAddress is the end-to-end half: the harness
// as it actually runs must pass its own guard. This is the arm that would go
// red the day somebody pins the port.
func TestBootDoesNotProduceTheDefaultAddress(t *testing.T) {
	s := Boot(t)
	if namesDefaultPort(s.URL) {
		t.Fatalf("Boot produced %s, which names the deployment's port", s.URL)
	}
	// AND THE GUARD IS REACHED ON THE REAL PATH, not only in the arms above.
	nc, _ := s.Admin(t, "", "")
	nc.Close()
}
