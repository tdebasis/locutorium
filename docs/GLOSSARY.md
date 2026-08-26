# Glossary — the house's words, and what each one is

The Locutorium has a vocabulary. Each word below is a real mechanism; the plain meaning is given
first, the theme second. *The house* is a product concept — any set of endpoints sharing one medium —
and never a particular deployment.

| word | mechanism | in the theme |
|---|---|---|
| **the house** | one machine's set of endpoints sharing one medium | the silent house; nobody speaks in the halls |
| **the locutorium** | the bus: `loc`, the medium, the queues and topics | the one room where speaking is allowed |
| **endpoint** | a named mailbox; a line in `endpoints`; a credential in `creds/` | a member of the house |
| **attendance** | `loc sub`: a listener registered for an endpoint (`run/<ep>.listener.pid`) | being present in the room |
| **queue** | a per-endpoint stream, `QUEUE_<ep>`, work-queue retention: a message is removed when read | a word to one person, held until they come |
| **topic / the room** | the shared stream `TOPICS`, subjects `topic.<name>`, every reader has a cursor | the room where the house converses |
| **window** | `topic_window`: how long a topic's messages live before retention removes them | when the talking stops, the room forgets |
| **envelope** | one JSON line: `id`, `ts`, `from`, `to`, `kind`, `body` | the note itself |
| **spool** | files under `run/`: `<ep>.wake.spool.raw` (drained), `<ep>.spool` (rendered), `<ep>.read.spool` | the message left at your door |
| **wake / the knock** | `hooks/wake <ep> <count>`, run by the listener after it spools | the knock on the door |
| **doorbell / nudge** | `hooks/nudge <ep> <line>`; rung by `@name` in a topic and by sends | the bell; advisory, never load-bearing |
| **courier** | a deployment helper the wake hook may spawn to carry spooled bodies onto an endpoint's surface; never speaks on the bus | the one who brings the note in |
| **breaker** | `wake_breaker_per_minute` / `_per_hour`: caps wakes; suppression loses nothing | not knocking sixty times |
| **registry** | `loc registry`: who is attending, read from the medium's monitor (`monitor_url`) | the attendance book |
| **watch** | a read-only credential (`creds/watch`) and `loc watch` | the silent observer |
| **admin** | the credential that creates streams (`loc doctor --init`) | the keeper of the keys |
| **identity** | `LOC_IDENTITY` or `hooks/identity`; a refusal, never a guess | knowing who is speaking |
| **say-semantics** | `send_requires_attendance = yes`: a send to an absent endpoint is refused | you cannot speak to an empty chair |
| **medium** | the transport a provider drives — v1 is `nats-server` with JetStream on loopback | the air in the room |
| **provider** | `lib/providers/<name>.sh`; a provider is a Locutorium provider iff the suite passes | the room's builder |
| **house rules** | `PROTOCOL.md`: identity, addressing, the envelope, the doorbell grammar, what receipt means, what the bus never carries | the rules of the room |
