// Package mcpserve is a seat's own MCP server: the one process an agent
// runtime launches for that seat, and therefore the one process that holds the
// seat's process id.
//
// THE HOST IN MINIATURE. docs/PRESENCE.md §The host launches: whoever launches
// an agent is the only party that receives its process id for free, which is
// what makes registration mechanical rather than remembered. A stdio MCP
// server is launched by the runtime with the session and dies with it, so it
// has that same shape at seat scale — it registers the seat with the runtime's
// own pid before it serves a tool, and unregisters when the runtime lets go of
// it. There is no hook to install and no launcher to write.
//
// It is also the ADAPTER — it learns who is calling from the initialize
// handshake, so the event stream can say so — and the LISTENER: it watches the
// seat's own queue subject and rings the pane's bell. DELIVERY IS A BELL AND
// NOTHING ELSE. The line carries a count and no body; the agent fetches the
// bodies through the read tool, whose result opens with a fixed reminder to
// put them on the screen.
//
// Nothing here knows what runtime is on the other end of the pipe. The server
// reads LOC_IDENTITY and LOC_HOME and speaks on stdin and stdout, and the
// pane-specific last inch stays in the deployment's hooks/nudge.
package mcpserve

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tdebasis/locutorium/internal/config"
	"github.com/tdebasis/locutorium/internal/loc"
	model "github.com/tdebasis/locutorium/internal/presence"
)

// Deps is everything this package needs from the tool around it.
//
// THE VERBS ARE HANDED IN, NOT REIMPLEMENTED. A tool that answered differently
// from the command line would be a second implementation of the same verb, and
// one of the two would be wrong. Every field below that names a verb is the
// very function `loc` dispatches to.
type Deps struct {
	// Endpoint is the seat, <instance>.<agent>. A bare identity has already
	// been refused by name before this struct is built.
	Endpoint string
	// Version is the loc this is, for the initialize handshake.
	Version string

	Subscribe   func(args []string) error
	Unsubscribe func(args []string) error
	Send        func(w io.Writer, to, body string) error
	Read        func(w io.Writer, peek bool) error
	Status      func(w io.Writer, endpoint string) error
	Topics      func(w io.Writer) error
	Emit        func(kind, endpoint, tool string)

	// The medium's listener seam. Watch rings arrived once per message
	// published to the seat's queue subject and reconnected once per
	// re-established connection; Unread answers the backlog question at start
	// and after each of those.
	Watch  func(endpoint string, arrived, reconnected func()) (stop func(), err error)
	Unread func(endpoint string) (int, error)

	// Seams a test replaces. The zero value is the real thing.
	Nudge func(endpoint, line string) error
	Now   func() time.Time
	Ppid  func() int
	Wd    func() (string, error)

	// Where a line goes that the RUNTIME must see. Stdout is the protocol's,
	// byte for byte, so anything said to a human goes here.
	Stderr io.Writer
}

const (
	// handshakeWait is how long the server waits for the client to introduce
	// itself before registering as an anonymous one. It is bounded because a
	// runtime that never sends initialize is still a runtime that launched us,
	// and a seat that stayed unregistered waiting for a courtesy would be
	// invisible to everyone.
	handshakeWait = 3 * time.Second

	// departureWait bounds the unregistration on the way out. The runtime is
	// already gone by then and is not waiting for us; a medium that has gone
	// with it must not turn the exit into a hang.
	departureWait = 2 * time.Second

	// What a client that did not name itself is recorded as. Both are written
	// down rather than left empty, because `subscribe` requires them and an
	// empty type would read as a fact rather than as an absence.
	defaultClientType    = "mcp-client"
	defaultClientVersion = "unknown"
)

// bellLine is the whole of a wake: a count and where to go for the bodies.
// ONE LINE, NO BODY — a pane is not a mailbox, and a bell that carried the
// message would make the read tool optional and the reminder unreachable.
const bellLine = "🔔 %d new → read"

// Serve registers the seat, serves the four tools until the runtime lets go of
// this process, and unregisters. It returns an error only when the server
// could not start; every ordinary ending is a clean one.
func Serve(ctx context.Context, d Deps, t mcp.Transport) error {
	d = d.withDefaults()

	// THE HANDLER IS INSTALLED BEFORE THERE IS ANYTHING TO GIVE UP.
	//
	// Everything below this line — the preflight, the handshake, the
	// registration, the pid file and the first ring of the bell, which runs
	// the deployment's nudge hook and waits for it — takes time a signal can
	// arrive in. Notifying only once the server settled down to wait left
	// exactly that stretch with Go's DEFAULT disposition in place: terminate
	// where you stand. A TERM landing there killed a process that had already
	// registered, and the seat stayed in the registry naming a runtime that
	// had gone — the one outcome registering was supposed to make impossible.
	//
	// The channel is buffered, so a signal arriving before anyone is reading
	// it is HELD rather than dropped, and wait finds it the moment it looks.
	// The registration is never abandoned half-made: it finishes, and then
	// the server departs through the one goodbye path there is, which is also
	// the only code that undoes what registering did.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	// PREFLIGHT, BEFORE A BYTE IS SPOKEN. A seat held by a LIVE process that
	// is not the runtime which launched us is not ours to take: two servers
	// draining one queue would each receive part of the mail. Refusing here
	// means the runtime shows this server as failed, which is the truth.
	if held, err := heldByAnother(d); err != nil {
		return err
	} else if held != nil {
		return heldElsewhere(d.Endpoint, held)
	}

	s := &server{d: d}
	srv := mcp.NewServer(&mcp.Implementation{Name: "loc", Version: d.Version}, nil)
	s.addTools(srv)

	ss, err := srv.Connect(ctx, t, nil)
	if err != nil {
		return err
	}

	// The client names itself in the initialize handshake, which arrives after
	// the connection is up — so the registration waits for it rather than
	// guessing. What it learns is what `registry` will show for this seat.
	name, version := awaitClient(ss)
	if err := s.register(name, version); err != nil {
		_ = ss.Close()
		return err
	}

	// THE BELL FIRST, THEN THE FILE THAT CLAIMS THERE IS ONE.
	//
	// The pid file is what a sender reads to decide it need not ring: a seat
	// whose server is up rings its own bell, so `send` stays quiet (cmd/loc,
	// hasItsOwnBell). Writing the file before the listener exists opens a
	// window where that is a lie — the sender is told the seat rings, the seat
	// is not yet watching, and the message arrives with no bell at all, which
	// is the one outcome this whole arrangement exists to prevent. The order
	// makes the file mean what it says.
	stopBell := s.startBell()
	s.claimPIDFile()
	reason := s.wait(ss, sig)
	stopBell()
	s.logGoodbye(reason)
	s.depart(reason)
	return nil
}

// server is one running seat.
type server struct {
	d Deps
	b *bell
}

// departReason describes why the server is departing.
type departReason struct {
	kind   string // "eof" or "signal"
	signal os.Signal
}

// wait blocks until the runtime lets go: stdin reaches EOF (the runtime
// exited) or a signal arrives. Either is an ordinary ending. The signal
// channel is handed in rather than made here because it is installed at the
// top of Serve, before there is a registration to strand; a signal that
// arrived during startup is already in it and is taken at once.
func (s *server) wait(ss *mcp.ServerSession, sig <-chan os.Signal) departReason {
	done := make(chan struct{})
	go func() { defer close(done); _ = ss.Wait() }()

	// ONCE THE END IS DECIDED, EVERY LATER SIGNAL IS IGNORED FOR GOOD.
	//
	// Observed under a real runtime: `goodbye ... signal interrupt` and then
	// nothing — no `left`, the registration still in place, the pid file still
	// there. A runtime signals the whole process tree, and the launched
	// process forwards its own copy downward on top of that, so a second
	// signal is not an edge case here, it is the normal delivery. Stopping
	// the notification would put the default disposition back — terminate
	// where you stand — for exactly the stretch that matters: the goodbye
	// line, the unregistration and the pid file all run AFTER this returns.
	// Ignoring instead means no signal from any source can interrupt them,
	// and the process still ends, on its own, a moment later.
	//
	// SIGKILL still wins. Nothing here changes that, and nothing needs to:
	// the two-generation shape in cmd/loc/mcp.go is what survives a kill.
	defer signal.Ignore(syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	select {
	case <-done:
		return departReason{kind: "eof"}
	case s := <-sig:
		_ = ss.Close()
		return departReason{kind: "signal", signal: s}
	}
}

// awaitClient reads the client's name and version out of the handshake.
func awaitClient(ss *mcp.ServerSession) (name, version string) {
	deadline := time.Now().Add(handshakeWait)
	for {
		if p := ss.InitializeParams(); p != nil && p.ClientInfo != nil && p.ClientInfo.Name != "" {
			v := p.ClientInfo.Version
			if v == "" {
				v = defaultClientVersion
			}
			return p.ClientInfo.Name, v
		}
		if time.Now().After(deadline) {
			return defaultClientType, defaultClientVersion
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// register puts the seat in the registry with the RUNTIME'S pid — our parent,
// the process that launched this server and whose death ends it. Registering
// our own pid would record the adapter rather than the agent.
func (s *server) register(clientType, clientVersion string) error {
	if err := s.displaceOrRefuse(); err != nil {
		return err
	}
	cwd, err := s.d.Wd()
	if err != nil {
		return err
	}
	return s.d.Subscribe([]string{
		s.d.Endpoint,
		"--pid", strconv.Itoa(s.d.Ppid()),
		"--type", clientType,
		"--version", clientVersion,
		"--display", agentToken(s.d.Endpoint),
		"--cwd", cwd,
	})
}

// displaceOrRefuse clears whoever holds the seat, or refuses to take it.
//
// A DEAD PREDECESSOR IS DISPLACED AND SAID SO. A seat whose last runtime
// crashed left a registration behind that nothing else will clear until a
// sweep runs, and refusing to start over it would make a crash cost a seat.
// The displacement is written to the delivery log with the dead pid, because
// it is the only trace that the previous occupant did not leave cleanly.
func (s *server) displaceOrRefuse() error {
	held, err := model.Load(s.d.Endpoint)
	if err != nil || held == nil {
		return err
	}
	if held.Alive() && held.Process.PID != s.d.Ppid() {
		return heldElsewhere(s.d.Endpoint, held)
	}
	dead := !held.Alive()
	if err := s.d.Unsubscribe([]string{s.d.Endpoint, "--force"}); err != nil {
		return err
	}
	if dead {
		s.log(fmt.Sprintf("displaced %s: pid %d is gone", s.d.Endpoint, held.Process.PID))
	}
	return nil
}

// heldByAnother returns the registration that bars this server from starting,
// or nil when nothing does.
func heldByAnother(d Deps) (*model.Registration, error) {
	held, err := model.Load(d.Endpoint)
	if err != nil || held == nil {
		return nil, err
	}
	if held.Alive() && held.Process.PID != d.Ppid() {
		return held, nil
	}
	return nil, nil
}

// heldElsewhere names the pid. Naming it is the whole point of the refusal:
// the operator needs to tell a crashed predecessor from a running agent, and
// only a process id lets them look.
func heldElsewhere(endpoint string, held *model.Registration) error {
	return fmt.Errorf("endpoint '%s' is held by a LIVE process, pid %d, which did not launch this server; "+
		"a seat has one listener, so this one will not start", endpoint, held.Process.PID)
}

// logGoodbye logs the reason the server is departing.
func (s *server) logGoodbye(reason departReason) {
	if reason.kind == "eof" {
		s.log(fmt.Sprintf("goodbye %s: eof", s.d.Endpoint))
	} else if reason.kind == "signal" {
		s.log(fmt.Sprintf("goodbye %s: signal %s", s.d.Endpoint, reason.signal.String()))
	}
}

// depart frees the seat on the way out, bounded.
func (s *server) depart(reason departReason) {
	done := make(chan error, 1)
	go func() {
		// --force because our own pid is alive: it is this very process doing
		// the leaving, and the safe-reconciliation refusal exists to stop
		// somebody ELSE displacing a running agent.
		done <- s.d.Unsubscribe([]string{s.d.Endpoint, "--reason", "clean", "--force"})
	}()

	select {
	case err := <-done:
		if err != nil {
			s.log(fmt.Sprintf("leave failed %s: %v", s.d.Endpoint, err))
		} else {
			s.log(fmt.Sprintf("left %s", s.d.Endpoint))
		}
	case <-time.After(departureWait):
		s.log(fmt.Sprintf("leave timed out %s", s.d.Endpoint))
	}

	s.releasePIDFile()
}

// THE SERVING PROCESS NAMES ITSELF, because nothing else in the deployment
// can. The registration carries the RUNTIME's pid — that is the whole point of
// it, and it is the right answer to "who is at this seat" — which leaves "and
// which process is actually serving it" unanswerable, the more so because the
// server deliberately runs one generation removed from the process the runtime
// launched (cmd/loc/mcp.go). One file, written when the seat is taken and
// removed when it is given up, is what lets an operator find the server, and a
// stale one is worse than none: it would send the next reader to a stranger,
// so its removal is part of departing.
func (s *server) pidFile() string {
	return filepath.Join(config.Home(), "run", s.d.Endpoint+".mcp.pid")
}

// claimPIDFile records this process: TWO LINES, the pid and the start time the
// presence model records for it, written with that model's own function so the
// value and the comparison can never drift apart. The pair is the identity —
// the pid alone begins naming a stranger the moment the kernel reuses the
// number, and a reader deciding whether this server is running would then get
// a confident yes about somebody else.
//
// A failure is not fatal: the file is an aid to whoever is looking, not a
// lock, and a seat that could not write it is still a seat being served.
func (s *server) claimPIDFile() {
	if err := os.MkdirAll(filepath.Join(config.Home(), "run"), 0o700); err != nil {
		return
	}
	pid := os.Getpid()
	_ = os.WriteFile(s.pidFile(),
		[]byte(strconv.Itoa(pid)+"\n"+model.StartedAt(pid)+"\n"), 0o600)
}

// releasePIDFile removes it on the way out.
func (s *server) releasePIDFile() { _ = os.Remove(s.pidFile()) }

// warn is a line that must not be lost: it goes to the delivery log, where a
// deployment already looks for what was delivered and what was not, AND to the
// runtime's own log, because the delivery log is a file somebody has to think
// to open and a failure nobody reads is a failure nobody fixes. Stdout is the
// protocol's, so the second copy goes to stderr.
func (s *server) warn(line string) {
	s.log(line)
	_, _ = fmt.Fprintf(s.d.Stderr, "loc mcp: %s\n", line)
}

// log appends one line to the seat's delivery log, in the shell listener's
// format and its own file, so a deployment has ONE place to look for what was
// delivered and what was not.
func (s *server) log(line string) {
	dir := filepath.Join(config.Home(), "run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, s.d.Endpoint+".delivery.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s %s\n", s.d.Now().UTC().Format("2006-01-02T15:04:05Z"), line)
}

// withDefaults fills the seams a test replaces with the real thing.
func (d Deps) withDefaults() Deps {
	if d.Nudge == nil {
		d.Nudge = loc.NudgeErr
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Ppid == nil {
		d.Ppid = os.Getppid
	}
	if d.Wd == nil {
		d.Wd = os.Getwd
	}
	if d.Stderr == nil {
		d.Stderr = os.Stderr
	}
	return d
}

// agentToken is the part of an endpoint after the dot — the seat's own name
// within its instance, which is what a display name should read as.
func agentToken(endpoint string) string {
	if i := strings.IndexByte(endpoint, '.'); i >= 0 {
		return endpoint[i+1:]
	}
	return endpoint
}
