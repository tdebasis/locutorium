package nats

// The queue enumerator (#27). The sweep destroys a queue no row claims and
// recreates a queue no listing showed, so this function is the one input where
// a short list is worse than no list at all.

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/loctest"
)

// Queues reports every queue as the ENDPOINT it belongs to, in endpoint order,
// and nothing else the store happens to hold.
func TestQueuesListsEveryQueueAsAnEndpoint(t *testing.T) {
	seats := []string{"workshop.scribe", "workshop.clerk", "atelier.scribe"}
	n := newNamespaced(t, seats, seats)
	// Two objects that are not a seat's queue: the message plane's own stream,
	// and a QUEUE_-prefixed name that does not reverse to a valid endpoint.
	nc, js := n.srv.Admin(t, "admin", testPassword)
	defer nc.Close()
	for _, cfg := range []*natsgo.StreamConfig{
		{Name: "TOPICS", Subjects: []string{"topic.>"}, Storage: natsgo.MemoryStorage, Replicas: 1},
		{Name: "QUEUE_one_two_three", Subjects: []string{"queue.one.two.three"}, Storage: natsgo.MemoryStorage, Replicas: 1},
	} {
		if _, err := js.AddStream(cfg); err != nil {
			t.Fatalf("seed %s: %v", cfg.Name, err)
		}
	}

	p := n.as(t, "workshop.scribe")
	got, err := p.Queues()
	if err != nil {
		t.Fatalf("Queues: %v", err)
	}
	want := []string{"atelier.scribe", "workshop.clerk", "workshop.scribe"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Queues = %v, want %v — dotted, sorted, and nothing that is not a seat's queue", got, want)
	}
}

// A deployment where nothing has subscribed reports no queues, and that is not
// an error: it is what an empty parlor looks like.
func TestQueuesOnAnEmptyStore(t *testing.T) {
	n := newNamespaced(t, []string{"workshop.scribe"}, nil)
	p := n.as(t, "workshop.scribe")
	got, err := p.Queues()
	if err != nil || len(got) != 0 {
		t.Errorf("Queues = %v, %v; want no queues and no error", got, err)
	}
}

// A medium that cannot be reached is an ERROR, never an empty list. An empty
// list would tell the sweep that every registered seat has lost its queue.
func TestQueuesOnAnUnreachableMediumIsAnError(t *testing.T) {
	n := newNamespaced(t, []string{"workshop.scribe"}, []string{"workshop.scribe"})
	write(t, filepath.Join(n.home, "config"), "nats_url = "+closedPort(t)+"\n")

	p := n.as(t, "workshop.scribe")
	got, err := p.Queues()
	if err == nil {
		t.Fatalf("Queues = %v, nil; want an error from an unreachable medium", got)
	}
	if got != nil {
		t.Errorf("Queues returned %v beside its error; a failed listing has no answer", got)
	}
}

// A listing the deployment REFUSES is an error too, and the refusal is not
// mistaken for "there are no queues". The seat here may create and read its
// own queue and may not list the store.
func TestQueuesReportsAListingTheDeploymentRefuses(t *testing.T) {
	const seat = "workshop.scribe"
	home := t.TempDir()
	users := []*natsserver.User{
		{Username: "admin", Password: testPassword},
		{Username: seat, Password: testPassword, Permissions: &natsserver.Permissions{
			// Everything the seat needs, and the two listing endpoints denied.
			Publish: &natsserver.SubjectPermission{
				Allow: []string{"queue.>", "$JS.API.>"},
				Deny:  []string{"$JS.API.STREAM.NAMES", "$JS.API.STREAM.LIST"},
			},
			Subscribe: &natsserver.SubjectPermission{Allow: []string{"queue.>", "_INBOX.>"}},
		}},
	}
	// OPT-IN: this case needs the medium to REFUSE a stream listing, so it
	// boots the authenticated shape the product does not have.
	srv := loctest.Boot(t, loctest.WithRefusals(users, seat))
	write(t, filepath.Join(home, "config"), "nats_url = "+srv.URL+"\n")
	t.Setenv("LOC_HOME", home)
	nc, js := srv.Admin(t, "admin", testPassword)
	defer nc.Close()
	if _, err := js.AddStream(queueConfig(seat)); err != nil {
		t.Fatalf("seed the seat's queue: %v", err)
	}

	t.Setenv("LOC_IDENTITY", seat)
	p := &Provider{}
	t.Cleanup(p.Close)
	got, err := p.Queues()
	if err == nil {
		t.Fatalf("Queues = %v, nil; want the refused listing reported", got)
	}
	if !strings.Contains(err.Error(), "cannot list the queues") {
		t.Errorf("Queues error = %v, want it to name what could not be done", err)
	}
	if got != nil {
		t.Errorf("Queues returned %v beside its error", got)
	}
}

// The backing-object name reverses to the endpoint it was derived from, and
// anything that is not one of ours is refused rather than guessed at.
func TestEndpointOfStreamReversesTheSubstitution(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		ok       bool
	}{
		{"QUEUE_workshop_scribe", "workshop.scribe", true},
		{"QUEUE_a_b", "a.b", true},
		{"TOPICS", "", false},
		{"QUEUE_scribe", "", false},        // no instance: not an endpoint
		{"QUEUE_one_two_three", "", false}, // three segments: not an endpoint
		{"QUEUE_", "", false},              // nothing at all
		{"NOT_A_QUEUE_workshop_scribe", "", false},
	}
	for _, tc := range cases {
		endpoint, ok := endpointOfStream(tc.name)
		if ok != tc.ok || endpoint != tc.endpoint {
			t.Errorf("endpointOfStream(%q) = %q, %v; want %q, %v", tc.name, endpoint, ok, tc.endpoint, tc.ok)
		}
	}
}

// The other listing (#136): a queue object whose name does not read back as an
// endpoint, by its raw name. Seven such objects sat on one deployment for five
// weeks, in no listing and in no report.
//
// The two listings partition the store's queue objects. This case asserts both
// halves at once, because the defect was that a name fell out of both.
func TestUnqualifiedQueuesListsWhatQueuesCannotName(t *testing.T) {
	const seat = "workshop.scribe"
	n := newNamespaced(t, []string{seat}, []string{seat})
	nc, js := n.srv.Admin(t, "admin", testPassword)
	defer nc.Close()
	// TOPICS is not a queue at all. The other two are queue objects made
	// before an endpoint carried an instance name.
	for _, cfg := range []*natsgo.StreamConfig{
		{Name: "TOPICS", Subjects: []string{"topic.>"}, Storage: natsgo.MemoryStorage, Replicas: 1},
		{Name: "QUEUE_scribe", Subjects: []string{"queue.scribe"}, Storage: natsgo.MemoryStorage, Replicas: 1},
		{Name: "QUEUE_one_two_three", Subjects: []string{"queue.one.two.three"}, Storage: natsgo.MemoryStorage, Replicas: 1},
	} {
		if _, err := js.AddStream(cfg); err != nil {
			t.Fatalf("seed %s: %v", cfg.Name, err)
		}
	}

	p := n.as(t, seat)
	got, err := p.UnqualifiedQueues()
	if err != nil {
		t.Fatalf("UnqualifiedQueues: %v", err)
	}
	want := []string{"QUEUE_one_two_three", "QUEUE_scribe"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UnqualifiedQueues = %v, want %v — raw names, sorted, and nothing that is not a queue object", got, want)
	}
	// The valid seat's queue belongs to the OTHER listing and to that one only.
	queues, err := p.Queues()
	if err != nil {
		t.Fatalf("Queues: %v", err)
	}
	if !reflect.DeepEqual(queues, []string{seat}) {
		t.Errorf("Queues = %v, want just %v — the bare names are not endpoints and are not here", queues, seat)
	}
}

// An empty store has no unqualified queues, and that is not an error.
func TestUnqualifiedQueuesOnAnEmptyStore(t *testing.T) {
	n := newNamespaced(t, []string{"workshop.scribe"}, nil)
	p := n.as(t, "workshop.scribe")
	got, err := p.UnqualifiedQueues()
	if err != nil || len(got) != 0 {
		t.Errorf("UnqualifiedQueues = %v, %v; want nothing and no error", got, err)
	}
}

// A listing that cannot complete is an error here too, never a short list. An
// empty answer would read as "the deployment is clean".
func TestUnqualifiedQueuesOnAnUnreachableMediumIsAnError(t *testing.T) {
	n := newNamespaced(t, []string{"workshop.scribe"}, []string{"workshop.scribe"})
	write(t, filepath.Join(n.home, "config"), "nats_url = "+closedPort(t)+"\n")

	p := n.as(t, "workshop.scribe")
	got, err := p.UnqualifiedQueues()
	if err == nil {
		t.Fatalf("UnqualifiedQueues = %v, nil; want an error from an unreachable medium", got)
	}
	if got != nil {
		t.Errorf("UnqualifiedQueues returned %v beside its error", got)
	}
}
