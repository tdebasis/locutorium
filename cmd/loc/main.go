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
	"os/signal"
	"regexp"
	"syscall"

	"github.com/tdebasis/locutorium/internal/config"
	"github.com/tdebasis/locutorium/internal/loc"
	// Aliased: the frozen integration suite in this package already declares a
	// type called `presence` for its scratch deployment, and a package name and
	// a type name cannot both be that word here.
	model "github.com/tdebasis/locutorium/internal/presence"
	"github.com/tdebasis/locutorium/internal/provider"

	// The medium adapters this binary carries. Importing one is what makes it
	// selectable by name in the deployment's config.
	_ "github.com/tdebasis/locutorium/internal/provider/nats"
)

const usage = `usage: loc <verb> [args]

messages
  send <endpoint> <body>    deliver to an endpoint's queue (guaranteed)
  publish <topic> <body>    speak in a topic (@name rings that endpoint)
  read [--peek]             your queue backlog + topic conversations
  topics                    what conversations are active right now
  transcript [--seat <e>] [--uid <id>] [--pending] [--json] [date]
                            the day's record, joined by message

presence — called by whoever launches agents
  subscribe <endpoint> --pid <n> --type <t> --version <v>
                            [--display <name>] [--role <role>] [--cwd <dir>]
                            register an agent instance and create its queue
  unsubscribe <endpoint> [--reason clean|expiry] [--force]
                            free the endpoint and destroy its queue
  sweep                     reconcile every row, queue and process

presence — called by adapters
  emit <kind> <endpoint> [--ts <t>] [--tool <name>] [--refs <ids>]
                            publish one lifecycle or activity event

presence — called by a person, or by a consumer
  registry [<instance>] [--json]
                            who is registered, asked of the instance's host
  status [<endpoint>]       one agent's four facts, attending among them;
                            bare, the whole deployment
  unread <endpoint>         mail not taken: a number, or the word unknown
  watch [<instance>]        follow the event stream, read-only (^C to stop)

the deployment
  start                     boot the broker and the heartbeat, detached
  stop [--force]            stop the daemon, and the broker it runs

other
  mcp                       serve this seat to an agent runtime over stdio
  version                   which loc this is
`

// main is a wrapper and nothing else. Every decision, the exit code included,
// is made in run, which writes to the streams it is handed — so the whole tool
// can be driven by a test without a process boundary, and the process boundary
// is not the only place its behaviour is pinned down.
func main() {
	// A READER THAT GOES AWAY IS A WRITE ERROR, NOT A DEATH.
	//
	// Go's rule: a write to descriptor 1 or 2 that gets EPIPE raises SIGPIPE,
	// and a program that has not said otherwise is killed by it. That is the
	// wrong ending for this tool. `read` fetches before it prints and
	// acknowledges only after the write that carried the bytes returned nil,
	// so an EPIPE is already handled everywhere it can arrive: stop, take
	// nothing further, return the error — and the close on the way out hands
	// the unshown message back with a negative acknowledgement. Being killed
	// at the write skips that close, and the message it never showed stays in
	// flight for the consumer's whole ack-wait while the next read shows an
	// empty mailbox. Ignoring the signal is what turns EPIPE back into an
	// ordinary error the verbs already know what to do with.
	//
	// Only main gains this. run stays a plain function with no process-wide
	// state of its own, so it remains drivable by a test.
	signal.Ignore(syscall.SIGPIPE)

	os.Exit(run(launchArgs(os.Args[1:]), os.Stdout, os.Stderr))
}

// launchArgs marks a real `loc mcp` invocation as a LAUNCH.
//
// The server deliberately runs one generation below the process an agent
// runtime starts, so that a runtime which ends its child with SIGKILL cannot
// take the seat's goodbye with it (cmd/loc/mcp.go, mcpParent). Re-executing is
// something only a real process invocation can do, and main is the only caller
// that IS one: run is also called in-process by tests, where "re-execute this
// binary" means re-running the test binary, which is a fork bomb. So the
// launch is asked for here, by name, and everywhere else `mcp` is the server
// itself. Everything else passes through untouched.
func launchArgs(args []string) []string {
	if len(args) == 1 && args[0] == "mcp" {
		return []string{"mcp", launchFlag}
	}
	return args
}

// errUsage means "you typed it wrong". It is the one failure that prints the
// verb list on STDOUT rather than a `loc:` line on stderr: someone who has not
// got the invocation right yet is reading, not scripting.
var errUsage = errors.New("usage")

// exitStatus is a CHILD'S exit code carried back to run, which returns it
// unchanged and prints nothing of its own. The `mcp` verb re-executes this
// binary and waits on the result (cmd/loc/mcp.go); the child writes to the
// very stderr this process was handed, so anything it had to say is already
// there in this tool's one error shape. A `loc:` line here would be the tool
// speaking twice about one failure.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// run dispatches one invocation and returns the process's exit code.
//
// THIS TOOL HAS EXACTLY TWO OUTCOMES, and which one it is gets decided here
// rather than wherever the problem was noticed: 0, or `loc: <what>` on stderr
// and 1. Nothing below this function picks a code, and nothing below it writes
// a FAILURE, so the error shape the conformance suite diffs cannot drift one
// verb at a time.
//
// A verb may still write a NOTICE to stderr, and one does. `transcript` names
// each corrupt line it skipped and then prints the rest of the record: the
// read succeeded, so it is not a failure and must not take the exit code, and
// the notice must not land in the middle of the report on stdout. That is the
// second stream's job. stderr is handed down for it rather than reached for
// through the package, so a test drives it the same way it drives stdout.
func run(args []string, stdout, stderr io.Writer) int {
	err := dispatch(args, stdout, stderr)
	var status exitStatus
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage):
		fmt.Fprint(stdout, usage)
		return 1
	case errors.As(err, &status):
		return int(status)
	default:
		fmt.Fprintf(stderr, "loc: %v\n", err)
		return 1
	}
}

// dispatch runs one verb, writing whatever it has to say to w. errw is the
// second stream, for the one verb that has a notice to make alongside a
// successful read.
func dispatch(args []string, w, errw io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	verb, rest := args[0], args[1:]

	switch verb {
	case "version":
		// Answered before any provider is opened: `version` prints the stamp
		// linked in at build time and touches no medium, so it must work on a
		// machine that has just cloned this and has no deployment yet.
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
		// With an endpoint it is the presence report: four facts, each with
		// its reason. The fourth is `attending`, which asks the broker
		// whether this endpoint holds a queue. Bare it is the message plane's
		// unread counts, which is what it has always been.
		if len(rest) > 0 {
			return statusEndpoint(w, rest[0])
		}
		return statusVerb(w)

	case "unread":
		// ONE ARGUMENT, COUNTED HERE. A status line runs this on a timer and
		// reads the exit code and the stream, so an invocation it got wrong
		// must be refused as a typing mistake rather than reported as a
		// deployment that cannot be reached.
		if len(rest) != 1 {
			return errUsage
		}
		return unreadVerb(w, rest[0])

	case "subscribe":
		return subscribeVerb(rest)

	case "unsubscribe":
		return unsubscribeVerb(rest)

	case "sweep":
		// The change count is the daemon's to log. A caller at a terminal has
		// the lines the sweep printed, which say the same thing in words.
		_, err := sweepVerb(w, rest)
		return err

	case "emit":
		return emitVerb(rest)

	case "registry":
		return registryVerb(w, rest)

	case "watch":
		return watchVerb(w, rest)

	case "read":
		return readVerb(w, rest)

	case "transcript":
		return transcriptVerb(w, errw, rest)

	case "start":
		return startVerb(w, rest)

	case "stop":
		return stopVerb(w, rest)

	case "mcp":
		return mcpVerb(rest)

	default:
		return errUsage
	}
}

// withProvider opens the configured provider and hands it to one verb. Any
// failure — opening the medium, or the verb itself — comes back as an error
// for run to turn into the single shape this tool has.
func withProvider(fn func(provider.Provider) error) error {
	p, err := provider.Open(config.Value(config.Provider))
	if err != nil {
		return err
	}
	defer p.Close()
	return fn(p)
}

// send puts one envelope in one endpoint's queue.
//
// The order of the checks is the point: identity, then length, then
// attendance. A caller who is nobody is told that rather than being told
// about the body they sent.
//
// THE ENVELOPE IS BUILT BEFORE THE FIRST CHECK, AND THAT IS A REVERSAL. It
// used to be built last, so that a refusal never left a half-sent thing
// behind. Nothing here is half-sent: an envelope is a struct and a uuid until
// SendQueue takes it, and SendQueue is still the last thing that runs. What
// building it early buys is a NAME FOR EACH REFUSAL. A turned-away message
// used to leave no trace at all — a line on standard error, and a record that
// held nothing — so a seat that was refused four times in a minute and a seat
// that sent nothing looked the same to whoever read the day's file.
func send(p provider.Provider, w io.Writer, to, body string) error {
	from, idErr := loc.Identity()
	e := loc.NewEnvelope(from, to, "msg", body)
	if idErr != nil {
		loc.LogFailed(e, "no identity")
		return idErr
	}
	if err := loc.CheckBody(body); err != nil {
		loc.LogFailed(e, "too long")
		return err
	}
	// WHERE ATTENDANCE COMES FROM. On a medium that carries presence, a queue
	// exists exactly while an agent is subscribed to it, so its absence IS an
	// absent recipient. That is a live fact, and it is the only roster this
	// tool has: in the inner parlor there are no mailboxes for agents that are
	// not running. A medium without presence cannot answer the question at
	// all, so it does not refuse on it.
	pr, hasPresence := p.(provider.Presence)
	if hasPresence {
		if err := model.ValidEndpoint(to); err != nil {
			// A name that cannot be an endpoint holds no queue, so the record
			// says the same thing here as it does for a name that is spelt
			// legally and is simply not attending.
			loc.LogFailed(e, "target not registered")
			return err
		}
		attended, err := pr.QueueExists(to)
		if err != nil {
			// The question could not be asked, which is a fact about the
			// medium and not about the recipient.
			loc.LogFailed(e, "bus unreachable")
			return err
		}
		if !attended {
			loc.LogFailed(e, "target not registered")
			return fmt.Errorf("nobody is attending '%s': no live subscription, "+
				"so no queue to deliver to", to)
		}
	}
	// Say-semantics (config-gated; enable only once every endpoint registers
	// at session-up): a send expects an attending peer. When the deployment
	// KNOWS nobody is attending, accepting the message would manufacture a
	// false belief in the sender. Durable store-and-forward semantics belong
	// to a different channel, not to send.
	//
	// THE GATE READS NO PIDFILE ON A PRESENCE MEDIUM. ListenerAlive answers out
	// of a listener pidfile, which only a deployment whose agents run a shell
	// listener ever writes. An agent that joined through the presence model
	// (`subscribe`) runs no such listener and leaves no such file, so there the
	// pidfile's silence says nothing about attendance — it is silent for every
	// peer, present or absent. On that medium attendance IS the live queue
	// (PRESENCE.md §Queue lifetime; §Inner and outer parlors), so the question
	// this key asks has already been asked and answered a few lines up by the
	// QueueExists refusal, from a live fact instead of a file. Without presence
	// the pidfile is still the only answer available, and stays the one used.
	if !hasPresence && config.Value(config.SendRequiresAttendance) == "yes" && !loc.ListenerAlive(to) {
		loc.LogFailed(e, "target not registered")
		return fmt.Errorf("not attending: '%s' has no live listener "+
			"(say-semantics: a send expects an attending peer; "+
			"use a durable channel for messages meant to wait)", to)
	}
	env, err := e.Marshal()
	if err != nil {
		// Marshal fails only if an envelope field stops being a string, so
		// this is a build defect rather than one of the four refusals. The
		// message did not reach the medium, which is what `bus unreachable`
		// says, and a line under the closest word beats a refusal that
		// leaves no line at all.
		loc.LogFailed(e, "bus unreachable")
		return err
	}
	if err := p.SendQueue(to, env); err != nil {
		loc.LogFailed(e, "bus unreachable")
		return err
	}
	loc.LogSent(e)
	// THE SENDER ONLY QUEUES. A send puts the message in the recipient's queue
	// and does nothing else. The seat's own server is the one thing that
	// notifies, through the notifier its registered type names (R36,
	// 2026-09-10). A seat registered as none is not notified at all and finds
	// its mail on the next read. A sender that rang as well would be a second
	// bell that cannot coalesce, is not capped, and does not know what else is
	// waiting for that seat.
	// THE UID IS ON THE SUCCESS LINE so the sender can find its own message
	// in the day's file. The record now carries a `read` line under the same
	// uid, and a sender that was never told the uid would have to match on a
	// body to find out whether anybody took what it sent.
	fmt.Fprintf(w, "sent → queue.%s uid=%s\n", to, e.ID)
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
	// A MENTION IS DELIVERED TO THE TOPIC AND ANNOUNCED TO NOBODY. The named
	// seat reads the topic from its own position and finds the mention there
	// (R36, 2026-09-10). Ringing the seat here would be the sender notifying,
	// which is the one thing a sender does not do.
	fmt.Fprintf(w, "published → #%s\n", topic)
	return nil
}
