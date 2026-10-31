# 0005. Wire format of integer fields

Date: 2026-10-31 · Status: accepted

## Context

Section 3.2 says the Int64 kind is a JSON string on the wire to avoid precision loss above
2^53. The spec's own examples declare integer fields with `"type": "integer"` (task `progress`,
section 4.3), and the generated TypeScript in section 13.1 shows `progress?: number`. A view's
JSON Schema must validate the wire document, so a field declared `"type": "integer"` cannot be
sent as a string.

## Decision

- `"type": "integer"` infers the Int64 kind with a JSON number wire format. Values outside
  ±2^53 are rejected when they arrive as JSON numbers that are not exact.
- `"type": "string", "x-kind": "int64"` infers Int64 with the string wire format of section 3.2,
  for values that may exceed 2^53 (counters, external IDs).
- Internally both are `kernel.Int64`; `api.ValueType.NumberWire` records the wire choice.

## Consequences

The authored schema remains the wire contract, which keeps published views and codegen
consistent with what authors wrote.
