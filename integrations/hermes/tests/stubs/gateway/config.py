from dataclasses import dataclass, field
from typing import Any, Dict


class Platform:
    def __init__(self, value: str):
        self.value = value


@dataclass
class PlatformConfig:
    enabled: bool = True
    extra: Dict[str, Any] = field(default_factory=dict)
