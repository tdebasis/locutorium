# Glossary — the house's words, and what each one is

The Locutorium has a vocabulary. Each word below names a real mechanism. The table gives the plain
meaning first and the theme second. *The house* means any set of endpoints that share one medium. It
never means a particular deployment.

| word | mechanism | in the theme |
|---|---|---|
| **the house** | one machine's set of endpoints sharing one medium | the silent house; nobody speaks in the halls |
| **the locutorium** | the bus: `loc`, the medium, the queues and topics | the one room where speaking is allowed |
| **endpoint** | a named mailbox; a row in the ledger while its seat is subscribed | a member of the house |
| **seat** | the running agent that holds an endpoint and speaks as it | the person in the chair |
| **ledger** | which endpoints are registered right now: one file per endpoint under `run/presence`, not a single file. `loc status` reads it. | the attendance book's open page |
| **attendance** | a registered seat: its `loc mcp` server holds the endpoint and rings its bell | being present in the room |
| **queue** | a per-endpoint stream, `QUEUE_<ep>`, work-queue retention: a message is removed when read | a word to one person, held until they come |
| **topic** | the shared stream `TOPICS`, subjects `topic.<name>`, every reader has a cursor | the room where the house converses |
| **window** | `topic_window`: how long a topic's messages live before retention removes them | when the talking stops, the room forgets |
| **envelope** | one JSON line: `id`, `ts`, `from`, `to`, `kind`, `body` | the note itself |
| **wake** | the seat's `loc mcp` server rings the notifier that `LOC_LISTENER_TYPE` names | the knock on the door |
| **bell** | one bell line; the addressee's own server rings it when mail lands in the queue. A topic publish rings nobody. | the bell; advisory, never load-bearing |
| **courier** | the `claude` notifier: it starts one Claude session to carry the bell onto a seat's surface | the one who brings the note in |
| **breaker** | `wake_breaker_per_minute` / `_per_hour`: caps wakes; suppression loses nothing | not knocking sixty times |
| **retry** | `wake_retry_seconds`: a bell a busy pane refused rings again, each wait twice the last, up to 60s | knocking again once the room is quiet |
| **registry** | `loc registry`: it asks an instance's host process over the bus. Each seat now runs its own server, so no host process answers and the verb fails. Use `loc status`. | the attendance book |
| **watch** | `loc watch`, which follows the event stream and takes nothing | the silent observer |
| **identity** | `LOC_IDENTITY`, and nothing else; a refusal, never a guess | knowing who is speaking |
| **say-semantics** | `send_requires_attendance = yes`: a send to an absent endpoint is refused | you cannot speak to an empty chair |
| **medium** | the transport a provider drives — v1 is `nats-server` with JetStream on loopback | the air in the room |
| **provider** | an adapter compiled into the binary and named by the `provider` config key. A provider is a Locutorium provider when `conformance/run.sh` passes against it, and not otherwise. | the room's builder |
| **house rules** | `PROTOCOL.md`: identity, addressing, the envelope, the bell, what receipt means, what the bus never carries | the rules of the room |
