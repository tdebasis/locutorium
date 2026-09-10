package nats

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/loc"
	"github.com/tdebasis/locutorium/internal/loctest"
	"github.com/tdebasis/locutorium/internal/presence"
	"github.com/tdebasis/locutorium/internal/provider"
)

// The whole file talks to a server this process starts, on a port the kernel
// picks, bound to loopback only. It never reaches the operator's own
// deployment: LOC_HOME is a scratch directory for every test, so the default
// nats_url (127.0.0.1:4222) is never the one that gets used, and the
// credentials are ones written here.

const testPassword = "conformance-scratch"

// topicWindow mirrors the shell adapter's default `topic_window` of 7d.
const topicWindow = 7 * 24 * time.Hour

// harness is one scratch deployment: a server, a $LOC_HOME, and the streams
// the code expects to find. The shapes come from the NATS provider's
// `provider_doctor --init`:
//
//	QUEUE_<e>  subjects queue.<e>, work-queue retention, one durable pull
//	           consumer named <e>, deliver-all, explicit ack
//	TOPICS     subjects topic.>, limits retention, max-age = the window
//
// Storage is memory rather than file: the retention semantics under test are
// identical, and nothing survives the test that way.
type harness struct {
	srv       *loctest.Server
	home      string
	url       string
	endpoints []string
}

func newHarness(t *testing.T, endpoints ...string) *harness {
	t.Helper()

	home := t.TempDir()
	users := append([]string{"admin"}, endpoints...)

	// The server boot, the admin observer, closedPort and write are shared with
	// the CLI-level presence suite through internal/loctest; the stream shapes
	// and the flat single-password user set are this package's own.
	uu := make([]*natsserver.User, 0, len(users))
	for _, u := range users {
		uu = append(uu, &natsserver.User{Username: u, Password: testPassword})
	}
	srv := loctest.Boot(t, uu, false)

	h := &harness{srv: srv, home: home, url: srv.URL, endpoints: endpoints}

	write(t, filepath.Join(home, "config"), "nats_url = "+h.url+"\n")
	write(t, filepath.Join(home, "endpoints"), strings.Join(endpoints, "\n")+"\n")
	seedLedger(t, home, endpoints...)
	for _, u := range users {
		write(t, filepath.Join(home, "creds", u), testPassword)
	}
	t.Setenv("LOC_HOME", home)

	h.initStreams(t)
	return h
}

// initStreams is the Go equivalent of `loc doctor --init`.
func (h *harness) initStreams(t *testing.T) {
	t.Helper()
	nc, js := h.admin(t)
	defer nc.Close()

	for _, e := range h.endpoints {
		if _, err := js.AddStream(&natsgo.StreamConfig{
			Name:      "QUEUE_" + e,
			Subjects:  []string{"queue." + e},
			Retention: natsgo.WorkQueuePolicy,
			Storage:   natsgo.MemoryStorage,
			Replicas:  1,
		}); err != nil {
			t.Fatalf("add stream QUEUE_%s: %v", e, err)
		}
		if _, err := js.AddConsumer("QUEUE_"+e, &natsgo.ConsumerConfig{
			Durable:       e,
			DeliverPolicy: natsgo.DeliverAllPolicy,
			AckPolicy:     natsgo.AckExplicitPolicy,
		}); err != nil {
			t.Fatalf("add consumer %s: %v", e, err)
		}
	}
	if _, err := js.AddStream(&natsgo.StreamConfig{
		Name:      "TOPICS",
		Subjects:  []string{"topic.>"},
		Retention: natsgo.LimitsPolicy,
		MaxAge:    topicWindow,
		Storage:   natsgo.MemoryStorage,
		Replicas:  1,
	}); err != nil {
		t.Fatalf("add stream TOPICS: %v", err)
	}
}

// admin opens a second connection for the test's own assertions, so nothing
// here reaches through the provider it is checking.
func (h *harness) admin(t *testing.T) (*natsgo.Conn, natsgo.JetStreamContext) {
	t.Helper()
	return h.srv.Admin(t, "admin", testPassword)
}

// as returns a connected provider speaking as id, closed at test end.
func (h *harness) as(t *testing.T, id string) *Provider {
	t.Helper()
	t.Setenv("LOC_IDENTITY", id)
	p := &Provider{}
	t.Cleanup(p.Close)
	return p
}

func write(t *testing.T, path, content string) {
	t.Helper()
	loctest.Write(t, path, content)
}

// closedPort returns a loopback address nothing is listening on. Used for the
// unreachable-medium cases, so they never hit a real server by accident.
func closedPort(t *testing.T) string {
	t.Helper()
	return loctest.ClosedPort(t)
}

// ---------------------------------------------------------------- New/Close

func TestNewReturnsUnconnectedProvider(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	np, ok := p.(*Provider)
	if !ok {
		t.Fatalf("New returned %T, want *Provider", p)
	}
	if np.nc != nil || np.js != nil {
		t.Error("New opened a connection; it must not connect until asked")
	}
	// Closing something that never connected is a no-op, not a panic: the CLI
	// defers Close on every path, including the ones that failed early.
	p.Close()
	p.Close()
}

func TestCloseReleasesTheConnection(t *testing.T) {
	h := newHarness(t, "ada")
	p := h.as(t, "ada")
	if err := p.connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if p.nc == nil {
		t.Fatal("connect left no connection")
	}
	p.Close()
	if p.nc != nil || p.js != nil {
		t.Error("Close left the connection fields set")
	}
}

func TestRegisteredUnderItsName(t *testing.T) {
	// The init function in this package is what makes `provider = nats` mean
	// anything; without it the binary would build and then refuse every verb.
	p, err := provider.Open("nats")
	if err != nil {
		t.Fatalf("Open(nats): %v", err)
	}
	if _, ok := p.(*Provider); !ok {
		t.Errorf("Open(nats) returned %T", p)
	}
	p.Close()
}

// ----------------------------------------------------------------- connect

func TestConnectIsIdempotent(t *testing.T) {
	h := newHarness(t, "ada")
	p := h.as(t, "ada")
	if err := p.connect(); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	first := p.nc
	if err := p.connect(); err != nil {
		t.Fatalf("second connect: %v", err)
	}
	if p.nc != first {
		t.Error("connect opened a second connection instead of reusing the first")
	}
}

func TestConnectWithoutCredentialsNamesThePathNotTheSecret(t *testing.T) {
	h := newHarness(t, "ada")
	p := h.as(t, "ada")
	credfile := filepath.Join(h.home, "creds", "ada")
	if err := os.Remove(credfile); err != nil {
		t.Fatalf("remove creds: %v", err)
	}

	err := p.connect()
	if err == nil {
		t.Fatal("connect succeeded with no credentials")
	}
	want := "no credentials for 'ada' at " + credfile
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Error("the error carries the credential content")
	}
}

func TestConnectWithoutIdentityRefuses(t *testing.T) {
	newHarness(t, "ada")
	t.Setenv("LOC_IDENTITY", "")
	p := &Provider{}
	t.Cleanup(p.Close)

	err := p.connect()
	if err == nil {
		t.Fatal("connect succeeded with no identity")
	}
	if !strings.Contains(err.Error(), "cannot determine sender identity") {
		t.Errorf("got %q, want the unattributable-caller refusal", err.Error())
	}
}

func TestConnectToAnUnreachableServer(t *testing.T) {
	h := newHarness(t, "ada")
	write(t, filepath.Join(h.home, "config"), "nats_url = "+closedPort(t)+"\n")
	p := h.as(t, "ada")

	err := p.connect()
	if err != errConnect {
		t.Fatalf("got %v, want the errConnect sentinel", err)
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Error("the error carries the credential content")
	}
}

func TestConnectRejectsWrongCredentials(t *testing.T) {
	h := newHarness(t, "ada")
	write(t, filepath.Join(h.home, "creds", "ada"), "not-the-password")
	p := h.as(t, "ada")

	if err := p.connect(); err != errConnect {
		t.Fatalf("got %v, want the errConnect sentinel", err)
	}
}

func TestCredentialsAreReadWithoutTheirTrailingNewline(t *testing.T) {
	// The shell wrote these with printf and read them with $(cat), which drops
	// trailing newlines. A hand-edited file has one; it must still work.
	h := newHarness(t, "ada")
	write(t, filepath.Join(h.home, "creds", "ada"), testPassword+"\r\n")
	p := h.as(t, "ada")

	if err := p.connect(); err != nil {
		t.Fatalf("connect with a newline-terminated credential file: %v", err)
	}
}

// --------------------------------------------------------------- SendQueue

func TestSendQueueDelivers(t *testing.T) {
	h := newHarness(t, "ada", "bob")
	p := h.as(t, "ada")

	env := envelope(t, "ada", "bob", "message-one")
	if err := p.SendQueue("bob", env); err != nil {
		t.Fatalf("SendQueue: %v", err)
	}

	nc, js := h.admin(t)
	defer nc.Close()
	info, err := js.StreamInfo("QUEUE_bob")
	if err != nil {
		t.Fatalf("StreamInfo: %v", err)
	}
	if info.State.Msgs != 1 {
		t.Fatalf("QUEUE_bob holds %d messages, want 1", info.State.Msgs)
	}
	// A returned nil is the code's own statement that the acknowledgement
	// carried a sequence; the stream's first sequence is where that number
	// came from.
	if info.State.FirstSeq != 1 {
		t.Errorf("first stored sequence is %d, want 1", info.State.FirstSeq)
	}
}

func TestSendQueueRoundTripIsByteIdentical(t *testing.T) {
	h := newHarness(t, "ada", "bob")
	p := h.as(t, "ada")

	env := envelope(t, "ada", "bob", "see <path> & mind the ampersand")
	if err := p.SendQueue("bob", env); err != nil {
		t.Fatalf("SendQueue: %v", err)
	}

	nc, js := h.admin(t)
	defer nc.Close()
	msg, err := js.GetMsg("QUEUE_bob", 1)
	if err != nil {
		t.Fatalf("GetMsg: %v", err)
	}
	if msg.Subject != "queue.bob" {
		t.Errorf("stored on %q, want queue.bob", msg.Subject)
	}
	if string(msg.Data) != string(env) {
		t.Errorf("stored bytes differ from what was sent:\n got %q\nwant %q", msg.Data, env)
	}
}

func TestSendQueueWithNoStoreListening(t *testing.T) {
	// queue.ghost is covered by no stream and nothing else answers, so there
	// is no acknowledgement to be had. The sender is told, rather than left
	// believing the message arrived.
	h := newHarness(t, "ada")
	p := h.as(t, "ada")

	err := p.SendQueue("ghost", envelope(t, "ada", "ghost", "into the void"))
	if err == nil {
		t.Fatal("SendQueue succeeded with nothing to acknowledge it")
	}
	want := "send failed: no acknowledgement from the store (is the server up? try: loc doctor)"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

func TestSendQueueToAnUnreachableServer(t *testing.T) {
	h := newHarness(t, "ada")
	write(t, filepath.Join(h.home, "config"), "nats_url = "+closedPort(t)+"\n")
	p := h.as(t, "ada")

	err := p.SendQueue("bob", envelope(t, "ada", "bob", "hello"))
	if err == nil || !strings.Contains(err.Error(), "no acknowledgement from the store") {
		t.Fatalf("got %v, want the unacknowledged-send error", err)
	}
}

func TestSendQueueWithoutCredentialsSurfacesThatError(t *testing.T) {
	// An unreachable medium and a missing credential are different problems.
	// The second is the one no retry fixes, so it must not be flattened into
	// "is the server up?".
	h := newHarness(t, "ada")
	if err := os.Remove(filepath.Join(h.home, "creds", "ada")); err != nil {
		t.Fatalf("remove creds: %v", err)
	}
	p := h.as(t, "ada")

	err := p.SendQueue("bob", envelope(t, "ada", "bob", "hello"))
	if err == nil || !strings.Contains(err.Error(), "no credentials for 'ada'") {
		t.Fatalf("got %v, want the missing-credentials error", err)
	}
}

func TestSendQueueRejectsAnAcknowledgementWithoutASequence(t *testing.T) {
	h := newHarness(t, "ada")
	nc, _ := h.admin(t)
	defer nc.Close()
	// Something answers on the subject, but it is not the store.
	sub, err := nc.Subscribe("queue.impostor", func(m *natsgo.Msg) {
		_ = m.Respond([]byte(`{"error":"nope"}`))
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	p := h.as(t, "ada")
	err = p.SendQueue("impostor", envelope(t, "ada", "impostor", "hello"))
	if err == nil {
		t.Fatal("SendQueue accepted a reply that carried no sequence")
	}
	want := `send failed: unexpected acknowledgement: {"error":"nope"}`
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

// ------------------------------------------------------------ PublishTopic

func TestPublishTopicDelivers(t *testing.T) {
	h := newHarness(t, "ada")
	p := h.as(t, "ada")

	env := envelope(t, "ada", "#standup", "morning")
	if err := p.PublishTopic("standup", env); err != nil {
		t.Fatalf("PublishTopic: %v", err)
	}

	nc, js := h.admin(t)
	defer nc.Close()
	msg, err := js.GetLastMsg("TOPICS", "topic.standup")
	if err != nil {
		t.Fatalf("GetLastMsg: %v", err)
	}
	if msg.Subject != "topic.standup" {
		t.Errorf("stored on %q, want topic.standup", msg.Subject)
	}
	if string(msg.Data) != string(env) {
		t.Errorf("stored bytes differ from what was published:\n got %q\nwant %q", msg.Data, env)
	}
}

func TestPublishTopicWithNoStoreListening(t *testing.T) {
	h := newHarness(t, "ada")
	nc, js := h.admin(t)
	defer nc.Close()
	if err := js.DeleteStream("TOPICS"); err != nil {
		t.Fatalf("delete TOPICS: %v", err)
	}

	p := h.as(t, "ada")
	err := p.PublishTopic("standup", envelope(t, "ada", "#standup", "morning"))
	if err == nil {
		t.Fatal("PublishTopic succeeded with no TOPICS stream")
	}
	want := "publish failed: no acknowledgement from the store (is the server up? try: loc doctor)"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

// ------------------------------------------------------------------ Topics

func TestTopicsListsActiveSubjectsWithCounts(t *testing.T) {
	h := newHarness(t, "ada")
	p := h.as(t, "ada")

	for _, spec := range []struct {
		topic string
		n     int
	}{{"standup", 1}, {"release", 3}} {
		for i := 0; i < spec.n; i++ {
			if err := p.PublishTopic(spec.topic, envelope(t, "ada", "#"+spec.topic, fmt.Sprintf("line %d", i))); err != nil {
				t.Fatalf("PublishTopic %s: %v", spec.topic, err)
			}
		}
	}

	var out strings.Builder
	if err := p.Topics(&out); err != nil {
		t.Fatalf("Topics: %v", err)
	}
	// Sorted, one line each, in the shape the README publishes.
	want := "#release  (3 in window)\n#standup  (1 in window)\n"
	if out.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", out.String(), want)
	}
}

func TestTopicsWithNothingSaid(t *testing.T) {
	h := newHarness(t, "ada")
	p := h.as(t, "ada")

	var out strings.Builder
	if err := p.Topics(&out); err != nil {
		t.Fatalf("Topics: %v", err)
	}
	if out.String() != "(no active topics)\n" {
		t.Errorf("got %q, want %q", out.String(), "(no active topics)\n")
	}
}

func TestTopicsWithNoTopicsStreamReadsAsQuiet(t *testing.T) {
	// A missing stream is an operator's problem, and `loc doctor` is where it
	// gets diagnosed. `loc topics` answers a question about the conversation.
	h := newHarness(t, "ada")
	nc, js := h.admin(t)
	defer nc.Close()
	if err := js.DeleteStream("TOPICS"); err != nil {
		t.Fatalf("delete TOPICS: %v", err)
	}

	p := h.as(t, "ada")
	var out strings.Builder
	if err := p.Topics(&out); err != nil {
		t.Fatalf("Topics: %v", err)
	}
	if out.String() != "(no active topics)\n" {
		t.Errorf("got %q, want %q", out.String(), "(no active topics)\n")
	}
}

func TestTopicsWithAnUnreachableServerReadsAsQuiet(t *testing.T) {
	h := newHarness(t, "ada")
	write(t, filepath.Join(h.home, "config"), "nats_url = "+closedPort(t)+"\n")
	p := h.as(t, "ada")

	var out strings.Builder
	if err := p.Topics(&out); err != nil {
		t.Fatalf("Topics: %v", err)
	}
	if out.String() != "(no active topics)\n" {
		t.Errorf("got %q, want %q", out.String(), "(no active topics)\n")
	}
}

// FINDING, recorded as it stands rather than fixed here.
//
// connect returns the same errConnect sentinel for two different problems: a
// server that is not there, and credentials the server REJECTED. Topics and
// Status both treat that sentinel as "the medium is down" and answer anyway —
// so an endpoint whose password is wrong is told the room is empty and every
// queue is unreadable ("?"), rather than being told its credentials were
// refused. A MISSING credentials file is refused outright (the test above);
// a WRONG one is not, and it is just as unfixable by retrying.
//
// These two tests pin the current behaviour so that changing it is a decision
// somebody makes, not a diff nobody notices.
func TestTopicsWithRejectedCredentialsReadsAsQuiet(t *testing.T) {
	h := newHarness(t, "ada")
	write(t, filepath.Join(h.home, "creds", "ada"), "not-the-password")
	p := h.as(t, "ada")

	var out strings.Builder
	if err := p.Topics(&out); err != nil {
		t.Fatalf("Topics: %v", err)
	}
	if out.String() != "(no active topics)\n" {
		t.Errorf("got %q, want %q", out.String(), "(no active topics)\n")
	}
}

func TestStatusWithRejectedCredentialsPrintsQuestionMarks(t *testing.T) {
	h := newHarness(t, "ada", "bob")
	write(t, filepath.Join(h.home, "creds", "ada"), "not-the-password")
	p := h.as(t, "ada")

	var out strings.Builder
	if err := p.Status(&out); err != nil {
		t.Fatalf("Status: %v", err)
	}
	want := "ada          unread: ?\nbob          unread: ?\n"
	if out.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", out.String(), want)
	}
}

func TestTopicsRefusesAnUnattributableCallerBeforePrinting(t *testing.T) {
	h := newHarness(t, "ada")
	if err := os.Remove(filepath.Join(h.home, "creds", "ada")); err != nil {
		t.Fatalf("remove creds: %v", err)
	}
	p := h.as(t, "ada")

	var out strings.Builder
	err := p.Topics(&out)
	if err == nil {
		t.Fatal("Topics printed a room it could not see into")
	}
	if !strings.Contains(err.Error(), "no credentials for 'ada'") {
		t.Errorf("got %q, want the missing-credentials error", err.Error())
	}
	if out.String() != "" {
		t.Errorf("Topics wrote %q before failing", out.String())
	}
}

func TestTopicSubjectsIgnoresNonTopicSubjects(t *testing.T) {
	// The filter is `topic.>` and the prefix check is belt-and-braces; both
	// are exercised here so that neither can quietly stop mattering.
	h := newHarness(t, "ada", "bob")
	p := h.as(t, "ada")

	if err := p.SendQueue("bob", envelope(t, "ada", "bob", "not a topic")); err != nil {
		t.Fatalf("SendQueue: %v", err)
	}
	if err := p.PublishTopic("standup", envelope(t, "ada", "#standup", "a topic")); err != nil {
		t.Fatalf("PublishTopic: %v", err)
	}

	subjects := p.topicSubjects()
	if len(subjects) != 1 {
		t.Fatalf("topicSubjects returned %v, want exactly topic.standup", subjects)
	}
	if n, ok := subjects["topic.standup"]; !ok || n != 1 {
		t.Errorf("topicSubjects returned %v, want topic.standup: 1", subjects)
	}
}

func TestTopicSubjectsWithoutAConnection(t *testing.T) {
	h := newHarness(t, "ada")
	write(t, filepath.Join(h.home, "config"), "nats_url = "+closedPort(t)+"\n")
	p := h.as(t, "ada")

	if got := p.topicSubjects(); got != nil {
		t.Errorf("got %v, want nil when the medium cannot be reached", got)
	}
}

// ------------------------------------------------------------------ Status

func TestStatusCountsPendingPerEndpoint(t *testing.T) {
	h := newHarness(t, "ada", "bob", "carol")
	p := h.as(t, "ada")

	for i := 0; i < 2; i++ {
		if err := p.SendQueue("bob", envelope(t, "ada", "bob", fmt.Sprintf("m%d", i))); err != nil {
			t.Fatalf("SendQueue: %v", err)
		}
	}

	var out strings.Builder
	if err := p.Status(&out); err != nil {
		t.Fatalf("Status: %v", err)
	}
	want := "ada          unread: 0\nbob          unread: 2\ncarol        unread: 0\n"
	if out.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", out.String(), want)
	}
}

// seedLedger writes one registration per endpoint, which is what a subscribe
// would have left behind. STATUS READS THE LEDGER, not the `endpoints` file
// (#27): the roster is the record of who subscribed, so a harness that names
// endpoints has to leave that record.
func seedLedger(t *testing.T, home string, endpoints ...string) {
	t.Helper()
	dir := filepath.Join(home, "run", "presence")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("make the ledger dir: %v", err)
	}
	for _, e := range endpoints {
		row := fmt.Sprintf(`{"endpoint":%q,"instance":%q,"process":{"pid":%d,"started":""},"registered":"2026-01-14T09:12:04.318Z"}`,
			e, presence.Instance(e), os.Getpid())
		write(t, filepath.Join(dir, e+".json"), row)
	}
}

// clearLedger empties the roster, which is what "nobody has subscribed" is.
func clearLedger(t *testing.T, home string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(home, "run", "presence")); err != nil {
		t.Fatalf("clear the ledger: %v", err)
	}
}

func TestStatusWithAnEmptyRegistryPrintsNothing(t *testing.T) {
	h := newHarness(t)
	write(t, filepath.Join(h.home, "endpoints"), "")
	clearLedger(t, h.home)
	p := h.as(t, "admin")

	var out strings.Builder
	if err := p.Status(&out); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if out.String() != "" {
		t.Errorf("got %q, want no lines", out.String())
	}
}

func TestStatusPrintsAQuestionMarkForACountItCannotGet(t *testing.T) {
	// A report that refuses to print because one number is missing is no
	// report. Both ways a number goes missing are covered: no consumer, and
	// no server.
	h := newHarness(t, "ada", "bob")
	nc, js := h.admin(t)
	defer nc.Close()
	if err := js.DeleteConsumer("QUEUE_bob", "bob"); err != nil {
		t.Fatalf("delete consumer: %v", err)
	}

	p := h.as(t, "ada")
	var out strings.Builder
	if err := p.Status(&out); err != nil {
		t.Fatalf("Status: %v", err)
	}
	want := "ada          unread: 0\nbob          unread: ?\n"
	if out.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", out.String(), want)
	}
}

func TestStatusWithAnUnreachableServerPrintsQuestionMarks(t *testing.T) {
	h := newHarness(t, "ada", "bob")
	write(t, filepath.Join(h.home, "config"), "nats_url = "+closedPort(t)+"\n")
	p := h.as(t, "ada")

	var out strings.Builder
	if err := p.Status(&out); err != nil {
		t.Fatalf("Status: %v", err)
	}
	want := "ada          unread: ?\nbob          unread: ?\n"
	if out.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", out.String(), want)
	}
}

func TestStatusRefusesAnUnattributableCaller(t *testing.T) {
	h := newHarness(t, "ada")
	if err := os.Remove(filepath.Join(h.home, "creds", "ada")); err != nil {
		t.Fatalf("remove creds: %v", err)
	}
	p := h.as(t, "ada")

	var out strings.Builder
	err := p.Status(&out)
	if err == nil {
		t.Fatal("Status reported on a deployment it could not authenticate to")
	}
	if !strings.Contains(err.Error(), "no credentials for 'ada'") {
		t.Errorf("got %q, want the missing-credentials error", err.Error())
	}
	if out.String() != "" {
		t.Errorf("Status wrote %q before failing", out.String())
	}
}

// failingWriter is a reader that goes away mid-render: the same shape as a
// `loc status | head -1`.
type failingWriter struct{ after int }

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.after <= 0 {
		return 0, fmt.Errorf("broken pipe")
	}
	w.after--
	return len(p), nil
}

func TestStatusStopsWhenTheReaderGoesAway(t *testing.T) {
	h := newHarness(t, "ada", "bob")
	p := h.as(t, "ada")

	if err := p.Status(&failingWriter{after: 1}); err == nil {
		t.Error("Status swallowed a write failure")
	}
}

func TestTopicsStopsWhenTheReaderGoesAway(t *testing.T) {
	h := newHarness(t, "ada")
	p := h.as(t, "ada")
	if err := p.PublishTopic("standup", envelope(t, "ada", "#standup", "morning")); err != nil {
		t.Fatalf("PublishTopic: %v", err)
	}

	if err := p.Topics(&failingWriter{after: 0}); err == nil {
		t.Error("Topics swallowed a write failure")
	}
}

// envelope builds the same bytes the CLI would put on the medium, so the
// round-trip checks compare against a real wire form and not a stand-in.
func envelope(t *testing.T, from, to, body string) []byte {
	t.Helper()
	b, err := loc.NewEnvelope(from, to, "msg", body).Marshal()
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return b
}
