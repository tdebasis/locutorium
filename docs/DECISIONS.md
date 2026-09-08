# Decisions

*Why the house is shaped the way it is, kept beside the shape itself.*

The other docs describe what the Locutorium does (`PROTOCOL.md`, `CONTRACT.md`), the words it uses
(`GLOSSARY.md`), and how presence works (`PRESENCE.md`). This one keeps the record of *why*, for the
calls that weren't obvious the first time they came up — one entry per call, in Context / Decision /
Consequences form. New entries append; existing ones are not renumbered once another entry cites them.

Some calls that produced this record were about a deployment's own courier, not about the bus itself
— those are marked, and their reasoning generalizes even where the specific mechanism doesn't.

---

## 1. Two courier shapes for one wake, not one universal mechanism

**Context:** A wake may spawn a courier to carry a spooled body onto an endpoint's surface (see
*Glossary: courier*). Two shapes qualify, and they are not interchangeable: a **mechanical** courier
that writes onto that surface — a terminal, typically — and never touches the bus itself (the
listener already did, before the courier ever runs; see entry 2); and an
**agent-native** courier that resolves the destination through its own runtime's inter-instance
addressing, where that runtime happens to offer one. Only some runtimes offer the second kind. *(One
reference implementation of the agent-native shape, built against Claude Code's own session-addressing
tool, is named `claude-courier` in the deployment that built it — a deployment-specific name for one
instance of this shape, not a second category.)*

Writing onto a terminal is exactly the mechanism whose collision risk motivated building a bus at all
(*README: "Keystrokes typed into another agent's window collide with whatever it was typing and submit
the mixture"*). Attendance and the wake do not fully retire that risk for a courier that still writes
into the terminal: knowing an endpoint is genuinely *idle*, not merely registered, is a separate and
still-imperfect problem — this deployment currently has two named, open defects in that liveness check
(one cannot distinguish a whole delivery from a lost head; the other answers "would this execute?"
rather than "is the recipient ready?", and is known to pass on a busy surface). A courier that instead
pushes through the runtime's own addressing never touches the terminal's input stream at all, so it
isn't exposed to that collision risk regardless of whether the liveness check is right.

**Decision:** Support both, chosen per endpoint at attendance time. Mechanical is the default — it
works for any endpoint's runtime, is free of any dependency on a language model, and is faster in
absolute terms since nothing has to start. Agent-native is preferred **only** where the runtime offers
it, and the reason is correctness, not speed: it removes a real, currently-open collision risk that
mechanical delivery still carries. It is measurably slower to run than a bare keystroke write.

**Consequences:** An endpoint's courier shape is a property of that endpoint's own runtime and of
whether the open liveness defects above are trusted for it — not a house-wide default, and not chosen
for latency in either direction. A house mixing runtimes runs both shapes side by side, deliberately,
rather than discovering the mismatch only when a runtime without agent-native addressing needs one —
and closing the two liveness defects would remove agent-native's only justification for the runtimes
that have both options, which is worth revisiting if they ever are.

---

## 2. The bus is touched by the listener, never by a mechanical courier

**Context:** A message is posted to the bus by its sender, and pulled off the bus by the endpoint's
listener, in the ordinary way — none of that changes here. What's new is only what happens after: by
the time any courier runs, the listener has already drained the queue into a spool and the message has
already left the bus (*Glossary: wake*). A courier whose only job is presenting an already-drained
spool has no reason to hold a bus credential at all — and the risk isn't hypothetical: an agent-native
courier that *could* read live content once reasoned about it and acted on its own judgment, when its
only job was to place it.

**Decision:** The reference mechanical courier holds no bus credential and never calls `loc`. It
writes exactly the spool the wake hook handed it, and nothing else.

**Consequences:** This shape is structurally non-agentic, so it cannot decide anything about a
message's content — cheaper and faster than any agent-based courier by construction, and immune to
the failure mode above by removing the actor that could cause it, not by instructing that actor not
to.

---

## 3. Registered liveness names the runtime's own process, never a proxy for it

**Context:** `PRESENCE.md` already settles this for the general case: a runtime that launches its own
server is the host "by proxy," and the registered pid must be the thing whose death actually ends the
session — never a shell or wrapper placed in front of it (*PRESENCE.md: How an agent joins*). The same
rule binds an agent-native courier's addressing: if the runtime crashes but something in front of it
survives, the registration must not read as still alive.

**Decision:** No exception for courier-style delivery. The discipline is the general presence rule
already written, not a separate one invented for this case.

**Consequences:** An implementation that finds it convenient to register something more stable than
the actual runtime process — a supervising shell, say — is choosing convenience over correctness, and
`PRESENCE.md`'s own reasoning already explains why that trade is wrong.

---

## 4. Agent-native addressing needs a stable identity the runtime must be told to keep

*(Deployment-specific reasoning; generalizes past the one runtime it was measured against.)*

**Context:** Measured directly against a real runtime's own inter-instance messaging: an instance's
default display identity is not guaranteed stable across its own lifetime — one drifted mid-session,
unprompted, with nothing restarted — and even a stable identity isn't safely discoverable by search
among many unrelated instances on one machine.

**Decision:** An agent-native courier addresses an instance only by an identity the deployment fixed
explicitly at launch — a name given at start, never one assumed or discovered later. This is a
precondition the deployment must satisfy; the Locutorium documents it without attempting to enforce or
work around it from outside the runtime.

**Consequences:** A deployment that launches instances without fixing this identity will see
agent-native delivery fail unpredictably, in a way that looks like a bus problem but isn't one — worth
stating plainly rather than left as a silent assumption.

---

## 5. No silent defaults on how an endpoint is reached

**Context:** An earlier version of this reasoning let a delivery choice fall back to a computed
default when the deployment didn't specify one. Rejected: an override-if-set pattern still hides an
incorrect default underneath a misconfiguration, and a broken setup should fail loud rather than limp
along silently wrong.

**Decision:** How an endpoint is reached is required configuration, checked at registration.
Attendance refuses to start if it's missing — for every endpoint, not only the ones where a default
would happen to be wrong.

**Consequences:** There is no configuration state where "nobody said, so we guessed" reads as
success. This matches the house's existing failures-are-announced discipline (*PRESENCE.md: Properties
this produces*) rather than adding a new exception to it.

---

## 6. Locutorium ships the mechanism; who wires it up, and how, is the deployment's call

**Context:** Whether attendance is registered by the runtime itself, by whatever process launched it,
or by neither until something else notices — these are legitimate, different answers for different
deployments, and none of them changes anything about the bus itself.

**Decision:** The Locutorium ships the primitives — register, watch, send, read — and, at most, one
labeled reference implementation per delivery shape. It does not decide who calls the primitives or
when. That decision, and its correctness, belongs entirely to whatever runs on top.

**Consequences:** A deployment that has the runtime register itself carries a different risk profile
(readiness — registering before the instance can actually be reached) than one where a launcher
registers on the instance's behalf (a readiness race of its own, mitigated by waiting for a liveness
signal before registering). Both are valid; the choice, and the mitigation, is the deployment's to
make, not the bus's.

---

## 7. A resumable identity beats both a cold restart and a permanently-running process, where the runtime supports it

*(Deployment-specific reasoning, about an agent-native courier's own process shape.)*

**Context:** Three shapes were possible for an agent-native courier: restart cold for every message,
run one instance continuously, or reuse one fixed identity across calls without keeping it running
between them. Continuous was ruled out before any measurement, not by it: it needs its own supervisor,
its own liveness check layered on top of the one this whole design already answers for endpoints, and
it holds a process slot the whole time for work that is actually rare. That left the other two worth
measuring: timed, repeated round trips through a real agent-native path showed reusing one fixed
identity consistently faster than restarting cold, with no overlap between the two clusters across
repeated runs.

**Decision:** Where the runtime supports resuming a specific prior instance by a fixed identity, a
courier should do that — not restart cold each time, not run continuously.

**Consequences:** This gets most of a continuously-running courier's speed advantage without its
operating cost: it still starts and exits per call, so nothing new has to be supervised or watched for
liveness. Left open: whether latency degrades as a resumed identity's own history grows across many
calls over its lifetime — not something a short measurement can show either way, which is what the
next entry hedges against rather than assumes away.

---

## 8. Pin a courier's execution profile explicitly; don't let it inherit a caller's default

*(Deployment-specific reasoning, but the discipline generalizes.)*

**Context:** It's tempting to justify a cheaper execution tier by also assuming it's the faster one.
Checked directly against a costlier alternative on the same task: the comparison came back
inconclusive at the sample size tested — the cheaper tier was not shown to be faster.

**Decision:** Choose a courier's execution tier for what it actually costs to run continuously, state
it as that, and pin it explicitly on every invocation — never left to inherit whatever the calling
account or environment currently defaults to.

**Consequences:** An unstated inherited default means a future account-wide change silently changes
what every courier invocation costs, with nothing in this design noticing. An explicit pin means it
can't.

---

## 9. Bound a resumed identity's growth; don't restrict how it authenticates to do it

*(Deployment-specific reasoning, but the ordering of concerns generalizes.)*

**Context:** One considered way to keep a resumed courier identity's context from growing without
limit turned out to restrict how it could authenticate at all — a real conflict for any deployment
that can't use that authentication mode.

**Decision:** Bound a resumed identity's context growth through whatever mechanism the runtime offers
for that specifically — a compaction floor, a rotation policy. Treat any approach that also narrows
authentication as disqualified, regardless of how well it bounds context.

**Consequences:** Which mechanism exists, and what it's called, is deployment-specific by nature. What
generalizes is the ordering: satisfy authentication first, bound context second, and never let the
second override the first.
