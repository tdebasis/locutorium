# Locutorium — agent presence, activity, and lifecycle

*How Locutorium knows which agents exist, whether they are alive, and what they are doing.*

> Seed material for external documentation. Written to be publishable: no local paths, no
> deployment-specific names. Implementation planning lives elsewhere.

## Scope

Locutorium is a message bus for software agents. This document describes how it answers three
questions about them, why the answers are separated the way they are, and what delivery guarantees
follow from those answers.

It does not describe message addressing or the wire format.

## Vocabulary

| Term | Meaning |
|---|---|
| **agent** | A program that does work and can be talked to. A coding CLI, a script — anything with a process. |
| **agent instance** | One run of an agent. Restarting an agent produces a new instance. |
| **endpoint** | A named address on the bus. An agent instance occupies one while it runs. |
| **broker** | The message-passing service itself — what knows which endpoints are connected. |
| **host** | The application that supervises agents: creates their workspace, launches them, registers them with the broker. |
| **consumer** | Anything reading the event stream — a display, a router, a host rebuilding its picture. Agents are not automatically consumers. |
| **adapter** | A small translator, one per kind of agent, converting that agent's native lifecycle signals into the bus's events. |
| **unit of work** | One request-and-response cycle. For a conversational CLI, a single turn. |
| **idle window** | How long activity events may be absent before an agent is treated as idle. Set per deployment; may differ per agent type, since some report activity more sparsely than others. |
| **parlor** | A scope of the bus with its own delivery guarantees. See *Inner and outer parlors*. |

## Three questions, three mechanisms

The central rule: **each fact is answered by whatever already knows it.** Nothing infers another
party's fact from a side effect.

| Question | Meaning | Answered by |
|---|---|---|
| **Reachable** | Will a message sent to this endpoint be received? | The broker's own connection state |
| **Alive** | Is a process running for this agent? | **The operating system**, from a recorded process id and start time |
| **Working** | Is it doing something right now? | Events the agent emits through its adapter |

These are independent. An agent can be reachable and alive but idle; alive but unreachable if its
connection dropped.

**Liveness is a process id — within one machine.** Asking the operating system whether a process
exists works identically whatever that process is, which makes it the one presence signal that needs
no per-agent knowledge. Every alternative — matching a process name, watching a file the agent
writes, waiting for the agent to answer — requires knowing something specific about that kind of
agent.

🔴 **This is an inner-parlor mechanism only.** A process id means nothing on another machine.
Anything checking one must run on the same machine as the process, which rules out a remote medium —
**Locutorium runs the expiry check**, from its local component beside the processes it is watching.

**A process id alone is not an identity.** Operating systems reuse them. A registration therefore
records the process id *and its start time*; the pair is unambiguous where the number alone is not.

## How an agent joins

1. The host prepares the agent's workspace.
2. **The host launches the agent** — and therefore holds its process id.
3. The host subscribes the agent to the bus, passing its details, **including the process id**.

**Why the host launches.** Whoever runs a command is the only party that receives the process id for
free. Any other arrangement requires *searching* for the agent — matching a process name, or
inspecting a terminal — which is specific to each kind of agent, breaks when one is nested inside a
wrapper, and grows a new special case for every agent added.

Registration is therefore **mechanical**: it happens because the host created the agent, not because
the agent remembered to announce itself. Conventions an agent must remember are the ones that fail
quietly.

**If the agent fails to start, the host says so.** The host is the only party that expected a
registration, so it is the only one that can notice its absence — and absence is not an event.
A failure to launch is announced, not inferred from silence.

**What a registration carries:** the endpoint identifier · the agent's **type and version** ·
display information · the process id and its start time · the working directory.

The type is what lets everything downstream stay uniform: register once with it, and later events
need not repeat it. It is also where per-agent allowances belong — a consumer that knows an agent
type reports activity sparsely can widen the idle window for that type, with no special case in the
event stream.

## How an agent leaves

Two ways, and they are the same event with different reasons:

- **`reason: clean`** — the host unsubscribed it deliberately.
- **`reason: expiry`** — the process is gone. A periodic check finds the recorded process id no
  longer alive and unsubscribes on the agent's behalf.

An agent that crashes cannot announce its own departure, so something must do it for them. Because
the registration carries the process id, this needs no detection — only a periodic check that the
recorded process still exists, running on that process's machine.

**A subscription does not outlive its agent by more than one check interval** — and only while the
check is running. It is not structurally impossible for a registration to persist after its agent;
it is bounded by how often something looks. Deployments should size that interval accordingly.

An agent that restarts subscribes again, as a new instance, with a new process id.

## Subjects, endpoints and queues

### Endpoint names are namespaced by instance

```
<instance>.<agent>          e.g.  workshop.scribe
```

More than one host may run on a machine, sharing a broker. Namespacing by instance makes a collision
**impossible by construction** rather than avoided by convention — two hosts may each have an agent
called `scribe` without either being able to receive the other's mail. Relying on everyone choosing
distinct names does not survive somebody copying a configuration.

### Where things are published

| Purpose | Subject |
|---|---|
| Messages **to** an agent | `queue.<instance>.<agent>` |
| Lifecycle and activity **events** | `topic.<instance>` |

**Events go to one subject per instance**, not one per agent. A consumer watching an instance sees
every agent in it with a single subscription, and sees nothing from any other instance.

**Publishers** to the events subject are the host and the adapters. **Consumers** are anything
building a picture — a display, a router, a host restarting. Agents do not subscribe to it by
default; being an agent and being a consumer are separate roles.

### Queue lifetime

In the inner parlor the queue is created when an agent subscribes and destroyed when it
unsubscribes. There is no queue for an agent that is not running, and therefore no mailbox to
accumulate, drain or reconcile.

**A note for implementers.** Subjects and the durable objects backing them are often named under
different rules — a message broker may allow characters in a subject that it forbids in a stream or
consumer name, dots being the common case. Where that is so, the backing object's name is derived
from the endpoint by substitution:

```
subject          queue.workshop.scribe
backing object   QUEUE_workshop_scribe
```

The substitution must be **injective** — no two endpoints may map to the same object name — which is
why endpoint names are restricted to a character set that makes it so.

## Events

### Taxonomy

Event kinds are `<subject>.<verb>`. Two subjects today — the agent's membership, and its work.

| `kind` | Meaning | Emitted by |
|---|---|---|
| `agent.subscribe` | An agent instance joined; carries the full registration | host |
| `agent.unsubscribe` | It left; carries `reason` | host, or the expiry check |
| `activity.start` | A unit of work began | adapter |
| `activity.end` | It finished | adapter |
| `tool.pre` | A tool call is beginning | adapter |
| `tool.post` | A tool call has finished | adapter |

A new subject is added when a genuinely different *thing* is being reported. A new **reason** or
field is added when the same thing happens for a different cause. See *Naming rule* below.

### Common envelope

Every event is a bus message. These fields are always present:

| Field | Type | Notes |
|---|---|---|
| `id` | string | Unique per event. Used for exact deduplication and for `refs` to point at. |
| `ts` | RFC 3339, UTC, millisecond precision | **When the thing happened**, stamped by the emitter — not when it was published, and not the broker's receipt time. |
| `kind` | string | From the taxonomy above. |
| `endpoint` | string | Which agent this concerns. |
| `refs` | array of `id` | Optional. Relates this event to earlier ones. |

### Payloads

**`agent.subscribe`** — the only event carrying the full picture. Everything a consumer knows about
an agent comes from here or from the registry.

```json
{
  "id": "ev_7f3a91c2",
  "ts": "2026-01-14T09:12:04.318Z",
  "kind": "agent.subscribe",
  "endpoint": "scribe",
  "instance": "workshop",
  "agent":   { "type": "acme-cli", "version": "3.2.0" },
  "process": { "pid": 48213, "started": "2026-01-14T09:12:04.006Z" },
  "display": { "name": "The Scribe", "role": "Records" },
  "cwd": "/…/workspaces/scribe"
}
```

`process.started` is the operating system's start time for that pid. The pair `(pid, started)` is the
identity; the number alone is not, because operating systems reuse them.

**`agent.unsubscribe`**

```json
{
  "id": "ev_a02d5518",
  "ts": "2026-01-14T11:47:52.902Z",
  "kind": "agent.unsubscribe",
  "endpoint": "scribe",
  "reason": "expiry"
}
```

`reason` is `clean` (deliberately unsubscribed) or `expiry` (the process is gone). Further causes are
added as **values here**, never as new event kinds.

**`activity.start`** and **`activity.end`** — no fields beyond the envelope.

```json
{ "id": "ev_c41b0d7e", "ts": "2026-01-14T09:31:20.114Z",
  "kind": "activity.start", "endpoint": "scribe" }
```

**`tool.pre`** and **`tool.post`** — `tool` names what is being run. `tool.post` references the
`tool.pre` it closes, because agents may run tools in parallel and a consumer otherwise cannot tell
which finish belongs to which start.

```json
{ "id": "ev_15c8ff40", "ts": "2026-01-14T09:31:21.006Z",
  "kind": "tool.pre",  "endpoint": "scribe", "tool": "shell" }

{ "id": "ev_9be271aa", "ts": "2026-01-14T09:31:23.882Z",
  "kind": "tool.post", "endpoint": "scribe", "tool": "shell",
  "refs": ["ev_15c8ff40"] }
```

### Notes on the fields

**Only `agent.subscribe` is heavy.** Every later event is an id, a timestamp, a kind and an endpoint.
A consumer already knows the agent's type, version and display from the registration, so nothing
repeats it.

**`ts` is emitter-stamped, and this is load-bearing.** Hooks fire as separate short-lived processes —
one per turn start, one per tool call, one per turn end — each making its own connection. There is
therefore no single-publisher ordering guarantee *between an agent's own events*, and two in quick
succession can arrive in either order. The timestamp is what lets a consumer tell which is newer.

**Unknown fields are ignored, not rejected.** A consumer written against this document must tolerate
fields it does not recognise, so that events can gain detail without every consumer changing.

**Naming rule.** *The same thing happening for different reasons is one event with a reason field.
Different things happening are different events.* Leaving cleanly and leaving by expiry are the same
occurrence with different causes, so they share an event. Beginning a tool call and finishing one
are different moments, so they do not.

### Deriving state from events

```
alive, no activity events            → idle
activity.start, then tool events     → active
activity.end                         → idle, immediately
no events for the idle window        → idle, by timeout
process id no longer alive           → away
```

**This derivation is order-dependent, and three things contain that.**

**Consumers discard stale events rather than ordering them.** Each consumer keeps, per agent, the
timestamp of the last event it applied, and drops anything older. An `activity.start` from 10:04:25
arriving after an `activity.end` from 10:04:30 is recognised as an older fact and ignored, rather
than flipping the agent back to *active*. This also deduplicates redelivery for free.

Discarding is safe only because the bus keeps no history: a consumer holds a current picture, not a
record, so an old event that is dropped loses nothing. It would be the wrong behaviour for something
building an archive.

**Events that must be ordered come from one emitter.** A single publisher's messages arrive in the
order they were sent, so `activity.start` and `activity.end` — both from the same adapter — cannot
be seen reversed. Events from *different* emitters have no such guarantee, which is why the ones that
matter are deliberately independent facts rather than a sequence (see *How an agent joins*).

**And the idle window bounds the rest.** A consumer that has wrongly concluded *active* returns to
*idle* once the window passes with nothing further.

**Absence of activity events means idle. It never means away.** An agent waiting for instructions
emits nothing; that is its resting state, not a fault. Away is a process question and only a process
question.

**The end event is an optimisation, not a dependency.** When it arrives, the transition is immediate.
When it is lost, the idle window covers it. Nothing depends on a departure being successfully
announced — which is the failure mode of a design built on start-and-end alone.

**Tool events are the activity signal, not a manufactured tick.** Working agents use tools
frequently, so their own work supplies the pulse. Nothing has to be kept alive or shut down cleanly.
A stretch of work using no tools produces no events; the idle window is sized to ride that out.

## Failure and degradation

**Publishing an event must never block the agent.** An adapter runs inside the agent's own lifecycle
hook, so anything it waits on, the agent waits on. If the broker is unavailable or slow, the adapter
**drops the event and returns immediately.** Presence degrades; work does not stop.

This is a deliberate ranking: an agent that cannot report what it is doing is a display problem, an
agent that cannot work is a real one. It also means the event stream is **best-effort** — a consumer
must not treat a missing event as evidence of anything.

The idle window is what makes that survivable. A dropped `activity.end` resolves itself when the
window passes; a dropped `tool.pre` costs nothing; a dropped `agent.subscribe` is the one real loss,
and is recoverable by asking the registry.

**If the medium is unavailable, the operation returns an error immediately. There is no retry.**

Whether to start the agent anyway, try again, or refuse outright is policy, and policy belongs to
whoever is calling. Locutorium reports the failure accurately and does not decide what should be done
about it.

*A note on why there is no retry, so it is not mistaken for an oversight.* A bounded retry would
cover one case: a medium still starting up while a host races it. That case does not arise where the
medium starts with the machine and hosts are started by hand afterwards — and adding the retry
anyway would mean specifying behaviour for a situation nobody has observed, which later reads as
evidence the situation occurs. **Add it if it is ever seen**, most likely when a host becomes
something that starts automatically at login and the two come up together.

## Trust, in the inner parlor

**Actions are trusted.** Within one machine there is no question of who may unsubscribe whom, or of
one participant defending itself against another — they are all the same user's processes. Namespacing
by instance exists to prevent *accidents*, not attacks: two hosts cannot deliver into each other's
queues by mistake, which is a correctness property rather than a security one.

Adversarial separation is a different tier and belongs to a deployment that has one; it is not a
property of the inner parlor and is not claimed here.

## One agent per endpoint

An endpoint holds **one agent instance at a time.** Subscribing an endpoint that already holds a
registration is **refused**: the incumbent keeps its queue, and the subscribe fails with a non-zero
status that names the incumbent's process id and start time — so the caller can tell a crashed
predecessor from a live agent — and names the fix. Displacing an instance is a deliberate act, not a
side effect of someone else subscribing — the consumer frees the endpoint first with `unsubscribe`,
then subscribes.

**Refusing rather than replacing puts the decision where the knowledge is**, and `unsubscribe`
enforces it rather than trusting the caller. `unsubscribe <endpoint>` succeeds only when the incumbent's
recorded process is gone: an empty endpoint is a no-op, a **dead** incumbent is cleared, and a **live**
incumbent makes `unsubscribe` **fail**, naming the live process. So a host relaunching an agent may call
`unsubscribe` then `subscribe` without a pre-check — a crashed predecessor is cleared and the new
instance registers, while a still-live predecessor makes the `unsubscribe` refuse, so a restart can
never displace a running agent. Stopping the old process before reclaiming its endpoint is the
consumer's responsibility; the refusal is the warning that it has not. A restarted *host* reconciles the
same way, by the registry and each recorded process (see *Who is here right now*).

Removing a **live** agent on purpose — a deliberate shutdown or displacement — is a separate, explicit
act: **`unsubscribe --force`** removes the incumbent regardless of liveness. Plain `unsubscribe` is safe
reconciliation; `--force` is the deliberate one. In the outer parlor both are authenticated, and
`--force`, which displaces a live remote agent, is the higher-privilege action. The sweep remains the
backstop for a host that never cleaned up.

**What must not persist is *two* instances draining one queue**, each receiving part of the mail.
Refusing the second subscribe prevents that by protecting the incumbent rather than silently killing
it; a live instance is never displaced by accident.

The rule survives the move outward unchanged: `unsubscribe` is ungated in the inner parlor because
local processes are trusted (see *Trust, in the inner parlor*), and **authenticated** in the outer
parlor, where a subscription is a protected resource no remote peer may displace without proving it
may. See *Inner and outer parlors*.

## Schema evolution

The event schema evolves **additively**: new fields, new `kind` values, new `reason` values. Because
consumers ignore fields they do not recognise, a producer may add detail without waiting for them.

There is no version field, and that is deliberate — a version number invites branching behaviour in
every consumer. A change that would break an existing consumer gets a **new `kind`** instead, so old
consumers keep working on the events they already understand and simply do not see the new one.

## Who is here right now

Events describe **changes**. They do not describe **current state**, and the bus keeps no history —
so a consumer that starts after an agent has already subscribed has missed the only event carrying
that agent's type, version and details.

**The registry answers that, and the host holds it.** A consumer joining late asks for the current
registry rather than replaying a history that does not exist; from then on it follows events. This
keeps derived state out of the bus without requiring every consumer to have been present since the
beginning.

**It is a request and a reply, not a stored topic:**

```
request   registry.<instance>          (no body)
reply     { "agents": [ …one registration object per live agent… ] }
```

Each entry is the same shape as the `agent.subscribe` payload, so a consumer has one format to
understand and can apply a reply exactly as it would apply the events it missed.

The host answers because the host holds the registrations — it created them. **The bus stores
nothing**; if no host is running, the request goes unanswered, which is the truthful reply.

The same request is what a person's tooling uses to list registered agents with their details.

**After a host restart** the host rebuilds by asking the broker which endpoints in its namespace are
connected, and checking each recorded process. Agents outlive their host, so a restarted host must
rediscover them rather than assume it has none.

## Command-line operations

The operations this model requires, grouped by who calls them. Message send and receive are outside
this document's scope and are not listed.

### Called by the host

| Operation | Effect |
|---|---|
| `subscribe <endpoint> --pid <n> --type <t> --version <v> [--display …] [--cwd …]` | Registers an agent instance, creates its queue, publishes `agent.subscribe`. **Refused (non-zero) if the endpoint already holds a registration** — free it with `unsubscribe` first. |
| `unsubscribe <endpoint> [--reason clean\|expiry] [--force]` | Removes the registration, destroys the queue, publishes `agent.unsubscribe`. Defaults to `clean`. Succeeds on an empty endpoint (no-op) or a **dead** incumbent, and **refuses a live incumbent** (non-zero, naming its process) unless **`--force`** is given. An unconditional `unsubscribe`-then-`subscribe` restart is therefore always safe. |
| `sweep [<instance>]` | Checks every registration in the namespace against its recorded process and unsubscribes those whose process is gone, with `--reason expiry`. **Must run on the machine holding the processes.** Idempotent; safe to run on a timer. |

### Called by adapters

| Operation | Effect |
|---|---|
| `emit <kind> <endpoint> [--ts …] [--tool …] [--refs …]` | Publishes one activity event. Non-blocking: on failure it drops the event and exits successfully, because an adapter runs inside the agent's hook and must never make the agent wait. |

`--ts` lets the caller supply the moment the thing happened. An adapter should always pass it; when
omitted the publish time is used, which is only correct for something reporting itself immediately.

### Called by a person, or by a consumer

| Operation | Effect |
|---|---|
| `registry [<instance>] [--json]` | Lists current registrations with their details — type, version, process, display, working directory, and how long each has been registered. This is the request described in *Who is here right now*. |
| `watch [<instance>]` | Follows the event stream, printing events as they arrive. Read-only. |
| `status <endpoint>` | Reports one agent: registered or not, process alive or not, and its current activity state with the reason for that conclusion. |

**`status` reports its three inputs separately**, rather than collapsing them into one word. *Away*
because no process, *idle* because no activity within the window, and *idle* because the window has
elapsed with events missing are different situations, and a tool that prints only the conclusion
hides which one it is looking at.

### Exit codes

An operation that could not do what was asked exits non-zero and says why on standard error.
Registering an endpoint that is already taken, sweeping from the wrong machine, or asking the
registry when no host is running are each **failures, not empty successes** — a command that prints
nothing and exits zero is indistinguishable from one that found nothing, which is the ambiguity this
whole design exists to avoid.

## Adapters

Each kind of agent has an adapter: a small program installed as that agent's native lifecycle hook,
which translates its signals into the events above.

**All agent-specific knowledge lives in the adapter and nowhere else.** Nothing downstream — no
consumer, no display, no routing — knows or asks what kind of agent produced an event. Supporting a
new agent is writing one adapter.

Adapters are testable without their agent: given a recorded input, assert the event produced.

## Inner and outer parlors

Two scopes with deliberately different guarantees.

| | **Inner parlor** | **Outer parlor** |
|---|---|---|
| Agents | Local; expected to be present | Remote; connect intermittently |
| Delivery | At-most-once, to a live peer | Store-and-forward |
| Queue | Created on subscribe, destroyed on unsubscribe | Durable mailbox |
| Absent recipient | Send is refused — nowhere to deliver | Normal; the message waits |
| Liveness | Process id | **Open** — a process id does not cross a machine |
| Displacing a held endpoint | consumer calls `unsubscribe` (trusted, ungated) | consumer calls **authenticated `unsubscribe`** (the subscription is a protected resource) |

**In the inner parlor there are no mailboxes for agents that are not running.** A message to an agent
that is not there fails immediately and visibly, rather than waiting for someone who may never
return. If an agent is not running, the answer is to start it.

**This makes refusing to send to an absent agent correct rather than defensive**, and it means
nothing accumulates within the inner parlor: no queues outliving their agents, no backlogs to drain.

**Accepted consequence:** an agent that stops with messages still queued loses them. Appropriate
where agents are expected to be present; not appropriate across a network, which is what the outer
parlor is for.

**At the boundary, mail waits.** A message arriving from the outer parlor holds at the edge until an
inner agent is alive to take it. Durability therefore does not leak inward, and the inner parlor
never acquires persistence it does not want — but note that waiting mail *is* state, and something
at the boundary must deliver it when an agent returns.

## Properties this produces

**Failures are announced, not inferred.** A refused send says so. An expiry is an event. A failure to
launch is reported by the host. Nothing important is communicated by silence.

**The bus holds no derived state.** It carries events; consumers build their own picture, and ask the
registry for current state when they join.

**Nothing accumulates inside the inner parlor.** A subscription is destroyed with its agent, and the
queue with the subscription — bounded, as above, by the expiry check interval.

## Deliberately not provided

- **No message history.** The bus carries messages; it does not archive them.
- **No group addressing.** Messages go to one endpoint.
- **No durable local mailboxes.** See *Inner and outer parlors*.
- **No responsiveness check.** A live process id does not mean an agent will answer. Determining that
  requires asking it, which costs the agent real work — appropriate as a diagnostic when something
  already looks wrong, never as a routine signal.

## Out of scope

- **The outer parlor.** Networked liveness, clock skew between machines, and durable mailboxes belong
  to it. Nothing here is designed for them.
- **Caller error handling.** Locutorium reports failures accurately; deciding what to do about one is
  the caller's policy.
- **Adversarial security.** See *Trust, in the inner parlor*.
