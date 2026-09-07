package main

// The `mcp` verb's SUBPROCESS suite: the seat's server as an agent runtime
// actually gets it — a real process, launched with a real parent, spoken to
// over its real stdin and stdout by a real MCP client.
//
// The process boundary is not decoration here. THE PID IS THE WHOLE CLAIM:
// a stdio server is launched by the runtime and dies with it, so the pid it
// registers must be its PARENT's — the runtime's — and no in-process test can
// have a parent to be wrong about. The same boundary is what makes "the
// runtime exited" expressible at all: closing the client closes the pipe, and
// the seat must be free within a second of it.

import (
	"context"
	"os"
	osexec "os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCP_TheServerHoldsTheSeatItWasLaunchedFor(t *testing.T) {
	bin := buildLoc(t)
	p := newPresence(t)
	tune(t, p.home, "wake_window_seconds = 1")
	spool := nudgeSpool(t, p.home)

	ctx := context.Background()
	cmd := osexec.Command(bin, "mcp")
	cmd.Env = append(os.Environ(), "LOC_HOME="+p.home, "LOC_IDENTITY="+e1)
	cmd.Stderr = os.Stderr

	client := mcp.NewClient(&mcp.Implementation{Name: testClientName, Version: testClientVersion}, nil)
	sess, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect to the seat's server: %v", err)
	}

	// ── registration: the runtime's pid, and the runtime's name ─────────────
	if !waitFor(5*time.Second, func() bool { return registration(t, e1) != nil }) {
		t.Fatalf("the server did not register %s", e1)
	}
	reg := registration(t, e1)
	if reg.Process.PID != os.Getpid() {
		t.Errorf("registered pid %d; the process that launched the server is %d — "+
			"a server that registers its own pid records the adapter, not the agent",
			reg.Process.PID, os.Getpid())
	}
	if reg.Agent.Type != testClientName || reg.Agent.Version != testClientVersion {
		t.Errorf("registered as %s %s; the client introduced itself as %s %s",
			reg.Agent.Type, reg.Agent.Version, testClientName, testClientVersion)
	}

	// ── the bell: a send from another endpoint rings once ───────────────────
	// Sent by the SUPERVISOR, which is how a message reaches a seat in this
	// deployment: the host sends on an agent's behalf.
	p.as(t, "host")
	if code := run([]string{"send", e1, "the clerk has a question"}, os.Stdout, os.Stderr); code != 0 {
		t.Fatalf("send to the attended seat failed with %d", code)
	}
	if !waitFor(6*time.Second, func() bool { return len(bells(t, spool)) == 1 }) {
		t.Fatalf("one send rang %d bells: %q", len(bells(t, spool)), bells(t, spool))
	}
	if got := bells(t, spool)[0]; got != "🔔 1 new → read" {
		t.Errorf("bell line %q; one arrival is one new message and the line carries no body", got)
	}

	// ── the runtime exits: the seat is free within a second ─────────────────
	if err := sess.Close(); err != nil {
		t.Fatalf("close the client: %v", err)
	}
	if !waitFor(time.Second, func() bool { return registration(t, e1) == nil }) {
		t.Errorf("a second after the runtime let go, %s was still registered; "+
			"the seat a dead runtime held is a seat nobody can take", e1)
	}
}

// A LIVE SEAT IS NOT TAKEN. Two servers draining one queue would each receive
// part of the mail, so the second refuses — and names the pid, because that is
// the only thing that lets an operator tell a crashed predecessor from a
// running agent.
func TestMCP_ASecondServerForALiveSeatRefusesAndNamesThePid(t *testing.T) {
	bin := buildLoc(t)
	p := newPresence(t)

	// A process this test owns and reaps, standing in for the incumbent's
	// runtime. It must not be this test process: our own pid IS the parent of
	// the server we are about to launch, and re-launching from the same
	// runtime is exactly the case that displaces rather than refuses.
	incumbent := osexec.Command("sleep", "30")
	if err := incumbent.Start(); err != nil {
		t.Fatalf("start the stand-in incumbent: %v", err)
	}
	pid := incumbent.Process.Pid
	t.Cleanup(func() { _ = incumbent.Process.Kill(); _, _ = incumbent.Process.Wait() })

	p.as(t, "host")
	if code := run([]string{"subscribe", e1, "--pid", strconv.Itoa(pid),
		"--type", "acme-cli", "--version", "1.0.0"}, os.Stdout, os.Stderr); code != 0 {
		t.Fatalf("seed the incumbent registration: exit %d", code)
	}

	cmd := osexec.Command(bin, "mcp")
	cmd.Env = append(os.Environ(), "LOC_HOME="+p.home, "LOC_IDENTITY="+e1)
	cmd.Stdin = nil
	var errOut strings.Builder
	cmd.Stderr = &errOut
	err := cmd.Run()

	if err == nil {
		t.Fatalf("a second server started over a live seat; it said %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "pid "+strconv.Itoa(pid)) {
		t.Errorf("the refusal did not name the incumbent's pid %d: %q", pid, errOut.String())
	}
	if !strings.HasPrefix(errOut.String(), "loc: ") {
		t.Errorf("the refusal is not in this tool's one error shape: %q", errOut.String())
	}
}
