package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/tdebasis/locutorium/internal/config"
	"github.com/tdebasis/locutorium/internal/loc"
	// Aliased: the frozen integration suite in this package already declares a
	// type called `presence` for its scratch deployment, and a package name and
	// a type name cannot both be that word here.
	model "github.com/tdebasis/locutorium/internal/presence"
	"github.com/tdebasis/locutorium/internal/provider"
)

// The presence verbs. Each one is a THIN skin: it reads the arguments, asks
// internal/presence what the model says, and asks the medium for the one thing
// only the medium can do. Nothing here decides what a fact means.

// registryTimeout is how long a registry request waits for the instance's
// supervisor. Long enough that a busy host answers, short enough that a person
// who has none is told so rather than left waiting.
const registryTimeout = 2 * time.Second

// withPresence opens the configured provider and hands its presence extension
// to one verb. A medium that carries messages but not presence is named in the
// refusal, because "this provider cannot" and "you typed it wrong" are
// different answers and only one of them is fixed by reading the usage.
func withPresence(fn func(provider.Presence) error) error {
	name := config.Get("provider", "")
	p, err := provider.Open(name)
	if err != nil {
		return err
	}
	defer p.Close()
	pr, ok := p.(provider.Presence)
	if !ok {
		return fmt.Errorf("provider '%s' does not support presence", name)
	}
	return fn(pr)
}

// parseFlags reads `--name value` pairs and bare switches out of args, in any
// order. Anything it does not know is a usage error rather than a silent
// ignore: a misspelled flag that changes nothing is the failure a caller does
// not notice.
//
// A FLAG GIVEN WITH AN EMPTY VALUE IS REFUSED, so that absent and empty stay
// different facts. Every optional field here is stored `omitempty` and read
// back as a plain string, so `--cwd ""` and no `--cwd` at all are byte
// identical on the wire and identical after unmarshal. That tolerance is
// harmless while a field is decorative — an empty cwd only means nobody said
// where an agent works — and stops being harmless the moment something
// depends on the value. An empty delivery address is not a missing detail. It
// is an endpoint nothing can reach, recorded as though it had been configured,
// and a registration outlives the process that wrote it.
//
// The check lives HERE, where a flag name is matched, rather than in a caller
// scanning args for the flag it cares about. "The string `--address` appears
// in args" is a different question from "`--address` was given as a flag" —
// `--cwd --address` satisfies the first and not the second — and a guard that
// answers the adjacent question fires when it should not.
func parseFlags(args []string, values map[string]*string, switches map[string]*bool) error {
	for i := 0; i < len(args); i++ {
		if p, ok := switches[args[i]]; ok {
			*p = true
			continue
		}
		p, ok := values[args[i]]
		if !ok || i+1 >= len(args) {
			return errUsage
		}
		if args[i+1] == "" {
			return fmt.Errorf("%s was given with no value; omit the flag to leave it unset", args[i])
		}
		*p = args[i+1]
		i++
	}
	return nil
}

// leadingWord takes an optional positional argument off the front of args,
// which is how `[<instance>]` is spelled in front of flags.
func leadingWord(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

// instanceOf resolves an omitted instance to the CALLER'S OWN, read from its
// configured identity. There is no "every instance": each one is a separate
// subject with its own supervisor, so asking for all of them is undefined.
func instanceOf(given string) (string, error) {
	if given != "" {
		return given, nil
	}
	id, err := loc.Identity()
	if err != nil {
		return "", err
	}
	if model.ValidEndpoint(id) != nil {
		return "", fmt.Errorf("no instance given, and the caller's identity '%s' is not "+
			"an endpoint of the form <instance>.<agent> to take one from", id)
	}
	return model.Instance(id), nil
}

// incumbent names who holds an endpoint, concretely enough for the caller to
// tell a crashed predecessor from a running agent — which is the entire point
// of refusing rather than replacing.
func incumbent(reg *model.Registration) string {
	if reg.Process.Started != "" {
		return fmt.Sprintf("pid %d (started %s)", reg.Process.PID, reg.Process.Started)
	}
	// A predecessor already gone when it was registered has no start time of
	// its own; when the registration was written is what dates it instead.
	return fmt.Sprintf("pid %d (started unknown; registered %s)", reg.Process.PID, reg.Registered)
}

// ------------------------------------------------------------------ subscribe

// subscribeVerb registers an agent instance, creates its queue, and announces
// it.
//
// An endpoint holds ONE INSTANCE AT A TIME, and a held one is REFUSED. The
// incumbent keeps its queue; displacing it is a deliberate act by whoever
// knows the old process is finished, not a side effect of somebody else
// subscribing. What must not persist is two instances draining one queue, each
// receiving part of the mail.
//
// --force takes a held endpoint deliberately, the way unsubscribe --force
// frees one. It is a separate word because it is a separate decision. It does
// not create the queue differently, so mail already in the queue is kept: the
// new holder reads what the old one left.
func subscribeVerb(args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	endpoint := args[0]
	if err := model.ValidEndpoint(endpoint); err != nil {
		return err
	}
	var pidArg, agentType, version, display, role, cwd, address string
	force := false
	if err := parseFlags(args[1:], map[string]*string{
		"--pid": &pidArg, "--type": &agentType, "--version": &version,
		"--display": &display, "--role": &role, "--cwd": &cwd, "--address": &address,
	}, map[string]*bool{"--force": &force}); err != nil {
		return err
	}
	if pidArg == "" || agentType == "" || version == "" {
		return errUsage
	}
	pid, err := strconv.Atoi(pidArg)
	if err != nil || pid <= 0 {
		return fmt.Errorf("invalid --pid '%s': a process id is a positive number", pidArg)
	}

	held, err := model.Load(endpoint)
	if err != nil {
		return err
	}
	if held != nil && !force {
		return fmt.Errorf("endpoint '%s' is held by %s; free it with 'loc unsubscribe %s', "+
			"or take it with 'loc subscribe %s --force'",
			endpoint, incumbent(held), endpoint, endpoint)
	}

	// A pid that is ALREADY GONE is registered anyway, with no start time. The
	// supervisor is reporting what it launched; noticing that it did not
	// survive is the sweep's job, and refusing here would leave the caller
	// with an endpoint in no state at all.
	reg := &model.Registration{
		Endpoint:   endpoint,
		Instance:   model.Instance(endpoint),
		Agent:      model.Agent{Type: agentType, Version: version},
		Process:    model.Process{PID: pid, Started: model.StartedAt(pid)},
		Cwd:        cwd,
		Address:    address,
		Registered: model.Now(),
	}
	// Either half stands on its own: a deployment may name its agents without
	// saying what they are for, or say what they are for without renaming them.
	if display != "" || role != "" {
		reg.Display = &model.Display{Name: display, Role: role}
	}

	return withPresence(func(pr provider.Presence) error {
		if err := pr.CreateQueue(endpoint); err != nil {
			return err
		}
		if err := model.Save(reg); err != nil {
			return err
		}
		return emitJoin(pr, reg)
	})
}

// emitJoin publishes the one heavy event. Everything a consumer knows about an
// agent comes from here or from the registry, so it carries the whole
// registration and later events need repeat none of it.
func emitJoin(pr provider.Presence, reg *model.Registration) error {
	ev := model.NewEvent(model.KindSubscribe, reg.Endpoint)
	ev.Instance = reg.Instance
	ev.Agent = &reg.Agent
	ev.Process = &reg.Process
	ev.Display = reg.Display
	ev.Cwd = reg.Cwd
	ev.Address = reg.Address
	return emitEvent(pr, ev)
}

// emitDeparture publishes the leaving event. Leaving cleanly and leaving by
// expiry are the SAME OCCURRENCE with different causes, so they are one event
// with a reason rather than two kinds.
func emitDeparture(pr provider.Presence, endpoint, reason string) error {
	ev := model.NewEvent(model.KindUnsubscribe, endpoint)
	ev.Reason = reason
	return emitEvent(pr, ev)
}

func emitEvent(pr provider.Presence, ev model.Event) error {
	payload, err := ev.Marshal()
	if err != nil {
		return err
	}
	return pr.Emit(model.Instance(ev.Endpoint), payload)
}

// ---------------------------------------------------------------- unsubscribe

// unsubscribeVerb removes a registration, destroys the queue, and announces
// the departure.
//
// Plain unsubscribe is SAFE RECONCILIATION: an empty endpoint is a no-op, a
// dead incumbent is cleared, and a LIVE one is refused. That is what lets a
// supervisor relaunch an agent with an unconditional unsubscribe-then-
// subscribe — a crashed predecessor is cleared, a running one makes the
// unsubscribe refuse, and a restart can never displace a working agent by
// accident. --force is the deliberate displacement, and it is a separate word
// because it is a separate decision.
func unsubscribeVerb(args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	endpoint := args[0]
	if err := model.ValidEndpoint(endpoint); err != nil {
		return err
	}
	reason, force := "clean", false
	if err := parseFlags(args[1:],
		map[string]*string{"--reason": &reason},
		map[string]*bool{"--force": &force}); err != nil {
		return err
	}
	if reason != "clean" && reason != "expiry" {
		return fmt.Errorf("invalid --reason '%s': a departure is 'clean' or 'expiry'", reason)
	}

	reg, err := model.Load(endpoint)
	if err != nil {
		return err
	}
	if reg != nil && reg.Alive() && !force {
		return fmt.Errorf("endpoint '%s' is held by a LIVE agent, %s; stop that process first, "+
			"or displace it deliberately with 'loc unsubscribe %s --force'",
			endpoint, incumbent(reg), endpoint)
	}

	return withPresence(func(pr provider.Presence) error {
		attended, err := pr.QueueExists(endpoint)
		if err != nil {
			return err
		}
		if reg == nil && !attended {
			// Nothing was here. A no-op says nothing and announces nothing:
			// there was no departure to report.
			return nil
		}
		if err := pr.DeleteQueue(endpoint); err != nil {
			return err
		}
		if err := model.Remove(endpoint); err != nil {
			return err
		}
		return emitDeparture(pr, endpoint, reason)
	})
}

// ---------------------------------------------------------------------- sweep

// sweepVerb RECONCILES every instance it finds, or one when it is named. The
// ledger says who is here. The broker says which queues exist. This verb makes
// the two agree.
//
// THE BARE FORM KNOWS NOTHING IN ADVANCE. It reads every row on this machine
// and lists every namespaced queue on the broker. It does not take an instance
// from the caller's identity: a sweep reads two records rather than asking one
// instance's host a question, so there is nothing for one instance's name to
// scope. `sweep <instance>` is the narrow form, and it is the only form that
// takes a name.
//
// An agent that crashes cannot announce its own departure, so something must do
// it on the agent's behalf. The registration carries the pid, so this needs no
// detection, only a periodic look. It MUST RUN ON THE MACHINE HOLDING THE
// PROCESSES: a process id means nothing anywhere else.
//
// There are three passes and THEIR ORDER MATTERS.
//
//  1. Reap the rows whose process is gone.
//  2. Destroy the queues no row holds.
//  3. Remake the queues live rows have lost.
//
// Pass 1 runs before pass 3. A dead seat's row must be gone before pass 3 looks
// for rows without queues. In the other order pass 3 makes a queue for a dead
// seat, and pass 1 then has to destroy the queue pass 3 just made.
//
// The broker is asked for its listing ONCE, before any pass changes anything,
// so all three passes compare against one picture taken at one moment.
//
// A LISTING THAT FAILED STOPS THE WHOLE SWEEP, every instance, and that is
// deliberate. Ignorance is not an empty listing: a sweep that read a refusal as
// "no queues" would destroy every live seat's queue. The incident it records at
// that point is what makes the stop visible on a timer, which sees an exit code
// and nothing else.
func sweepVerb(w io.Writer, args []string) error {
	instance, rest := leadingWord(args)
	if len(rest) > 0 {
		return errUsage
	}
	if instance != "" {
		if err := model.ValidInstance(instance); err != nil {
			return err
		}
	}
	rows, unreadable, err := model.List(instance)
	if err != nil {
		return err
	}

	if err := withPresence(func(pr provider.Presence) error {
		queued, err := pr.Queues(instance)
		if err != nil {
			// The sweep stops here, so this record is the only place a timer
			// can learn what refused the listing.
			//
			// ONE REASON FOR BOTH FAULTS. A dial that never connected and a
			// listing that connected and then failed arrive here as the same
			// kind of value: internal/provider/nats keeps its
			// unreachable-medium sentinel unexported, and it wraps its listing
			// errors with %v rather than %w, so neither errors.Is nor a type
			// assertion can tell the two apart from this package. The detail
			// text carries the difference verbatim.
			//
			// IT IS RECORDED AND NOT PUBLISHED. The medium is the thing that
			// just failed, so an event about the failure would fail the same
			// way.
			model.LogIncident(model.NewIncident(instance, "", model.IncidentEnumerationRefused, err.Error()))
			return err
		}
		attended := make(map[string]bool, len(queued))
		for _, e := range queued {
			attended[e] = true
		}
		held := make(map[string]bool, len(rows))
		for _, r := range rows {
			held[r.Endpoint] = true
		}
		if err := reportUnreadableRows(pr, unreadable); err != nil {
			return err
		}

		// Pass 1. The process is gone, so everything that named it goes: the
		// queue, the registration, and the pidfile of the server that held the
		// seat.
		reaped := map[string]bool{}
		for _, r := range rows {
			if r.Alive() {
				continue
			}
			if err := pr.DeleteQueue(r.Endpoint); err != nil {
				return err
			}
			if err := model.Remove(r.Endpoint); err != nil {
				return err
			}
			if err := model.ReleaseServerPID(r.Endpoint); err != nil {
				return err
			}
			if err := emitDeparture(pr, r.Endpoint, "expiry"); err != nil {
				return err
			}
			reaped[r.Endpoint] = true
			fmt.Fprintf(w, "reaped %s: pid %d is gone\n", r.Endpoint, r.Process.PID)
		}

		// Pass 2. A queue no registration holds takes mail nobody will ever
		// read. It is destroyed, and the departure is announced, because a
		// consumer that saw the join must be told the seat is empty.
		//
		// AN UNREADABLE ROW SKIPS THIS WHOLE PASS. The row that could not be
		// read may be the row that holds one of these endpoints, and destroying
		// a live seat's queue on the strength of a row nobody could read is the
		// one outcome this pass must never produce.
		//
		// A row pass 1 reaped is still a row here, so a queue pass 1 destroyed
		// is not destroyed a second time.
		if len(unreadable) == 0 {
			for _, e := range queued {
				if held[e] {
					continue
				}
				if err := pr.DeleteQueue(e); err != nil {
					return err
				}
				if err := emitDeparture(pr, e, "expiry"); err != nil {
					return err
				}
				fmt.Fprintf(w, "destroyed the queue for %s: no registration holds it\n", e)
			}
		}

		// Pass 3. A live seat whose queue the broker has lost gets its queue
		// back, and the join is announced again so a consumer can hear it.
		//
		// THE ROW IS NOT REWRITTEN. The agent has been registered since it
		// subscribed, and a reconcile that restamped Registered would make
		// every sweep look like a new arrival.
		for _, r := range rows {
			if reaped[r.Endpoint] || attended[r.Endpoint] {
				continue
			}
			if err := pr.CreateQueue(r.Endpoint); err != nil {
				return err
			}
			if err := emitJoin(pr, r); err != nil {
				return err
			}
			fmt.Fprintf(w, "remade the queue for %s\n", r.Endpoint)
		}
		return nil
	}); err != nil {
		return err
	}

	if len(unreadable) > 0 {
		return unreadableRowError(unreadable)
	}
	return nil
}

// reportUnreadableRows records and announces one incident for each row the
// ledger could not read. The house detected the fault, so the house reports it
// both ways: a line in run/incidents for whoever looks later, and an event for
// whoever is watching now. The event goes out through emitEvent, which is how
// emitJoin publishes.
//
// The instance comes from the ROW, not from the verb's argument. A bare sweep
// has no instance of its own, and an incident belongs to the house that owns
// the row it is about.
func reportUnreadableRows(pr provider.Presence, unreadable []model.Unreadable) error {
	for _, u := range unreadable {
		ev := model.NewIncident(model.Instance(u.Endpoint), u.Endpoint, model.IncidentLedgerUnreadable, u.Err)
		model.LogIncident(ev)
		if err := emitEvent(pr, *ev); err != nil {
			return err
		}
	}
	return nil
}

// unreadableRowError is what a sweep that skipped pass 2 exits with. The sweep
// did not finish, so it must not report success: a timer reads the exit code
// and nothing else.
func unreadableRowError(unreadable []model.Unreadable) error {
	names := make([]string, 0, len(unreadable))
	for _, u := range unreadable {
		names = append(names, "'"+u.Endpoint+"'")
	}
	if len(names) == 1 {
		return fmt.Errorf("the registration for %s is not readable; pass 2 (orphan queues) skipped", names[0])
	}
	return fmt.Errorf("the registrations for %s are not readable; pass 2 (orphan queues) skipped",
		strings.Join(names, ", "))
}

// ----------------------------------------------------------------------- emit

// emitVerb publishes one lifecycle or activity event.
//
// THIS IS THE ONE VERB THAT DOES NOT REPORT AN UNREACHABLE MEDIUM. An adapter
// runs inside the agent's own lifecycle hook, so anything it waits on the
// agent waits on, and anything it fails at the agent fails at. A presence
// system that cannot say what an agent is doing is a display problem; an agent
// that cannot work is a real one. So the event is dropped and the exit is
// clean. Every other verb treats an unreachable medium as the failure it is.
func emitVerb(args []string) error {
	if len(args) < 2 {
		return errUsage
	}
	kind, endpoint := args[0], args[1]
	if !model.ValidKind(kind) {
		return errUsage
	}
	if err := model.ValidEndpoint(endpoint); err != nil {
		return err
	}
	var ts, tool, refs string
	if err := parseFlags(args[2:], map[string]*string{
		"--ts": &ts, "--tool": &tool, "--refs": &refs,
	}, nil); err != nil {
		return err
	}

	ev := model.NewEvent(kind, endpoint)
	if ts != "" {
		// Carried VERBATIM. The timestamp is when the thing happened, stamped
		// by whoever it happened to — not when this process got round to
		// publishing it, which is the only thing this process could know.
		ev.TS = ts
	}
	ev.Tool = tool
	if refs != "" {
		ev.Refs = strings.Split(refs, ",")
	}

	// The local record is updated whether or not the medium can be reached: an
	// event that could not be published still happened, and a reader asking
	// later deserves the truth about it.
	if model.RecordsActivity(kind) {
		if _, err := model.ApplyActivity(endpoint, ev.TS, kind); err != nil {
			return err
		}
	}
	payload, err := ev.Marshal()
	if err != nil {
		return err
	}
	_ = withPresence(func(pr provider.Presence) error { return pr.Emit(model.Instance(endpoint), payload) })
	return nil
}

// -------------------------------------------------------------------- registry

// registryVerb asks an instance's supervisor who is registered right now.
//
// Events describe CHANGES and the bus keeps no history, so a consumer that
// starts after an agent subscribed has missed the only event carrying that
// agent's details. It asks for the current picture instead of replaying a
// history that does not exist. The supervisor answers because the supervisor
// holds the registrations — it created them — and if none is running the
// request goes unanswered, which is the truthful reply and a FAILURE rather
// than an empty roster.
func registryVerb(w io.Writer, args []string) error {
	instance, rest := leadingWord(args)
	asJSON := false
	if err := parseFlags(rest, nil, map[string]*bool{"--json": &asJSON}); err != nil {
		return err
	}
	instance, err := instanceOf(instance)
	if err != nil {
		return err
	}
	if err := model.ValidInstance(instance); err != nil {
		return err
	}
	subject := "registry." + instance
	return withPresence(func(pr provider.Presence) error {
		reply, err := pr.Request(subject, registryTimeout)
		if err != nil {
			return fmt.Errorf("no host is answering %s (the request went unanswered)", subject)
		}
		return renderRegistry(w, reply, asJSON)
	})
}

// renderRegistry prints a roster. --json hands back what the supervisor said,
// unread: a consumer wants the reply, and a reply reshaped by this tool is a
// second format for it to learn.
func renderRegistry(w io.Writer, reply []byte, asJSON bool) error {
	if asJSON {
		_, err := fmt.Fprintln(w, strings.TrimRight(string(reply), "\n"))
		return err
	}
	var roster struct {
		Agents []model.Registration `json:"agents"`
	}
	if err := json.Unmarshal(reply, &roster); err != nil {
		return fmt.Errorf("the answer on this subject is not a registry: %v", err)
	}
	if len(roster.Agents) == 0 {
		_, err := fmt.Fprintln(w, "(no agents registered)")
		return err
	}
	for _, a := range roster.Agents {
		role := ""
		if a.Display != nil && a.Display.Role != "" {
			role = "  (" + a.Display.Role + ")"
		}
		if _, err := fmt.Fprintf(w, "%-24s %s %s  pid %d  since %s  %s%s\n",
			a.Endpoint, a.Agent.Type, a.Agent.Version, a.Process.PID, a.Registered, a.Cwd, role); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------- watch

// watchVerb follows an instance's events, read-only, until the connection ends.
func watchVerb(w io.Writer, args []string) error {
	instance, rest := leadingWord(args)
	if len(rest) > 0 {
		return errUsage
	}
	instance, err := instanceOf(instance)
	if err != nil {
		return err
	}
	if err := model.ValidInstance(instance); err != nil {
		return err
	}
	return withPresence(func(pr provider.Presence) error { return pr.Watch(instance, w) })
}

// --------------------------------------------------------------------- status

// statusEndpoint reports one agent's three facts. It touches NO MEDIUM: every
// answer it gives is held locally, which is what lets it answer at all when
// the broker is the thing that is wrong.
func statusEndpoint(w io.Writer, endpoint string) error {
	if err := model.ValidEndpoint(endpoint); err != nil {
		return err
	}
	reg, err := model.Load(endpoint)
	if err != nil {
		return err
	}
	act, err := model.LoadActivity(endpoint)
	if err != nil {
		return err
	}
	window, err := time.ParseDuration(config.Get("idle_window", "10m"))
	if err != nil {
		return fmt.Errorf("invalid idle_window in this deployment's config: %v", err)
	}
	// An unregistered endpoint is a truthful answer to a fair question, not a
	// failure: the caller asked whether anyone is there, and nobody is.
	_, err = io.WriteString(w, model.Derive(endpoint, reg, act, time.Now(), window))
	return err
}
