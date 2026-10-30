---
name: golang-observability
description: "Implement and review Go logs, metrics, traces, profiling, exporters, alerts, and telemetry for correct signals, bounded cost, lifecycle, and privacy."
---

# Go observability

Instrument questions, not vanity dashboards. Define the service-level objective and failure modes first, then choose signals that explain them. Keep instrumentation bounded, cheap on the hot path, cancellable, and safe for the data's trust and retention class.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Trace each request outcome to emitted logs, metrics, spans, and alerts. Check exactly-once counting, units, histogram buckets, error labels, and missing cancellation/partial-stream outcomes.
- Calculate label cardinality and inspect exported fields for secrets, PII, unsafe encodings, and attacker-controlled trace/context data before export.
- Follow exporter startup, duplicate registration, handler errors, queue saturation, sampling, flush, and shutdown; logging/instrumentation can block or amplify an outage.
- Verify propagation and parentage, async span lifetime, dashboard queries against real series, and alert windows against the SLO. A synthetic signal does not prove incident detection.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Signal design

- **Logs** explain individual events and decisions. Use `log/slog` or an equivalent structured/custom handler; JSON is useful for machine ingestion but is not mandatory. Include stable event names and request/trace IDs, not raw request bodies or credentials.
- **Metrics** describe aggregate health. Use counters for events, gauges for current state, and histograms for latency/size. Keep label cardinality bounded and never use arbitrary user IDs, URLs, or error text as labels.
- **Traces** show a request's path across services and dependencies. Propagate trace context at protocol boundaries, create spans around meaningful work, record status and bounded attributes, and sample according to latency/error importance.
- **Profiles** explain CPU, memory, blocking, and goroutine behavior. Capture pprof or trace data briefly and protect endpoints; continuous profiling requires an explicit collector and overhead/privacy review.

Choose one owner for each signal and correlate with a request ID or trace ID. Do not duplicate the same event in every layer or log an error at each wrapper.

## Logging

```go
logger.InfoContext(ctx, "order.accepted",
	"order_id", orderID,
	"trace_id", traceID(ctx),
)
logger.ErrorContext(ctx, "order.persist",
	"err", err,
)
```

Define redaction at the boundary and test it. Treat email, IP address, account ID, device ID, free text, and linkable pseudonyms as potentially personal data; a hash or internal identifier is not automatically anonymous. Minimize fields, use access controls and retention limits, and document a lawful purpose where applicable. Return safe public errors separately from detailed internal logs; logging an error does not authorize exposing it to a caller.

## Metrics and traces

Use low-cardinality names and labels. A histogram's buckets and exemplars should reflect the SLO. Record dependency errors and timeouts with a bounded classification (`timeout`, `unavailable`, `invalid`) rather than an unbounded message. Export OpenTelemetry or Prometheus data through a configured collector/endpoint and handle exporter shutdown with a bounded context.

Keep trace attributes short and scrubbed. Never put access tokens, full URLs containing secrets, request bodies, or unbounded user input into spans. Propagation does not authenticate a caller; verify identity and authorization independently.

## Profiling and dynamic configuration

Environment variables are read at process startup unless the application explicitly implements reload. Setting an environment variable on a running process does not toggle profiling or logging by itself. A dynamic control plane must be authenticated, authorized, bounded, observable, and shut down cleanly. For pprof/trace, bind a separate debug listener or protected route, restrict network access, and cap capture duration/size.

See [profiling](references/profiling.md), [metrics](references/metrics.md), [tracing](references/tracing.md), and [logging](references/logging.md).

## Alerting and dashboards

Alert on symptoms users experience and on sustained error-budget burn, not every transient spike. Every alert needs an owner, severity, runbook link, query window, and a way to silence expected maintenance. Dashboards should show traffic, errors, latency, saturation, dependency health, resource limits, and deployment markers with the same labels used by alerts. See [alerting](references/alerting.md).

## Client telemetry and privacy

If collecting browser or client events, document the purpose, data categories, retention, access, user choices, processor locations, and deletion path. Internal IDs, stable device IDs, IP addresses, and pseudonymous correlation keys can remain personal data because they can link events. Self-hosting changes data-transfer and processor exposure but does not remove privacy obligations. Prefer consent-aware, sampled, minimized events and redact before export.

## Verification checklist

- failure modes and SLOs have a signal and a runbook;
- labels, log fields, and span attributes have bounded cardinality;
- secrets and personal data are excluded or intentionally minimized;
- startup versus live-reload behavior is explicit;
- exporters, queues, and shutdown paths have bounded lifetimes;
- debug endpoints are authenticated or network-isolated;
- dashboards and alerts are tested with representative failures.

Related local skills: `golang-benchmark`, `golang-performance`, `golang-security`, `golang-context`, and `golang-error-handling`.
