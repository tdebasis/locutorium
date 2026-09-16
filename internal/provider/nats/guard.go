package nats

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"

	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/config"
)

// underTest reports whether this process is a test binary. It is a variable so
// that the guard's own cases can pin it true or false. It is the shape of
// refuseListen in cmd/loc/daemon.go.
//
// testing.Testing() is false in the shipped binary. The import adds no
// behaviour outside `go test`.
var underTest = testing.Testing

// dialBroker opens one connection. It is a variable for one reason: a case must
// be able to prove that the guard refuses BEFORE anything reaches the network.
var dialBroker = natsgo.Connect

// refuseDefaultUnderTest refuses the product's default broker address when a
// test binary asks for it. It answers nil in the shipped binary.
//
// WHY THE CLIENT AND NOT THE HARNESS. On 2026-09-16 a `go test ./...` in this
// repository deleted every queue on the machine's live deployment. Two cases in
// cmd/loc made a scratch LOC_HOME, wrote no config file, and therefore took the
// key table's default nats_url. On a machine that runs the product, that
// address is the live broker. One case ran a sweep. The sweep deletes a queue
// that no row in its registry claims, the scratch registry was empty, and all
// six live queues went. Any message unread at that moment was lost. The same
// class happened on 2026-09-12, and the answer then was discipline in the
// harness. Discipline failed twice. Every package that speaks NATS dials
// through connectWithin, so the refusal belongs here.
//
// LOCALHOST IS THE DEFAULT TOO. The comparison reads the host and the port that
// url.Parse finds, not the two strings. 127.0.0.1, ::1 and localhost name one
// listener on this machine, so the guard refuses all three at the default port.
// A test that wants a broker boots its own on a port the kernel picks. A test
// that wants no broker points at a closed port. Neither is ever this address.
func refuseDefaultUnderTest(raw string) error {
	if !underTest() {
		return nil
	}
	def := config.Default(config.NATSURL)
	if !sameBroker(raw, def) {
		return nil
	}
	return fmt.Errorf("refusing to dial %s under go test: it is the default %s, "+
		"and on a machine that runs the Locutorium it is the live broker. "+
		"A test must pin %s in its own scratch config, to a closed port or to a "+
		"broker it booted on a port the kernel picked. On 2026-09-16 a test run "+
		"against this address deleted every queue on a live deployment",
		raw, config.NATSURL, config.NATSURL)
}

// sameBroker reports whether two nats URLs name one broker on this machine.
//
// It compares the host and the port, not the text. A trailing slash, a capital
// letter in the scheme and the three loopback names write one address several
// ways. Two loopback names are one host here, because they reach one listener.
func sameBroker(a, b string) bool {
	ua, ok := parseBroker(a)
	if !ok {
		return false
	}
	ub, ok := parseBroker(b)
	if !ok {
		return false
	}
	if ua.Port() != ub.Port() {
		return false
	}
	ha, hb := strings.ToLower(ua.Hostname()), strings.ToLower(ub.Hostname())
	if ha == hb {
		return true
	}
	return loopbackHost(ha) && loopbackHost(hb)
}

// parseBroker reads one address. A value with no scheme gets the product's, so
// that a hand-edited `nats_url = 127.0.0.1:4222` cannot walk past the guard.
func parseBroker(raw string) (*url.URL, bool) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return nil, false
	}
	if !strings.Contains(raw, "://") {
		raw = "nats://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return nil, false
	}
	return u, true
}

// loopbackHost reports whether a host name reaches this machine and no other.
func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
