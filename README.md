# Locutorium

**In a silent house, the locutorium is the one room where speaking is
permitted.**

A messaging substrate for independently-running agents — different runtimes,
different lifetimes, no shared process, often not running at all. Endpoints
register, discover each other, and converse: **queues** for one saying
something to another (delivery guaranteed, through any downtime), **topics**
for conversations (born when spoken in, gone when the talking stops). It is a
channel, not a record: nothing is kept unless a participant deliberately
writes it down elsewhere.

```
$ loc send ada "the build is green"
$ loc publish standup "@ada ready when you are"
$ loc read
$ loc topics
$ loc watch
```

- **Contract first:** the semantics live in [docs/CONTRACT.md](docs/CONTRACT.md);
  a provider is a Locutorium provider iff [conformance/run.sh](conformance/run.sh)
  passes against it.
- **Provider v1:** NATS + JetStream, loopback-only by default. The adapter
  seam (`lib/providers/`) is where other media plug in.
- **Private by default:** the most private deployment profile is the
  zero-config one. Exposure is added deliberately, never removed belatedly.

## Getting started

```
brew install nats-server nats-io/nats-tools/nats
providers/nats/bootstrap.sh <endpoint> [<endpoint> ...]
# point your nats-server service at ~/.locutorium/nats-server.conf, then:
LOC_IDENTITY=admin loc doctor --init
loc doctor
```

Status: early. Interfaces may move.
