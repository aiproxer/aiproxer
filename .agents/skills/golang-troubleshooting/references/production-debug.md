# Production debugging

Prefer existing metrics, structured logs, traces, and health signals. Define read-only access, authentication, retention, and redaction before enabling diagnostics. Keep pprof on a private listener or protected route; never expose it to an untrusted network.

Capture request IDs, revision, instance, duration, status, and bounded error classifications—not secrets or raw request bodies. Correlate a small sample of logs with profiles and traces. Do not enable broad debug logging or attach a debugger without an operational approval and rollback plan.

For network symptoms, compare DNS, connect/TLS, server wait, and response transfer separately. For file or process symptoms, capture the exact path/command after redaction and check permissions, limits, and platform differences. Preserve artifacts with timestamps and remove temporary diagnostic state after the incident.
