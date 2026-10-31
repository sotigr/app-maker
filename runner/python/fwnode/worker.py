"""Worker main loop. The runner starts ``python -I -c <bootstrap>``, which
puts fwnode on sys.path and calls ``main(bundle_dir, entry)``."""

import importlib.util
import os
import sys
import traceback

from .context import Context, HostError
from .protocol import Channel


def _channel():
    # Keep the protocol on private copies of fd 0 and 1; user prints go to
    # stderr and reads from stdin see end of file.
    rfile = os.fdopen(os.dup(0), "rb", buffering=0)
    wfile = os.fdopen(os.dup(1), "wb")
    devnull = os.open(os.devnull, os.O_RDONLY)
    os.dup2(devnull, 0)
    os.dup2(2, 1)
    sys.stdout = sys.stderr
    return Channel(rfile, wfile)


def _load(bundle, entry):
    path = os.path.join(bundle, entry)
    name = os.path.splitext(entry)[0].replace("/", ".")
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise ImportError(f"cannot load {entry}")
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    fn = getattr(mod, "run", None)
    if not callable(fn):
        raise ImportError(f"{entry} defines no run(input, ctx) function")
    return fn


def _error(code, message, unhandled=False):
    return {"t": "result", "error": {"code": code, "message": message[:4000], "unhandled": unhandled}}


def main(bundle=None, entry=None):
    if bundle is None:
        bundle, entry = sys.argv[1], sys.argv[2]
    ch = _channel()
    sys.path.insert(0, bundle)
    try:
        run = _load(bundle, entry)
    except BaseException as e:  # noqa: BLE001 - report every import failure
        ch.send({"t": "loadError", "message": "".join(traceback.format_exception_only(e)).strip()})
        return 1
    ch.send({"t": "ready"})
    while True:
        try:
            msg = ch.recv()
        except EOFError:
            return 0
        if msg.get("t") != "execute":
            ch.send(_error("protocol", f"unexpected frame {msg.get('t')!r}"))
            continue
        ctx = Context(ch, msg.get("idempotencyKey", ""), msg.get("deadlineUnixMs", 0))
        try:
            out = run(msg.get("input"), ctx)
        except HostError as e:
            ch.send(_error(e.code, e.message))
            continue
        except Exception:  # noqa: BLE001 - user code
            ch.send(_error("code_error", traceback.format_exc(), unhandled=True))
            continue
        try:
            ch.send({"t": "result", "output": out})
        except (TypeError, ValueError) as e:
            ch.send(_error("invalid_output", f"the output is not JSON: {e}"))


if __name__ == "__main__":
    sys.exit(main())
