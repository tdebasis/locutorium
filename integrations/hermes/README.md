# Hermes Agent plugin for loc

A platform plugin for [Hermes Agent](https://github.com/NousResearch/hermes-agent). It gives the Hermes gateway one seat on a local `loc` bus.

Status: a first version, tested on one machine with Hermes Agent v0.21.5 (upstream commit `bddd22be`).

## What it does

- The gateway holds the seat. The desktop app does not.
- A message that another seat sends to this seat arrives in Hermes as a chat message from that seat.
- The reply of the model goes back to that seat as one bus message.
- If the model answers `[SILENT]`, Hermes sends nothing. The adapter marks every bus message as one that may go unanswered,
  because Hermes otherwise rejects a silent answer to a direct message.

## How it works

1. When the gateway starts the platform, the adapter opens a bell listener on `127.0.0.1`.
2. The adapter starts one `loc mcp` child with the `webhook` listener type.
   `loc mcp` registers the seat for its parent process, which is the gateway.
3. When mail arrives, `loc mcp` sends a `POST` to the bell listener.
4. The adapter runs `loc read --json` and gives each queue message to Hermes.
5. To reply, the adapter runs `loc send <seat> <text>`.
6. When the gateway stops the platform, the adapter closes the input of the child.
   The child then leaves the bus, and loc frees the seat.

The plugin needs a `loc` that has the `webhook` listener type
([#201](https://github.com/tdebasis/locutorium/issues/201)).

## Install

From the root of a clone of this repository:

```
ln -s "$PWD/integrations/hermes/loc" ~/.hermes/plugins/platforms/loc
```

In `~/.hermes/config.yaml`:

```yaml
plugins:
  enabled: [platforms/loc]

platforms:
  loc:
    enabled: true
    extra:
      identity: workshop.scribe   # the seat, <instance>.<agent>
      loc_bin: /path/to/loc       # optional; default is loc on PATH
      port: 0                     # optional; 0 lets the kernel pick a free port
```

Then start the gateway again.

## Who can talk to the bot

The gateway decides this, as it does for every platform.

- By default a new sender gets a pairing code. The operator approves the sender with `hermes pairing approve loc <code>`.
- `LOC_ALLOWED_USERS` in the profile's `.env` is a list of seat names that need no approval.
- `LOC_ALLOW_ALL_USERS=true` accepts every sender. Use it only for a test.

To limit where the adapter sends, set `allowed_recipients` in `platforms.loc.extra` to a list of seat names.
The adapter then refuses a send to any other seat. With no list, the adapter sends to every seat.

These controls bind the adapter. They do not bind the agent.
loc does not authenticate a sender, so any process on the machine can run `loc` under the name of any seat.
That process can send as the seat, and it can read the queue of the seat.
An agent that has a terminal tool or a code tool is such a process: it can run `loc` itself, and the adapter does not see it.
If the agent must not do that, remove those tools from the agent. Rules in the prompt of the agent are guidance only.

A bus message is never a gateway command. The adapter marks every message as conversation,
so a body that starts with `/` goes to the model as text.

## Settings for a quiet bus

Hermes sends notices that are for a person: a home-channel prompt, an "interrupting" notice, tool progress.
On the bus each one becomes a message to a seat. These settings in `config.yaml` stop them:

```yaml
display:
  busy_text_mode: queue          # a message that arrives during a turn waits; no notice is sent
  platforms:
    loc:
      tool_progress: "off"
      streaming: false
      interim_assistant_messages: false
      long_running_notifications: false
      busy_ack_detail: false

platforms:
  loc:
    gateway_restart_notification: false
    home_channel:                # any home stops the one-time home-channel prompt
      platform: loc
      chat_id: workshop.scribe   # the seat itself: the adapter refuses to send to its own seat
      name: none
```

## Limits

- The sender and recipient controls bind the adapter and not the agent. See "Who can talk to the bot".
- Topics are not handled. `loc read` takes the topic messages too, and the adapter logs each one and does not pass it on.
- `loc read` takes a message from the queue before Hermes has it. The adapter writes every message it read to
  `logs/loc-inbox.jsonl` in the Hermes home first, and it logs the id of each message that it does not pass on.
- A bus message has a limit of 4000 characters. A longer reply is cut, and the cut is marked at the end of the reply.
- The seat exists only while the gateway runs. When the gateway stops, loc deletes the queue and the unread mail in it.
  A message that arrives in the last seconds before a stop can be lost. loc records that loss in its transcript.
- If the loc server is killed while the gateway runs, the adapter takes the seat again. loc then deletes the queue of
  the old server, and the mail that was waiting in it is lost. loc records that loss in its transcript too.
- The inbox log is not rotated.

## Tests

From the root of the repository:

```
python3 -m unittest discover -s integrations/hermes/tests
```

`tests/test_core.py` covers the helpers in `loc/core.py`.
`tests/test_adapter.py` runs the adapter against stand-ins for Hermes (`tests/stubs/`) and for loc (`tests/fake_loc.py`).
The tests do not start Hermes or loc, so they prove the mechanics of the adapter only.
