package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tdebasis/locutorium/internal/config"
	"github.com/tdebasis/locutorium/internal/loc"
	"github.com/tdebasis/locutorium/internal/mcpserve"
	model "github.com/tdebasis/locutorium/internal/presence"
	"github.com/tdebasis/locutorium/internal/provider"
)

// The internal arguments the launched process hands its own child. They are
// deliberately absent from the usage text: nobody types them, and a runtime
// configured with `args: ["mcp"]` is the only caller there is.
const (
	launchFlag = "--launch"
	serveFlag  = "--serve"
	pidFlag    = "--pid"
)

// mcpVerb serves this seat to whatever agent runtime launched it.
//
// It is the ONE VERB THAT DOES NOT RETURN. Every other verb performs one
// operation and exits; this one registers the seat, holds it for the life of
// the runtime's session, and frees it on the way out. What it is and why it
// has that shape is in internal/mcpserve.
//
// TWO GENERATIONS, AND THE SECOND IS THE SERVER. With --launch — which only
// main puts there, for a real `loc mcp` on a command line — this is the
// process the runtime launched, and it does one thing: re-execute itself and
// wait. With --serve it IS that re-execution, and it registers, serves and
// departs. Why, is in mcpParent.
//
// BARE, IT SERVES IN PLACE. Re-executing is something only a process can do
// to itself, and only when the file it would re-execute is this tool; a
// caller inside another program — a test driving run() — has neither, so the
// bare form is the server and the flag is what asks for the launch. Getting
// that the wrong way round would have a test binary re-execute ITSELF, which
// is a fork bomb and not a server.
func mcpVerb(args []string) error {
	switch {
	case len(args) == 0:
		return mcpServe(os.Getppid())
	case len(args) == 1 && args[0] == launchFlag:
		return mcpParent()
	case len(args) == 3 && args[0] == serveFlag && args[1] == pidFlag:
		pid, err := strconv.Atoi(args[2])
		if err != nil || pid <= 0 {
			return errUsage
		}
		return mcpServe(pid)
	default:
		return errUsage
	}
}

// mcpParent is the process the runtime launched, and it serves nothing.
//
// A RUNTIME MAY END ITS MCP CHILD WITHOUT WARNING. Observed: an agent runtime
// ends the stdio server it started with SIGKILL — no TERM, no HUP, nothing
// delivered and nothing run. A process that IS the server cannot survive that
// to unregister, so the seat it held stays in the registry naming a pid that
// is gone, and only the next server or a sweep clears it.
//
// The fix is to make the process the runtime can kill not be the server. This
// one re-executes the same binary with the SAME stdin, stdout and stderr, so
// the child speaks to the runtime directly and nothing is copied or proxied,
// and then only waits. Kill this one and the child still holds the runtime's
// descriptors: it keeps the seat, keeps ringing, and departs when the pipe
// finally closes — which is the ONE ending a kill cannot suppress, because it
// is caused by the runtime's own exit rather than delivered to us.
//
// NO DAEMONISING, NO NEW SESSION. A plain child already outlives its parent's
// death; it is only re-parented, and cmd/loc/mcp_reexec_test.go holds that as
// an assertion rather than an assumption. Detaching further would cut the
// child loose from the descriptors that are the whole point of it.
//
// The runtime's pid goes DOWN EXPLICITLY. Once there is a process in between,
// the child's os.Getppid() is this wrapper, not the runtime — so the pid the
// registration must carry is read here, where it is still the truth, and
// passed as an argument.
func mcpParent() error {
	cmd, err := selfCommand(os.Getppid())
	if err != nil {
		return err
	}
	// THE SAME THREE DESCRIPTORS, HANDED STRAIGHT DOWN. Nothing is copied and
	// nothing is proxied: the child speaks to the runtime itself, which is
	// what makes the runtime's exit close ITS pipe and not ours.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}

	// The endings that ARE delivered are passed along rather than obeyed: this
	// process exiting on a TERM would orphan a server still holding the seat,
	// and the child already knows how to give a seat up cleanly.
	//
	// FORWARDED EVEN THOUGH THE CHILD SHARES OUR PROCESS GROUP. A runtime
	// that signals the group has already reached the child directly, so the
	// forward is usually a SECOND copy of a signal it has had — but a runtime
	// is equally free to signal only the process it launched, and that one
	// would otherwise reach nobody. Forwarding is therefore kept, and made
	// harmless at the other end: once the child has decided to depart it
	// ignores these for the rest of its life (internal/mcpserve, wait), so a
	// duplicate cannot cut its goodbye short.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sig)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case s := <-sig:
			_ = cmd.Process.Signal(s)
		case err := <-done:
			supervisorLog(err)
			return childStatus(err)
		}
	}
}

// selfCommand builds the re-execution: this same binary, told to serve, with
// the runtime's pid written into its arguments.
//
// A SEAM, because supervision is the part worth testing and re-executing is
// the part that cannot be. What mcpParent does — start, forward, wait, report
// the status — is ordinary logic with ordinary mistakes in it; the one thing
// that resists a test is `os.Executable`, which inside a test binary names the
// test binary, and a test binary re-executing itself is a fork bomb. Replacing
// this one function lets a case drive the supervision against a stand-in child
// whose ending it chooses.
var selfCommand = func(runtimePID int) (*osexec.Cmd, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return osexec.Command(self, "mcp", serveFlag, pidFlag, strconv.Itoa(runtimePID)), nil
}

// childStatus turns the child's ending into this process's exit code.
//
// SAID ONCE, BY WHOEVER SAW IT. The child writes to the very stderr the
// runtime handed us, so a refusal has already been printed in this tool's one
// error shape by the time we get here; reporting the wait's error as well
// would make one failure read as two. A child killed by a signal has no exit
// code of its own, and 1 is the truthful answer for it: something went wrong,
// and what it was is not ours to invent.
func childStatus(err error) error {
	if err == nil {
		return nil
	}
	var ee *osexec.ExitError
	if !errors.As(err, &ee) {
		return err
	}
	if code := ee.ExitCode(); code > 0 {
		return exitStatus(code)
	}
	return exitStatus(1)
}

// supervisorLog appends one line to the child's delivery log, best-effort.
//
// The wait's error is named apart from every other error in here on purpose:
// it is the only thing this function has to say, and a shadowed `err` reported
// each ending as `exit 0` — the one status that rules out the explanation
// somebody reading an abandoned seat's log is looking for.
func supervisorLog(waitErr error) {
	endpoint, err := loc.Identity()
	if err != nil {
		return
	}

	dir := filepath.Join(config.Home(), "run")
	// Best-effort, and unchecked on purpose: if the directory cannot be made
	// the open below fails and says so by returning, and one refusal is enough.
	_ = os.MkdirAll(dir, 0o700)
	logPath := filepath.Join(dir, endpoint+".delivery.log")

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()

	_, _ = fmt.Fprintf(f, "%s supervisor %s: child ended %s\n",
		time.Now().UTC().Format("2006-01-02T15:04:05Z"), endpoint, childEnding(waitErr))
}

// childEnding renders how the child ended, in the words that distinguish the
// endings from each other. A process killed by a signal has NO exit code —
// ExitCode answers -1 for it — so the code is asked for only once it is known
// to be one, and the signal is named where there is one to name.
func childEnding(err error) string {
	if err == nil {
		return "exit 0"
	}
	var ee *osexec.ExitError
	if !errors.As(err, &ee) {
		return fmt.Sprintf("error: %v", err)
	}
	if code := ee.ExitCode(); code >= 0 {
		return fmt.Sprintf("exit %d", code)
	}
	// No exit code at all means the process was killed by a signal, and the
	// wait status already names it — "signal: killed". Only the punctuation is
	// changed, so the line reads like the exit form beside it.
	return strings.Replace(ee.String(), "signal: ", "signal ", 1)
}

// mcpServe is the generation that actually holds the seat. It is handed the
// runtime's pid because it can no longer read it: its own parent is the
// wrapper above.
func mcpServe(runtimePID int) error {
	// THE HANDLER IS INSTALLED BEFORE THE FIRST DIAL.
	//
	// Serve installs one before it registers anything, which covers the
	// stretch inside it. This covers the stretch BEFORE it: mcpDeps resolves
	// the seat — running an identity hook the deployment wrote, and waiting
	// for it — reads the version and opens the provider, and a runtime is
	// free to end its server anywhere in there. Leaving that stretch on Go's
	// default disposition would make an ordinary stop end the process by
	// signal rather than cleanly, and a tool that reports a failure for an
	// ordinary stop teaches every supervisor above it that stopping it goes
	// wrong.
	//
	// The channel is buffered and handed to Serve, so there is ONE
	// registration for the whole life of the process and a signal that landed
	// during startup is still waiting when the server looks.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	d, release, err := mcpDeps()
	if err != nil {
		return err
	}
	defer release()
	d.Signals = sig
	d.Ppid = func() int { return runtimePID }
	return mcpserve.Serve(context.Background(), d, &mcp.StdioTransport{})
}

// mcpDeps resolves the seat and wires the server to THIS BINARY'S OWN VERBS.
// It is separate from mcpVerb so a test can drive the same wiring over an
// in-memory transport, and so that everything fallible about starting up
// happens before a transport is opened.
func mcpDeps() (mcpserve.Deps, func(), error) {
	none := func() {}
	me, err := loc.Identity()
	if err != nil {
		return mcpserve.Deps{}, none, err
	}
	// A BARE IDENTITY IS REFUSED BY NAME. The presence model bars bare
	// endpoints — a consumer watching two instances could not tell two agents
	// of the same name apart — and a seat is exactly the thing that gets
	// registered, so there is nothing here for an unqualified name to be.
	if err := model.ValidEndpoint(me); err != nil {
		return mcpserve.Deps{}, none, err
	}
	v, err := version()
	if err != nil {
		return mcpserve.Deps{}, none, err
	}

	// ONE LONG-LIVED PROVIDER, FOR THE LISTENER ONLY. The tools open and close
	// their own exactly as the command line does, so a tool call is the verb
	// and not a variation on it; the bell needs a connection that outlives
	// them all.
	name := config.Value(config.Provider)
	p, err := provider.Open(name)
	if err != nil {
		return mcpserve.Deps{}, none, err
	}
	l, ok := p.(provider.Listener)
	if !ok {
		p.Close()
		return mcpserve.Deps{}, none, fmt.Errorf("provider '%s' cannot tell a seat when its mail arrives", name)
	}

	return mcpserve.Deps{
		Endpoint:    me,
		Version:     v,
		Subscribe:   subscribeVerb,
		Unsubscribe: unsubscribeVerb,
		Send: func(w io.Writer, to, body string) error {
			return withProvider(func(pp provider.Provider) error { return send(pp, w, to, body) })
		},
		Read: func(w io.Writer, peek bool) error {
			return withProvider(func(pp provider.Provider) error { return read(pp, w, peek) })
		},
		Status: statusTool,
		Topics: func(w io.Writer) error {
			return withProvider(func(pp provider.Provider) error { return pp.Topics(w) })
		},
		// The emitter drops an unpublishable event on purpose; that decision
		// belongs to the verb and is not repeated here.
		Emit:   func(kind, endpoint, tool string) { _ = emitVerb([]string{kind, endpoint, "--tool", tool}) },
		Watch:  l.WatchQueue,
		Unread: l.Unread,
	}, p.Close, nil
}

// statusTool picks between the two status reports exactly as dispatch does:
// with an endpoint it is the presence report, bare it is the deployment's.
func statusTool(w io.Writer, endpoint string) error {
	if endpoint != "" {
		return statusEndpoint(w, endpoint)
	}
	return statusVerb(w)
}
