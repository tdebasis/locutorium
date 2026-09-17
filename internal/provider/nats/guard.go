package nats

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
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

// ErrRefusedUnderTest is what Dial returns when the guard fires. Callers that
// fold every dial error into errConnect let this one through, because "cannot
// reach the medium" is the opposite of what happened: the medium was reachable
// and the guard stopped the dial. A test that hit the guard has to be told so.
var ErrRefusedUnderTest = fmt.Errorf("refused under go test")

// Dial is the one way this module opens a connection. It refuses the default
// broker under `go test` and otherwise dials. Every caller in the module and
// the daemon uses it; a new natsgo.Connect anywhere else is a defect.
func Dial(url string, opts ...natsgo.Option) (*natsgo.Conn, error) {
	if err := refuseDefaultUnderTest(url); err != nil {
		return nil, err
	}
	return dialBroker(url, opts...)
}

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
// harness. Discipline failed twice. Every connection this module opens goes
// through Dial below — connectWithin, WatchQueue's long-lived listener and
// the daemon's ensureTopics — so the refusal sits on that one seam and no
// call site can walk around it. The first cut of this guard sat inside
// connectWithin only, and WatchQueue dialled past it (Assayer F1, 2026-09-16).
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
	// THE VALUE IS A LIST. The nats client splits nats_url on commas and dials
	// each server in turn, so a list that names the default anywhere reaches
	// it. Every entry is checked (Assayer F2, 2026-09-16).
	for _, one := range strings.Split(raw, ",") {
		if sameBroker(one, def) {
			return fmt.Errorf("%w: refusing to dial %s: it names the default %s, "+
				"and on a machine that runs the Locutorium that is the live broker. "+
				"A test must pin %s in its own scratch config, to a closed port or to a "+
				"broker it booted on a port the kernel picked. On 2026-09-16 a test run "+
				"against this address deleted every queue on a live deployment",
				ErrRefusedUnderTest, strings.TrimSpace(one), config.NATSURL, config.NATSURL)
		}
	}
	return nil
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
	if portOf(ua) != portOf(ub) {
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

// portOf is the port as a number, with the nats client's default when the
// address names none. `nats://localhost` and `127.0.0.1:04222` are both the
// default broker to the client, so they are to the guard (Assayer F2, N1).
func portOf(u *url.URL) int {
	p := u.Port()
	if p == "" {
		return 4222
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return -1
	}
	return n
}

// loopbackHost reports whether a host name reaches this machine and no other.
func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	// 0.0.0.0 and :: are "this host" to a dialler on this host, so they reach
	// the same listener (Assayer N1).
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}
