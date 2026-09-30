from dataclasses import dataclass
from typing import Any, Optional


@dataclass
class SendResult:
    success: bool
    message_id: Optional[str] = None
    error: Optional[str] = None


@dataclass
class SessionSource:
    chat_id: str
    chat_type: str
    user_id: Optional[str]
    message_id: Optional[str]


class BasePlatformAdapter:
    def __init__(self, config, platform):
        self.config = config
        self.platform = platform
        self.name = "Loc"
        self._running = False
        self._message_handler = self._record
        self.events = []
        self.fatal = None
        self.fail_handler = False

    def _mark_connected(self, **_):
        self._running = True

    def _mark_disconnected(self):
        self._running = False

    def _wire_plugin_handlers(self, native: Any = None):
        pass

    def _set_fatal_error(self, code, message, *, retryable):
        self._running = False
        self.fatal = (code, message, retryable)

    def build_source(self, chat_id, chat_name=None, chat_type="dm", user_id=None, user_name=None, message_id=None, **_):
        return SessionSource(chat_id=chat_id, chat_type=chat_type, user_id=user_id, message_id=message_id)

    async def _record(self, event):
        self.events.append(event)

    async def handle_message(self, event):
        if not self._message_handler:
            return
        if self.fail_handler:
            raise RuntimeError("the handler failed")
        await self._message_handler(event)
