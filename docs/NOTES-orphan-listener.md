# NOTES — the orphan listener (2026-08-26)

Status: **reproduced and captured. Not fixed.** The capture below names the
mechanism; the fix is deliberately not in this commit, because a change to the
listener's lifetime has to be certified by a green suite and this suite is not
green yet.

## The symptom

`conformance/run.sh`'s case *"rapid unsub/resub leaves no orphan listener"* fails
intermittently, and when it does, *"breaker caps wakes and trips loud"* fails a
few lines above it with **more wakes than were asked for**. The two are the same
event seen twice: an extra listener nobody can see is attending the endpoint, so
it wakes alongside the registered one.

A failure means a listener survived the final `unsub`: invisible to
`_loc_listener_alive` because the pidfile no longer names it, unkillable by
`unsub` for the same reason, and waking beside every later registration until the
machine is restarted.

## What reproduces it, and what does not

| instrument | iterations | load average | reproduced |
|---|---|---|---|
| `conformance/orphan-probe.sh` (death-window sequence alone) | 50 | 3.59 → 2.86 | no |
| `conformance/orphan-probe.sh` (same, two busy loops) | 50 | 3.20 → 3.93 | no |
| **full suite, back to back** | **6 runs** | **2.1 – 2.9** | **2 of 6** |

That is the first real finding: **the rapid unsub/resub sequence on its own does
not produce the orphan.** One hundred iterations of exactly the sequence the case
performs produced nothing. The orphan appears only in a full suite run, where the
endpoint has already been registered and unregistered a dozen times, has a
backlog, and has hooks that take real time. Any future instrument has to carry
that history with it; looping the last five lines of the case is not enough.

Failing runs, verbatim:

```
  ✗ breaker caps wakes and trips loud (got 2 wakes)
  ✗ rapid unsub/resub leaves no orphan listener (no wake after final unsub)

  ✗ breaker caps wakes and trips loud (got 3 wakes)
  ✗ rapid unsub/resub leaves no orphan listener (no wake after final unsub)
```

Two wakes, then three: the orphans accumulate across a run.

## The capture

Taken from a survivor still alive minutes after the suite that made it had
exited and deleted its whole house:

```
  PID  PPID STAT WCHAN  STARTED                      COMMAND
40881     1 S    -      Wed Aug 26 23:39:30 2026     bash …/bin/loc sub
41178 40881 S    -      Wed Aug 26 23:39:41 2026     nats subscribe queue.alice --raw

bash  40881  1w  REG   …/run/alice.listener.log
bash  40881  3r  FIFO  …/run/alice.listen.fifo   (node 101651684)
nats  41178  1w  FIFO  …/run/alice.listen.fifo   (node 101651684)
nats  41178  3u  KQUEUE count=0

$ ls …/tmp.UlJWIbefBW
ls: No such file or directory
```

Four facts follow from it, and together they are the mechanism:

1. **The listener is not stuck in `open`.** It holds `3r` on the fifo; the open
   completed and its own tap is the writer on the same inode. So the
   blocked-in-`exec 3<` hypothesis is **not** what happened here.
2. **The house is gone.** `LOC_HOME` was `rm -rf`'d by the suite's teardown while
   both processes ran on. Both hold the fifo by an unlinked inode. Nothing on
   disk names either of them any more; there is no pidfile left to kill them by.
3. **The tap has no connection.** Its only descriptors are the fifo, `/dev/null`
   and an empty kqueue — the server it subscribed to was killed at teardown. It
   did not exit when its server died.
4. **So the listener loops forever.** The inner loop decides between "timeout
   tick" and "dead pipe" by `kill -0 "$_L_TAP"` — deliberately, because bash 3.2
   cannot distinguish a `read -t` timeout from EOF by status. A tap that outlives
   its server is alive by that test, so every 2-second timeout reads as a normal
   quiet tick, forever.

And the reason it was never killed in the first place, from the process
snapshots taken through a run: there are windows, seconds long, in which **a live
listener exists while `run/<ep>.listener.pid` is absent or empty**, and windows
in which two listener generations are alive at once. `loc sub` writes the pidfile
*after* forking the listener, and under load the new listener spends about eleven
seconds inside its first depth-check-drain-wake before it even starts its tap —
while `loc unsub` deletes the pidfile unconditionally, whether or not the kill it
just issued did anything. The pidfile is the only handle either verb has. Any
generation whose lifetime does not line up with its own pidfile entry is
unreachable from that moment on.

## The shape of the fix (not applied here)

Three parts, in the order they matter:

1. **The listener must not depend on a file to be killable.** The employment
   check is the honest liveness signal: a listener whose watched pid is gone
   should exit on its own. A listener with no watched pid currently lives until
   somebody kills it by pidfile — that is the hole.
2. **The tap must not be trusted as a liveness proxy.** A `nats subscribe` whose
   server has died stays alive and turns the loop's tick/EOF test into "always
   tick". The loop needs a positive signal — a heartbeat, a deadline, or a check
   that the house it belongs to still exists on disk.
3. **Per-generation fifo names** (`$me.$$.listen.fifo`), so two generations can
   never contend for one path and `_cleanup` can remove only its own.

## The contract a fix must not break

The sender never touches the fifo; only the listener does. What the sender, the
nudge hook and the wake hook share with the listener is:

- `run/<ep>.listener.pid` — two lines, pid then `ps -o lstart=` start time, read
  by `_loc_listener_alive`;
- `run/<ep>.delivery.log`.

**A fix may rename the fifo freely. It must not change the pidfile's path or its
two-line format, and it must not change the delivery-log path** — a running
listener from an older revision and a newly-installed sender have to agree on
those two things until the next restart.

## Reproducing it again

Run the full suite back to back, not the probe:

```
for n in 1 2 3 4 5 6; do conformance/run.sh > run-$n.log 2>&1; done
grep -c '✗' run-*.log
```

Expect roughly one failing run in three on a loaded machine, and check for
survivors between runs — `pgrep -f 'nats subscribe queue.alice'` — because an
orphan left alive makes the *next* run's tap-liveness case pass for the wrong
reason. `conformance/orphan-probe.sh` remains in the tree as the narrow
instrument: it is the thing that proved the sequence alone is innocent.
