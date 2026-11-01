"""Frames between runner and worker: a 4-byte big-endian length, then JSON."""

import json
import struct

MAX_FRAME = 16 << 20


class Channel:
    def __init__(self, rfile, wfile):
        self._r = rfile
        self._w = wfile

    def send(self, msg):
        data = json.dumps(msg, separators=(",", ":"), allow_nan=False).encode()
        self._w.write(struct.pack(">I", len(data)) + data)
        self._w.flush()

    def recv(self):
        head = self._r.read(4)
        if len(head) < 4:
            raise EOFError("runner closed the channel")
        (n,) = struct.unpack(">I", head)
        if n > MAX_FRAME:
            raise ValueError("frame too large")
        data = self._r.read(n)
        if len(data) < n:
            raise EOFError("runner closed the channel")
        return json.loads(data)
