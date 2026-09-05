// Command loc is the Locutorium CLI.
//
// In a silent house, the locutorium is the one room where speaking is allowed.
// Queues are one endpoint saying something to another; topics are the room
// where they converse. Nothing here is a record: what matters gets written
// down elsewhere, deliberately, by whoever it mattered to.
package main

import (
	"fmt"
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

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Print(usage)
		os.Exit(1)
	}
	verb, rest := args[0], args[1:]

	switch verb {
	case "version":
		v, err := version()
		if err != nil {
			die(err)
		}
		fmt.Printf("loc %s\n", v)
		return

	case "send":
		if len(rest) < 2 {
			fmt.Print(usage)
			os.Exit(1)
		}
		run(func(p provider.Provider) error { return send(p, rest[0], rest[1]) })

	case "publish":
		if len(rest) < 2 {
			fmt.Print(usage)
			os.Exit(1)
		}
		run(func(p provider.Provider) error { return publish(p, rest[0], rest[1]) })

	case "topics":
		run(func(p provider.Provider) error { return p.Topics(os.Stdout) })

	case "status":
		run(func(p provider.Provider) error { return p.Status(os.Stdout) })

	// The read path and the listener are not in this binary yet. They are
	// named here rather than falling through to usage, so that "this build
	// cannot" is never mistaken for "you typed it wrong".
	case "read", "sub", "unsub", "registry", "watch", "doctor":
		fmt.Fprintln(os.Stderr, "loc: not implemented in this build")
		os.Exit(1)

	default:
		fmt.Print(usage)
		os.Exit(1)
	}
}

// run opens the configured provider, hands it to one verb, and turns any
// failure into the single error shape this tool has: `loc: <what>` on stderr,
// exit 1.
func run(fn func(provider.Provider) error) {
	p, err := provider.Open(config.Get("provider", ""))
	if err != nil {
		die(err)
	}
	defer p.Close()
	if err := fn(p); err != nil {
		die(err)
	}
}

func die(err error) {
	fmt.Fprintf(os.Stderr, "loc: %v\n", err)
	os.Exit(1)
}

// send puts one envelope in one endpoint's queue.
//
// The order of the checks is the point: identity, then length, then the
// registry, then attendance. Everything that can refuse a message does so
// BEFORE the envelope exists, so a refusal never leaves a half-sent thing
// behind.
func send(p provider.Provider, to, body string) error {
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
	fmt.Printf("sent → queue.%s\n", to)
	return nil
}

// topicName is the whole grammar of a topic: lowercase, starting with a
// letter or digit. A room's name goes in a subject, a filename and a header,
// so it is kept to what all three take without quoting.
var topicName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// publish speaks one envelope in a topic and rings whoever was named.
func publish(p provider.Provider, topic, body string) error {
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
	fmt.Printf("published → #%s\n", topic)
	return nil
}
