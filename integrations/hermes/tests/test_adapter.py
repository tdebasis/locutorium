"""Tests for the adapter, against stand-ins for Hermes and for loc.

They prove the adapter's own mechanics. They prove nothing about Hermes, and nothing about loc.
Run: python3 -m unittest discover -s tests
"""

import asyncio
import json
import logging
import os
import signal
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "stubs"))
sys.path.insert(0, os.path.join(HERE, ".."))

from gateway.config import PlatformConfig  # noqa: E402
from loc import adapter as loc_adapter  # noqa: E402
from loc import core  # noqa: E402

ME = "workshop.scribe"
FAKE_LOC = os.path.join(HERE, "fake_loc.py")


def envelope(uid, sender="workshop.alice", body="hello", to=ME):
    return json.dumps({"id": uid, "ts": "2026-09-30T05:55:36Z", "from": sender, "to": to, "kind": "msg", "body": body})


class AdapterCase(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.home = tempfile.mkdtemp(prefix="loc-adapter-test-")
        self.inbox = os.path.join(self.home, "inbox.jsonl")
        self.adapter = loc_adapter.LocAdapter(PlatformConfig(extra={
            "identity": ME, "loc_bin": FAKE_LOC, "loc_home": self.home, "inbox_log": self.inbox}))
        self.logs = []
        handler = logging.Handler()
        handler.emit = lambda record: self.logs.append((record.levelno, record.getMessage()))
        self._handler = handler
        loc_adapter.logger.addHandler(handler)
        loc_adapter.logger.setLevel(logging.DEBUG)

    async def asyncTearDown(self):
        await self.adapter.disconnect()
        loc_adapter.logger.removeHandler(self._handler)

    def path(self, name):
        return os.path.join(self.home, name)

    def queue(self, *lines):
        with open(self.path("queue.jsonl"), "a") as f:
            f.write("\n".join(lines) + "\n")

    def port(self):
        return self.adapter._server.sockets[0].getsockname()[1]

    async def post(self, path):
        reader, writer = await asyncio.open_connection("127.0.0.1", self.port())
        writer.write(("POST %s HTTP/1.1\r\nHost: x\r\nContent-Length: 2\r\n\r\n{}" % path).encode())
        await writer.drain()
        status = (await reader.readline()).decode()
        writer.close()
        return int(status.split(" ")[1])

    async def ring(self):
        self.assertEqual(await self.post(self.adapter._bell_path), 202)
        await self.settle()

    async def settle(self):
        for _ in range(200):
            if not self.adapter._draining:
                return
            await asyncio.sleep(0.02)
        self.fail("the drain did not end")

    def warnings(self):
        return [text for level, text in self.logs if level >= logging.WARNING]

    # -- connection ----------------------------------------------------------

    async def test_connect_takes_the_seat_with_the_webhook_type(self):
        self.assertTrue(await self.adapter.connect())
        self.assertTrue(os.path.exists(self.path("registered")))
        with open(self.path("env.json")) as f:
            env = json.load(f)
        self.assertEqual(env["LOC_IDENTITY"], ME)
        self.assertEqual(env["LOC_LISTENER_TYPE"], "webhook")
        self.assertEqual(env["LOC_LISTENER_ADDRESS"], "http://127.0.0.1:%d%s" % (self.port(), self.adapter._bell_path))

    async def test_connect_again_keeps_the_same_server(self):
        self.assertTrue(await self.adapter.connect())
        with open(self.path("registered")) as f:
            first = f.read()
        self.assertTrue(await self.adapter.connect(is_reconnect=True))
        with open(self.path("registered")) as f:
            self.assertEqual(f.read(), first)

    async def test_a_refused_seat_fails_the_connect_and_says_why(self):
        open(self.path("refuse"), "w").close()
        self.assertFalse(await self.adapter.connect())
        self.assertTrue(any("held by a LIVE process" in text for text in self.warnings()), self.logs)

    async def test_disconnect_frees_the_seat(self):
        await self.adapter.connect()
        await self.adapter.disconnect()
        self.assertFalse(os.path.exists(self.path("registered")))

    async def test_a_row_older_than_this_server_does_not_count_as_registered(self):
        # The row of a dead server reads "registered: yes". loc replaces it a moment later.
        answers = ["workshop.scribe\n  registered: yes (since 2020-01-01T00:00:00.000Z; webhook 0)\n"] * 3
        calls = []
        real = loc_adapter._run_loc

        async def status_first_stale(extra, identity, *args):
            if args[:1] == ("status",) and answers:
                calls.append("stale")
                return 0, answers.pop(), ""
            return await real(extra, identity, *args)

        loc_adapter._run_loc = status_first_stale
        try:
            self.assertTrue(await self.adapter.connect())
        finally:
            loc_adapter._run_loc = real
        self.assertEqual(len(calls), 3, "connect accepted a registration older than its own server")

    # -- H4: what "the seat is lost" means -------------------------------------

    async def test_a_killed_wrapper_does_not_lose_the_seat(self):
        await self.adapter.connect()
        self.adapter._seat.send_signal(signal.SIGKILL)
        await self.adapter._seat.wait()
        await asyncio.sleep(0.3)
        self.assertIsNone(self.adapter.fatal)
        self.assertTrue(os.path.exists(self.path("registered")), "the server left when only the wrapper died")
        # The bell still works, and a second connect starts no second server.
        self.queue(envelope("after-kill"))
        await self.ring()
        self.assertEqual([e.message_id for e in self.adapter.events], ["after-kill"])
        self.assertTrue(await self.adapter.connect(is_reconnect=True))
        # Closing the input still reaches the server, although the wrapper is gone.
        await self.adapter.disconnect()
        self.assertFalse(os.path.exists(self.path("registered")))

    async def test_the_seat_is_lost_when_the_server_exits(self):
        await self.adapter.connect()
        with open(self.path("registered")) as f:
            os.kill(int(f.read()), signal.SIGKILL)
        for _ in range(100):
            if self.adapter.fatal:
                break
            await asyncio.sleep(0.02)
        self.assertIsNotNone(self.adapter.fatal)
        self.assertEqual(self.adapter.fatal[0], "loc_seat_lost")
        self.assertTrue(self.adapter.fatal[2], "the loss must be retryable")

    # -- H1: a bus message is never a gateway command ---------------------------

    async def test_a_body_that_starts_with_a_slash_is_conversation(self):
        await self.adapter.connect()
        self.queue(envelope("slash", body="/yolo"), envelope("spaced", body="  /restart"))
        await self.ring()
        self.assertEqual([e.text for e in self.adapter.events], ["/yolo", "  /restart"])
        for event in self.adapter.events:
            self.assertFalse(event.allow_gateway_control)
            self.assertFalse(event.is_command())

    async def test_a_bus_message_may_go_unanswered(self):
        # Hermes lets the model's [SILENT] stand only when reply_expected is False.
        await self.adapter.connect()
        self.queue(envelope("notice", body="No reply is needed."))
        await self.ring()
        self.assertIs(self.adapter.events[0].reply_expected, False)

    # -- H2: the seat's own name -------------------------------------------------

    async def test_mail_under_the_seats_own_name_is_not_passed_on(self):
        await self.adapter.connect()
        self.queue(envelope("own", sender=ME), envelope("other"))
        await self.ring()
        self.assertEqual([e.message_id for e in self.adapter.events], ["other"])
        self.assertTrue(any("own" in text and "own name" in text for text in self.warnings()), self.logs)

    async def test_the_adapter_does_not_send_to_its_own_seat(self):
        await self.adapter.connect()
        result = await self.adapter.send(ME, "hello me")
        self.assertFalse(result.success)
        self.assertFalse(os.path.exists(self.path("sent.jsonl")))

    # -- H3: mail that was read is never dropped in silence ------------------------

    async def test_every_sender_loc_accepts_is_passed_on(self):
        await self.adapter.connect()
        senders = ["ada", "Test.Upper", "test.under_score", "a.b.c", "workshop.alice"]
        self.queue(*[envelope("m%d" % i, sender=s) for i, s in enumerate(senders)])
        await self.ring()
        self.assertEqual([e.source.chat_id for e in self.adapter.events], senders)

    async def test_what_is_not_passed_on_is_logged_with_its_id(self):
        await self.adapter.connect()
        self.queue(envelope("blank", body="   "), envelope("room", to="standup"), "not json", envelope("ok"))
        await self.ring()
        self.assertEqual([e.message_id for e in self.adapter.events], ["ok"])
        text = "\n".join(self.warnings())
        for what in ("blank", "room", "not json"):
            self.assertIn(what, text)

    async def test_mail_is_written_down_before_it_is_passed_on(self):
        await self.adapter.connect()
        self.adapter.fail_handler = True
        self.queue(envelope("doomed", body="the only copy"))
        await self.ring()
        self.assertEqual(self.adapter.events, [])
        with open(self.inbox) as f:
            self.assertIn("the only copy", f.read())
        self.assertEqual(os.stat(self.inbox).st_mode & 0o777, 0o600)
        errors = [text for level, text in self.logs if level >= logging.ERROR]
        self.assertTrue(any("doomed" in text and self.inbox in text for text in errors), self.logs)

    async def test_mail_taken_by_a_read_that_is_cancelled_is_written_down(self):
        await self.adapter.connect()
        open(self.path("slow_read"), "w").close()
        self.queue(envelope("mid-stop", body="taken just before the stop"))
        self.assertEqual(await self.post(self.adapter._bell_path), 202)
        for _ in range(200):
            if not os.path.exists(self.path("queue.jsonl")):
                break
            await asyncio.sleep(0.02)
        await asyncio.sleep(0.2)
        await self.adapter.disconnect()  # cancels the drain while `loc read` is still running
        with open(self.inbox) as f:
            self.assertIn("taken just before the stop", f.read())
        errors = [text for level, text in self.logs if level >= logging.ERROR]
        self.assertTrue(
            any("mid-stop" in text and "stopped" in text and self.inbox in text for text in errors), self.logs)

    async def test_a_stop_between_two_messages_names_each_one_not_passed_on(self):
        await self.adapter.connect()
        passed = []

        async def slow_handler(event):
            passed.append(event.message_id)
            if event.message_id == "second":
                await asyncio.sleep(60)  # the stop arrives while this one is in flight

        self.adapter._message_handler = slow_handler
        self.queue(envelope("first"), envelope("second"), envelope("third"))
        self.assertEqual(await self.post(self.adapter._bell_path), 202)
        for _ in range(200):
            if "second" in passed:
                break
            await asyncio.sleep(0.02)
        await self.adapter.disconnect()
        errors = "\n".join(text for level, text in self.logs if level >= logging.ERROR)
        self.assertEqual(passed, ["first", "second"])
        self.assertNotIn("first", errors)
        for uid in ("second", "third"):
            self.assertIn(uid, errors, "a message that was read and not passed on is not named")
        self.assertIn("the adapter stopped", errors)

    async def test_with_no_handler_the_mail_stays_in_the_queue(self):
        await self.adapter.connect()
        self.adapter._message_handler = None
        self.queue(envelope("waiting"))
        await self.ring()
        self.assertTrue(os.path.exists(self.path("queue.jsonl")), "the mail was read with nobody to give it to")

    # -- the bell ---------------------------------------------------------------

    async def test_only_the_token_path_rings(self):
        await self.adapter.connect()
        self.queue(envelope("one"))
        for path in ("/bell", "/bell/", "/bell/" + "0" * 32, "/", self.adapter._bell_path + "/x"):
            self.assertEqual(await self.post(path), 404, path)
        await asyncio.sleep(0.1)
        self.assertEqual(self.adapter.events, [])
        await self.ring()
        self.assertEqual(len(self.adapter.events), 1)

    async def test_bells_during_a_drain_ask_for_one_more_drain(self):
        await self.adapter.connect()
        drains = []

        async def slow():
            drains.append(1)
            await asyncio.sleep(0.2)

        self.adapter._drain_once = slow
        for _ in range(25):
            self.assertEqual(await self.post(self.adapter._bell_path), 202)
        await self.settle()
        self.assertEqual(len(drains), 2)

    # -- send ---------------------------------------------------------------------

    async def test_a_reply_goes_to_the_sender(self):
        await self.adapter.connect()
        result = await self.adapter.send("workshop.alice", "- a body that starts with a dash")
        self.assertTrue(result.success)
        self.assertEqual(result.message_id, "00000000-0000-0000-0000-000000000001")
        with open(self.path("sent.jsonl")) as f:
            sent = json.loads(f.read())
        self.assertEqual(sent, {"from": ME, "to": "workshop.alice", "body": "- a body that starts with a dash"})

    async def test_a_reply_that_is_too_long_is_cut_and_says_so(self):
        await self.adapter.connect()
        await self.adapter.send("workshop.alice", "x" * (core.MAX_BODY_CHARS + 1))
        with open(self.path("sent.jsonl")) as f:
            body = json.loads(f.read())["body"]
        self.assertEqual(len(body), core.MAX_BODY_CHARS)
        self.assertTrue(body.endswith(core.CUT_MARK))

    async def test_with_a_recipient_list_the_adapter_sends_to_no_other_seat(self):
        self.adapter._extra["allowed_recipients"] = ["workshop.carol"]
        await self.adapter.connect()
        refused = await self.adapter.send("workshop.alice", "hello")
        self.assertFalse(refused.success)
        self.assertIn("allowed_recipients", refused.error)
        self.assertFalse(os.path.exists(self.path("sent.jsonl")))
        self.assertTrue((await self.adapter.send("workshop.carol", "hello")).success)

    async def test_a_recipient_list_that_is_wrong_allows_nobody(self):
        await self.adapter.connect()
        for wrong in ([], {"workshop.alice": True}, 7, "", None, False):
            self.adapter._extra["allowed_recipients"] = wrong
            self.assertFalse((await self.adapter.send("workshop.alice", "hello")).success, repr(wrong))
        self.assertFalse(os.path.exists(self.path("sent.jsonl")))
        # An absent key means no restriction, and a comma-separated string is a list.
        del self.adapter._extra["allowed_recipients"]
        self.assertTrue((await self.adapter.send("workshop.alice", "hello")).success)
        self.adapter._extra["allowed_recipients"] = "workshop.bob, workshop.alice"
        self.assertTrue((await self.adapter.send("workshop.alice", "hello")).success)

    async def test_a_reply_to_a_name_that_is_not_a_seat_fails_loudly(self):
        await self.adapter.connect()
        result = await self.adapter.send("ada", "hello")
        self.assertFalse(result.success)
        self.assertTrue(any("ada" in text for text in self.warnings()), self.logs)


if __name__ == "__main__":
    unittest.main()
