# 0009: Code nodes, the runner role, and process isolation

Status: accepted (2026-11-01)

## Context
Spec 11.1-11.6 defines code nodes: a gRPC protocol between engine and
runner, a Python runtime (`fwnode`), isolation levels, warm worker pools,
uv-built dependencies, and idempotent re-execution. It does not say how the
runner talks to Python processes. The user chose (2026-11-01):
- gRPC engine↔runner; stdio length-prefixed JSON frames runner↔worker, so
  fwnode uses only the Python standard library (no grpcio in user
  environments);
- uv-built virtual environments keyed by the lock file hash when uv is on
  PATH; without uv, nodes without requirements use the system python3 and
  nodes with requirements fail with a clear error.

## Decision
- Protocol: `api/proto/codenode/v1/codenode.proto` (`make proto`), service
  `Runner { Load; Run (bidi: Execute → Callback*/CallbackResult* → Result,
  Cancel); Stats }`. Generated Go code is committed.
- Ports in `internal/runtime/api` (`CodeRunner`, `CodeRequest`,
  `CodeBundle`, `CodeError`); the runner service and worker pools in
  `internal/runtime/application` (per node, bundle hash, entry and limits;
  at most `runner.maxWorkers` per pool; recycled after 500 executions,
  30 minutes, or an unhandled exception); gRPC server in
  `internal/runtime/interfaces/grpc`; engine client in
  `internal/runtime/infrastructure/grpcrunner` (reloads bundles after a
  runner restart); process isolation in
  `internal/runtime/infrastructure/process`.
- Process isolation: own process group, empty environment (no secrets),
  private temporary directory as HOME/TMPDIR, read-only bundle directory,
  `ulimit -v` (memoryMb, default 512) and `-n 256`, wall-clock deadline
  (min of limits.timeoutMs and the flow budget) enforced by killing the
  process group, outputs capped at 1 MiB. Outbound HTTP goes through a
  per-worker egress proxy (`platform/egress.Proxy`) that allows only the
  node's `limits.network` (host:port, host:*, *.domain:port) and refuses
  private ranges; code that ignores the proxy variables is not contained,
  which is why process isolation is limited to development and single-
  replica deployments (config validation refuses it in the replicated
  topology). `container` and `microvm` are refused as not implemented.
- `fwnode` lives in `runner/python/fwnode`, embedded into the binary and
  written to the runner cache; the worker starts with `python -I -c
  <bootstrap>` because `-I` ignores PYTHONPATH.
- The bundle is every file under the `nodes/` directory holding the code
  (or `modules/<m>/nodes/`), so nodes can import sibling modules; the hash
  covers those files and the lock file.
- Engine: built-in `python` node (kind Code); its inputs are the
  properties of `inputSchema` (type-checked at validate time; validated at
  run time before leaving the engine); `outputSchema` is required and
  validated (retryable mismatch). Host calls run as the run's principal
  through the write set: `feature.get`, `feature.query`, `llm.complete`
  (via the router; keys never reach code), `log`. Worker crashes and
  unhandled exceptions are retryable when the node declares `retry`.
- The runner role is in the default roles; with no `runner.listen` it
  serves on a private Unix socket and the engine of the same process
  connects to it; `runner.addr` points the engine at a remote runner.

## Consequences
- No container or microVM isolation yet (needs Docker); no pool minimum
  (workers start on demand; cold start is reported by Stats and logged).
- `fw apply` does not pre-build environments; the runner builds them on
  first Load.
- `python.allowedPackages` in app.json is not enforced yet.
