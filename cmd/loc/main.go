// Command loc is the Locutorium CLI.
//
// In a silent house, the locutorium is the one room where speaking is allowed.
// Queues are one endpoint saying something to another; topics are the room
// where they converse. Nothing here is a record: what matters gets written
// down elsewhere, deliberately, by whoever it mattered to.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/tdebasis/locutorium/internal/config"
	"github.com/tdebasis/locutorium/internal/loc"
	"github.com/tdebasis/locutorium/internal/provider"

	// The medium adapters this binary carries. Importing one is what makes it
	// selectable by name in the deployment's config.
	_ "github.com/tdebasis/locutorium/internal/provider/nats"
)

const usage = `usage: loc <verb> [args]

  send <endpoint> <body>    deliver to an endpoint's queue (guaranteed)
  publish <topic> <body>    speak in a topic (@name rings that endpoint)
  read [--peek]             your queue backlog + topic conversations
  sub [--watch-pid <pid>]   register attendance: start your wake listener
  unsub                     end attendance (the queue keeps holding messages)
  registry                  who is attending, read from the medium
  topics                    what conversations are active right now
  status                    unread counts per endpoint
  watch                     follow all traffic, read-only (^C to stop)
  doctor [--init]           health checks; --init creates streams (admin)
  version                   which loc this is
`

// main is a wrapper and nothing else. Every decision, the exit code included,
// is made in run, which writes to the streams it is handed — so the whole tool
// can be driven by a test without a process boundary, and the process boundary
// is not the only place its behaviour is pinned down.
func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// errUsage means "you typed it wrong". It is the one failure that prints the
// verb list on STDOUT rather than a `loc:` line on stderr: someone who has not
// got the invocation right yet is reading, not scripting.
var errUsage = errors.New("usage")

// errNotImplemented is a verb this build knows the name of and cannot do. It
// is an ordinary error, so it prints in the ordinary shape: naming it here is
// what keeps "this build cannot" from being mistaken for "you typed it wrong".
var errNotImplemented = errors.New("not implemented in this build")

// run dispatches one invocation and returns the process's exit code.
//
// THIS TOOL HAS EXACTLY TWO OUTCOMES, and which one it is gets decided here
// rather than wherever the problem was noticed: 0, or `loc: <what>` on stderr
// and 1. Nothing below this function writes to stderr or picks a code, so the
// error shape the conformance suite diffs cannot drift one verb at a time.
func run(args []string, stdout, stderr io.Writer) int {
	err := dispatch(args, stdout)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage):
		fmt.Fprint(stdout, usage)
		return 1
	default:
		fmt.Fprintf(stderr, "loc: %v\n", err)
		return 1
	}
}

// dispatch runs one verb, writing whatever it has to say to w.
func dispatch(args []string, w io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	verb, rest := args[0], args[1:]

	switch verb {
	case "version":
		// Answered before any provider is opened: `version` reads the VERSION
		// file and touches no medium, so it must work on a machine that has
		// just cloned this and has no deployment yet.
		v, err := version()
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "loc %s\n", v)
		return nil

	case "send":
		if len(rest) < 2 {
			return errUsage
		}
		return withProvider(func(p provider.Provider) error { return send(p, w, rest[0], rest[1]) })

	case "publish":
		if len(rest) < 2 {
			return errUsage
		}
		return withProvider(func(p provider.Provider) error { return publish(p, w, rest[0], rest[1]) })

	case "topics":
		return withProvider(func(p provider.Provider) error { return p.Topics(w) })

	case "status":
		return withProvider(func(p provider.Provider) error { return p.Status(w) })

	// The read path and the listener are not in this binary yet. They are
	// named here rather than falling through to usage.
	case "read", "sub", "unsub", "registry", "watch", "doctor":
		return errNotImplemented

	default:
		return errUsage
	}
}

// withProvider opens the configured provider and hands it to one verb. Any
// failure — opening the medium, or the verb itself — comes back as an error
// for run to turn into the single shape this tool has.
func withProvider(fn func(provider.Provider) error) error {
	p, err := provider.Open(config.Get("provider", ""))
	if err != nil {
		return err
	}
	defer p.Close()
	return fn(p)
}

// send puts one envelope in one endpoint's queue.
//
// The order of the checks is the point: identity, then length, then the
// registry, then attendance. Everything that can refuse a message does so
// BEFORE the envelope exists, so a refusal never leaves a half-sent thing
// behind.
func send(p provider.Provider, w io.Writer, to, body string) error {
	from, err := loc.Identity()
	if err != nil {
		return err
	}
	if err := loc.CheckBody(body); err != nil {
		return err
	}
	if !loc.EndpointExists(to) {
		return fmt.Errorf("unknown endpoint '%s' (not in this deployment's registry)", to)
	}
	// Say-semantics (config-gated; enable only once every endpoint registers
	// at session-up): a send expects an attending peer. When the deployment
	// KNOWS nobody is attending, accepting the message would manufacture a
	// false belief in the sender. Durable store-and-forward semantics belong
	// to a different channel, not to send.
	if config.Get("send_requires_attendance", "no") == "yes" && !loc.ListenerAlive(to) {
		return fmt.Errorf("not attending: '%s' has no live listener "+
			"(say-semantics: a send expects an attending peer; "+
			"use a durable channel for messages meant to wait)", to)
	}
	env, err := loc.NewEnvelope(from, to, "msg", body).Marshal()
	if err != nil {
		return err
	}
	if err := p.SendQueue(to, env); err != nil {
		return err
	}
	loc.Nudge(to, "[LOC] 1 new → loc read")
	fmt.Fprintf(w, "sent → queue.%s\n", to)
	return nil
}

// topicName is the whole grammar of a topic: lowercase, starting with a
// letter or digit. A room's name goes in a subject, a filename and a header,
// so it is kept to what all three take without quoting.
var topicName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// publish speaks one envelope in a topic and rings whoever was named.
func publish(p provider.Provider, w io.Writer, topic, body string) error {
	if !topicName.MatchString(topic) {
		return fmt.Errorf("invalid topic name '%s'", topic)
	}
	// Same medium, same limit: a room is not a document store either.
	if err := loc.CheckBody(body); err != nil {
		return err
	}
	from, err := loc.Identity()
	if err != nil {
		return err
	}
	env, err := loc.NewEnvelope(from, "#"+topic, "msg", body).Marshal()
	if err != nil {
		return err
	}
	if err := p.PublishTopic(topic, env); err != nil {
		return err
	}
	for _, m := range loc.Mentions(body) {
		if loc.EndpointExists(m) {
			loc.Nudge(m, fmt.Sprintf("[LOC] 1 new in #%s → loc read", topic))
		}
	}
	fmt.Fprintf(w, "published → #%s\n", topic)
	return nil
}
