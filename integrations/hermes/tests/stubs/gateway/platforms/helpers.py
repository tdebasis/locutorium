class MessageDeduplicator:
    def __init__(self, max_size=1000, ttl_seconds=300):
        self._seen = set()

    def is_duplicate(self, key):
        if key in self._seen:
            return True
        self._seen.add(key)
        return False

    def clear(self):
        self._seen.clear()
