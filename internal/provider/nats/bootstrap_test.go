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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	for _, tool := range bootstrapNeeds {
		if _, err := exec.LookPath(tool); err != nil {
			// Skipped BY NAME, not silently: the case runs where the script's
			// own tools are (a developer machine, the macOS lane) and is
			// skipped where they are not (the ubuntu lane).
			t.Skipf("bootstrap.sh needs %s(1) on PATH; not here", tool)
		}
	}

	const seat = "workshop.scribe"
	home := t.TempDir()

	script, err := filepath.Abs(filepath.Join("..", "..", "..", "providers", "nats", "bootstrap.sh"))
	if err != nil {
		t.Fatalf("locate bootstrap.sh: %v", err)
	}
	cmd := exec.Command("bash", script, seat)
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

	// Seeded as the admin the script generated, through its own credential
	// file — so the assertion is not made through the code under test.
	adminPass := credential(t, home, "admin")
	anc, err := natsgo.Connect(srv.ClientURL(), natsgo.UserInfo("admin", adminPass), natsgo.Timeout(10*time.Second))
	if err != nil {
		t.Fatalf("connect as the generated admin: %v", err)
	}
	defer anc.Close()
	ajs, err := anc.JetStream()
	if err != nil {
		t.Fatalf("admin jetstream: %v", err)
	}
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
