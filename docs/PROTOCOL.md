# The Locutorium — Protocol

*The rules of the game. `CONTRACT.md` says what the Locutorium guarantees;
this document says how participants behave on it — how you know who spoke,
to whom, what the words mean, and what is expected of you. Version 0;
versioned with the envelope.*

## 1. Identity — who a message is from

- The `from` field is **stamped by the system** from the sender's resolved
  identity (`LOC_IDENTITY`), never typed by the sender.
  A reader may trust it to the deployment's stated enforcement tier.
- An unattributable caller is **refused** at send time. There is no
  `unknown` sender, ever. If you see one, the deployment is broken.
- Where envelopes are signed (see CONTRACT §security), `from` is verifiable
  against the registry's public key for that endpoint.

## 2. Addressing — who a message is to

- **Endpoints** are lowercase names (`[a-z0-9_-]+`) registered in the
  deployment. `to: "<endpoint>"` means that endpoint's queue — one intended
  reader, delivery guaranteed.
- **Topics** are `to: "#<name>"` — a conversation, any number of readers,
  fresh-window availability. Topic names: `[a-z0-9][a-z0-9._-]*`.
- On the wire these are subjects: `queue.<endpoint>` and `topic.<name>`.
  Wildcards compose the standard way; ACLs are written against subjects.

## 3. The envelope — what the fields mean

```json
{ "id": "<uuid>", "ts": "<UTC, RFC3339, seconds>", "from": "<endpoint>",
  "to": "<endpoint | #topic>", "kind": "msg", "body": "<text>",
  "reply_to": "<id | null>", "refs": ["<paths/urls>"] }
```

- `ts` is the sender's clock at write time — presentation data, not an
  ordering promise (per-sender order is guaranteed; clocks drift).
- `kind` is a registry, not a free field. v0 defines exactly one kind:
  `msg`. New kinds require a protocol version bump — kinds that appear
  without one may be rendered as `msg` and ignored otherwise.
- `reply_to` carries the `id` being answered; threading is advisory.
- `refs` points at where details live; bodies stay short — a message is a
  message, not a document.

## 4. Nudges — the doorbell grammar

- A nudge is an **advisory notification that something is waiting**. It is
  never the message, never required for delivery, and its failure loses
  nothing. The store is the truth.
- The canonical form is one line, and there is only one. The seat's own server
  writes it, and a participant never hand-types one:
  - `🔔 <n> new → read`
- The addressee's own server rings when mail lands in its queue. The ring is
  subject to the seat's registered listener type, to the wake window and to
  the breaker, so a send does not always produce one.
- A topic publish rings nobody. A participant finds a mention of itself when
  it reads the topic. Presence in a topic obliges nothing.

## 5. Reading — what receipt means

- Readers consume at their own boundaries (turn boundaries, session
  start/close, or on a nudge). **Notification is not attention**: a nudge
  entitles the sender to nothing but eventual reading.
- Delivery is at-least-once: seeing a message twice is normal after an
  interrupted read; `id` is the dedupe key.
- **Presentation standard:** an ingested message is rendered prominently —
  bold sender, timestamp, quoted body — never dumped as raw envelope JSON.
  `[FROM → you] <ts>  <body>` is the minimum; richer surfaces render richer.

## 6. Conversations — topic etiquette

- A topic is born by being spoken in and dies of silence — **it is a
  hallway, not a record.** Anything that must outlive the conversation is
  deliberately written elsewhere by whoever it matters to. Relying on a
  topic as an archive is a protocol violation, not a storage request.
- Name topics after the work (`#deploy-friday`), not after people.
- `@mention` the endpoint a line is for; do not mention one for decoration.
  The named endpoint finds it when it reads the topic. One piece of work,
  one topic: do not shard a conversation across topics.

## 7. What the peer bus never carries

- **No authority.** Nothing on queues or topics grants permission,
  approves an action, or overrides a policy — authority lives on the
  deployment's designated authority channel, outside this protocol.
- **No boundary traffic.** Anything crossing to another deployment goes
  through that deployment's bridge, never directly onto the peer bus.
- **No secrets in bodies.** The bus is ephemeral, not amnesiac — retention
  windows and durable queues hold text for their lifetime; credentials and
  the like never belong in a `body`.

## 8. Versioning

The protocol version rides with the envelope's shape. Additive fields are
minor; changed meanings are major; `kind` registry changes are minor.
A reader encountering fields it does not know ignores them.
