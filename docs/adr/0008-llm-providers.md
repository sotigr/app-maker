# 0008: LLM providers, aliases, and the llm.complete node

Status: accepted (2026-10-31)

## Context
Spec 11.7 defines `llm.complete`, model aliases mapped in environment
configuration, provider keys held as secrets, structured output from the
node's `outputSchema`, `streamTo`, and per-call accounting under a monthly
budget. It leaves open where providers are configured and how the port
crosses context boundaries.

## Decision
- The provider port lives in `internal/runtime/api` (`LLMProvider`,
  `LLMClient`) because the Flow context may import only `api` packages
  (ADR 0004); alias and cost rules live in `internal/runtime/domain/llm`,
  the router (budget, call records) in `internal/runtime/application`.
- Adapters (`internal/runtime/infrastructure/llm`):
  - `anthropic`: the official Go SDK (`anthropic-sdk-go`), beta Messages
    API with server-side refusal fallbacks (`fallbacks: "default"`),
    `output_config.format` for the output schema (objects get
    `additionalProperties: false`; unsupported constraints are stripped
    and still validated by the engine). Sampling parameters are never sent:
    the API rejects `temperature` since 2026-10-21.
  - `openai-compatible`: plain HTTP chat completions (hosted vendors and
    local servers), `response_format: json_schema`.
  - `fake`: deterministic; with a schema it returns the simplest valid
    document (strings carry the prompt).
- app.json `llm.aliases` values are `"<provider>:<model>"` (no prefix means
  `anthropic`). Defaults: `default` and `large` → `anthropic:claude-opus-5-5`,
  `fast` → `anthropic:claude-haiku-5-5`.
- Server config `llm.providers.<name>` = `{kind, baseURL, apiKey}`; `apiKey`
  must be a `${secret:<name>}` reference. `fake` and `anthropic` (SDK
  credential resolution) always exist. `llm.provider` / `FW_LLM_PROVIDER`
  forces one provider for every alias; `fw dev` defaults to `--llm fake`,
  manifest tests and e2e always use the fake.
- The engine validates every node output that has an `outputSchema`; for
  External and Code nodes a mismatch is retryable (`retry` applies).
- Calls are recorded on the flowRun's node status (`nodes.<id>.llm`:
  alias, provider, model, tokens, latency, cost) and logged.
- Budget spend is tracked in memory per UTC month; exhausting it returns
  `rate_limited`.

## Consequences
- Spend resets on restart and is per process; a durable, shared counter
  belongs with the PostgreSQL/Valkey work (phase 10).
- Instance rate limits for LLM calls arrive with the general limiter.
- `streamTo` publishes through the gateway's ephemeral path (≤5/s), so the
  target must be an `x-persist: false` field writable by the run's
  principal; failures disable streaming without failing the node.
