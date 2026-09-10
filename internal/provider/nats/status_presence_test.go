package nats

import (
	"path/filepath"
	"strings"
	"testing"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/loctest"
	"github.com/tdebasis/locutorium/internal/presence"
)

// The bare `loc status` report under the presence model.
//
// Endpoints are namespaced <instance>.<agent>, and a subject may carry a dot
// where a durable object's name may not — so the queue behind an endpoint is
// presence.StreamName(e), dots substituted for underscores. A presence-model
// subscribe creates the stream and NO CONSUMER, so the untaken count is the
// stream's own message count: work-queue retention drops a message when it is
// taken, which makes a stored message exactly an untaken one.
//
// The deployment here is the presence one — dotted endpoints, no consumers —
// and is deliberately not the flat single-token harness the rest of this
// package's suite uses; the whole point is what happens once a name has a dot
// in it.

// namespaced is a scratch deployment whose endpoints carry an instance prefix.
type namespaced struct {
	srv  *loctest.Server
	home string
}

// newNamespaced boots a broker, writes a $LOC_HOME whose `endpoints` file
// holds the dotted names, and creates a backing queue for each endpoint named
// in withQueues — the others are left with no stream at all, which is the
// second of the two answers the report has to give.
func newNamespaced(t *testing.T, endpoints []string, withQueues []string) *namespaced {
	t.Helper()

	home := t.TempDir()
	users := []*natsserver.User{{Username: "admin", Password: testPassword}}
	for _, e := range endpoints {
		users = append(users, &natsserver.User{Username: e, Password: testPassword})
	}
	srv := loctest.Boot(t, users, "admin", false)

	write(t, filepath.Join(home, "config"), "nats_url = "+srv.URL+"\n")
	seedLedger(t, home, endpoints...)
	t.Setenv("LOC_HOME", home)

	nc, js := srv.Admin(t, "admin", testPassword)
	defer nc.Close()
	for _, e := range withQueues {
		if _, err := js.AddStream(&natsgo.StreamConfig{
			Name:      presence.StreamName(e),
			Subjects:  []string{"queue." + e},
			Retention: natsgo.WorkQueuePolicy,
			Storage:   natsgo.MemoryStorage,
			Replicas:  1,
		}); err != nil {
			t.Fatalf("seed queue for %s: %v", e, err)
		}
	}
	return &namespaced{srv: srv, home: home}
}

// store puts one message into an endpoint's queue, through an independent
// admin connection, so the count under test is not reported by the code that
// is being checked.
func (n *namespaced) store(t *testing.T, endpoint, body string) {
	t.Helper()
	nc, js := n.srv.Admin(t, "admin", testPassword)
	defer nc.Close()
	if _, err := js.Publish("queue."+endpoint, []byte(body)); err != nil {
		t.Fatalf("store a message for %s: %v", endpoint, err)
	}
}

// as returns a provider speaking as id against this deployment.
func (n *namespaced) as(t *testing.T, id string) *Provider {
	t.Helper()
	t.Setenv("LOC_IDENTITY", id)
	p := &Provider{}
	t.Cleanup(p.Close)
	return p
}

func TestStatusCountsANamespacedEndpointsStoredMessages(t *testing.T) {
	e := "workshop.scribe"
	n := newNamespaced(t, []string{e}, []string{e})
	n.store(t, e, "one for the scribe")

	p := n.as(t, e)
	var out strings.Builder
	if err := p.Status(&out); err != nil {
		t.Fatalf("Status: %v", err)
	}
	want := "workshop.scribe unread: 1\n"
	if out.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", out.String(), want)
	}
}

func TestStatusPrintsAQuestionMarkForANamespacedEndpointWithNoQueue(t *testing.T) {
	held, free := "workshop.scribe", "workshop.clerk"
	n := newNamespaced(t, []string{held, free}, []string{held})
	n.store(t, held, "one for the scribe")

	p := n.as(t, held)
	var out strings.Builder
	if err := p.Status(&out); err != nil {
		t.Fatalf("Status: %v", err)
	}
	// ENDPOINT ORDER, NOT FILE ORDER. The roster is the ledger, and a ledger
	// is a directory, so the report is sorted rather than left in whatever
	// order a hand-maintained file happened to list.
	want := "workshop.clerk unread: ?\nworkshop.scribe unread: 1\n"
	if out.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", out.String(), want)
	}
}
