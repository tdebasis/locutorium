# A walkthrough — two seats, and a bell that rings

This is the shortest path from a fresh checkout to the moment the project exists for. One agent
says something. The other agent's pane wakes on its own.

Seven steps. Step 6 is the one worth seeing; steps 1 to 5 exist to reach it.

The two seats here are `house.ada` and `house.bob`. An endpoint name is `<instance>.<agent>`, so
`house` is the instance and `ada` and `bob` are the agents (`CLI.md` §subscribe). Use your own
names.

You need `tmux`, an agent runtime that launches MCP servers over stdio, and the dependencies in
[`INSTALL.md`](INSTALL.md).

## 1. Install `loc`, then start the daemon

```
./install.sh
loc start
```

`./install.sh` builds the newest release tag, copies the stamped binary, and links `loc` onto your
PATH ([`INSTALL.md`](INSTALL.md)). `loc start` writes `$LOC_HOME/config` on its first run and prints
every default it wrote. It then boots the broker and the heartbeat, and prints `started, pid <n>`.
A daemon that is already up prints `already running, pid <n>` and exits 0 (`CLI.md` §start).

The broker runs inside the binary and binds loopback. `loc start` refuses any other address.

## 2. Open two panes

```
tmux new-session -s work \; split-window
```

That gives one session named `work` holding two panes. Do not start the agent runtimes yet. Step 3
reads each pane's address first, and an idle shell is the easiest place to read it from.

## 3. Read each pane's address

Run this inside a pane. It prints that pane's address:

```
tmux display-message -p '#{session_name}:#{window_index}.#{pane_index}'
```

This walkthrough calls the two answers `work:0.0` and `work:0.1`, which is what tmux's default
`base-index` and `pane-base-index` give. Yours differ if you changed either setting.

tmux accepts more than one form of address, and the forms do not survive a tmux server restart
equally well. [`CLI.md` §mcp](CLI.md#mcp) says which form to prefer and what breaks after a
restart.

## 4. Give each runtime its seat

A seat attends through its own `loc mcp` server, and the agent runtime launches that server. The
server reads three environment variables. One is the same for both seats. Two differ.

| seat | `LOC_IDENTITY` | `LOC_LISTENER_TYPE` | `LOC_LISTENER_ADDRESS` |
|---|---|---|---|
| ada | `house.ada` | `tmux` | `work:0.0` |
| bob | `house.bob` | `tmux` | `work:0.1` |

The configuration block itself is in [`CLI.md` §mcp](CLI.md#mcp), under **Configuring a runtime**.
It gives a JSON form and a TOML form. Copy the one your runtime reads, then fill in the two values
from the table above. This page keeps no second copy of that block, so the two cannot drift apart.

`LOC_LISTENER_TYPE` chooses the notifier that rings the seat. `tmux` types the bell into the pane.
`claude` sends a one-shot courier. `none` rings nothing. This walkthrough uses `tmux`, because a
pane waking is the thing to watch. Both listener variables are required, and the server refuses to
start without them (`CLI.md` §mcp).

Now start an agent runtime in each pane. Each runtime launches its `loc mcp` server, and that
server registers its seat.

## 5. Confirm that both seats registered

```
loc registry house
```

`registry` reads the ledger on this machine. It needs no identity and it answers with the broker
down (`CLI.md` §registry). Both seats must appear. Read each one's `address:` line and compare it
with what step 3 printed for that pane.

Do this before you send. `loc send` refuses an endpoint the ledger does not hold (`CLI.md` §send).

## 6. Send, and watch the other pane wake

In ada's pane, ask the agent to send something to `house.bob`. The runtime calls the seat's `send`
tool, which takes `to` and `body` and runs the same function the command line runs (`CLI.md` §mcp).
The command-line equivalent is:

```
LOC_IDENTITY=house.ada loc send house.bob "the build is green; the tag is yours"
```

It prints `sent → queue.house.bob uid=<uuid>`, and that is all it does. **The send rings nothing.**
It puts one message in bob's queue and stops there (`CLI.md` §send). Bob's own server is the only
thing that rings bob's bell.

Now look at the other pane. Bob's `loc mcp` server holds a subscription on bob's queue. It sees the
arrival, coalesces anything else that lands inside `wake_window_seconds`, and hands its notifier one
line. The `tmux` notifier asks tmux whether the pane accepts input, types the line into `work:0.1`,
and presses Enter. The line is:

```
🔔 1 new from ada → read
```

The bell carries a count and the sender's short name. It carries no body. A later try in the same
streak asks the broker for the count and names no sender, so it reads:

```
🔔 1 new → read
```

Bob answers the bell by reading. The `read` tool opens with a fixed reminder the server writes, a
blank line, and then exactly what `loc read` prints:

```
Nothing below has been shown to anyone yet; the bell only rang. Print it on screen verbatim, then act on it.

── queue.house.bob ──
house.ada -> house.bob   09:00
+ the build is green; the tag is yours

── topics ──
```

That read takes the message. The queue hands it over exactly once (`CLI.md` §read).

That is the loop. One agent spoke, and the other pane woke, with nobody watching a file and nobody
typing into somebody else's window.

## 7. When the other pane does not wake

The message is not lost. Whatever the bell did, the queue still holds the message, and `loc read`
in bob's pane still takes it. The three checks below find the bell. Run them in this order.

**First, ask the house what it sees.**

```
loc status
```

The first line reports the daemon. Then comes one line per seat. `house.bob: row ok, queue ok` says
the seat is registered and its queue exists. That line gains `mail unread, bell tried <k> of <n>,
last <result> (<reason>) at <time>` once a bell has rung for mail that nobody took. The `(<reason>)`
part appears only when the try carried one, as a refusal does.

Check again a few minutes later and that suffix changes form. Once the streak has spent its last
try, it reads `mail unread, bell made <n> of <n> tries, <k> rang, last <result> at <time>; no more
until new mail`. This is the form a pane that never woke settles into, and `<k> rang` is the part to
read: it separates a bell that reached the pane from one that never did. The bell then stays quiet
until new mail arrives. [`CLI.md` §status](CLI.md#status) gives both shapes, and
[`OPERATORS.md` §The bell](OPERATORS.md) states the rule behind the second one.

**Second, compare the recorded address with the pane.**

```
loc registry house
```

Read bob's `address:` line. It must equal what step 3 printed for bob's pane. A wrong address fails
quietly: the send succeeds, the queue holds the message, `loc status` still shows the seat, and the
bell goes to some other pane or to none.

**Third, read what the notifier did.**

```
tail $LOC_HOME/run/house.bob.delivery.log
```

`$LOC_HOME` defaults to `~/.locutorium` ([`OPERATORS.md`](OPERATORS.md)). The seat's server and its
notifier each write a line per attempt:

| line | what happened |
|---|---|
| `wake house.bob count=1` | the server handed the notifier one bell |
| `nudge house.bob rang '🔔 1 new from ada → read'` | the notifier typed that line into the pane |
| `nudge house.bob refused: no pane address` | the seat registered with an empty address |
| `nudge house.bob refused: pane_in_mode` | the pane is in copy mode, so somebody may be reading it |
| `nudge house.bob refused: not a prompt` | the pane is not an agent's input box, and typing into a shell would execute the text |
| `nudge house.bob refused: cannot read pane work:0.1: <error>` | tmux could not answer about that address |
| `nudge house.bob BELL FAILED: <reason>` | the notifier broke |

A refusal and a failure both leave the message in the queue. [`OPERATORS.md`](OPERATORS.md) §The
bell says what the bell does after each outcome, and [`AGENTS.md`](AGENTS.md) §When a message seems
missing asks the same question from the seat's own side.
