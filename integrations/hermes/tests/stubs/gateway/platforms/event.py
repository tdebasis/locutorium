from dataclasses import dataclass, field
from datetime import datetime
from typing import Any, Optional


class MessageType:
    TEXT = "text"


@dataclass
class MessageEvent:
    text: str
    message_type: str = MessageType.TEXT
    source: Any = None
    message_id: Optional[str] = None
    raw_message: Any = None
    timestamp: datetime = field(default_factory=datetime.now)
    # The default is True, as in Hermes (gateway/platforms/event.py).
    allow_gateway_control: bool = True
    # None means unknown, and Hermes then rejects a silent reply. The default is None, as in Hermes.
    reply_expected: Optional[bool] = None

    def is_command(self) -> bool:
        return self.allow_gateway_control and (self.text or "").lstrip().startswith("/")
