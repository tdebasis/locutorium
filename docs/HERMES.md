# Hermes Agent on the bus

This page shows how to give a [Hermes Agent](https://github.com/NousResearch/hermes-agent) a seat on a
loc bus, so that other seats can send it a message and get an answer without anyone prompting it.

Tested with Hermes Agent v0.21.5 (upstream commit `bddd22be`) on macOS, on one machine.

## What Hermes is

Hermes Agent is an open-source agent runtime from Nous Research. It runs local or hosted models. It has a
desktop app for chatting with the agent, and a **gateway**: a separate process that connects the agent to
messaging platforms such as Slack or Telegram.

The plugin in [`integrations/hermes/`](../integrations/hermes/) adds loc to that list of platforms. To
Hermes, the bus is one more place where messages come from.

## What you get

- A Hermes agent with its own seat, for example `workshop.scribe`.
- A message that another seat sends to that seat arrives in Hermes as a chat message from the sender.
- The model's answer goes back to the sender as one bus message.
- Each sending seat has its own conversation in Hermes. You can read those conversations in the app.

## How it works

Hermes runs as more than one process. The desktop app has its own backend, and the gateway is a different
process. **The gateway holds the seat. The app does not.** Only one process can hold a seat, and the
gateway is the one that receives messages.

1. The gateway starts the plugin. The plugin opens a small listener on `127.0.0.1`.
2. The plugin starts one `loc mcp` process with the `webhook` listener type. loc registers the seat.
3. Mail arrives for the seat. loc rings the bell: one `POST` to the plugin's listener.
4. The plugin runs `loc read --json` and hands each message to Hermes.
5. The model answers. The plugin runs `loc send` to the seat that wrote.
6. When the gateway stops, the plugin closes `loc mcp`, and loc frees the seat.

## What you need

- Hermes Agent, installed and able to answer in its own app.
- A `loc` that has the `webhook` listener type, and a running bus (`loc start`) on the same machine.
- A clone of this repository, for the plugin.

## Install

Stop the Hermes app and the gateway before you start.

1. Make a backup of `~/.hermes/config.yaml`.

2. Link the plugin into Hermes. From the root of your clone:

   ```
   mkdir -p ~/.hermes/plugins/platforms
   ln -s "$PWD/integrations/hermes/loc" ~/.hermes/plugins/platforms/loc
   ```

3. Enable the plugin. In `~/.hermes/config.yaml`:

   ```yaml
   plugins:
     enabled:
       - platforms/loc
   ```

4. Add the platform. In the same file:

   ```yaml
   platforms:
     loc:
       enabled: true
       gateway_restart_notification: false
       home_channel:
         platform: loc
         chat_id: workshop.scribe
         name: none
       extra:
         identity: workshop.scribe
         loc_bin: /path/to/loc
         port: 0
   ```

   Set `identity` to the seat's name, and set `home_channel.chat_id` to the same name.

5. Add the settings for a quiet bus (explained below):

   ```yaml
   display:
     busy_text_mode: queue
     platforms:
       loc:
         tool_progress: "off"
         streaming: false
         interim_assistant_messages: false
         long_running_notifications: false
         busy_ack_detail: false
   ```

6. If your config has an `mcp_servers` entry that starts `loc mcp`, remove it. With that entry, the app and
   the gateway both try to take the seat, and one of them loses.

7. Check that Hermes sees the plugin:

   ```
   hermes plugins list
   ```

   The line for `loc-platform` should say `enabled`.

8. Start the gateway. For a first try, run it in a terminal:

   ```
   hermes gateway run
   ```

   To keep it running in the background, use `hermes gateway install`.

9. Check that the seat is on the bus:

   ```
   loc status workshop.scribe
   ```

   It should say `registered: yes`.

10. Send the seat a message from another seat. The first answer is a pairing code, not the model.

11. Approve the sender:

    ```
    hermes pairing approve loc <code>
    ```

12. Send the message again. This time the agent answers.

Repeat steps 10 to 12 for each seat that should be able to talk to the agent.

## Who can talk to the agent

Hermes decides this, the same way it does for every platform.

- By default a new sender gets a pairing code, and you approve it with `hermes pairing approve loc <code>`.
- `LOC_ALLOWED_USERS` in the profile's `.env` is a list of seat names that need no approval.
- `LOC_ALLOW_ALL_USERS=true` accepts every sender. Use it only for a test.

To limit where the plugin sends, set `allowed_recipients` under `platforms.loc.extra` to a list of seat
names. The plugin then refuses to send to any other seat.

A bus message is never a Hermes command. A message that starts with `/` reaches the model as plain text.

## Settings for a quiet bus

Hermes sends notices that are meant for a person: a prompt to pick a home channel, a note that it was
interrupted, progress while a tool runs. On the bus, each of those would become a message to a seat.

| Setting | Why it's there |
|---|---|
| `home_channel` | Without one, Hermes sends a setup notice to each new sender. Pointing it at the seat's own name means nothing is sent, because the plugin refuses to send to itself. |
| `gateway_restart_notification: false` | Hermes doesn't announce each restart on the bus. |
| `busy_text_mode: queue` | A message that arrives while the agent is busy waits its turn, and no "interrupting" notice goes out. This setting applies to every gateway platform, not just loc. |
| `display.platforms.loc.*` | A bus message can't be edited after it's sent, so Hermes sends only the final answer: no streaming, no progress, no interim messages. |

## Limits and trust

- **The sender and recipient controls bind the plugin, not the agent.** loc doesn't authenticate senders, so
  any process on the machine can run `loc` under any seat's name: it can send as that seat and read that
  seat's queue. An agent with a terminal tool or a code tool is such a process. If the agent must not do
  that, remove those tools from it. Rules in the agent's prompt are guidance only.
- The seat exists only while the gateway runs.
- When the gateway stops, loc deletes the seat's queue. Mail that arrived in the last seconds before the
  stop can be lost. loc records the loss in its transcript.
- If the `loc mcp` process is killed while the gateway runs, the plugin takes the seat again, and mail
  waiting in the old queue is lost. loc records that too.
- The plugin handles queue messages only. It doesn't pass topic messages to Hermes.
- A bus message holds at most 4000 characters. A longer answer is cut, and the cut is marked.
- The plugin writes every message it reads to `~/.hermes/logs/loc-inbox.jsonl`. Nothing rotates that file.

## Finding it in the Hermes app

The plugin doesn't appear on the app's Plugins tab, because the app hides platform plugins there. Look on
the **Messaging** page instead, for the card named **Locutorium**. When the gateway holds the seat, the
card says it's connected and lists the approved senders.

## Removing it

1. Stop the gateway: `hermes gateway stop`.
2. In `~/.hermes/config.yaml`, remove `platforms/loc` from `plugins.enabled`, and remove the
   `platforms.loc` block.
3. Remove the link: `rm ~/.hermes/plugins/platforms/loc`.
4. Start the gateway again if you use it for other platforms.
