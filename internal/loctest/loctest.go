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
// It is deployment-neutral on purpose. The caller supplies the user set (and
// therefore the ACLs), so the same boot serves the nats package's flat
// single-password users and the presence suite's per-role, per-endpoint
// access-control block.
//
// V0 HAS NO AUTHENTICATION, so `loc` connects with no user name. The caller
// names one user that an unauthenticated connection is mapped to, which is
// how a test still asks what the tool does against a broker that refuses it
// something: the user set carries the permissions, and the tool arrives as
// that user without holding a credential.
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

// Boot starts an in-process nats-server with JetStream, bound to loopback on a
// kernel-chosen port, authorising exactly the supplied users. asUser names the
// one of them an unauthenticated client is taken to be; an empty asUser with a
// non-empty user set refuses every connection loc makes, which no test wants.
// When monitor is true it also opens a kernel-chosen HTTP monitoring port and
// records its URL.
//
// Storage is on disk in a temp dir the test framework removes; the retention
// semantics under test are identical to a real deployment's, and nothing
// survives the test.
func Boot(t *testing.T, users []*natsserver.User, asUser string, monitor bool) *Server {
	t.Helper()

	opts := &natsserver.Options{
		Host:       "127.0.0.1",
		Port:       -1, // ephemeral: the kernel picks, we ask afterwards
		JetStream:  true,
		StoreDir:   t.TempDir(),
		NoLog:      true,
		NoSigs:     true,
		Users:      users,
		NoAuthUser: asUser,
	}
	if monitor {
		opts.HTTPHost = "127.0.0.1"
		opts.HTTPPort = -1 // ephemeral, like the client port
	}

	srv, err := natsserver.NewServer(opts)
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
