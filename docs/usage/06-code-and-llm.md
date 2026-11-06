# 6. Code nodes and LLMs

## Python code nodes

When expressions are not enough, a `python` node runs a function from
`nodes/`:

```json
"classify": {
  "node": "python",
  "in": { "taskId": "feature.id" },
  "code": "nodes/triage.py",
  "inputSchema":  { "type": "object", "properties": { "taskId": { "type": "string" } }, "required": ["taskId"] },
  "outputSchema": { "type": "object",
                    "properties": { "label": { "type": "string", "enum": ["bug", "feature", "chore"] },
                                    "source": { "type": "string" } },
                    "required": ["label", "source"] },
  "limits": { "timeoutMs": 15000, "memoryMb": 512, "network": ["api.example.com:443"] },
  "retry": { "max": 1 }
}
```

```python
# nodes/triage.py
from rules import KEYWORDS          # sibling modules in nodes/ can be imported

def run(input, ctx):
    task = ctx.get_feature(input["taskId"])     # projected for the run's principal
    for label, words in KEYWORDS.items():
        if any(w in task["title"].lower() for w in words):
            return {"label": label, "source": "rule"}
    result = ctx.llm(model="fast", prompt=f"Classify: {task['title']}",
                     schema={"type": "object", "properties": {"label": {"type": "string", "enum": ["bug", "feature", "chore"]}},
                             "required": ["label"]})
    ctx.log("classified", label=result["label"])
    return {"label": result["label"], "source": "model"}
```

Rules:

- `inputSchema` and `outputSchema` are required. The node's `in` keys are
  the `inputSchema` properties (type-checked by `organon validate`); the output
  is validated before later nodes see it.
- `ctx` provides `get_feature(id)`, `query(type, where=, sort=, scope=, limit=)`,
  `http(method, url, headers=, body=, auth=)` (through the egress policy;
  `auth={"secret": "stripe", "header": "Authorization", "prefix": "Bearer "}`
  names a secret the node lists in `"secrets": ["stripe"]`, which the
  engine adds so your code never sees it),
  `llm(prompt=, model=, system=, schema=, max_tokens=, messages=)`,
  `log(msg, **fields)`, `idempotency_key` (stable across retries of the
  same run and node), and `remaining_ms()`. Host calls fail with
  `organon.HostError` (attribute `code`, e.g. `not_found`).
- Code cannot write. Return data and let `feature.update`/`feature.create`
  nodes write it, so every write is visible in the graph.
- Code may run more than once (retries, worker crashes). Pass
  `ctx.idempotency_key` to external APIs.
- `print` goes to the server log. No credentials or app secrets are in the
  environment (use `ctx.http` with `auth`); outbound HTTP works only for hosts in `limits.network`
  (`host:port`, `host:*`, `*.domain:port`) through the runner's proxy.
- Limits: `timeoutMs` (capped by the flow budget), `memoryMb` (default
  512), output ≤ 1 MiB.
- Dependencies: `"requirements": "nodes/requirements.lock"` (pinned, with
  `--hash`). The runner builds a virtual environment with `uv` (must be
  installed on the runner). `app.json` `python.allowedPackages` restricts
  what may be listed. Without requirements, the system `python3` is used.
- Isolation: the `process` level is for development and single-replica
  deployments of your own code; it does not contain code that ignores the
  proxy settings. `container`/`microvm` isolation is not available yet.

Python 3 must be installed where the `runner` role runs (by default the
same process).

## llm.complete

```json
"summarize": {
  "node": "llm.complete",
  "in": {
    "model": "'default'",
    "system": "'You summarize project status in at most three sentences.'",
    "prompt": "nodes.prompt.out",
    "maxTokens": "1000",
    "streamTo": "{ 'feature': feature.id, 'path': '/summaryDraft' }"
  },
  "outputSchema": { "type": "object",
                    "properties": { "summary": { "type": "string" }, "risk": { "type": "string", "enum": ["low", "medium", "high"] } },
                    "required": ["summary", "risk"] },
  "retry": { "max": 2 }
}
```

- `model` is an alias from `app.json` `llm.aliases` (default `'default'`).
  Manifests never name vendor model IDs.
- With `outputSchema`, the reply is that JSON object (the schema is sent as
  the structured output format and the reply is validated; a mismatch is
  retryable). Without it, the output is `{ "text": "..." }`.
- `messages` takes prior turns `[{role: 'user'|'assistant', content}]`;
  `prompt` is appended as the last user message.
- `streamTo` streams partial text into an `x-persist: false` field that
  the run's principal may update, so clients see it appear live.
- Each call is recorded on the run (`nodes.<name>.llm`: provider, model,
  tokens, latency, estimated cost) and counted against
  `llm.monthlyBudgetUsd`; when the budget is spent calls fail with
  `rate_limited`.

### Providers

Configured on the server (not in the manifest):

```yaml
llm:
  provider: ""            # force one provider for every alias (organon dev uses fake)
  providers:
    anthropic: { kind: anthropic, apiKey: "${secret:anthropic-key}" }
    local:     { kind: openai-compatible, baseURL: "http://localhost:11434/v1" }
```

- `fake` (always available): deterministic; with a schema it returns the
  simplest valid document. `organon dev` and `organon test` use it by default
  (`organon dev --llm anthropic` to use a real model).
- `anthropic` (always available): the Claude API; without `apiKey` it uses
  the SDK's own credentials (`ANTHROPIC_API_KEY`).
- `openai-compatible`: any chat-completions server, including local ones.

Aliases map to `"<provider>:<model>"`, e.g. `"local:llama3.1"`.
