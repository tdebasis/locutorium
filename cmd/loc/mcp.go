package main

import (
	"context"
	"fmt"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tdebasis/locutorium/internal/config"
	"github.com/tdebasis/locutorium/internal/loc"
	"github.com/tdebasis/locutorium/internal/mcpserve"
	model "github.com/tdebasis/locutorium/internal/presence"
	"github.com/tdebasis/locutorium/internal/provider"
)

// mcpVerb serves this seat to whatever agent runtime launched it.
//
// It is the ONE VERB THAT DOES NOT RETURN. Every other verb performs one
// operation and exits; this one registers the seat, holds it for the life of
// the runtime's session, and frees it on the way out. What it is and why it
// has that shape is in internal/mcpserve.
func mcpVerb(args []string) error {
	if len(args) > 0 {
		return errUsage
	}
	d, release, err := mcpDeps()
	if err != nil {
		return err
	}
	defer release()
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
	name := config.Get("provider", "")
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
// with an endpoint it is the presence report, bare it is the unread counts.
func statusTool(w io.Writer, endpoint string) error {
	if endpoint != "" {
		return statusEndpoint(w, endpoint)
	}
	return withProvider(func(p provider.Provider) error { return p.Status(w) })
}
