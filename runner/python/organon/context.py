"""The ``ctx`` object passed to ``run(input, ctx)`` (spec 11.2)."""

import time


class HostError(Exception):
    """A failed host call (feature.get, feature.query, llm.complete)."""

    def __init__(self, code, message):
        super().__init__(f"{code}: {message}")
        self.code = code
        self.message = message


class Context:
    def __init__(self, channel, idempotency_key, deadline_unix_ms):
        self._ch = channel
        self._next = 0
        self.idempotency_key = idempotency_key
        self._deadline = deadline_unix_ms

    def remaining_ms(self):
        """Milliseconds left before the engine cancels the node."""
        return max(0, int(self._deadline - time.time() * 1000))

    def _call(self, method, args):
        self._next += 1
        cid = self._next
        self._ch.send({"t": "callback", "id": cid, "method": method, "args": args})
        msg = self._ch.recv()
        if msg.get("t") != "callbackResult" or msg.get("id") != cid:
            raise RuntimeError(f"unexpected frame {msg.get('t')!r} while waiting for callback {cid}")
        err = msg.get("error")
        if err:
            raise HostError(err.get("code", "error"), err.get("message", ""))
        return msg.get("result")

    def get_feature(self, feature_id):
        """Reads a feature, projected for the run's principal."""
        return self._call("feature.get", {"id": feature_id})

    def query(self, type, where=None, sort=None, scope=None, limit=None):
        """Runs a query document (spec 7.1) and returns the features."""
        args = {"type": type}
        for k, v in (("where", where), ("sort", sort), ("scope", scope), ("limit", limit)):
            if v is not None:
                args[k] = v
        return self._call("feature.query", args)

    def llm(self, prompt=None, model="default", system=None, schema=None, max_tokens=None, messages=None):
        """Calls a model by alias; returns the parsed document when a schema
        is given, otherwise the reply text."""
        args = {"model": model}
        for k, v in (("prompt", prompt), ("system", system), ("schema", schema),
                     ("maxTokens", max_tokens), ("messages", messages)):
            if v is not None:
                args[k] = v
        out = self._call("llm.complete", args)
        return out if schema is not None else out.get("text")

    def log(self, msg, **kv):
        """Writes a structured log line attributed to the run and node."""
        self._call("log", {"msg": str(msg), "kv": {k: _plain(v) for k, v in kv.items()}})


def _plain(v):
    return v if isinstance(v, (str, int, float, bool, type(None))) else repr(v)
