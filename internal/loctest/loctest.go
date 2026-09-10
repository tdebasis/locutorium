// Package loctest is the shared support for the integration tests that drive
// `loc` end-to-end against a real broker.
//
// It exists because two test packages need the same three things and neither
// can borrow them from the other: an in-process nats-server with JetStream on
// loopback, a scratch $LOC_HOME the tool reads its deployment from, and an
// INDEPENDENT admin connection the test asserts effects through — so nothing a
// test checks is reported by the very code it is checking.
//
//   - internal/provider/nats/*_test.go drives the provider directly.
//   - cmd/loc/*_test.go drives the CLI's run() against `provider = nats`.
//
// A helper shared across packages cannot live in a *_test.go file, so this is
// an ordinary package that imports testing. Nothing outside a test imports it,
// so the shipped binary never grows by it; `go build ./...` only compiles it.
//
// THE HARNESS BOOTS THE PRODUCT'S SERVER. Its options come from
// internal/broker, the same builder cmd/loc/daemon.go calls, so a provider
// suite that passes here has been asked its questions of the server `loc start`
// actually runs. V0 puts no authentication on the loopback listener (R12,
// 2026-09-09), so the default boot authorises nobody and refuses nothing.
//
// WithRefusals is the opt-in for the other shape: a user set, and one user an
// unauthenticated client is mapped to. It exists ONLY so a test can manufacture
// a refusal and ask what the client does with one. It is NOT the product's
// shape, and a test that does not assert refusal handling must not use it.
package loctest

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/broker"
)

// Server is one scratch broker: a running nats-server and the URLs a client
// and a monitor reach it on. It is shut down when the test ends.
type Server struct {
	srv *natsserver.Server
	// URL is the client URL (nats://127.0.0.1:<ephemeral>).
	URL string
	// MonitorURL is the HTTP monitoring endpoint, set only when Boot was asked
	// for one; empty otherwise. The presence model's registry/status read
	// connection state from here.
	MonitorURL string
}

// Option changes the scratch server away from the product's shape.
type Option func(*natsserver.Options)

// WithRefusals boots an AUTHENTICATED server: users carrying the permissions
// the case needs, and asUser naming the one an unauthenticated client is taken
// to be.
//
// THE PRODUCT DOES NOT BOOT THIS. V0 has no authorization block at all, so this
// shape exists for one purpose: to make the medium answer NO, so a test can ask
// what the client does with a refusal. Use it only in a case that asserts
// refusal handling.
func WithRefusals(users []*natsserver.User, asUser string) Option {
	return func(o *natsserver.Options) {
		o.Users = users
		o.NoAuthUser = asUser
	}
}

// WithMonitor opens a kernel-chosen HTTP monitoring port and records its URL.
func WithMonitor() Option {
	return func(o *natsserver.Options) {
		o.HTTPHost = "127.0.0.1"
		o.HTTPPort = -1 // ephemeral, like the client port
	}
}

// Boot starts the product's in-process broker, bound to loopback on a
// kernel-chosen port, with a store the test framework removes.
//
// Storage is on disk; the retention semantics under test are identical to a
// real deployment's, and nothing survives the test.
func Boot(t *testing.T, opts ...Option) *Server {
	t.Helper()

	o := broker.Options("127.0.0.1", -1 /* ephemeral: the kernel picks */, t.TempDir())
	for _, opt := range opts {
		opt(o)
	}
	monitor := o.HTTPPort != 0

	srv, err := natsserver.NewServer(o)
	if err != nil {
		t.Fatalf("start scratch server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		srv.Shutdown()
		t.Fatal("scratch server did not become ready")
	}
	t.Cleanup(func() {
		srv.Shutdown()
		srv.WaitForShutdown()
	})

	s := &Server{srv: srv, URL: srv.ClientURL()}
	if monitor {
		if addr := srv.MonitorAddr(); addr != nil {
			s.MonitorURL = fmt.Sprintf("http://127.0.0.1:%d", addr.Port)
		}
	}
	return s
}

// Admin opens a second connection, as user, for the test's own assertions —
// stream existence, message counts, a live subscription witnessing events —
// so nothing here reaches through the provider it is checking. The caller
// closes it.
func (s *Server) Admin(t *testing.T, user, password string) (*natsgo.Conn, natsgo.JetStreamContext) {
	t.Helper()
	nc, err := natsgo.Connect(s.URL, natsgo.UserInfo(user, password), natsgo.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("admin connect as %q: %v", user, err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		t.Fatalf("admin jetstream: %v", err)
	}
	return nc, js
}

// ClosedPort returns a loopback nats URL nothing is listening on, for the
// unreachable-medium cases — so they never reach a real server by accident.
func ClosedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return "nats://" + addr
}

// Write writes content to path, creating parent directories, at the modes the
// deployment uses: 0700 directories, 0600 files.
func Write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
