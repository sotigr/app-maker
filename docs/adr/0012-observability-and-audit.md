# 0012: OpenTelemetry and the audit log

Status: accepted (2026-11-01)

## Context
Spec 14.3: OpenTelemetry tracing that follows a write through events,
jobs, flows, and code nodes; RED metrics and domain metrics; structured
logs without field values; an audit log written in the commit transaction
plus authentication events, kept 90 days, queryable by admins.

## Decision
- `kernel.Telemetry` (`kernel.Tel`) is a small facade (StartSpan, Inject,
  Extract, Count, Record) so application layers instrument without
  importing OpenTelemetry (layer rules, ADR 0004). The default is a no-op;
  `platform/observability.Setup` installs the OTel implementation.
- Exporters: OTLP/HTTP for traces and metrics when
  `observability.otlpEndpoint` (FW_OTLP_ENDPOINT) or
  OTEL_EXPORTER_OTLP_ENDPOINT is set; parent-based ratio sampling
  (`sampleRatio`, default 1). W3C trace context propagation.
- HTTP: one server span per request named by the matched route pattern
  (`observability.Route` wraps the ServeMux), continuing an incoming
  `traceparent`; `http.server.request.duration` by method, route, status.
- Jobs: `kernel.JobRequest.Trace` carries the enqueuing context (filled by
  both job adapters on insert); workers continue it in a `job <kind>` span
  and record `job.duration` by kind, queue, outcome.
- Spans/metrics: `flow <name>` + `flow.runs` by flow, trigger, status;
  `llm <alias>` + `llm.tokens` (input/output) and `llm.cost_usd`;
  `code <node>` + `codenode.cold_start_ms`; `stream.delivery_latency_s`
  (commit-to-delivery).
- Logs: `observability.LogHandler` adds trace_id/span_id to slog records.
- Audit: `kernel.AuditRecord` (actor, action, target, type, version,
  changed paths, causing run; never values). Content builds one row per
  domain event (except flowRun and blob bookkeeping) into `Batch.Audit`;
  repositories write them in the commit (`content_audit` table in SQLite,
  slice in memory) and implement `domain.AuditStore`. Authentication events
  (login, login_failed, mfa_verified/failed, totp_enabled/disabled,
  identity_linked, api_key_created) go through `AuthConfig.Audit` →
  `Content.AppendAudit` on the default repository. `GET /v1/audit`
  (admins; actor, target, action, since, before, limit) pages newest
  first. `expire_audit` runs daily with `audit.retentionDays` (90).

## Consequences
- Change-log events do not carry the trace yet, so gateway delivery is not
  linked to the request trace (only its latency is measured).
- Search index lag and migration progress metrics are not emitted yet.
- No dashboards are shipped (Helm/OCI were excluded).
