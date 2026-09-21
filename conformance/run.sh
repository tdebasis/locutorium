#!/usr/bin/env bash
# conformance/run.sh — the suite that defines the Locutorium.
#
# A provider IS a Locutorium provider iff this suite passes against it.
# Self-contained: boots a scratch server + deployment on a random port,
# runs every case, tears everything down. Never touches a real deployment.
#
# Guarantees exercised here (the contract's test section, v1 scope):
#   delivery to a dormant endpoint · consumption exactly once per queue ·
#   per-sender FIFO · unattributable refusal · cwd-independence ·
#   topic window visibility + bounded catch-up · @mention nudge hook ·
#   cold read with zero prior state · repo cleanliness (no deployment leakage)

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Which `loc` the suite drives. Defaults to the tree's own bin/, so an ordinary
# run is unchanged. The Go port points this at its build output and runs THIS
# IDENTICAL FILE against the new binary — the suite is the gate for both
# implementations, so it must not name one of them.
LOC_BIN_DIR="${LOC_BIN_DIR:-$ROOT/build/bin}"
# WHICH IMPLEMENTATION is under test — a different question from which binary to
# run, and the reason both are needed: the tree ships two, and their listeners
# are different things (the shell tool's background listener; the Go build's
# `mcp` server). `shell` (the default) changes nothing at all. `go` skips the
# cases that are the shell tool's by nature, BY NAME and counted, because a
# suite that quietly runs fewer cases against one implementation is not the same
# suite and cannot be the definition of anything.
# THE DEPLOYMENT THE SUITE LAYS IS NAMESPACED UNDER `go`, AND ONLY UNDER `go`.
#
# The presence model rules that an endpoint is always <instance>.<agent>: a
# bare name is never an endpoint, because a consumer watching two instances
# could not tell two agents of the same name apart. So the Go build refuses
# `bob` — correctly — and a suite that asks it about `bob` is asking a question
# the product does not have. The names are the SUITE'S OWN FIXTURE, not part of
# any guarantee, so the fixture moves and every case stays the case it was.
#
# Under `shell` this is the identity function: the shell lane's commands and
# assertions are byte-for-byte what they were.
#
# `ep` never prints an empty string and never prints a space, so its expansion
# is used unquoted throughout — including inside the double-quoted `bash -c`
# strings, where an inner quote would end the argument.
# Every endpoint is namespaced: the presence model bars a bare name.
ep() { printf 'house.%s' "$1"; }
# THE SAME ENDPOINT'S OTHER SPELLING. A subject may carry a dot; a JetStream
# object's name may not. So the stream, consumer and ack names substitute the
# dot, exactly as the bootstrap's grants and the tool's own naming do — and
# with no dot to substitute, an un-namespaced name is unchanged.
obj() { ep "$1" | tr . _; }
# The tool that LAYS the scratch house, as opposed to the one being questioned.
# Setup is not a case: the streams have to exist before anything can be asked
# about them. Under `go` the house is no longer laid by borrowing this tool —
# see the setup below, where the binary under test stands up its own seats —
# so this is what it always was on the default lane, and it is still named
# because the README demo further down lays a second house with it.
# THE SCRATCH BROKER'S PORT, AND WHY IT IS NEVER 4222. This suite runs on a
# developer's machine as well as in CI, and on a developer's machine the live
# house's broker holds 127.0.0.1:4222. CI runs on a hosted image and is out of
# that blast radius. The developer's machine is not, and that is the case this
# guards: a test run reached the live broker, swept it, and deleted the queues
# of every seat, losing the mail they held. The guard stays for that machine.
# `loc start` reads its listen address from
# the scratch home's `config`, so the port is written there before the first
# boot. A suite that took the product default would bind the live broker's port
# on the operator's own computer, and the daemon's loopback check would not
# stop it, because 127.0.0.1 is loopback.
PORT=$(( 20000 + RANDOM % 20000 ))
# The scratch server's address, named on EVERY nats(1) call in this file: with
# no target the tool goes to the operator's own live deployment on 4222.
# Defined here rather than beside the first case that needs it, because setup
# needs it too. (check-scratch-only.sh is the enforcer.)
NURL="nats://127.0.0.1:$PORT"
# The operator's real house, remembered BEFORE the scratch one replaces it: the
# cleanliness check reads its vocabulary list from there, so the suite must
# still check the tree against the deployment the machine actually runs.
REAL_LOC_HOME="${LOC_HOME:-$HOME/.locutorium}"
# WHERE THE SCRATCH TREES GO, AND WHY IT IS NOT JUST `mktemp -d`. The scratch
# broker is the daemon `loc start` detaches against this scratch home, so that
# path is the only thing about the process that says whose it is — and on
# a runner, a job that was cancelled is cleaned up afterwards by matching
# command lines against the runner's own directories. A bare `mktemp -d` follows
# TMPDIR, which on a runner is the per-user temp directory the service manager
# hands every process, not the runner's own temp directory: the path would name
# nothing the cleanup can scope to. Off a runner RUNNER_TEMP is unset and this
# is `mktemp -d` with a name on it. See conformance/leftovers.sh.
scratch_dir() { mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/loc-conformance.XXXXXX"; }
export LOC_HOME="$(scratch_dir)/deployment"
PATH="$LOC_BIN_DIR:$PATH"
# THE RUN'S OWN RECORD OF WHAT IT STARTED — the spawn ledger read by
# conformance/teardown.sh, and the reason it exists is written there.
#
# ONE HELPER, NOT EIGHT EDITED CALL SITES. `loc` is a shell function, so every
# `loc ...` line in this file already goes through it unchanged: the same verb,
# the same arguments, the same LOC_IDENTITY in the same environment, the same
# exit status, stderr untouched. For every verb but `sub` it is `command loc`
# and literally nothing else happens. Not a single case below is edited, which
# is the point — a capture that required touching each site would be a change
# to the cases, and the cases are the specification.
#
# For `sub` it reads the pid out of the one line the tool prints — `attending →
# queue.X (listener N)` — appends it to the ledger, and hands the same bytes
# on. It has to be read HERE: every site redirects that line to /dev/null, so
# this is the only point at which the pid the run just started is visible.
#
# `already attending` prints the same shape and also names a listener this run
# started, so it is recorded too. A pid appearing twice costs one extra kill(2)
# on a pid already being killed.
#
# The `bash -c "... loc ..."` cases run a child shell, which does not inherit
# this function and does not need to: none of them subscribe.
loc() {
  if [[ "${1:-}" != sub ]]; then command loc "$@"; return; fi
  local out rc spawned
  out="$(command loc "$@")"; rc=$?
  [[ -n "$out" ]] && printf '%s\n' "$out"
  spawned="$(printf '%s\n' "$out" | sed -n 's/.*(listener \([0-9][0-9]*\)).*/\1/p')"
  if [[ -n "$spawned" && -d "$LOC_HOME/run" ]]; then
    printf '%s\n' "$spawned" >> "$LOC_HOME/run/suite.spawned"
  fi
  return $rc
}
SERVER_PID=""
PASS=0; FAIL=0; SKIP=0

say()  { printf '%s\n' "$*"; }
ok()   { PASS=$((PASS+1)); say "  ✓ $1"; }
bad()  { FAIL=$((FAIL+1)); say "  ✗ $1"; }
# A skipped case is still a case: it prints its own description AND the reason
# it was not asked, so the run says out loud which questions it did not put and
# why. A bare name would make a skip a fact to be looked up; the reason makes
# the run its own record.
skip() { # skip <case name> <why it is not asked of this implementation>
  SKIP=$((SKIP+1)); say "  ○ skipped (shell-only): $1"; say "      why: $2"
}
check() { # check <description> <command...>
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then ok "$desc"; else bad "$desc"; fi
}
check_not() { # check_not <description> <command...>
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then bad "$desc"; else ok "$desc"; fi
}

# THE TEARDOWN, AND THE TRAPS THAT FIRE IT. In its own file so that it can be
# sourced by a test and exercised without booting the suite; see
# conformance/teardown.sh.
. "$ROOT/conformance/teardown.sh"

say "conformance: scratch deployment on port $PORT"
# THE CONFIG IS WRITTEN BEFORE THE BOOT, so that `loc start` finds a port that
# is this suite's and never the product default. `loc start` writes this file
# itself when it is absent, with 4222 in it; see the PORT comment above.
mkdir -p "$LOC_HOME/hooks"
cat > "$LOC_HOME/config" <<EOF
provider = nats
nats_url = $NURL
topic_window = 7d
send_requires_attendance = no
idle_timeout = 10m
heartbeat_log_retention_days = 7
EOF
# The broker is embedded, so the suite boots the product rather than a server
# beside it. This is also the first acceptance of `loc start`.
"$LOC_BIN_DIR/loc" start || { bad "loc start"; exit 1; }
# The daemon holds the port and this suite's teardown stops it; see the trap
# installed above. The pid is recorded so the teardown's own kill has a target
# if `loc stop` cannot be run.
SERVER_PID="$(sed -n 1p "$LOC_HOME/run/loc.pid" 2>/dev/null)"
  # THE HOUSE IS LAID THE WAY THE PRESENCE MODEL SAYS IT IS LAID `doctor --init`. That verb reads the registry
  # and creates one stream per name in it, spelled with the RAW name — for a
  # namespaced deployment that is QUEUE_house.alice, which is not a legal
  # JetStream object name. It would fail, and a setup that fails is not a
  # question the suite asked.
  #
  # So the house is laid the way the presence model says it is laid: the shared
  # room stream by the admin, and each seat's queue BY THE SEAT, through the
  # verb under test. That is not a shortcut around setup — it is the model. A
  # queue exists while someone is subscribed and not otherwise, which is what
  # makes `send` to an unattended endpoint refusable at all.
  #
  # TOPICS IS THE DAEMON'S. `loc start` creates the room stream with the
  # configuration the provider gives it, read from the config above, so the
  # suite no longer stands it up by hand.
  # BEFORE the subscribes below, and that ordering is now the point. A
  # registration used to announce itself on topic.<instance>, which fell
  # inside TOPICS' own subject space, so the room had to be stood up
  # afterwards or its history opened with three registration events. THE
  # COLLISION IS CLOSED AT THE SUBJECT LEVEL NOW: events are spoken on
  # presence.<instance>, a family no stream captures, so the room may exist
  # first and the case below asserts that it stayed empty through all three.
  # THE TYPE IS none, BECAUSE THIS SUITE HAS NO BELL. A registered type picks
  # the notifier that rings a seat, and the set is closed to tmux, claude and
  # none. There is no pane here and no runtime to carry a courier, so a seat in
  # this suite finds its mail on its next read, which is what none means.
  for _s in $(ep alice) $(ep bob) $(ep carol); do
    LOC_IDENTITY=$_s "$LOC_BIN_DIR/loc" subscribe "$_s" \
      --pid $$ --type none --version 0 >/dev/null \
      || { bad "stream init (subscribe $_s)"; exit 1; }
  done

# A TRAP, NOT A FIXTURE. R36 of 2026-09-10 ruled that a send only queues: the
# sender notifies nobody, and the seat's own server is the one thing that rings.
# This executable sits where the retired doorbell used to be looked for and
# records every call. Nothing should ever call it. It stays because the two
# cases below assert an ABSENCE, and an absence measured with no instrument is
# not a measurement: delete this file and both cases pass whatever the sender
# does.
cat > "$LOC_HOME/hooks/nudge" <<EOF
#!/bin/sh
echo "\$1 \$2" >> "$LOC_HOME/nudges.log"
EOF
chmod +x "$LOC_HOME/hooks/nudge"

  say "— registration events are not room history —"
  # The three seats above registered through the verb under test, into a room
  # stream that already existed. Each registration announced itself. An event
  # is not mail: were it still spoken inside the room's subject family, TOPICS
  # would be holding three of them on topic.house right now and the next
  # reader's cursor would be handed them as though someone had written.
  #
  # Two questions, because they fail differently. The count is the promise a
  # reader cares about; the subject listing names WHAT arrived, so a wrong
  # answer says where the leak is instead of only that there is one.
  _ti="$(env NATS_URL="$NURL" \
    nats stream info TOPICS --json 2>/dev/null)"
  _tsub="$(env NATS_URL="$NURL" \
    nats stream subjects TOPICS --json 2>/dev/null)"
  if [[ "$(jq -r '.state.messages' <<<"$_ti")" == "0" ]]; then
    ok "no registration reached the room stream"
  else bad "no registration reached the room stream"; fi
  if grep -q '"topic\.house"' <<<"$_tsub"; then
    bad "the room stream holds no topic.house subject"
  else ok "the room stream holds no topic.house subject"; fi

say "— identity —"
check_not "unattributable send is refused" \
  env -u LOC_IDENTITY loc send $(ep bob) "no identity"
check_not "send to unknown endpoint is refused" \
  env LOC_IDENTITY=$(ep alice) loc send $(ep mallory) "hi"

say "— queues: delivery, dormancy, consumption —"
check "send to a dormant endpoint succeeds (nobody reading)" \
  env LOC_IDENTITY=$(ep alice) loc send $(ep bob) "message-one"
env LOC_IDENTITY=$(ep alice) loc send $(ep bob) "message-two" >/dev/null 2>&1
out="$(LOC_IDENTITY=$(ep bob) loc read 2>/dev/null)"
if grep -q "message-one" <<<"$out" && grep -q "message-two" <<<"$out"; then
  ok "dormant endpoint receives full backlog on read"
else bad "dormant endpoint receives full backlog on read"; fi
if [[ "$(grep -o "message-[a-z]*" <<<"$out" | head -2 | tr '\n' ' ')" == "message-one message-two " ]]; then
  ok "per-sender FIFO order preserved"
else bad "per-sender FIFO order preserved"; fi
out2="$(LOC_IDENTITY=$(ep bob) loc read 2>/dev/null)"
if grep -q "message-one" <<<"$out2"; then
  bad "queue messages are consumed exactly once"
else ok "queue messages are consumed exactly once"; fi
# A QUEUE SEND RINGS NOBODY, AND THE MESSAGE IS STILL THERE. Both halves are
# asserted, because a send that queued nothing would also ring nothing.
env LOC_IDENTITY=$(ep alice) loc send $(ep bob) "message-three" >/dev/null 2>&1
check_not "queue send rings nobody, read finds it (nobody rung)" \
  grep -q "$(ep bob)" "$LOC_HOME/nudges.log"
check "queue send rings nobody, read finds it (read finds it)" \
  bash -c "LOC_IDENTITY=$(ep bob) loc read | grep -q message-three"

say "— reading must not destroy what it failed to show —"
# Consumption is irreversible (workqueue retention: the ack is a delete), so a
# read that fetches but fails to present must not be a deletion. Regression
# cover for a defect that destroyed five real messages: the ack happened inside
# a `fetch | render` pipeline, so a broken pipe or a renderer error consumed the
# message and showed nobody anything.
skip "a read that fails mid-render re-presents the message instead of losing it" "the case pipes read into head -c 1 with a 12-byte body, which the Go build writes into the pipe buffer before head has exited, so the write succeeds and the message is rightly taken; the shell tool passes only because it spawns its renderer after writing the heading, by which time head is gone; the property — a mid-render failure never loses a message — is proven for this build by cmd/loc/read_pipe_test.go, TestMessagePlane_ReadUnderAClosedPipeHandsTheMessageBack, which pads the body past the pipe buffer so the closed reader lands on the write that carries the message"

# --peek is the non-destructive read. It previously drained TOPICS with --ack,
# so peeking silently destroyed topic messages.
env LOC_IDENTITY=$(ep alice) loc send $(ep bob) "peek-canary" >/dev/null 2>&1
env LOC_IDENTITY=$(ep alice) loc publish standup "peek-topic-canary" >/dev/null 2>&1
LOC_IDENTITY=$(ep bob) loc read --peek >/dev/null 2>&1
# The guarantee is that peeking never DESTROYS. Note the sharp edge this poll
# exposes: a peeked message goes in-flight for the consumer's ack_wait, so an
# immediate real read shows an EMPTY queue and an agent reasonably concludes the
# message was lost. It returns on redelivery. Non-destructive, but the window is
# indistinguishable from loss at the moment it matters, and it is a live
# suspect for the "nudge says 1 new, read shows nothing" reports.
# ACCUMULATE across polls. Assigning each iteration would discard the topic
# line, which only ever appears in the first read (topics are consumed there),
# and the topic assertion below would then fail for a reason that has nothing
# to do with peeking.
out4=""
for _i in $(seq 1 40); do
  out4="$out4
$(LOC_IDENTITY=$(ep bob) loc read 2>/dev/null)"
  grep -q "peek-canary" <<<"$out4" && break
  sleep 1
done
if grep -q "peek-canary" <<<"$out4"; then
  ok "--peek never destroys the queue message (returns on redelivery)"
else bad "--peek never destroys the queue message (returns on redelivery)"; fi
if grep -q "peek-topic-canary" <<<"$out4"; then
  ok "--peek does not consume topic messages"
else bad "--peek does not consume topic messages"; fi

say "— message size is a guarantee, not a suggestion —"
# This carries conversation, not documents. The limit is in CHARACTERS: an emoji is
# four bytes, so a byte limit would refuse messages that look perfectly ordinary to
# whoever wrote them. Refusal happens at send, before the medium sees anything, and a
# warning-then-send-anyway is not a limit.
AT_LIMIT="$(python3 -c 'print("x"*4000)')"
check "a body at the limit is accepted" \
  env LOC_IDENTITY=$(ep alice) loc send $(ep bob) "$AT_LIMIT"
OVER="OVERSIZE-CANARY$(python3 -c 'print("x"*4001)')"
check_not "a body over the limit is refused at send" \
  env LOC_IDENTITY=$(ep alice) loc send $(ep bob) "$OVER"
# The refusal must be real: nothing may have reached the medium.
out_sz="$(LOC_IDENTITY=$(ep bob) loc read 2>/dev/null)"
if grep -q "OVERSIZE-CANARY" <<<"$out_sz"; then
  bad "a refused body never reaches the queue"
else ok "a refused body never reaches the queue"; fi
# 3999 emoji is ~16000 bytes. Accepted on characters, refused on bytes: this case is
# the entire reason the unit was chosen, so it fails loudly if someone "optimises" the
# counter into ${#var} or wc -c.
EMOJI_BODY="$(python3 -c 'print("\U0001F534"*3999)')"
check "an emoji body under the character limit is accepted (not judged by bytes)" \
  env LOC_IDENTITY=$(ep alice) loc send $(ep bob) "$EMOJI_BODY"
LOC_IDENTITY=$(ep bob) loc read >/dev/null 2>&1

say "— topics: window, mentions, independent cursors —"
env LOC_IDENTITY=$(ep alice) loc publish standup "@$(ep carol) please look at this" >/dev/null 2>&1
check "topic appears in the active list" \
  bash -c "LOC_IDENTITY=$(ep alice) loc topics | grep -q standup"
# AN @MENTION IS DELIVERED TO THE TOPIC AND ANNOUNCED TO NOBODY (R36). The
# mentioned endpoint finds it on read, at its own cursor, like every other
# reader.
check_not "@mention rings nobody, read finds it (nobody rung)" \
  grep -q "#standup" "$LOC_HOME/nudges.log"
c="$(LOC_IDENTITY=$(ep carol) loc read 2>/dev/null)"; b="$(LOC_IDENTITY=$(ep bob) loc read 2>/dev/null)"
# ONE READ, TWO CLAIMS. The mentioned endpoint's read is taken once and both
# cases assert against it, because a second read would move the same cursor.
if grep -q "please look" <<<"$c"; then
  ok "@mention rings nobody, read finds it (read finds it)"
else bad "@mention rings nobody, read finds it (read finds it)"; fi
if grep -q "please look" <<<"$c" && grep -q "please look" <<<"$b"; then
  ok "every reader's cursor sees the conversation independently"
else bad "every reader's cursor sees the conversation independently"; fi

# THE ACL CASES ARE GONE, AND THIS LINE SAYS SO. R12 of 2026-09-09 removed
# authentication from the loopback listener for V0, so there are no per-seat
# access lists left to deny anything and no watch identity to refuse. What the
# cases held is recorded in docs/DECISIONS.md, entry 12.

say "— cwd-independence —"
# TWO CASES, because one of the verbs is not shared. The guarantee is that the
# working directory never changes an answer, and it is worth asking of every
# verb that exists in the implementation being questioned. `doctor` exists in
# one of them: the Go build names it and answers "not implemented in this
# build". Leaving it inside the shared case would make that case fail under
# `go` for a reason with nothing to do with cwd, and dropping it would stop
# asking the shell tool a question it has always answered. So it is its own
# case, skipped by name, and the shared verbs keep theirs.
check "every verb works from an unrelated cwd" \
  bash -c "cd / && LOC_IDENTITY=$(ep alice) loc version >/dev/null && LOC_IDENTITY=$(ep alice) loc send $(ep bob) from-root >/dev/null && LOC_IDENTITY=$(ep alice) loc publish cwdcheck from-root >/dev/null && LOC_IDENTITY=$(ep alice) loc topics >/dev/null && LOC_IDENTITY=$(ep alice) loc status >/dev/null && LOC_IDENTITY=$(ep alice) loc read >/dev/null"

say "— the CLI finds its own house —"
# loc is reached through a link on PATH. Whatever shape that link takes, loc
# must find the tree it belongs to; a bare copy has no tree and must say so.
L="$(dirname "$LOC_HOME")/links"; mkdir -p "$L/bin"
# Relative links are resolved by the kernel against PHYSICAL paths; a relpath computed
# from a logical path under a symlinked directory dangles before loc ever runs.
rel="$(python3 -c 'import os,sys;print(os.path.relpath(os.path.realpath(sys.argv[1]),os.path.realpath(sys.argv[2])))' "$LOC_BIN_DIR/loc" "$L")"
ln -s "$rel" "$L/loc-rel"
check "a relative symlink to loc finds its house" env LOC_IDENTITY=$(ep alice) "$L/loc-rel" topics
ln -s "$LOC_BIN_DIR/loc" "$L/hop1"; ln -s hop1 "$L/hop2"
check "a two-hop symlink to loc finds its house" env LOC_IDENTITY=$(ep alice) "$L/hop2" topics
# A copy is a refusal for the shell tool and an ORDINARY INVOCATION for the Go
# build, and both are right. The shell tool is a script that must find lib/ and
# a provider beside it, so a copy is a broken installation and saying so is the
# service. The stamped binary carries its version and its provider inside it; a
# copy of it is simply loc, and refusing would be refusing to work for no reason.
# A COPY OF THE BINARY IS AN ORDINARY loc. The shell tool had to find lib/ and a
# provider beside it, so a copy of it was broken and refused on purpose; the
# binary carries both inside itself, so there is nothing here to refuse.

say "— the installer links loc and leaves when told —"
# --main IS REQUIRED HERE. The suite tests the checkout it was launched from, so
# the installer must build that checkout. With no flag install.sh builds the
# newest release tag instead, and the next case would run a binary from another
# commit. The uninstall carries --main for the same reason: the mode picks the
# artifact NAME, and a name taken from the newest tag does not match the copy
# that --main installed.
check "install.sh links loc into a prefix" \
  "$ROOT/install.sh" --main --prefix "$L/bin"
check "the installed link runs loc" env LOC_IDENTITY=$(ep alice) "$L/bin/loc" topics
inst2="$("$ROOT/install.sh" --main --prefix "$L/bin" 2>&1 || true)"
if grep -q "nothing to do" <<<"$inst2"; then ok "a second install has nothing to do"; else bad "a second install has nothing to do"; fi
"$ROOT/install.sh" --uninstall --main --prefix "$L/bin" >/dev/null 2>&1 || true
check_not "uninstall removes the link it made" test -e "$L/bin/loc"

say "— tag-mode install builds in a worktree, and that worktree must pass clean-check —"
# NO --main HERE, ON PURPOSE (#97). With no mode flag install.sh builds the
# newest tag in a temporary worktree, and a worktree's .git is a file holding an
# absolute path. The --main cases above build in place and make no worktree, so
# they never put that file in front of check-clean.sh; this is the coverage
# whose absence hid the defect.
TAGROOT="$(scratch_dir)"
# TWO CLONES, AND THE SECOND ONE IS THE POINT. install.sh reads the tag list
# from ORIGIN with `git ls-remote`, never from the clone it runs in, so a tag
# made in the install clone is invisible to it and the install refuses with
# exit 2. The tag goes on the upstream, and the install clone takes it from
# there as any user's clone does.
git clone --quiet "$ROOT" "$TAGROOT/upstream"
# THE FIXTURE MUST OWN ITS COMMIT, AND THE CLONE CARRIES THE REAL TAGS.
# `git describe` reports ONE tag for a commit that carries several, so on a
# release commit — where HEAD already carries v0.0.N — the fixture tag and the
# real tag sit on the same commit and describe returns the real one. The
# version assertion below then reads the release's number and the case fails.
# Measured: five of the six release commits to date failed exactly here, and
# every non-release push passed, because only a release commit is tagged.
# Deleting the tags in the scratch clone makes the fixture the only one the
# build can see, which is what this case always meant.
#
# EVERY TAG GOES, NOT ONLY THE RELEASE-SHAPED ONES. `git describe --tags` reads
# any tag, so a tag outside the `v[0-9]*` shape — a `nightly-1` — would be
# picked while a guard filtered to that shape reported the fixture alone.
# Measured with a plant: the assertion said OK while describe returned the
# planted tag, the same failure travelling through the guard.
while IFS= read -r stale_tag; do
  [[ -n "$stale_tag" ]] && git -C "$TAGROOT/upstream" tag -d "$stale_tag" >/dev/null
done < <(git -C "$TAGROOT/upstream" tag -l)
git -C "$TAGROOT/upstream" tag v9.9.9-test
# AND ASSERT THE ISOLATION, so this case cannot pass for the wrong reason. A
# fixture that silently shared its commit is what hid the defect above.
tags_seen="$(git -C "$TAGROOT/upstream" tag -l | tr '\n' ' ')"
if [[ "$tags_seen" == "v9.9.9-test " ]]; then
  ok "the tag-mode fixture is the only release tag in its clone"
else
  bad "the tag-mode fixture is the only release tag in its clone (saw: $tags_seen)"
fi
git clone --quiet "$TAGROOT/upstream" "$TAGROOT/clone"
# THE LIST IS WHAT MAKES THIS CASE ABLE TO FAIL. `make clean-check` runs the
# checker only when a list is readable, and this suite exports a scratch
# LOC_HOME that holds none, so without this line the check is skipped and the
# case passes against the defect it exists to catch. One line, matching any
# home directory on either runner.
printf '/[Uu]sers/[a-z]|/home/[a-z]\n' > "$TAGROOT/list"
taglog="$TAGROOT/install.log"
if env LOC_FORBIDDEN_FILE="$TAGROOT/list" "$TAGROOT/clone/install.sh" \
     --prefix "$TAGROOT/bin" >"$taglog" 2>&1; then
  ok "tag-mode install succeeds with a word list installed"
else
  bad "tag-mode install succeeds with a word list installed (see $taglog)"
fi
# AND IT MUST HAVE RUN, NOT BEEN SKIPPED. The checker prints one line or the
# other, and only "tree is clean" says the worktree's .git file was read and
# accepted. Asserting the install's exit status alone passes a skipped check.
if grep -q 'check-clean: tree is clean' "$taglog"; then
  ok "the clean check ran inside the tag build"
else
  bad "the clean check ran inside the tag build (log says: $(grep -m1 'check-clean' "$taglog" || echo 'nothing'))"
fi
check "tag-mode install links loc" test -e "$TAGROOT/bin/loc"
tagver="$("$TAGROOT/bin/loc" version 2>/dev/null || true)"
if grep -q '9\.9\.9-test' <<<"$tagver"; then
  ok "the tag-mode link reports the tag's own version"
else
  bad "the tag-mode link reports the tag's own version (got: $tagver)"
fi
env LOC_FORBIDDEN_FILE="$TAGROOT/list" "$TAGROOT/clone/install.sh" \
  --uninstall --prefix "$TAGROOT/bin" >/dev/null 2>&1 || true
check_not "tag-mode uninstall removes the link it made" test -e "$TAGROOT/bin/loc"

say "— cold read —"
check "endpoint with zero prior state reads cleanly" \
  env LOC_IDENTITY=$(ep carol) loc read

say "— topic window expiry (short-window scratch stream) —"
env NATS_URL="$NURL" \
  nats stream add WINDOWTEST --subjects 'wtest.>' --retention limits \
    --max-age 2s --storage file --replicas 1 --defaults >/dev/null 2>&1
env NATS_URL="$NURL" \
  nats pub wtest.x "ephemeral" >/dev/null 2>&1
sleep 4
n="$(env NATS_URL="$NURL" \
  nats stream info WINDOWTEST --json 2>/dev/null | jq -r .state.messages)"
if [[ "$n" == "0" ]]; then ok "messages expire at the window's edge (teardown-by-retention)"; else bad "messages expire at the window's edge (got $n)"; fi

# ── the listener's cases ─────────────────────────────────────────────────────
# Everything from here to the wake spool is ATTENDANCE: registration, wakes, the
# breaker, orphans, the fifo, the spool. All of it is THE SHELL TOOL'S SHAPE of
# attendance — a background listener, a pidfile, a fifo, a spool — and the Go
# build has none of those things. Its listener is the `mcp` verb: a stdio MCP
# server the agent runtime launches, which registers the seat with the runtime's
# own pid, rings the pane's bell, and hands the mail over through a tool. The
# properties are the same and the mechanism is not, and a case cannot be run
# against a mechanism that does not exist without changing what the case means.
# The suite also speaks no MCP, and it is not going to learn: these cases are
# skipped by name, with their reason and where the same property IS proven for
# this build, and the count is printed.
say "— delivery: registration, wake, backlog, liveness —"
skip "registration starts a live listener (verified pid, not file existence)" "this build's listener is not sub/unsub: it is the `mcp` verb, a stdio MCP server the agent runtime launches, which registers the seat with the runtime's pid and rings the pane's bell — so there is no pidfile, fifo or spool here for these cases to interrogate, and the suite speaks no MCP to drive one; the same properties are pinned for this build in cmd/loc/mcp_test.go (registration, the pid, the bell, EOF frees the seat, a live seat is not taken), cmd/loc/mcp_bell_test.go (one wake per window, coalescing, wake-on-backlog) and internal/mcpserve (the breaker, the delivery log, the tools)"
skip "registration invoked the channel-capture hook" "this build's listener is not sub/unsub: it is the `mcp` verb, a stdio MCP server the agent runtime launches, which registers the seat with the runtime's pid and rings the pane's bell — so there is no pidfile, fifo or spool here for these cases to interrogate, and the suite speaks no MCP to drive one; the same properties are pinned for this build in cmd/loc/mcp_test.go (registration, the pid, the bell, EOF frees the seat, a live seat is not taken), cmd/loc/mcp_bell_test.go (one wake per window, coalescing, wake-on-backlog) and internal/mcpserve (the breaker, the delivery log, the tools)"
skip "registry names an attending endpoint on a freshly bootstrapped deployment" "this build's `registry` reports no attendance at all: it reads the registration records from the ledger on this machine and prints them, and whether an endpoint holds a queue is `loc status`'s answer, derived from the broker. Two commands answering one question from two kinds of evidence would disagree one day and neither output would say which was wrong, so the fact lives in one of them. Bootstrapping this deployment also registers nobody — this build's listener is the `mcp` verb, a stdio MCP server the agent runtime launches, and the suite speaks no MCP to drive one — so there is no registered endpoint here for the case to read either. What `registry` does report is pinned in internal/presence/registry_test.go (the grouping, every label, the (not given) cases, the unreadable row, the empty answers, the --json shape) and in cmd/loc/presence_verbs_test.go (the verb against ledger fixtures, with no identity and with no presence extension). Un-skipping this case is tracked separately: it needs a rewritten assertion, not an un-skip"
skip "wake hook fired on arrival (first message wakes instantly)" "this build's listener is not sub/unsub: it is the `mcp` verb, a stdio MCP server the agent runtime launches, which registers the seat with the runtime's pid and rings the pane's bell — so there is no pidfile, fifo or spool here for these cases to interrogate, and the suite speaks no MCP to drive one; the same properties are pinned for this build in cmd/loc/mcp_test.go (registration, the pid, the bell, EOF frees the seat, a live seat is not taken), cmd/loc/mcp_bell_test.go (one wake per window, coalescing, wake-on-backlog) and internal/mcpserve (the breaker, the delivery log, the tools)"
skip "a wake never makes a message unreadable (read presents queue and spool)" "this build's listener is not sub/unsub: it is the `mcp` verb, a stdio MCP server the agent runtime launches, which registers the seat with the runtime's pid and rings the pane's bell — so there is no pidfile, fifo or spool here for these cases to interrogate, and the suite speaks no MCP to drive one; the same properties are pinned for this build in cmd/loc/mcp_test.go (registration, the pid, the bell, EOF frees the seat, a live seat is not taken), cmd/loc/mcp_bell_test.go (one wake per window, coalescing, wake-on-backlog) and internal/mcpserve (the breaker, the delivery log, the tools)"
skip "one message, one wake; the tap survives the event (no churn)" "this build's listener is not sub/unsub: it is the `mcp` verb, a stdio MCP server the agent runtime launches, which registers the seat with the runtime's pid and rings the pane's bell — so there is no pidfile, fifo or spool here for these cases to interrogate, and the suite speaks no MCP to drive one; the same properties are pinned for this build in cmd/loc/mcp_test.go (registration, the pid, the bell, EOF frees the seat, a live seat is not taken), cmd/loc/mcp_bell_test.go (one wake per window, coalescing, wake-on-backlog) and internal/mcpserve (the breaker, the delivery log, the tools)"
skip "unsub ends attendance (pidfile gone)" "this build's listener is not sub/unsub: it is the `mcp` verb, a stdio MCP server the agent runtime launches, which registers the seat with the runtime's pid and rings the pane's bell — so there is no pidfile, fifo or spool here for these cases to interrogate, and the suite speaks no MCP to drive one; the same properties are pinned for this build in cmd/loc/mcp_test.go (registration, the pid, the bell, EOF frees the seat, a live seat is not taken), cmd/loc/mcp_bell_test.go (one wake per window, coalescing, wake-on-backlog) and internal/mcpserve (the breaker, the delivery log, the tools)"
skip "wake-on-backlog: (re)registration wakes with the waiting count" "this build's listener is not sub/unsub: it is the `mcp` verb, a stdio MCP server the agent runtime launches, which registers the seat with the runtime's pid and rings the pane's bell — so there is no pidfile, fifo or spool here for these cases to interrogate, and the suite speaks no MCP to drive one; the same properties are pinned for this build in cmd/loc/mcp_test.go (registration, the pid, the bell, EOF frees the seat, a live seat is not taken), cmd/loc/mcp_bell_test.go (one wake per window, coalescing, wake-on-backlog) and internal/mcpserve (the breaker, the delivery log, the tools)"
skip "listener exits when the watched session dies (employment-tied)" "this build's listener is not sub/unsub: it is the `mcp` verb, a stdio MCP server the agent runtime launches, which registers the seat with the runtime's pid and rings the pane's bell — so there is no pidfile, fifo or spool here for these cases to interrogate, and the suite speaks no MCP to drive one; the same properties are pinned for this build in cmd/loc/mcp_test.go (registration, the pid, the bell, EOF frees the seat, a live seat is not taken), cmd/loc/mcp_bell_test.go (one wake per window, coalescing, wake-on-backlog) and internal/mcpserve (the breaker, the delivery log, the tools)"
skip "say-semantics: send to a known-absent endpoint is refused" "this build's listener is not sub/unsub: it is the `mcp` verb, a stdio MCP server the agent runtime launches, which registers the seat with the runtime's pid and rings the pane's bell — so there is no pidfile, fifo or spool here for these cases to interrogate, and the suite speaks no MCP to drive one; the same properties are pinned for this build in cmd/loc/mcp_test.go (registration, the pid, the bell, EOF frees the seat, a live seat is not taken), cmd/loc/mcp_bell_test.go (one wake per window, coalescing, wake-on-backlog) and internal/mcpserve (the breaker, the delivery log, the tools)"
skip "say-semantics: send to an attending endpoint succeeds" "this build's listener is not sub/unsub: it is the `mcp` verb, a stdio MCP server the agent runtime launches, which registers the seat with the runtime's pid and rings the pane's bell — so there is no pidfile, fifo or spool here for these cases to interrogate, and the suite speaks no MCP to drive one; the same properties are pinned for this build in cmd/loc/mcp_test.go (registration, the pid, the bell, EOF frees the seat, a live seat is not taken), cmd/loc/mcp_bell_test.go (one wake per window, coalescing, wake-on-backlog) and internal/mcpserve (the breaker, the delivery log, the tools)"
skip "breaker caps wakes and trips loud (1 wake for 3 sends at cap 1/min)" "this build's listener is not sub/unsub: it is the `mcp` verb, a stdio MCP server the agent runtime launches, which registers the seat with the runtime's pid and rings the pane's bell — so there is no pidfile, fifo or spool here for these cases to interrogate, and the suite speaks no MCP to drive one; the same properties are pinned for this build in cmd/loc/mcp_test.go (registration, the pid, the bell, EOF frees the seat, a live seat is not taken), cmd/loc/mcp_bell_test.go (one wake per window, coalescing, wake-on-backlog) and internal/mcpserve (the breaker, the delivery log, the tools)"
skip "suppressed wakes lose nothing (all three messages readable)" "this build's listener is not sub/unsub: it is the `mcp` verb, a stdio MCP server the agent runtime launches, which registers the seat with the runtime's pid and rings the pane's bell — so there is no pidfile, fifo or spool here for these cases to interrogate, and the suite speaks no MCP to drive one; the same properties are pinned for this build in cmd/loc/mcp_test.go (registration, the pid, the bell, EOF frees the seat, a live seat is not taken), cmd/loc/mcp_bell_test.go (one wake per window, coalescing, wake-on-backlog) and internal/mcpserve (the breaker, the delivery log, the tools)"
say "— rapid unsub/resub leaves no orphan listener —"
skip "rapid unsub/resub leaves no orphan listener (no wake after final unsub)" "this build's listener is not sub/unsub: it is the `mcp` verb, a stdio MCP server the agent runtime launches, which registers the seat with the runtime's pid and rings the pane's bell — so there is no pidfile, fifo or spool here for these cases to interrogate, and the suite speaks no MCP to drive one; the same properties are pinned for this build in cmd/loc/mcp_test.go (registration, the pid, the bell, EOF frees the seat, a live seat is not taken), cmd/loc/mcp_bell_test.go (one wake per window, coalescing, wake-on-backlog) and internal/mcpserve (the breaker, the delivery log, the tools)"
say "— the pidfile is the listener's licence to live —"
skip "listener exits within 5 s when its pidfile is removed" "this build's listener is not sub/unsub: it is the `mcp` verb, a stdio MCP server the agent runtime launches, which registers the seat with the runtime's pid and rings the pane's bell — so there is no pidfile, fifo or spool here for these cases to interrogate, and the suite speaks no MCP to drive one; the same properties are pinned for this build in cmd/loc/mcp_test.go (registration, the pid, the bell, EOF frees the seat, a live seat is not taken), cmd/loc/mcp_bell_test.go (one wake per window, coalescing, wake-on-backlog) and internal/mcpserve (the breaker, the delivery log, the tools)"
say "— attendance leaves nothing behind on disk —"
skip "repeated sub/unsub leaves no stale fifo behind" "this build's listener is not sub/unsub: it is the `mcp` verb, a stdio MCP server the agent runtime launches, which registers the seat with the runtime's pid and rings the pane's bell — so there is no pidfile, fifo or spool here for these cases to interrogate, and the suite speaks no MCP to drive one; the same properties are pinned for this build in cmd/loc/mcp_test.go (registration, the pid, the bell, EOF frees the seat, a live seat is not taken), cmd/loc/mcp_bell_test.go (one wake per window, coalescing, wake-on-backlog) and internal/mcpserve (the breaker, the delivery log, the tools)"
say "— a stranded wake spool is recovered by the next read —"
skip "stranded wake-spool bodies are presented by the next read" "this build's listener is not sub/unsub: it is the `mcp` verb, a stdio MCP server the agent runtime launches, which registers the seat with the runtime's pid and rings the pane's bell — so there is no pidfile, fifo or spool here for these cases to interrogate, and the suite speaks no MCP to drive one; the same properties are pinned for this build in cmd/loc/mcp_test.go (registration, the pid, the bell, EOF frees the seat, a live seat is not taken), cmd/loc/mcp_bell_test.go (one wake per window, coalescing, wake-on-backlog) and internal/mcpserve (the breaker, the delivery log, the tools)"
skip "a presented wake-spool body is forgotten, not re-presented" "this build's listener is not sub/unsub: it is the `mcp` verb, a stdio MCP server the agent runtime launches, which registers the seat with the runtime's pid and rings the pane's bell — so there is no pidfile, fifo or spool here for these cases to interrogate, and the suite speaks no MCP to drive one; the same properties are pinned for this build in cmd/loc/mcp_test.go (registration, the pid, the bell, EOF frees the seat, a live seat is not taken), cmd/loc/mcp_bell_test.go (one wake per window, coalescing, wake-on-backlog) and internal/mcpserve (the breaker, the delivery log, the tools)"
skip "cleanup reaps only this deployment's listeners (a same-path listener from another home survives)" "this build's listener is not sub/unsub: it is the `mcp` verb, a stdio MCP server the agent runtime launches, which registers the seat with the runtime's pid and rings the pane's bell — so there is no pidfile, fifo or spool here for these cases to interrogate, and the suite speaks no MCP to drive one; the same properties are pinned for this build in cmd/loc/mcp_test.go (registration, the pid, the bell, EOF frees the seat, a live seat is not taken), cmd/loc/mcp_bell_test.go (one wake per window, coalescing, wake-on-backlog) and internal/mcpserve (the breaker, the delivery log, the tools)"
# ── end of the listener's cases ──────────────────────────────────────────────

say "— the front page shows what the tool prints —"
# README's "Two agents, one conversation" block is captured, not typed. This case
# replays its five commands in a fresh house (ada/bob/carol, own port, own server)
# and diffs the output against the block with timestamps masked. If the tool's
# output ever moves, the page moves with it or this fails.
skip "README transcript equals a fresh run (timestamps masked)" "the captured transcript addresses bare endpoints, which the presence model bars — a bare name is never an endpoint — so replaying it against this build would be replaying a deployment it cannot have; the Go build's render contract is internal/loc/render_test.go, which diffs this same README block directly"

say "— repo cleanliness (future-public discipline) —"
say "  (cleanliness list read from: $REAL_LOC_HOME/forbidden)"
# THE EXIT CODE IS READ, NOT THE TRUTHINESS OF THE COMMAND. The gate answers
# three ways — 0 clean, 1 found, 2 cannot measure — and a bare `if` folds 2 into
# the failure branch and reports a cannot-measure as a finding. The script's own
# header says the two are never conflated; its caller conflated them one file
# away. It failed closed, so this was a wrong message rather than a hole.
clean_rc=0
LOC_FORBIDDEN_FILE="$REAL_LOC_HOME/forbidden" "$ROOT/conformance/check-clean.sh" >/dev/null 2>&1 || clean_rc=$?
case "$clean_rc" in
  0) ok "repo carries no deployment/internal vocabulary" ;;
  1) bad "repo carries no deployment/internal vocabulary (run conformance/check-clean.sh)" ;;
  *) bad "cleanliness check CANNOT MEASURE (exit $clean_rc) — nothing was checked; run conformance/check-clean.sh to see why" ;;
esac
# With no list on the machine at all the check must still run and still pass on
# a clean tree — a missing list is a weaker check, never a hard failure.
check "cleanliness check runs on its generic list when no list is installed" \
  env LOC_FORBIDDEN_FILE=/nonexistent "$ROOT/conformance/check-clean.sh"
# And it must actually FAIL on a dirty tree. Asserting pass-only proves nothing:
# a checker that always exits 0 passes that. A scratch tree, one file holding a
# nonce word, a list naming that nonce — the check has to find it. The copy of
# the script sits at <tree>/conformance/ because the check walks up one level
# from itself to decide what tree it is checking.
CLEAN_T="$(mktemp -d)"; mkdir -p "$CLEAN_T/conformance"
cp "$ROOT/conformance/check-clean.sh" "$CLEAN_T/conformance/check-clean.sh"
CLEAN_NONCE="zzq$(( RANDOM ))vocab"
printf 'a line that says %s and should not survive review\n' "$CLEAN_NONCE" > "$CLEAN_T/leaky.md"
printf '# scratch list\n%s\n' "$CLEAN_NONCE" > "$CLEAN_T/list"
check_not "cleanliness check fails on a tree that carries a listed word" \
  env LOC_FORBIDDEN_FILE="$CLEAN_T/list" "$CLEAN_T/conformance/check-clean.sh"
rm -rf "$CLEAN_T"
# THE COMMIT LANE HAD NO ARM AT ALL UNTIL HERE (#197). Every case above
# exercises the TREE lane. The scratch trees are not git repositories, so the
# commit lane skips them and exits 0 — a pass that says nothing about it. Both
# halves of that lane shipped with a fire control that ran once and never again,
# and a detector whose control does not run at every revision is the class this
# suite exists to catch.
#
# This arm gives the lane a repository, with a CLEAN tree and the nonce only in
# a commit MESSAGE. If this case goes green the commit lane is what found it,
# and that rests on one line: check-clean.sh passes --exclude-dir=.git, so the
# tree lane never reads the object store the message is kept in. `measured:` a
# repository whose only nonce is under .git, run with an EMPTY commit range so
# the tree lane answers alone, exits 0. Were that exclusion removed this arm
# would still go red, for the wrong reason.
#
# Two traps, both measured while building the throwaway by hand. The script
# walks up one level from itself, so the copy sits at <tree>/conformance/. And
# the list lives OUTSIDE the scanned tree, or the tree lane finds the nonce in
# the list file and the case passes for the wrong reason.
CLEAN_T="$(mktemp -d)"; CLEAN_L="$(mktemp -d)"; mkdir -p "$CLEAN_T/conformance"
cp "$ROOT/conformance/check-clean.sh" "$CLEAN_T/conformance/check-clean.sh"
CLEAN_NONCE="zzq$(( RANDOM ))msg"
printf '# scratch list\n%s\n' "$CLEAN_NONCE" > "$CLEAN_L/list"
(
  cd "$CLEAN_T" || exit 1
  git init -q -b main . && git config user.email c@example.com && git config user.name C
  printf 'nothing to see here\n' > file.md
  git add -A && git commit -q -m "initial commit"
  git update-ref refs/remotes/origin/main "$(git rev-parse HEAD)"
  printf 'still nothing to see here\n' > file.md
  git add file.md
  git commit -q -m "chore: touch the file

the body of this message carries $CLEAN_NONCE and the tree does not"
) >/dev/null 2>&1
check_not "cleanliness check fails on a clean tree with a listed word in a commit message" \
  env LOC_FORBIDDEN_FILE="$CLEAN_L/list" "$CLEAN_T/conformance/check-clean.sh"
# AND THE SAME REPOSITORY PASSES ONCE THE MESSAGE IS CLEAN. Without this the arm
# above is satisfied by a checker that refuses every git repository, and
# "refuses everything" and "refuses correctly" are the same green.
( cd "$CLEAN_T" && git commit -q --amend -m "chore: touch the file" ) >/dev/null 2>&1
check "cleanliness check passes that same repository once the message is clean" \
  env LOC_FORBIDDEN_FILE="$CLEAN_L/list" "$CLEAN_T/conformance/check-clean.sh"
rm -rf "$CLEAN_T" "$CLEAN_L"

# ── the PATH lanes, and the premise the message arm rests on ───────────────
#
# Three arms, and none of them duplicates another. The first two give the path
# lanes a fire control at every revision instead of one that ran once in a
# scratchpad (#185 item 2). The third asserts what the message arm above only
# describes.
#
# EACH OF THE FIRST TWO MUST ISOLATE ONE LANE OR IT PROVES NOTHING. A word in an
# ADDED file's path is caught already, by accident: the added-lines scan prints
# path:line:content, so the path rides along beside the line. An arm built the
# obvious way goes red before and after and measures nothing. So the commit
# range is emptied for the tree lane, and for the commit lane the rename goes
# out AND BACK, leaving the tip clean.
CLEAN_T="$(mktemp -d)"; CLEAN_L="$(mktemp -d)"; mkdir -p "$CLEAN_T/conformance"
cp "$ROOT/conformance/check-clean.sh" "$CLEAN_T/conformance/check-clean.sh"
CLEAN_NONCE="zzq$(( RANDOM ))path"
printf '# scratch list\n%s\n' "$CLEAN_NONCE" > "$CLEAN_L/list"
(
  cd "$CLEAN_T" || exit 1
  git init -q -b main . && git config user.email c@example.com && git config user.name C
  printf 'clean content\n' > plain.md
  git add -A && git commit -q -m "initial commit"
  git mv plain.md "$CLEAN_NONCE-file.md"
  git commit -q -m "chore: rename"
  git update-ref refs/remotes/origin/main "$(git rev-parse HEAD)"
) >/dev/null 2>&1
check_not "cleanliness check fails on a listed word in a file PATH (tree lane alone)" \
  env LOC_FORBIDDEN_FILE="$CLEAN_L/list" "$CLEAN_T/conformance/check-clean.sh"
rm -rf "$CLEAN_T" "$CLEAN_L"

CLEAN_T="$(mktemp -d)"; CLEAN_L="$(mktemp -d)"; mkdir -p "$CLEAN_T/conformance"
cp "$ROOT/conformance/check-clean.sh" "$CLEAN_T/conformance/check-clean.sh"
CLEAN_NONCE="zzq$(( RANDOM ))rename"
printf '# scratch list\n%s\n' "$CLEAN_NONCE" > "$CLEAN_L/list"
(
  cd "$CLEAN_T" || exit 1
  git init -q -b main . && git config user.email c@example.com && git config user.name C
  printf 'clean content\n' > plain.md
  git add -A && git commit -q -m "initial commit"
  git update-ref refs/remotes/origin/main "$(git rev-parse HEAD)"
  git mv plain.md "$CLEAN_NONCE-tmp.md" && git commit -q -m "chore: rename out"
  git mv "$CLEAN_NONCE-tmp.md" plain.md && git commit -q -m "chore: rename back"
) >/dev/null 2>&1
check_not "cleanliness check fails on a PURE RENAME through a listed path (tip clean)" \
  env LOC_FORBIDDEN_FILE="$CLEAN_L/list" "$CLEAN_T/conformance/check-clean.sh"
rm -rf "$CLEAN_T" "$CLEAN_L"

# THE MESSAGE ARM ABOVE RESTS ON --exclude-dir=.git AND NOTHING ASSERTED IT.
# Measured during review: remove that exclusion and BOTH message arms still
# pass, so neither notices. The word is plain text in exactly one place,
# .git/COMMIT_EDITMSG, and `git commit --amend` rewrites that file — even an
# amend that FAILS does — while the commit object itself is compressed and no
# text grep reads it. So the arm that guards this needs its OWN repository, and
# must never amend.
#
# The tree lane answers alone here, by an emptied commit range, and must exit 0.
# Were the exclusion removed it would find the word in COMMIT_EDITMSG and this
# arm would go red.
CLEAN_T="$(mktemp -d)"; CLEAN_L="$(mktemp -d)"; mkdir -p "$CLEAN_T/conformance"
cp "$ROOT/conformance/check-clean.sh" "$CLEAN_T/conformance/check-clean.sh"
CLEAN_NONCE="zzq$(( RANDOM ))dotgit"
printf '# scratch list\n%s\n' "$CLEAN_NONCE" > "$CLEAN_L/list"
(
  cd "$CLEAN_T" || exit 1
  git init -q -b main . && git config user.email c@example.com && git config user.name C
  printf 'clean content\n' > plain.md
  git add -A && git commit -q -m "initial commit"
  git commit -q --allow-empty -m "chore: a commit whose body carries $CLEAN_NONCE"
  git update-ref refs/remotes/origin/main "$(git rev-parse HEAD)"
) >/dev/null 2>&1
# THE FIXTURE IS ASSERTED FIRST. If the word is not actually under .git in plain
# text, the arm below passes for free and guards nothing.
check "the .git fixture holds the word in plain text, so the arm below is not vacuous" \
  grep -rqI "$CLEAN_NONCE" "$CLEAN_T/.git"
check "cleanliness check does NOT read .git, so a message cannot reach the tree lane" \
  env LOC_FORBIDDEN_FILE="$CLEAN_L/list" "$CLEAN_T/conformance/check-clean.sh"
rm -rf "$CLEAN_T" "$CLEAN_L"

# THE TWO SCANS KEEP TWO EXCLUSION LISTS AND NOTHING ASSERTED THEY AGREE. The
# content scan excludes with grep's --exclude flags; the path scan excludes with
# find's -not -path. They are written to match and they can drift apart
# silently, which is the defect class this suite exists for.
#
# A word planted in an excluded directory, in BOTH the path and the content,
# must be invisible to both scans. The control beneath it proves the fixture
# can be seen at all.
CLEAN_T="$(mktemp -d)"; CLEAN_L="$(mktemp -d)"
mkdir -p "$CLEAN_T/conformance" "$CLEAN_T/.idea" "$CLEAN_T/.vscode"
cp "$ROOT/conformance/check-clean.sh" "$CLEAN_T/conformance/check-clean.sh"
CLEAN_NONCE="zzq$(( RANDOM ))excl"
printf '# scratch list\n%s\n' "$CLEAN_NONCE" > "$CLEAN_L/list"
printf 'ide state holding %s\n' "$CLEAN_NONCE" > "$CLEAN_T/.idea/$CLEAN_NONCE.md"
printf 'ide state holding %s\n' "$CLEAN_NONCE" > "$CLEAN_T/.vscode/$CLEAN_NONCE.md"
check "both scans exclude the same IDE directories, by path and by content" \
  env LOC_FORBIDDEN_FILE="$CLEAN_L/list" "$CLEAN_T/conformance/check-clean.sh"
printf 'a tracked file holding %s\n' "$CLEAN_NONCE" > "$CLEAN_T/seen.md"
check_not "CONTROL: the same word outside those directories IS found" \
  env LOC_FORBIDDEN_FILE="$CLEAN_L/list" "$CLEAN_T/conformance/check-clean.sh"
rm -rf "$CLEAN_T" "$CLEAN_L"
# An installed list must not switch the generic list off (#137). A maintainer's
# own list can omit one home-path form, and then that form passed on their machine
# and failed on a machine with no list. The installed list here names a nonce the
# tree does not carry, so only the generic list can find the planted path. The
# list sits outside the scanned tree, or the check would find the nonce in the
# list file itself and fail this case for the wrong reason. The path is built at
# run time because this file is itself scanned by the check.
CLEAN_T="$(mktemp -d)"; mkdir -p "$CLEAN_T/tree/conformance"
cp "$ROOT/conformance/check-clean.sh" "$CLEAN_T/tree/conformance/check-clean.sh"
printf '# scratch list\nzzq%snomatch\n' "$(( RANDOM ))" > "$CLEAN_T/list"
printf 'a line that says %s and should not survive review\n' \
  "$(printf '/ho%s/zed/src' me)" > "$CLEAN_T/tree/leaky.md"
check_not "cleanliness check applies the generic list when a deployment list is installed" \
  env LOC_FORBIDDEN_FILE="$CLEAN_T/list" "$CLEAN_T/tree/conformance/check-clean.sh"
rm -rf "$CLEAN_T"

# THE LAST ACCEPTANCE OF THE VERBS. The suite booted with `loc start`, so it
# stops with `loc stop`; the teardown trap runs it again if this line is never
# reached, and a second stop is a no-op that says so.
say "— the house stops when told —"
if "$LOC_BIN_DIR/loc" stop; then ok "loc stop ends the daemon it started"; else bad "loc stop ends the daemon it started"; fi
SERVER_PID=""

say ""
# The skipped count is printed on every run, zero included: "0 skipped" on the
# default lane is the evidence that the switch is off, which is a fact worth
# stating rather than assuming.
say "conformance: $PASS passed, $FAIL failed, $SKIP skipped (shell-only)"
[[ $FAIL -eq 0 ]]
