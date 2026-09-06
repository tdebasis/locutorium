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
func subscribeVerb(args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	endpoint := args[0]
	if err := model.ValidEndpoint(endpoint); err != nil {
		return err
	}
	var pidArg, agentType, version, display, role, cwd string
	if err := parseFlags(args[1:], map[string]*string{
		"--pid": &pidArg, "--type": &agentType, "--version": &version,
		"--display": &display, "--role": &role, "--cwd": &cwd,
	}, nil); err != nil {
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
	if held != nil {
		return fmt.Errorf("endpoint '%s' is held by %s; free it with 'loc unsubscribe %s'",
			endpoint, incumbent(held), endpoint)
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

// sweepVerb unsubscribes every registration whose process is gone.
//
// An agent that crashes cannot announce its own departure, so something must
// do it on its behalf — and because the registration carries the pid, this
// needs no detection, only a periodic look. It MUST RUN ON THE MACHINE HOLDING
// THE PROCESSES: a process id means nothing anywhere else.
func sweepVerb(args []string) error {
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
	regs, err := model.List(instance)
	if err != nil {
		return err
	}
	var gone []*model.Registration
	for _, r := range regs {
		if !r.Alive() {
			gone = append(gone, r)
		}
	}
	if len(gone) == 0 {
		// Idempotent, and audibly so: a sweep with nothing to reap publishes
		// NOTHING. The absence of a second departure is what makes it safe to
		// run on a timer.
		return nil
	}
	return withPresence(func(pr provider.Presence) error {
		for _, r := range gone {
			if err := pr.DeleteQueue(r.Endpoint); err != nil {
				return err
			}
			if err := model.Remove(r.Endpoint); err != nil {
				return err
			}
			if err := emitDeparture(pr, r.Endpoint, "expiry"); err != nil {
				return err
			}
		}
		return nil
	})
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
