# Glossary — the house's words, and what each one is

The Locutorium has a vocabulary. Each word below is a real mechanism; the plain meaning is given
first, the theme second. *The house* is a product concept — any set of endpoints sharing one medium —
and never a particular deployment.

| word | mechanism | in the theme |
|---|---|---|
| **the house** | one machine's set of endpoints sharing one medium | the silent house; nobody speaks in the halls |
| **the locutorium** | the bus: `loc`, the medium, the queues and topics | the one room where speaking is allowed |
| **endpoint** | a named mailbox; a row in the ledger while its seat is subscribed | a member of the house |
| **attendance** | a registered seat: its `loc mcp` server holds the endpoint and rings its bell | being present in the room |
| **queue** | a per-endpoint stream, `QUEUE_<ep>`, work-queue retention: a message is removed when read | a word to one person, held until they come |
| **topic / the room** | the shared stream `TOPICS`, subjects `topic.<name>`, every reader has a cursor | the room where the house converses |
| **window** | `topic_window`: how long a topic's messages live before retention removes them | when the talking stops, the room forgets |
| **envelope** | one JSON line: `id`, `ts`, `from`, `to`, `kind`, `body` | the note itself |
| **wake / the knock** | the seat's `loc mcp` server rings the notifier that `LOC_LISTENER_TYPE` names | the knock on the door |
| **doorbell / nudge** | one bell line, rung by `@name` in a topic and by a send | the bell; advisory, never load-bearing |
| **courier** | the `claude` notifier: it starts one Claude session to carry the bell onto a seat's surface | the one who brings the note in |
| **breaker** | `wake_breaker_per_minute` / `_per_hour`: caps wakes; suppression loses nothing | not knocking sixty times |
| **registry** | `loc registry`: who is attending, read from the medium's monitor (`monitor_url`) | the attendance book |
| **watch** | `loc watch`, which follows the event stream and takes nothing | the silent observer |
| **identity** | `LOC_IDENTITY`, and nothing else; a refusal, never a guess | knowing who is speaking |
| **say-semantics** | `send_requires_attendance = yes`: a send to an absent endpoint is refused | you cannot speak to an empty chair |
| **medium** | the transport a provider drives — v1 is `nats-server` with JetStream on loopback | the air in the room |
| **provider** | an adapter compiled into the binary and named by the `provider` config key; a provider is a Locutorium provider iff the suite passes | the room's builder |
| **house rules** | `PROTOCOL.md`: identity, addressing, the envelope, the doorbell grammar, what receipt means, what the bus never carries | the rules of the room |
