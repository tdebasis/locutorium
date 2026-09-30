"""Tests for the pure helpers. Run: python3 -m unittest discover -s tests"""

import json
import os
import sys
import unittest

# core.py imports nothing from Hermes. Import it as a plain module, so that the
# package __init__ (which needs Hermes) is not loaded.
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "loc"))
import core  # noqa: E402

ME = "workshop.scribe"


def line(**fields):
    base = {"id": "u-1", "ts": "2026-09-30T05:55:36Z", "from": "workshop.alice", "to": ME, "kind": "msg", "body": "hello"}
    base.update(fields)
    return json.dumps(base)


class SortReadOutput(unittest.TestCase):
    def ids(self, text):
        messages, declined = core.sort_read_output(text, ME)
        return [m["id"] for m in messages], declined

    def test_a_queue_message_is_passed_on(self):
        messages, declined = core.sort_read_output(line() + "\n", ME)
        self.assertEqual(len(messages), 1)
        self.assertEqual(messages[0]["from"], "workshop.alice")
        self.assertEqual(declined, [])

    def test_a_topic_message_is_declined_by_id(self):
        ids, declined = self.ids("\n".join([line(id="q"), line(id="t", to="standup")]))
        self.assertEqual(ids, ["q"])
        self.assertEqual([what for what, _ in declined], ["t"])

    def test_a_line_that_is_not_an_envelope_is_declined_and_named(self):
        ids, declined = self.ids("\n".join(["", "not json", "[1, 2]", line(id="ok")]))
        self.assertEqual(ids, ["ok"])
        self.assertEqual([what for what, _ in declined], ["not json", "[1, 2]"])

    def test_any_sender_loc_accepts_is_passed_on(self):
        senders = ["ada", "Test.Upper", "test.under_score", "a.b.c"]
        text = "\n".join(line(id=s, **{"from": s}) for s in senders)
        ids, declined = self.ids(text)
        self.assertEqual(ids, senders)
        self.assertEqual(declined, [])

    def test_mail_under_the_seats_own_name_is_declined(self):
        ids, declined = self.ids(line(id="own", **{"from": ME}))
        self.assertEqual(ids, [])
        self.assertEqual(declined[0][0], "own")
        self.assertIn("own name", declined[0][1])

    def test_no_sender_and_no_body_are_declined_with_the_id(self):
        text = "\n".join([
            line(id="no-sender", **{"from": None}), line(id="blank-sender", **{"from": "  "}),
            line(id="empty", body="   "), line(id="number", body=7), line(id="ok"),
        ])
        ids, declined = self.ids(text)
        self.assertEqual(ids, ["ok"])
        self.assertEqual([what for what, _ in declined], ["no-sender", "blank-sender", "empty", "number"])

    def test_nothing_is_dropped_without_a_record(self):
        lines = [line(id="a"), "junk", line(id="b", to="room"), line(id="c", body=""), line(id="d")]
        messages, declined = core.sort_read_output("\n".join(lines), ME)
        self.assertEqual(len(messages) + len(declined), len(lines))

    def test_order_is_kept(self):
        ids, _ = self.ids("\n".join(line(id=str(i)) for i in range(5)))
        self.assertEqual(ids, ["0", "1", "2", "3", "4"])

    def test_empty_output_gives_nothing(self):
        self.assertEqual(core.sort_read_output("", ME), ([], []))


class Endpoint(unittest.TestCase):
    def test_valid(self):
        for name in ("workshop.scribe", "a.b", "team-1.agent-2"):
            self.assertTrue(core.valid_endpoint(name), name)

    def test_invalid(self):
        for name in ("", "scribe", "a.b.c", "Work.Scribe", "a_b.c", "a.b c", None, 7):
            self.assertFalse(core.valid_endpoint(name), name)


class SendOutput(unittest.TestCase):
    def test_uid_is_found(self):
        out = "sent → queue.workshop.scribe uid=427ac8d1-938a-456d-babb-97583f5c66e5\n"
        self.assertEqual(core.parse_send_uid(out), "427ac8d1-938a-456d-babb-97583f5c66e5")

    def test_no_uid(self):
        self.assertIsNone(core.parse_send_uid("refused"))
        self.assertIsNone(core.parse_send_uid(""))


class ClipBody(unittest.TestCase):
    def test_short_body_is_kept(self):
        self.assertEqual(core.clip_body("abc"), ("abc", False))

    def test_long_body_is_cut_to_the_limit_and_marked(self):
        body, clipped = core.clip_body("x" * (core.MAX_BODY_CHARS + 1))
        self.assertTrue(clipped)
        self.assertEqual(len(body), core.MAX_BODY_CHARS)
        self.assertTrue(body.endswith(core.CUT_MARK))

    def test_the_limit_counts_characters(self):
        body, clipped = core.clip_body("🔔" * core.MAX_BODY_CHARS)
        self.assertFalse(clipped)
        self.assertEqual(len(body), core.MAX_BODY_CHARS)


class Http(unittest.TestCase):
    def test_a_bell_request_head(self):
        head = b"POST /bell HTTP/1.1\r\nHost: 127.0.0.1:1\r\nContent-Type: application/json\r\nContent-Length: 93"
        self.assertEqual(core.parse_request_head(head), ("POST", "/bell", 93))

    def test_a_head_with_no_length(self):
        self.assertEqual(core.parse_request_head(b"GET /health HTTP/1.1\r\nHost: x"), ("GET", "/health", 0))

    def test_a_bad_head(self):
        self.assertEqual(core.parse_request_head(b"nonsense"), ("", "", 0))
        self.assertEqual(core.parse_request_head(b""), ("", "", 0))

    def test_a_bad_length_is_zero(self):
        self.assertEqual(core.parse_request_head(b"POST /bell HTTP/1.1\r\nContent-Length: many")[2], 0)
        self.assertEqual(core.parse_request_head(b"POST /bell HTTP/1.1\r\nContent-Length: -5")[2], 0)

    def test_only_a_post_to_the_exact_bell_path_is_a_bell(self):
        path = core.bell_path("abc123")
        self.assertEqual(path, "/bell/abc123")
        self.assertTrue(core.is_bell("POST", path, path))
        self.assertFalse(core.is_bell("GET", path, path))
        self.assertFalse(core.is_bell("POST", "/bell", path))
        self.assertFalse(core.is_bell("POST", path + "?x=1", path))
        self.assertFalse(core.is_bell("POST", path + "/extra", path))
        self.assertFalse(core.is_bell("POST", "", ""), "no bell path set means no bell")

    def test_the_time_of_registration_is_read_from_loc_status(self):
        text = "workshop.scribe\n  registered: yes (since 2026-09-30T07:41:33.453Z; webhook 0.2.0)\n"
        since = core.registered_since(text)
        self.assertEqual(since.isoformat(), "2026-09-30T07:41:33.453000+00:00")
        whole = core.registered_since("  registered: yes (since 2026-09-30T07:41:33Z; none 0)")
        self.assertEqual(whole.isoformat(), "2026-09-30T07:41:33+00:00")
        self.assertIsNone(core.registered_since("workshop.scribe\n  registered: no\n"))
        self.assertIsNone(core.registered_since("  registered: yes (since soon; none 0)"))
        self.assertIsNone(core.registered_since(""))

    def test_the_response_is_complete(self):
        self.assertEqual(
            core.http_response(202, "Accepted"),
            b"HTTP/1.1 202 Accepted\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")


class Handshake(unittest.TestCase):
    def test_two_json_lines(self):
        hello, ready = core.mcp_handshake_lines()
        self.assertTrue(hello.endswith(b"\n") and ready.endswith(b"\n"))
        self.assertEqual(json.loads(hello)["method"], "initialize")
        self.assertEqual(json.loads(ready)["method"], "notifications/initialized")
        self.assertNotIn("id", json.loads(ready))


if __name__ == "__main__":
    unittest.main()
