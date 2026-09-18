package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tdebasis/locutorium/internal/config"
	"github.com/tdebasis/locutorium/internal/loc"
	"github.com/tdebasis/locutorium/internal/mcpserve"
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
	name := config.Value(config.Provider)
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
func subscribeVerb(args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	endpoint := args[0]
	if err := model.ValidEndpoint(endpoint); err != nil {
		return err
	}
	var pidArg, agentType, version, display, role, cwd, address string
	if err := parseFlags(args[1:], map[string]*string{
		"--pid": &pidArg, "--type": &agentType, "--version": &version,
		"--display": &display, "--role": &role, "--cwd": &cwd, "--address": &address,
	}, nil); err != nil {
		return err
	}
	if pidArg == "" || agentType == "" || version == "" {
		return errUsage
	}
	// THE TYPE IS A CLOSED SET, because it chooses the notifier that rings
	// this seat (internal/mcpserve/notify.go, R28). A type with no notifier
	// behind it would register a seat that reads as reachable and rings
	// nothing, and the deployment would find that out by being told nothing.
	if err := mcpserve.ValidListenerType(agentType); err != nil {
		return err
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
		Address:    address,
		Registered: model.Now(),
	}
	// Either half stands on its own: a deployment may name its agents without
	// saying what they are for, or say what they are for without renaming them.
	if display != "" || role != "" {
		reg.Display = &model.Display{Name: display, Role: role}
	}

	// THE ROW IS WRITTEN FIRST, AND THE ORDER IS THE WHOLE SAFETY ARGUMENT.
	// Two writes make a subscribe, and a beat can land between them. Create
	// the queue first and the gap is a queue with no row, which is precisely
	// what the sweep's orphan pass destroys: a seat that subscribed during a
	// beat would be registered and unreachable until the next beat rebuilt it.
	// Write the row first and the gap is a row with no queue, which the
	// sweep's third pass repairs. The rule is general — order the two writes
	// so the gap falls where the reconciler's action matches the caller's
	// intent — and here the row's presence is what "arrived" means.
	return withPresence(func(pr provider.Presence) error {
		if err := model.Save(reg); err != nil {
			return err
		}
		betweenSubscribeWrites()
		if err := pr.CreateQueue(endpoint); err != nil {
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
		// The row goes first, for subscribe's reason read backwards. Delete
		// the queue first and the gap is a row with no queue, which the sweep
		// repairs — the daemon rebuilding a queue the caller just asked to be
		// destroyed, and undoing its own work on the next beat. Remove the row
		// first and the gap is a queue with no row, which the sweep's orphan
		// pass finishes on the caller's behalf while another row still holds
		// the instance. When this seat was the instance's last, no row is left
		// to account for the instance, the orphan pass does not reach the
		// queue, and running `loc unsubscribe` again is what removes it.
		if err := model.Remove(endpoint); err != nil {
			return err
		}
		betweenUnsubscribeWrites()
		if err := pr.DeleteQueue(endpoint); err != nil {
			return err
		}
		// WHICH OF THE THREE DEPARTURES THIS WAS. The verb already knows, and
		// nothing downstream can work it out: the queue is gone, and the row
		// went with it. `--reason expiry` is a registration that aged out.
		// A `--force` over a live incumbent is one agent taking a seat from
		// another, which is the case somebody comes looking for when a seat
		// stops receiving mail it was sent. Everything else is an agent
		// leaving its own seat, which is the ordinary end of a session.
		why := "left"
		switch {
		case reason == "expiry":
			why = "expired"
		case force && reg != nil && reg.Alive():
			why = "displaced"
		}
		loc.LogQueueDeleted(endpoint, why, time.Now().UTC())
		return emitDeparture(pr, endpoint, reason)
	})
}

// ---------------------------------------------------------------------- sweep

// betweenSubscribeWrites and betweenUnsubscribeWrites are the seam the F1
// cases need. A subscribe and an unsubscribe are each two writes with a gap
// between them, and the gap is where a beat does damage if the writes are
// ordered wrongly; a test cannot land a beat in a gap it cannot reach. They do
// nothing in the shipped binary.
var (
	betweenSubscribeWrites   = func() {}
	betweenUnsubscribeWrites = func() {}
)

// sweepVerb reconciles the three records a seat has: its ledger row, its queue
// and its process.
//
// IT TAKES NO ARGUMENT. A process id means something only on the machine that
// holds it, so a sweep is a machine-wide act by nature. There is still no
// instance argument, because the ledger's own rows say which instances this
// sweep may touch. A queue in any other instance is not a finding, so it is
// never listed here and never deleted. It returns the number of changes it
// made so the daemon can write that number in its heartbeat log.
//
// IT PUBLISHES NOTHING. A departure event says an agent left; a reap says a
// record was wrong. Emitting one for the other told every listener that an
// agent had just gone at the moment a stale record was tidied, which is a
// different fact and, for a queue that had been orphaned for a day, a false
// one.
//
// It is silent when it changes nothing, which is what makes it safe to run
// every five minutes.
func sweepVerb(w io.Writer, args []string) (changes int, err error) {
	if len(args) > 0 {
		return 0, errUsage
	}
	rows, unreadable, err := model.ListAll()
	if err != nil {
		return 0, err
	}
	err = withPresence(func(pr provider.Presence) error {
		queues, err := pr.Queues()
		if err != nil {
			return err
		}
		findings := model.Findings(rows, unreadable, queues)
		// THE ORPHAN PASS IS SKIPPED ENTIRELY, NOT ROW BY ROW. An unreadable
		// row may be the one that claims a queue this pass would destroy, and
		// there is no way to ask which. Skipping only the rows in question is
		// impossible for exactly the same reason it is unsafe to proceed.
		blocked := false
		for _, f := range findings {
			if f.Kind == model.UnreadableRow {
				blocked = true
			}
		}
		for _, f := range findings {
			switch f.Kind {
			case model.DeadPID:
				if err := pr.DeleteQueue(f.Endpoint); err != nil {
					return err
				}
				// `expired` and not `left`: the row outlived the process it
				// named. Nobody unsubscribed, so no agent decided this.
				loc.LogQueueDeleted(f.Endpoint, "expired", time.Now().UTC())
				if err := model.Remove(f.Endpoint); err != nil {
					return err
				}
				if err := model.ReleaseServerPID(f.Endpoint); err != nil {
					return err
				}
				changes++
				if _, err := fmt.Fprintf(w, "%s: reaped, the process is gone\n", f.Endpoint); err != nil {
					return err
				}
			case model.OrphanQueue:
				if blocked {
					continue
				}
				if err := pr.DeleteQueue(f.Endpoint); err != nil {
					return err
				}
				// A queue no row claims, inside an instance this ledger
				// holds. The line is written for exactly the question this
				// pass is dangerous for: a queue that held mail is gone, and
				// the record has to say a sweep took it rather than leaving
				// it to be guessed at.
				loc.LogQueueDeleted(f.Endpoint, "orphan", time.Now().UTC())
				changes++
				if _, err := fmt.Fprintf(w, "%s: queue deleted, no row holds it\n", f.Endpoint); err != nil {
					return err
				}
			case model.MissingQueue:
				// The row is not rewritten. The row was right; the medium had
				// lost the queue, and the repair is the queue alone.
				if err := pr.CreateQueue(f.Endpoint); err != nil {
					return err
				}
				changes++
				if _, err := fmt.Fprintf(w, "%s: queue recreated\n", f.Endpoint); err != nil {
					return err
				}
			case model.UnreadableRow:
				if _, err := fmt.Fprintf(w, "%s: row unreadable: %v\n", f.Endpoint, f.Err); err != nil {
					return err
				}
			}
		}
		if blocked {
			return fmt.Errorf("a ledger row could not be read, so no queue was deleted as an orphan; " +
				"repair or remove the row named above and sweep again")
		}
		return nil
	})
	return changes, err
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
	window, err := time.ParseDuration(config.Value(config.IdleTimeout))
	if err != nil {
		return fmt.Errorf("invalid idle_timeout in this deployment's config: %v", err)
	}
	// An unregistered endpoint is a truthful answer to a fair question, not a
	// failure: the caller asked whether anyone is there, and nobody is.
	_, err = io.WriteString(w, model.Derive(endpoint, reg, act, time.Now(), window))
	return err
}

// --------------------------------------------------------------- bare status

// statusVerb is the deployment's report: the daemon, then every seat, then the
// mail waiting for each of them.
//
// IT IS A DRY RUN OF THE NEXT BEAT, and it performs none of it. It reads the
// same findings the sweep acts on, so the two can never disagree about what is
// wrong, and it writes nothing at all — no queue created, no row removed, no
// sweep triggered. A read that repairs what it reports is an instrument that
// destroys its own evidence: the operator asks twice and gets two different
// answers, neither of which is the state that was there when they asked.
func statusVerb(w io.Writer) error {
	// THE REPORT IS COMPOSED BEFORE ANY OF IT IS PRINTED. A verb that fails
	// writes nothing to standard out — the rule every other verb here keeps —
	// and a status report is three sections deep, so streaming it would leave
	// a caller holding the first section of a report that failed at the third.
	var report strings.Builder
	if _, err := io.WriteString(&report, daemonLine()); err != nil {
		return err
	}
	if err := statusBody(&report); err != nil {
		return err
	}
	_, err := io.WriteString(w, report.String())
	return err
}

// statusBody writes the two sections that need the medium: what the next beat
// would repair, and what mail is waiting.
func statusBody(w io.Writer) error {
	name := config.Value(config.Provider)
	p, err := provider.Open(name)
	if err != nil {
		return err
	}
	defer p.Close()
	// A medium that carries mail without carrying presence has no queues to
	// compare the rows against, so the per-seat findings are simply absent.
	// The unread report below is the message plane's and does not need them.
	if pr, ok := p.(provider.Presence); ok {
		if err := seatLines(w, pr); err != nil {
			return err
		}
	}
	return p.Status(w)
}

// daemonLine is the first line of the report: whether the Locutorium's own
// daemon is running, and when it last beat.
//
// BOTH FILES MAY BE ABSENT AND NEITHER ABSENCE IS AN ERROR. The daemon writes
// them; on a deployment where it has never run there is nothing to read, and
// "not running" is the true answer rather than a failure to look.
func daemonLine() string {
	pid, started, ok := readDaemonPID()
	if !ok || !model.Alive(pid, started) {
		return "daemon: not running\n"
	}
	return fmt.Sprintf("daemon: running, pid %d, last beat %s\n", pid, lastBeat())
}

// readDaemonPID reads the two lines the daemon's pidfile holds: its pid and
// its start time, the same shape a seat's server pidfile has.
func readDaemonPID() (pid int, started string, ok bool) {
	b, err := os.ReadFile(model.DaemonPIDFile())
	if err != nil {
		return 0, "", false
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	pid, err = strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || pid <= 0 {
		return 0, "", false
	}
	if len(lines) > 1 {
		started = strings.TrimSpace(lines[1])
	}
	return pid, started, true
}

// lastBeat is the final line of today's heartbeat log, or a plain statement
// that there is not one yet. A daemon that has started and not yet beaten is a
// real state, and it is not the same as a daemon that is not running.
func lastBeat() string {
	path := filepath.Join(config.Home(), "run", "heartbeat", time.Now().Format("2006-01-02")+".log")
	b, err := os.ReadFile(path)
	if err != nil {
		return "no beat yet"
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "" {
		return "no beat yet"
	}
	return last
}

// seatLines prints one line per seat, and one per queue that no seat claims
// inside an instance the ledger holds a row for, saying what the next beat
// would do about it. A queue in any other instance is not reported, because
// the next beat would not touch it.
func seatLines(w io.Writer, pr provider.Presence) error {
	rows, unreadable, err := model.ListAll()
	if err != nil {
		return err
	}
	queues, err := pr.Queues()
	if err != nil {
		return err
	}
	bells := bellFailures(rows)
	trouble := map[string]string{}
	var orphans []string
	for _, f := range model.Findings(rows, unreadable, queues) {
		switch f.Kind {
		case model.DeadPID:
			trouble[f.Endpoint] = "pid dead, next beat reaps it"
		case model.MissingQueue:
			trouble[f.Endpoint] = "queue missing, next beat repairs it"
		case model.UnreadableRow:
			trouble[f.Endpoint] = fmt.Sprintf("row unreadable: %v", f.Err)
		case model.OrphanQueue:
			orphans = append(orphans, f.Endpoint)
		}
	}
	// Every seat the ledger names, readable or not, in one order.
	seats := make([]string, 0, len(rows)+len(unreadable))
	for _, r := range rows {
		seats = append(seats, r.Endpoint)
	}
	for _, u := range unreadable {
		seats = append(seats, u.Endpoint)
	}
	sort.Strings(seats)
	for _, e := range seats {
		state, wrong := trouble[e]
		if !wrong {
			state = "row ok, queue ok"
		}
		// The bell field is appended to whatever the row already says. A seat
		// can have a missing queue AND a dead bell, and the operator needs
		// both: the next beat repairs the one and repairs nothing about the
		// other.
		if ev, ok := bells[e]; ok {
			state += fmt.Sprintf(", bell FAILED %s: %s", ev.TS, ev.Reason)
		}
		if _, err := fmt.Fprintf(w, "%s: %s\n", e, state); err != nil {
			return err
		}
	}
	for _, q := range orphans {
		if _, err := fmt.Fprintf(w, "%s: queue with no row, next beat removes it\n", q); err != nil {
			return err
		}
	}
	return nil
}

// bellFailures is the THIRD FACT of the report: for each registered seat, the
// bell failure that still stands.
//
// `row ok, queue ok` answers two questions — the registry row exists, and the
// queue exists. Neither of them says the seat's operator can be told that mail
// arrived. On 2026-09-11 two seats were registered and attending with a dead
// bell, and `status` said nothing was wrong (#79). The message log carries a
// `bell-failed` seat event, and this reads it.
//
// A failure stands when it is LATER THAN THE REGISTRATION and no `read` by
// that seat came after it. The registration bound drops a failure from a
// previous occupant of the endpoint, whose bell is not this seat's bell. The
// read bound drops a failure the seat has already answered: a seat that took
// its mail has shown it can be reached, whatever the bell did.
//
// The last `bell-failed` wins, and "last" is the order the lines were
// appended in rather than the order of their stamps. The log is append-only
// and every writer stamps a line as it writes it, so the two agree; where a
// clock has stepped, the record's own order is the one this reports.
func bellFailures(rows []*model.Registration) map[string]loc.Event {
	out := map[string]loc.Event{}
	// A LOG THAT CANNOT BE READ ADDS NOTHING AND FAILS NOTHING. An absent
	// directory, an unreadable one — the report still owes the operator the
	// two facts it has always given, and a deployment that has never sent a
	// message has no log at all. Losing the field is the right cost here;
	// losing the report is not.
	events, err := loc.ReadEvents(loc.LogDir())
	if err != nil {
		return out
	}
	failed := map[string]loc.Event{}
	lastRead := map[string]time.Time{}
	for _, ev := range events {
		at, ok := ev.At()
		if !ok {
			continue
		}
		switch {
		case ev.Seat != "" && ev.Status == "bell-failed":
			failed[ev.Seat] = ev
		case ev.UID != "" && ev.Status == "read" && ev.By != "":
			if at.After(lastRead[ev.By]) {
				lastRead[ev.By] = at
			}
		}
	}
	for _, r := range rows {
		ev, ok := failed[r.Endpoint]
		if !ok {
			continue
		}
		at, _ := ev.At() // parsed above; a line that failed it is not in the map
		// A registration this code cannot parse is not a bound it can apply,
		// so the failure is not reported rather than reported unbounded.
		reg, err := time.Parse(time.RFC3339, r.Registered)
		if err != nil {
			continue
		}
		// THE TWO STAMPS HAVE DIFFERENT PRECISION. A registration is written
		// in milliseconds; a bell-failed line in whole seconds. The server
		// registers and then rings the backlog with no window, so a notifier
		// that is dead at startup fails within the same second, and a strict
		// "after the registration" comparison hides exactly the failure #79
		// was opened for (Assayer F1, 2026-09-16: 82ms after → hidden). So
		// the failure is hidden only when it is before the registration's
		// own second.
		if at.Before(reg.Truncate(time.Second)) {
			continue
		}
		if lastRead[r.Endpoint].After(at) {
			continue
		}
		out[r.Endpoint] = ev
	}
	return out
}
