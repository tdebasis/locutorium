"""Pure helpers for the loc platform adapter.

Nothing in this module imports Hermes, so the tests run with a plain Python.
"""

import json
import re
from datetime import datetime, timezone
from typing import Any, Dict, List, Optional, Tuple

# loc refuses a body longer than this many characters (internal/loc/body.go, MaxBodyChars).
MAX_BODY_CHARS = 4000

# Put at the end of a reply that was cut, so that the reader knows it is not whole.
CUT_MARK = "\n[cut: this reply was longer than the 4000-character limit of the bus]"

# The largest bell request the listener reads. A real bell is about 120 bytes.
MAX_REQUEST_BYTES = 64 * 1024

_ENDPOINT = re.compile(r"^[a-z0-9-]+\.[a-z0-9-]+$")
_UID = re.compile(r"uid=([0-9a-f-]{8,})")


def valid_endpoint(name: Any) -> bool:
    """True when the name has the form a loc seat must have: <instance>.<agent>."""
    return isinstance(name, str) and bool(_ENDPOINT.match(name))


def sort_read_output(text: str, identity: str) -> Tuple[List[Dict[str, Any]], List[Tuple[str, str]]]:
    """Sort the output of `loc read --json` into messages to pass on and lines to decline.

    The output is JSON Lines, one envelope per line: the queue messages of this seat, then the topic
    messages. `loc read` has already taken every one of them, so nothing may be dropped in silence.

    Returns ``(messages, declined)``. ``declined`` is a list of ``(what, reason)``; ``what`` is the
    message id when the line has one, else the start of the line.

    A sender is NOT checked against the seat-name pattern. loc accepts a sender of any form, so a
    message from one must reach the agent. The reply to it can fail, and that failure is logged.
    """
    messages: List[Dict[str, Any]] = []
    declined: List[Tuple[str, str]] = []
    for raw in text.splitlines():
        line = raw.strip()
        if not line:
            continue
        try:
            envelope = json.loads(line)
        except ValueError:
            declined.append((line[:80], "not JSON"))
            continue
        if not isinstance(envelope, dict):
            declined.append((line[:80], "not a JSON object"))
            continue
        what = str(envelope.get("id") or line[:80])
        if envelope.get("to") != identity:
            declined.append((what, "not addressed to this seat (a topic message)"))
            continue
        sender = envelope.get("from")
        body = envelope.get("body")
        if not isinstance(sender, str) or not sender.strip():
            declined.append((what, "no sender"))
            continue
        if sender == identity:
            # A reply to this message would come back as the next message, without end.
            declined.append((what, "sent under this seat's own name"))
            continue
        if not isinstance(body, str) or not body.strip():
            declined.append((what, "empty body, from %s" % sender))
            continue
        messages.append(envelope)
    return messages, declined


def parse_send_uid(text: str) -> Optional[str]:
    """Return the uid that `loc send` prints, or None."""
    match = _UID.search(text or "")
    return match.group(1) if match else None


def clip_body(content: str) -> Tuple[str, bool]:
    """Return the content cut to loc's limit, and whether it was cut.

    A cut body ends with CUT_MARK. A message that reads as whole and is not whole is the dangerous
    kind, so the cut is always visible to the reader.
    """
    if len(content) <= MAX_BODY_CHARS:
        return content, False
    return content[:MAX_BODY_CHARS - len(CUT_MARK)] + CUT_MARK, True


def parse_request_head(head: bytes) -> Tuple[str, str, int]:
    """Return (method, path, content length) from the head of an HTTP request.

    A head that cannot be parsed gives ("", "", 0).
    """
    try:
        lines = head.decode("latin-1").split("\r\n")
        method, path, _version = lines[0].split(" ", 2)
    except (ValueError, IndexError):
        return "", "", 0
    length = 0
    for line in lines[1:]:
        name, _, value = line.partition(":")
        if name.strip().lower() == "content-length":
            try:
                length = int(value.strip())
            except ValueError:
                length = 0
    return method, path, max(length, 0)


def http_response(status: int, reason: str) -> bytes:
    """Return a complete HTTP response with an empty body."""
    return ("HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n" % (status, reason)).encode("ascii")


def bell_path(token: str) -> str:
    """The path of the bell. The token is random for each connection of the adapter, so a program
    that does not know it, a web page for example, cannot ring the bell."""
    return "/bell/" + token


def is_bell(method: str, path: str, expected_path: str) -> bool:
    """True when the request is the bell: a POST to exactly the expected path."""
    return method == "POST" and bool(expected_path) and path == expected_path


_SINCE = re.compile(r"registered:\s+yes\s+\(since\s+([0-9T:.\-]+)Z")


def registered_since(status_text: str) -> Optional[datetime]:
    """Return the time at which the seat was registered, from the output of `loc status <endpoint>`.

    None when the seat is not registered, or when the line has no time that can be read.
    """
    match = _SINCE.search(status_text or "")
    if not match:
        return None
    stamp = match.group(1)
    try:
        if "." in stamp:
            head, fraction = stamp.split(".", 1)
            stamp = head + "." + (fraction + "000000")[:6]
            return datetime.strptime(stamp, "%Y-%m-%dT%H:%M:%S.%f").replace(tzinfo=timezone.utc)
        return datetime.strptime(stamp, "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc)
    except ValueError:
        return None


def mcp_handshake_lines() -> List[bytes]:
    """Return the two JSON-RPC lines that open an MCP session with `loc mcp`.

    `loc mcp` registers the seat after this handshake. The adapter calls no tool
    through the session. It keeps the session open, because the seat lives as
    long as the session does.
    """
    initialize = {
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {
            "protocolVersion": "2025-06-18", "capabilities": {},
            "clientInfo": {"name": "hermes-loc-platform", "version": "0.2.0"},
        },
    }
    initialized = {"jsonrpc": "2.0", "method": "notifications/initialized"}
    return [(json.dumps(initialize) + "\n").encode(), (json.dumps(initialized) + "\n").encode()]
