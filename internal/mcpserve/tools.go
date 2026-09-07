package mcpserve

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	model "github.com/tdebasis/locutorium/internal/presence"
)

// FOUR TOOLS, AND EACH IS A SKIN ON THE VERB OF THE SAME NAME.
//
// A tool runs the CLI's own function against an io.Writer and hands back what
// that function wrote — byte for byte, `send`'s gate and nudge included, and
// `read` acknowledging only after the text is assembled, exactly as the
// command line does. Nothing here decides what a verb means, which is what
// keeps one behaviour rather than two.
//
// A failure comes back in the shape the command line prints it in, `loc: ` and
// all, so an agent reading a tool error and an operator reading a terminal are
// reading the same sentence.

// ReadReminder is the one fixed line the read tool puts in front of everything
// it hands back.
//
// IT IS WRITTEN HERE, BY THE SERVER, AND NEVER BY THE MODEL. The bell in the
// pane carries a count and no body, so nothing the read tool returns has been
// in front of a person yet. Without this line the ordinary, reasonable thing
// for an agent to do with a tool result — summarise it, act on it, mention it
// in passing — silently destroys the delivery: the message arrived, was read,
// and was never seen. The line says what has not happened and what to do about
// it, in that order.
const ReadReminder = "Nothing below has been shown to anyone yet; the bell only rang. " +
	"Print it on screen verbatim, then act on it."

// sendArgs, readArgs, statusArgs and topicsArgs are the tools' inputs. The
// schemas are generated from these by the SDK.
type sendArgs struct {
	To   string `json:"to" jsonschema:"the endpoint to deliver to, <instance>.<agent>"`
	Body string `json:"body" jsonschema:"what to say"`
}

type readArgs struct {
	Peek bool `json:"peek,omitempty" jsonschema:"look without taking: nothing is consumed"`
}

type statusArgs struct {
	Endpoint string `json:"endpoint,omitempty" jsonschema:"one endpoint's three facts; omitted, the unread counts"`
}

type topicsArgs struct{}

// addTools registers the four.
func (s *server) addTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "send",
		Description: "Deliver one message to another endpoint's queue.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sendArgs) (*mcp.CallToolResult, any, error) {
		return s.call("send", func(w io.Writer) error { return s.d.Send(w, in.To, in.Body) })
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "read",
		Description: "Take this endpoint's waiting messages and the rooms' conversation. " +
			"Reading consumes: what it hands back is handed back once.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in readArgs) (*mcp.CallToolResult, any, error) {
		return s.read(in.Peek)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "status",
		Description: "With an endpoint, that agent's three facts. Without one, the unread count per endpoint.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in statusArgs) (*mcp.CallToolResult, any, error) {
		return s.call("status", func(w io.Writer) error { return s.d.Status(w, in.Endpoint) })
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "topics",
		Description: "Which rooms have traffic in the window.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ topicsArgs) (*mcp.CallToolResult, any, error) {
		return s.call("topics", func(w io.Writer) error { return s.d.Topics(w) })
	})
}

// call runs one verb between a tool.pre and a tool.post and returns what it
// wrote.
//
// THE EVENTS ARE BEST-EFFORT AND BRACKET THE CALL. This server is the adapter
// for its seat — it is the only party that knows a tool was invoked — and a
// presence system that cannot say what an agent is doing is a display problem
// while an agent that cannot work is a real one. So an event that will not
// publish is dropped and the call goes on.
func (s *server) call(name string, run func(io.Writer) error) (*mcp.CallToolResult, any, error) {
	s.emit(model.KindToolPre, name)
	defer s.emit(model.KindToolPost, name)

	var b bytes.Buffer
	if err := run(&b); err != nil {
		return nil, nil, fmt.Errorf("loc: %w", err)
	}
	return text(b.String()), nil, nil
}

// read is the one tool with something in front of the verb's output.
func (s *server) read(peek bool) (*mcp.CallToolResult, any, error) {
	s.emit(model.KindToolPre, "read")
	defer s.emit(model.KindToolPost, "read")

	var b bytes.Buffer
	c := &queueCount{w: &b}
	if err := s.d.Read(c, peek); err != nil {
		return nil, nil, fmt.Errorf("loc: %w", err)
	}
	// WHAT WAS HANDED OVER IS WRITTEN DOWN. The bell carried no body, so if
	// the agent does not print what is below, this line is the only remaining
	// evidence that a message was delivered and read — which is what makes a
	// message that was read and never shown findable afterwards.
	s.log(fmt.Sprintf("read %s handed=%d", s.d.Endpoint, c.n))

	return text(ReadReminder + "\n\n" + b.String()), nil, nil
}

// emit publishes one activity event for this seat, naming the tool.
func (s *server) emit(kind, tool string) {
	if s.d.Emit == nil {
		return
	}
	s.d.Emit(kind, s.d.Endpoint, tool)
}

// text is one tool result carrying one block of text.
func text(body string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: body}}}
}

// queueCount counts the messages `read` handed over out of the QUEUE, passing
// every byte through untouched.
//
// It leans on a guarantee read.go states out loud and tests: an envelope is
// rendered into a buffer and written in ONE call, precisely so half of one can
// never reach a reader. So between the queue heading and the topics heading,
// one Write is one message. The headings are the only other writes in that
// span, and both are recognised by their prefix — the peek trailer included,
// which begins with the topics heading.
type queueCount struct {
	w       io.Writer
	inQueue bool
	n       int
}

func (c *queueCount) Write(p []byte) (int, error) {
	switch {
	case bytes.HasPrefix(p, []byte("── queue.")):
		c.inQueue = true
	case bytes.HasPrefix(p, []byte("── topics ──")):
		c.inQueue = false
	case c.inQueue:
		c.n++
	}
	return c.w.Write(p)
}
