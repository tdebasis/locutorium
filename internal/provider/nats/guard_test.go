package nats

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/config"
)

// THE GUARD, TESTED WITHOUT A NETWORK.
//
// Every case here replaces dialBroker, so no case in this file opens a socket.
// That is the point: the claim under test is that the refusal happens BEFORE
// the dial, and a case that proved it by dialing a live broker would be the
// defect it checks. See guard.go for the 2026-09-16 outage.
//
// These cases set package variables. No case in this package calls
// t.Parallel, and none here may.

// refusalMark is the phrase only the guard writes.
const refusalMark = "refusing to dial"

// recorder stands in for the NATS client. It records the address it was asked
// for and opens nothing.
type recorder struct{ urls []string }

func (r *recorder) dial(url string, _ ...natsgo.Option) (*natsgo.Conn, error) {
	r.urls = append(r.urls, url)
	return nil, errors.New("this case never opens a connection")
}

// pin sets the two seams for one case and restores them after it.
func pin(t *testing.T, isTest bool) *recorder {
	t.Helper()
	r := &recorder{}
	oldUnder, oldDial := underTest, dialBroker
	underTest = func() bool { return isTest }
	dialBroker = r.dial
	t.Cleanup(func() { underTest, dialBroker = oldUnder, oldDial })
	return r
}

func TestTheDefaultBrokerIsRefusedUnderGoTestAndNothingIsDialled(t *testing.T) {
	r := pin(t, true)
	// An empty home holds no config file, so nats_url is the table's default:
	// the address that took a live deployment out on 2026-09-16.
	t.Setenv("LOC_HOME", t.TempDir())

	err := (&Provider{}).connect()
	if err == nil {
		t.Fatal("connect accepted the default address under go test")
	}
	if !strings.Contains(err.Error(), refusalMark) {
		t.Errorf("connect failed with %q, want the guard's refusal", err)
	}
	if !strings.Contains(err.Error(), config.Default(config.NATSURL)) {
		t.Errorf("the refusal %q does not name the address", err)
	}
	if len(r.urls) != 0 {
		t.Errorf("the guard dialled %v; it must refuse before the dial", r.urls)
	}
}

func TestAPinnedAddressReachesTheDialUnderGoTest(t *testing.T) {
	r := pin(t, true)
	home := t.TempDir()
	port := closedPort(t)
	write(t, filepath.Join(home, "config"), "nats_url = "+port+"\n")
	t.Setenv("LOC_HOME", home)

	err := (&Provider{}).connect()
	if err != errConnect {
		t.Fatalf("connect returned %v, want the errConnect sentinel", err)
	}
	if strings.Contains(err.Error(), refusalMark) {
		t.Error("the guard refused an address a test pinned for itself")
	}
	if len(r.urls) != 1 || r.urls[0] != port {
		t.Errorf("the dial saw %v, want one call for %s", r.urls, port)
	}
}

func TestTheShippedBinaryDialsTheDefaultAddress(t *testing.T) {
	r := pin(t, false)
	t.Setenv("LOC_HOME", t.TempDir())

	err := (&Provider{}).connect()
	if err != errConnect {
		t.Fatalf("connect returned %v, want the errConnect sentinel", err)
	}
	if strings.Contains(err.Error(), refusalMark) {
		t.Error("the guard refused the default address outside go test")
	}
	def := config.Default(config.NATSURL)
	if len(r.urls) != 1 || r.urls[0] != def {
		t.Errorf("the dial saw %v, want one call for %s", r.urls, def)
	}
}

// One broker, written several ways. The guard reads the host and the port, so
// each of these is the default address and each is refused.
func TestSameBrokerReadsTheHostAndThePort(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want bool
	}{
		{"nats://127.0.0.1:4222", true},
		{"nats://127.0.0.1:4222/", true},
		{"NATS://127.0.0.1:4222", true},
		{"  nats://127.0.0.1:4222  ", true},
		{"nats://localhost:4222", true},
		{"nats://LOCALHOST:4222", true},
		{"nats://[::1]:4222", true},
		{"127.0.0.1:4222", true},
		{"nats://127.0.0.1:4223", false},
		{"nats://10.0.0.4:4222", false},
		{"nats://example.test:4222", false},
		{"", false},
	} {
		if got := sameBroker(c.raw, config.Default(config.NATSURL)); got != c.want {
			t.Errorf("sameBroker(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}
