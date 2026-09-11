package broker

import "testing"

// THE POSTURE IS ONE ABSENT FIELD, SO IT IS ASSERTED. V0 puts no
// authentication on the loopback listener (R12, 2026-09-09), and the whole of
// that decision is that Users and NoAuthUser are unset here. A future edit that
// adds an authorization block would otherwise change the product's posture with
// nothing to stop it, and every test would still pass, because the integration
// harness reads this builder rather than spelling its own options.
//
// THIS IS THE ARM V1 MUST CHANGE ON PURPOSE. V1 adds authentication, and the
// commit that adds it must change these assertions in the same edit. The
// posture then moves by a decision, and never by drift.
func TestOptionsCarryNoAuthorizationBlock(t *testing.T) {
	o := Options("127.0.0.1", 4222, "/tmp/store")
	if o.Users != nil {
		t.Errorf("Options carries %d users; V0 authenticates nobody", len(o.Users))
	}
	if o.NoAuthUser != "" {
		t.Errorf("Options maps unauthenticated clients to %q; V0 has nobody to map to", o.NoAuthUser)
	}
	if o.Nkeys != nil || o.Authorization != "" || o.Username != "" || o.Password != "" {
		t.Error("Options carries a credential of some kind; V0 carries none")
	}
}

// The rest of the shape, so the harness and the daemon cannot boot a server
// that stores nowhere or logs over the daemon's own output.
func TestOptionsAreTheDaemonsShape(t *testing.T) {
	o := Options("127.0.0.1", -1, "/tmp/store")
	if o.Host != "127.0.0.1" || o.Port != -1 || o.StoreDir != "/tmp/store" {
		t.Errorf("Options = host %q port %d store %q, want the arguments back", o.Host, o.Port, o.StoreDir)
	}
	if !o.JetStream {
		t.Error("JetStream is off; queues and rooms are streams")
	}
	if !o.NoSigs {
		t.Error("NoSigs is off; the library would exit before the daemon removes its pidfile")
	}
	if !o.NoLog {
		t.Error("NoLog is off; the broker would write over the daemon's own output")
	}
}
