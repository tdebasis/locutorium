package nats

// A seat carrying the DEPLOYMENT'S OWN access control, reading its own queue.
//
// The rest of this package's suite authorises its seats with an unrestricted
// user, so every case in it passes on a build that has no idea what the
// deployment permits. The harness here is the opposite: its seat holds exactly
// the per-seat block providers/nats/bootstrap.sh writes, so what these cases
// see is what a real endpoint sees.
//
// What that block does NOT grant is a cursor on the seat's own queue. The
// answer to an ungranted JetStream API request is SILENCE — the request is
// simply not replied to — so the create expires, the fetch finds nothing, and
// a queue holding mail reads as empty. That is the one reading a reader must
// never be given, and these cases hold the line.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/loctest"
	"github.com/tdebasis/locutorium/internal/presence"
)

const (
	aclPassword = "acl-scratch"
	aclSeat     = "workshop.scribe"
)

// shippedSeat is the per-seat template of providers/nats/bootstrap.sh AS
// SHIPPED at 213338b — the script's lines 61-73, copied verbatim with $name
// substituted and nothing else changed. Every JetStream object name in it is
// spelled with the RAW endpoint name, dot and all.
//
//	publish: { allow: [
//	  "queue.*", "topic.>",
//	  "$JS.ACK.QUEUE_$name.>", "$JS.ACK.TOPICS.$name.>",
//	  "$JS.API.INFO",
//	  "$JS.API.STREAM.INFO.*", "$JS.API.STREAM.NAMES", "$JS.API.STREAM.LIST",
//	  "$JS.API.STREAM.SUBJECTS.TOPICS",
//	  "$JS.API.CONSUMER.INFO.>",
//	  "$JS.API.CONSUMER.MSG.NEXT.QUEUE_$name.$name",
//	  "$JS.API.CONSUMER.MSG.NEXT.TOPICS.$name",
//	  "$JS.API.CONSUMER.DURABLE.CREATE.TOPICS.$name",
//	  "$JS.API.CONSUMER.CREATE.TOPICS.$name", "$JS.API.CONSUMER.CREATE.TOPICS.$name.>" ] },
//	subscribe: { allow: ["queue.$name", "topic.>", "_INBOX.>"] } } }
func shippedSeat(name string) *natsserver.User {
	return &natsserver.User{Username: name, Password: aclPassword, Permissions: &natsserver.Permissions{
		Publish: &natsserver.SubjectPermission{Allow: []string{
			"queue.*", "topic.>",
			"$JS.ACK.QUEUE_" + name + ".>", "$JS.ACK.TOPICS." + name + ".>",
			"$JS.API.INFO",
			"$JS.API.STREAM.INFO.*", "$JS.API.STREAM.NAMES", "$JS.API.STREAM.LIST",
			"$JS.API.STREAM.SUBJECTS.TOPICS",
			"$JS.API.CONSUMER.INFO.>",
			"$JS.API.CONSUMER.MSG.NEXT.QUEUE_" + name + "." + name,
			"$JS.API.CONSUMER.MSG.NEXT.TOPICS." + name,
			"$JS.API.CONSUMER.DURABLE.CREATE.TOPICS." + name,
			"$JS.API.CONSUMER.CREATE.TOPICS." + name, "$JS.API.CONSUMER.CREATE.TOPICS." + name + ".>",
		}},
		Subscribe: &natsserver.SubjectPermission{Allow: []string{"queue." + name, "topic.>", "_INBOX.>"}},
	}}
}

// grantedSeat is the same block with the R2 spelling: every JetStream object
// name substitutes the dot, and the seat may create the cursor on its own
// queue. This is what bootstrap.sh writes after the fix, and it is here so the
// pair red/green is one harness apart rather than one build apart.
func grantedSeat(name string) *natsserver.User {
	s := strings.ReplaceAll(name, ".", "_")
	return &natsserver.User{Username: name, Password: aclPassword, Permissions: &natsserver.Permissions{
		Publish: &natsserver.SubjectPermission{Allow: []string{
			"queue.*", "queue.*.*", "topic.>",
			"$JS.ACK.QUEUE_" + s + ".>", "$JS.ACK.TOPICS." + s + ".>",
			"$JS.API.INFO",
			"$JS.API.STREAM.INFO.*", "$JS.API.STREAM.NAMES", "$JS.API.STREAM.LIST",
			"$JS.API.STREAM.SUBJECTS.TOPICS",
			"$JS.API.CONSUMER.INFO.>",
			"$JS.API.CONSUMER.MSG.NEXT.QUEUE_" + s + "." + s,
			"$JS.API.CONSUMER.MSG.NEXT.TOPICS." + s,
			"$JS.API.CONSUMER.CREATE.QUEUE_" + s + "." + s,
			"$JS.API.CONSUMER.DURABLE.CREATE.QUEUE_" + s + "." + s,
			"$JS.API.CONSUMER.DURABLE.CREATE.TOPICS." + s,
			"$JS.API.CONSUMER.CREATE.TOPICS." + s, "$JS.API.CONSUMER.CREATE.TOPICS." + s + ".>",
		}},
		Subscribe: &natsserver.SubjectPermission{Allow: []string{"queue." + name, "topic.>", "_INBOX.>"}},
	}}
}

// aclHarness is one scratch deployment whose seat holds a real access-control
// block. The queue stream is stood up by the admin and NO CONSUMER IS MADE:
// that is the presence-model shape, where subscribing creates the stream and
// the reader's cursor is the reader's own to make.
type aclHarness struct {
	home    string
	admin   *natsgo.Conn
	adminJS natsgo.JetStreamContext
}

func newACLHarness(t *testing.T, seat *natsserver.User) *aclHarness {
	t.Helper()
	home := t.TempDir()
	users := []*natsserver.User{{Username: "admin", Password: aclPassword}, seat}
	srv := loctest.Boot(t, users, false)

	loctest.Write(t, filepath.Join(home, "config"), "provider = nats\nnats_url = "+srv.URL+"\n")
	loctest.Write(t, filepath.Join(home, "endpoints"), seat.Username+"\n")
	for _, u := range users {
		loctest.Write(t, filepath.Join(home, "creds", u.Username), aclPassword)
	}
	t.Setenv("LOC_HOME", home)

	nc, js := srv.Admin(t, "admin", aclPassword)
	t.Cleanup(nc.Close)
	return &aclHarness{home: home, admin: nc, adminJS: js}
}

// seedQueue stands the backing stream up as a subscribe would have, and makes
// NO CONSUMER: that is the presence-model shape, where the reader's cursor is
// the reader's own to create. It is also the state `doctor --init` hides,
// because that runs as the admin and leaves a durable behind it.
func (h *aclHarness) seedQueue(t *testing.T, endpoint string) {
	t.Helper()
	if _, err := h.adminJS.AddStream(&natsgo.StreamConfig{
		Name:      presence.StreamName(endpoint),
		Subjects:  []string{"queue." + endpoint},
		Retention: natsgo.WorkQueuePolicy,
		Storage:   natsgo.MemoryStorage,
		Replicas:  1,
	}); err != nil {
		t.Fatalf("seed queue stream: %v", err)
	}
}

// post puts one envelope in the seat's queue as the admin, so nothing under
// test is used to set up what is under test.
func (h *aclHarness) post(t *testing.T, to, body string) {
	t.Helper()
	env := `{"id":"` + body + `","ts":"2026-01-14T09:00:00.000Z","from":"host","to":"` + to +
		`","kind":"msg","body":"` + body + `"}`
	if err := h.admin.Publish("queue."+to, []byte(env)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := h.admin.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

// A REFUSED CURSOR ON THE SEAT'S OWN QUEUE IS A FAILURE, NOT AN EMPTY MAILBOX.
//
// Under the shipped block the create is refused, the refusal arrives as
// silence, and the queue — which is holding mail — answers dry. A reader given
// that answer has been told its mailbox is empty when it is not, and the mail
// is still stored: the one state that is indistinguishable from loss.
func TestReadOfARefusedCursorOnTheSeatsOwnQueueIsNotDry(t *testing.T) {
	h := newACLHarness(t, shippedSeat(aclSeat))
	h.seedQueue(t, aclSeat)
	h.post(t, aclSeat, "shipped-acl-canary")

	t.Setenv("LOC_IDENTITY", aclSeat)
	p := &Provider{}
	t.Cleanup(p.Close)

	m, got, err := p.NextQueued(aclSeat, time.Second)
	if err == nil {
		t.Fatalf("a refused cursor read as an empty mailbox: got=%v msg=%v, want a failure", got, m)
	}
	for _, want := range []string{
		"cannot read queue." + aclSeat,
		"refused",
		"$JS.API.CONSUMER",
		presence.StreamName(aclSeat),
		"no cursor",
		"providers/nats/bootstrap.sh",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
	if got {
		t.Error("a refusal handed a message over as well")
	}
}

// A peek is the same fetch with the ack left undone, so it is refused the same
// way. A peek that answered "nothing here" would be the quieter half of the
// same lie.
func TestPeekOfARefusedCursorOnTheSeatsOwnQueueIsNotDry(t *testing.T) {
	h := newACLHarness(t, shippedSeat(aclSeat))
	h.seedQueue(t, aclSeat)
	h.post(t, aclSeat, "shipped-acl-canary")

	t.Setenv("LOC_IDENTITY", aclSeat)
	p := &Provider{}
	t.Cleanup(p.Close)

	if _, _, err := p.PeekQueued(aclSeat); err == nil {
		t.Fatal("a peek at a refused cursor read as an empty mailbox, want a failure")
	}
}

// The other side of the pair: with the grant the deployment is supposed to
// carry, the very same read hands the message over. Without this, the case
// above would pass on a build that failed every read.
func TestReadWithTheGrantedCursorOnTheSeatsOwnQueue(t *testing.T) {
	h := newACLHarness(t, grantedSeat(aclSeat))
	h.seedQueue(t, aclSeat)
	h.post(t, aclSeat, "granted-acl-canary")

	t.Setenv("LOC_IDENTITY", aclSeat)
	p := &Provider{}
	t.Cleanup(p.Close)

	m, got, err := p.NextQueued(aclSeat, 5*time.Second)
	if err != nil {
		t.Fatalf("a granted cursor was refused: %v", err)
	}
	if !got {
		t.Fatal("a granted cursor read an empty mailbox; the message is in the stream")
	}
	if !strings.Contains(string(m.Data()), "granted-acl-canary") {
		t.Errorf("got %q", string(m.Data()))
	}
	if err := m.Ack(); err != nil {
		t.Errorf("ack: %v", err)
	}
}

// A COLD ENDPOINT STAYS DRY. There is no stream, so nothing was refused and
// there is no mail to be wrong about — the reading is silence and a zero exit,
// exactly as before. The failure above must be the medium saying NO, never the
// medium saying NOTHING.
func TestReadOnAnEndpointWithNoStreamStaysDry(t *testing.T) {
	newACLHarness(t, grantedSeat(aclSeat))

	t.Setenv("LOC_IDENTITY", aclSeat)
	p := &Provider{}
	t.Cleanup(p.Close)

	m, got, err := p.NextQueued(aclSeat, time.Second)
	if err != nil {
		t.Fatalf("a cold endpoint failed instead of reading empty: %v", err)
	}
	if got || m != nil {
		t.Errorf("a cold endpoint handed something over: %v", m)
	}
}
