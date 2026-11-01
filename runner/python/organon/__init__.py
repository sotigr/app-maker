"""organon: the Python runtime of framework code nodes (spec 11.2).

A worker process imports the node's module once and runs ``run(input, ctx)``
for each execution. It talks to the runner over stdin/stdout with
length-prefixed JSON frames; user output to stdout goes to stderr instead.
The package uses only the standard library.
"""

from .context import Context, HostError

__all__ = ["Context", "HostError"]
