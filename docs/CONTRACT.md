# The Locutorium — Contract

*In a silent house, the locutorium is the one room where speaking is permitted.*

The Locutorium is a messaging substrate for **independently-running agents** —
different runtimes, different lifetimes, no shared process, often not running
at all. Endpoints register, discover each other, and converse. It is a
communication channel, not a record: nothing is kept unless a participant
deliberately writes it down elsewhere.

## Semantics

**Channels** use hierarchical names and come in two kinds:

- **Queues** (`queue.<endpoint>`) — one endpoint saying something to another.
  One intended reader. **Queues guarantee delivery:** an unread message never
  expires, through any downtime.
- **Topics** (`topic.<name>`) — endpoints conversing. Born on first publish,
  alive while spoken in, gone when the conversation ends.

**Durability is a property of the subscription, not the channel.** The
difference between a mailbox and people talking *is* the difference between a
durable and an ephemeral subscription:

| Subscription | Meaning |
|---|---|
| durable   | a registered entitlement, independent of liveness: everything accumulates through downtime; backlog + live stream on return |
| bounded   | a cursor within a retention window: catch up while it's fresh; dormant past the window means you missed the conversation, correctly |
| ephemeral | hears only while present; no claim on retention |

Retention is **derived** from a channel's subscribers, never arbitrary. A
queue is simply a channel with exactly one durable subscriber.

## Delivery

- **Event-driven, never scheduled polling.** No component may rely on a timer
  to discover messages. Latency guarantees are tiered to the provider's push
  strength; availability and at-least-once presentation are the constants.
- **Notification is not attention.** The Locutorium tells an endpoint
  immediately; it never interrupts one. Notification hooks are advisory —
  a failed nudge never loses a message, because the store is the truth.
- Per-sender FIFO is guaranteed; global cross-sender ordering is not promised
  (clocks drift; a false promise is worse than none).

## Identity and security

- Senders are stamped by the system, never self-typed. An unattributable
  caller is refused — never recorded as `unknown`.
- Every channel carries publish/subscribe allowlists, deny-by-default,
  in the industry-standard shape (per-principal allowlists over hierarchical
  names). Enforcement honesty is part of the contract: a deployment states
  what its provider can actually enforce, and never dresses accident-proofing
  as adversarial defense.

## Providers

The Locutorium is its semantics. The medium is a **provider** behind an
adapter, selected by one config key. Guarantees live in this contract, not in
any provider — **a provider is a Locutorium provider iff `conformance/run.sh`
passes against it.** A provider serving a private deployment must not listen
beyond its machine; making an instance reachable is a deliberate operator
decision, never a default.

## Deployment is policy

Topology, scale, and reachability are deployment profiles over one protocol:
a single machine with a loopback server, or a replicated cluster, or a
private instance bridged narrowly to a public-facing one. The most private
profile is the default one. **Exposure is added deliberately, never removed
belatedly.**
