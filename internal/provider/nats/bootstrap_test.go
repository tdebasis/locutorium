package nats

// THE SHIPPED SCRIPT'S OWN OUTPUT, BOOTED AND READ FROM.
//
// Every other case in this tree writes its access control in Go — which means
// the suite can be green while providers/nats/bootstrap.sh, the thing an
// operator actually runs, grants something else entirely. That is exactly the
// gap the refused-cursor defect lived in.
//
// So this one runs the script, boots the config it wrote, and reads a fresh
// namespaced queue through it. Nothing here touches the operator's own
// deployment: LOC_HOME is a temp dir, the server's port and monitoring port
// are overridden to a kernel-chosen one and to none, and the script installs
// nothing — it writes files and prints the steps an operator takes next.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/presence"
)

// bootstrapNeeds are the two tools the script shells out to: `openssl` for the
// passwords and `nats` for the bcrypt hashes it puts in the config. Neither is
// a Go dependency and neither is installed by this suite.
var bootstrapNeeds = []string{"nats", "openssl"}

// A NAMESPACED SEAT BOOTSTRAPPED BY THE SHIPPED SCRIPT CAN READ ITS OWN QUEUE.
//
// Fresh means what a presence-model subscribe leaves behind: a stream and NO
// consumer, so the reader's cursor is the reader's own to create. The seat is
// namespaced because that is where the two spellings diverge — the subject
// carries a dot, the backing object may not — and a grant written in the wrong
// one refuses the reader its own mail in silence.
func TestABootstrappedSeatReadsItsOwnFreshQueue(t *testing.T) {
	const seat = "workshop.scribe"
	home, _, anc, ajs := bootstrapHouse(t, seat)

	if _, err := ajs.AddStream(&natsgo.StreamConfig{
		Name:      presence.StreamName(seat),
		Subjects:  []string{"queue." + seat},
		Retention: natsgo.WorkQueuePolicy,
		Storage:   natsgo.MemoryStorage,
		Replicas:  1,
	}); err != nil {
		t.Fatalf("seed the seat's queue: %v", err)
	}
	const body = "bootstrapped-canary"
	env := `{"id":"` + body + `","ts":"2026-01-14T09:00:00.000Z","from":"admin","to":"` + seat +
		`","kind":"msg","body":"` + body + `"}`
	if err := anc.Publish("queue."+seat, []byte(env)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := anc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", seat)
	p := &Provider{}
	t.Cleanup(p.Close)

	m, got, err := p.NextQueued(seat, 5*time.Second)
	if err != nil {
		t.Fatalf("the bootstrapped seat could not read its own queue: %v", err)
	}
	if !got {
		t.Fatal("the bootstrapped seat read an empty mailbox; the message is in its stream")
	}
	if !strings.Contains(string(m.Data()), body) {
		t.Errorf("got %q", string(m.Data()))
	}
	// The acknowledgement is a grant of its own — the ack subject carries the
	// backing object's name, not the subject's — so the message must actually
	// leave the stream.
	if err := m.Ack(); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if err := waitForEmpty(t, ajs, presence.StreamName(seat)); err != nil {
		t.Error(err)
	}
}

// A BOOTSTRAPPED SEAT STANDS ITS OWN QUEUE UP AND TAKES IT DOWN AGAIN.
//
// This is the presence model's ordinary day, and it is exactly what the
// conformance suite's Go lane now does at setup: `subscribe` creates the
// endpoint's queue, mail arrives and is read, `unsubscribe` destroys it. What
// makes that possible is three grants on the seat's OWN backing object and
// nothing else — create, inspect, delete — which is what the presence suite's
// access-control harness has always given a seat and what the shipped script
// did not.
//
// Without them the create is REFUSED, and a refused JetStream request is
// answered with silence: the call waits out its timeout and the seat is told
// only that the medium did not reply. So a seat that cannot be granted this
// does not fail loudly at subscribe — it hangs, then reports a timeout, and
// the deployment looks broken rather than misconfigured.
func TestABootstrappedSeatCreatesReadsAndDestroysItsOwnQueue(t *testing.T) {
	const seat = "workshop.scribe"
	home, _, anc, ajs := bootstrapHouse(t, seat)

	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", seat)
	p := &Provider{}
	t.Cleanup(p.Close)

	// NOBODY IS ATTENDING A FRESHLY BOOTSTRAPPED DEPLOYMENT. The script writes
	// credentials and permissions; it creates no streams, so there is no
	// mailbox for an agent that has not arrived.
	attended, err := p.QueueExists(seat)
	if err != nil {
		t.Fatalf("the bootstrapped seat could not ask whether it is attended: %v", err)
	}
	if attended {
		t.Fatal("a freshly bootstrapped deployment already holds a queue for the seat")
	}

	// SUBSCRIBE: the seat creates its own queue.
	if err := p.CreateQueue(seat); err != nil {
		t.Fatalf("the bootstrapped seat could not create its own queue: %v", err)
	}
	if attended, err := p.QueueExists(seat); err != nil {
		t.Fatalf("the bootstrapped seat could not inspect the queue it just made: %v", err)
	} else if !attended {
		t.Fatal("the queue the seat created is not there")
	}

	// It is a real queue: mail put in it is read out of it. Seeded as the
	// admin, so what is asserted is not asserted through the code under test.
	const body = "subscribed-canary"
	env := `{"id":"` + body + `","ts":"2026-01-14T09:00:00.000Z","from":"admin","to":"` + seat +
		`","kind":"msg","body":"` + body + `"}`
	if err := anc.Publish("queue."+seat, []byte(env)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := anc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	m, got, err := p.NextQueued(seat, 5*time.Second)
	if err != nil {
		t.Fatalf("the bootstrapped seat could not read the queue it made: %v", err)
	}
	if !got {
		t.Fatal("the bootstrapped seat read an empty mailbox; the message is in its stream")
	}
	if !strings.Contains(string(m.Data()), body) {
		t.Errorf("got %q", string(m.Data()))
	}
	if err := m.Ack(); err != nil {
		t.Fatalf("ack: %v", err)
	}

	// UNSUBSCRIBE: the seat destroys it. Nothing accumulates for an agent that
	// is not running, so leaving the stream behind would not be a tidiness
	// failure — it would be a different model.
	if err := p.DeleteQueue(seat); err != nil {
		t.Fatalf("the bootstrapped seat could not destroy its own queue: %v", err)
	}
	if attended, err := p.QueueExists(seat); err != nil {
		t.Fatalf("the bootstrapped seat could not confirm the queue is gone: %v", err)
	} else if attended {
		t.Error("the queue survived the seat's departure")
	}
	// Asked again through the admin, so the answer does not come from the
	// build whose grants are the question.
	if _, err := ajs.StreamInfo(presence.StreamName(seat)); !errors.Is(err, natsgo.ErrStreamNotFound) {
		t.Errorf("the admin still finds %s: %v", presence.StreamName(seat), err)
	}
}

// bootstrapHouse runs the SHIPPED script, boots the configuration it wrote,
// and hands back the deployment plus an admin connection to it.
//
// Nothing here touches the operator's own deployment: LOC_HOME is a temp dir,
// the fixed client and monitoring ports are overridden to a kernel-chosen one
// and to none, and the script installs nothing — it writes files and prints
// the steps an operator takes next.
func bootstrapHouse(t *testing.T, seats ...string) (string, *natsserver.Server, *natsgo.Conn, natsgo.JetStreamContext) {
	t.Helper()
	for _, tool := range bootstrapNeeds {
		if _, err := exec.LookPath(tool); err != nil {
			// Skipped BY NAME, not silently: the case runs where the script's
			// own tools are (a developer machine, the macOS lane) and is
			// skipped where they are not (the ubuntu lane).
			t.Skipf("bootstrap.sh needs %s(1) on PATH; not here", tool)
		}
	}
	home := t.TempDir()

	script, err := filepath.Abs(filepath.Join("..", "..", "..", "providers", "nats", "bootstrap.sh"))
	if err != nil {
		t.Fatalf("locate bootstrap.sh: %v", err)
	}
	cmd := exec.Command("bash", append([]string{script}, seats...)...)
	cmd.Env = append(os.Environ(), "LOC_HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bootstrap.sh: %v\n%s", err, out)
	}

	// The config the script wrote, parsed by the server's own parser — so what
	// boots here is the operator's file and not a paraphrase of it. Only the
	// two things that would reach beyond this test are overridden: the fixed
	// client port and the fixed monitoring port.
	opts, err := natsserver.ProcessConfigFile(filepath.Join(home, "nats-server.conf"))
	if err != nil {
		t.Fatalf("parse the generated config: %v", err)
	}
	opts.Port = -1 // kernel-chosen, never the deployment's 4222
	opts.HTTPPort = 0
	opts.HTTPHost = ""
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	opts.NoLog, opts.NoSigs = true, true

	srv, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("start the bootstrapped server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		srv.Shutdown()
		t.Fatal("the bootstrapped server did not become ready")
	}
	t.Cleanup(func() { srv.Shutdown(); srv.WaitForShutdown() })

	// The seat reaches the scratch server, not the deployment's fixed URL.
	cfg := filepath.Join(home, "config")
	conf, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("read the generated config: %v", err)
	}
	if !strings.Contains(string(conf), "nats://127.0.0.1:4222") {
		t.Fatalf("the generated config no longer names the deployment url:\n%s", conf)
	}
	if err := os.WriteFile(cfg,
		[]byte(strings.Replace(string(conf), "nats://127.0.0.1:4222", srv.ClientURL(), 1)), 0o600); err != nil {
		t.Fatalf("point the config at the scratch server: %v", err)
	}

	// Connected as the admin the script generated, through its own credential
	// file — so a seeding write and a final check are made outside the code
	// under test.
	anc, err := natsgo.Connect(srv.ClientURL(), natsgo.UserInfo("admin", credential(t, home, "admin")),
		natsgo.Timeout(10*time.Second))
	if err != nil {
		t.Fatalf("connect as the generated admin: %v", err)
	}
	t.Cleanup(anc.Close)
	ajs, err := anc.JetStream()
	if err != nil {
		t.Fatalf("admin jetstream: %v", err)
	}
	return home, srv, anc, ajs
}

// credential reads one generated credential file. Its CONTENT never reaches an
// error message or the test log.
func credential(t *testing.T, home, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "creds", name))
	if err != nil {
		t.Fatalf("read the generated credential for %s: %v", name, err)
	}
	return strings.TrimRight(string(b), "\r\n")
}

// waitForEmpty gives the acknowledgement — which is a publish, and fire and
// forget — a moment to be applied before the count is believed.
func waitForEmpty(t *testing.T, js natsgo.JetStreamContext, stream string) error {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last uint64
	for time.Now().Before(deadline) {
		info, err := js.StreamInfo(stream)
		if err != nil {
			return err
		}
		if info.State.Msgs == 0 {
			return nil
		}
		last = info.State.Msgs
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("the acknowledgement did not delete the message: %s still holds %d "+
		"— the seat's ack subject is refused", stream, last)
}

// ------------------------------------------------------------- the supervisor
//
// THE SWEEP'S IDENTITY, GRANTED FROM THE SHIPPED SCRIPT'S OWN OUTPUT.
//
// `loc sweep` reaps a dead seat's registration, and reaping it means deleting
// that seat's queue and remaking it. A seat may delete only its own queue, so
// the sweep cannot run as a seat. Running it as the admin would hand a
// permanent timer the whole broker. So the script generates a third identity
// that may reap any seat's queue and may not touch the topic store or write
// anybody's mail. These cases boot the generated configuration and read that
// boundary from both sides.

// acl is one connection made as a generated user, plus the refusals the server
// reported on it.
//
// A JetStream request the deployment refuses is answered with an error line on
// the CONNECTION and nothing at all on the reply subject, so this handler is
// the only place a refusal can be read. A core publish is refused the same way.
type acl struct {
	nc *natsgo.Conn
	js natsgo.JetStreamContext

	mu  sync.Mutex
	bad []string
}

func (a *acl) refusals() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.bad...)
}

// connectAs opens one connection as a generated user, through that user's own
// credential file. The credential's CONTENT never reaches a log or an error.
func connectAs(t *testing.T, srv *natsserver.Server, home, user string) *acl {
	t.Helper()
	a := &acl{}
	nc, err := natsgo.Connect(srv.ClientURL(),
		natsgo.UserInfo(user, credential(t, home, user)),
		natsgo.Timeout(10*time.Second),
		natsgo.NoReconnect(),
		natsgo.ErrorHandler(func(_ *natsgo.Conn, _ *natsgo.Subscription, err error) {
			if err == nil || !strings.Contains(err.Error(), "Permissions Violation") {
				return
			}
			a.mu.Lock()
			a.bad = append(a.bad, err.Error())
			a.mu.Unlock()
		}),
	)
	if err != nil {
		t.Fatalf("connect as the generated %s: %v", user, err)
	}
	t.Cleanup(nc.Close)
	// THE WAIT IS BOUNDED HERE. A refused request never answers, so the
	// default wait would turn every refusal in these cases into a two-minute
	// hang rather than a result.
	js, err := nc.JetStream(natsgo.MaxWait(3 * time.Second))
	if err != nil {
		t.Fatalf("%s jetstream: %v", user, err)
	}
	a.nc, a.js = nc, js
	return a
}

// sawRefusalOf reports whether the server refused something naming subject,
// waiting a moment because a refusal arrives asynchronously.
func sawRefusalOf(a *acl, subject string) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range a.refusals() {
			if strings.Contains(line, subject) {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// THE SUPERVISOR REAPS AND REMAKES ANY SEAT'S QUEUE.
//
// The queue is made BY THE SEAT, through the shipped grants, so what the
// supervisor finds is a real seat's queue and not a fixture the admin placed.
func TestSupervisorCanListAndDeleteAnySeatsQueue(t *testing.T) {
	const seat = "workshop.scribe"
	home, srv, _, ajs := bootstrapHouse(t, seat)

	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", seat)
	p := &Provider{}
	t.Cleanup(p.Close)
	if err := p.CreateQueue(seat); err != nil {
		t.Fatalf("the seat could not create its own queue: %v", err)
	}

	sup := connectAs(t, srv, home, "supervisor")

	var listed []string
	found := false
	for n := range sup.js.StreamNames() {
		listed = append(listed, n)
		if n == presence.StreamName(seat) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the supervisor listed %v; %s is not among them (refusals: %v)",
			listed, presence.StreamName(seat), sup.refusals())
	}

	if err := sup.js.DeleteStream(presence.StreamName(seat)); err != nil {
		t.Fatalf("the supervisor could not delete the seat's queue: %v (refusals: %v)", err, sup.refusals())
	}
	// Asked through the admin, so the answer does not come from the
	// connection whose grants are the question.
	if _, err := ajs.StreamInfo(presence.StreamName(seat)); !errors.Is(err, natsgo.ErrStreamNotFound) {
		t.Errorf("the admin still finds %s: %v", presence.StreamName(seat), err)
	}

	if _, err := sup.js.AddStream(queueConfig(seat)); err != nil {
		t.Fatalf("the supervisor could not remake the seat's queue: %v (refusals: %v)", err, sup.refusals())
	}
	if _, err := ajs.StreamInfo(presence.StreamName(seat)); err != nil {
		t.Errorf("the queue the supervisor remade is not there: %v", err)
	}
}

// THE SUPERVISOR MAY NOT TOUCH THE TOPIC STORE.
//
// The rooms are the house's history. A timer that reaps mailboxes has no
// business deleting or standing up the one stream everybody reads from.
func TestSupervisorCannotTouchTheTopicStore(t *testing.T) {
	const seat = "workshop.scribe"
	home, srv, _, ajs := bootstrapHouse(t, seat)

	topics := &natsgo.StreamConfig{
		Name:     "TOPICS",
		Subjects: []string{"topic.>"},
		Storage:  natsgo.MemoryStorage,
		Replicas: 1,
	}
	if _, err := ajs.AddStream(topics); err != nil {
		t.Fatalf("seed the topic store: %v", err)
	}

	sup := connectAs(t, srv, home, "supervisor")
	if err := sup.js.DeleteStream("TOPICS"); err == nil {
		t.Error("the supervisor deleted the topic store")
	}
	if _, err := ajs.StreamInfo("TOPICS"); err != nil {
		t.Fatalf("the topic store did not survive the supervisor's delete: %v", err)
	}

	// The same boundary from the other side: it may not stand the store up
	// either, so a deployment cannot acquire a TOPICS of the timer's shape.
	if err := ajs.DeleteStream("TOPICS"); err != nil {
		t.Fatalf("clear the topic store: %v", err)
	}
	if _, err := sup.js.AddStream(topics); err == nil {
		t.Error("the supervisor created the topic store")
	}
	if _, err := ajs.StreamInfo("TOPICS"); !errors.Is(err, natsgo.ErrStreamNotFound) {
		t.Errorf("a TOPICS stream exists after the supervisor's create: %v", err)
	}
}

// THE SUPERVISOR MAY NOT WRITE ANYBODY'S MAIL.
//
// It reaps a seat's mailbox; it never speaks into one. A publish it makes to a
// seat's queue subject is refused by the deployment.
func TestSupervisorCannotPublishMail(t *testing.T) {
	const seat = "workshop.scribe"
	home, srv, _, _ := bootstrapHouse(t, seat)

	sup := connectAs(t, srv, home, "supervisor")
	// The client accepts the publish; the SERVER refuses it, and says so on
	// the connection rather than in the return of this call.
	if err := sup.nc.Publish("queue."+seat, []byte(`{"body":"not the supervisor's to send"}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := sup.nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if !sawRefusalOf(sup, "queue."+seat) {
		t.Errorf("the deployment accepted a supervisor's publish to queue.%s; refusals seen: %v",
			seat, sup.refusals())
	}
}

// EVERY GENERATED CREDENTIAL, BY NAME AND BY MODE.
//
// The set is asserted in both directions: a name that is missing is a broken
// deployment, and a name nobody asked for is a credential the operator did not
// know they had.
func TestBootstrapGeneratesEveryCredentialAt0600(t *testing.T) {
	const seat = "workshop.scribe"
	home, _, _, _ := bootstrapHouse(t, seat)

	want := map[string]bool{seat: false, "admin": false, "watch": false, "supervisor": false}
	entries, err := os.ReadDir(filepath.Join(home, "creds"))
	if err != nil {
		t.Fatalf("read the generated creds directory: %v", err)
	}
	for _, e := range entries {
		if _, ok := want[e.Name()]; !ok {
			t.Errorf("the script generated a credential nobody asked for: %s", e.Name())
			continue
		}
		want[e.Name()] = true
		fi, err := e.Info()
		if err != nil {
			t.Errorf("stat creds/%s: %v", e.Name(), err)
			continue
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("creds/%s is mode %04o, want 0600", e.Name(), perm)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("the script generated no credential for %s", name)
		}
	}
}
