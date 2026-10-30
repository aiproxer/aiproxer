# Downstream cache affinity

Opt-in, fill-only locality hinting for requests with an admitted authoritative
session. Add this row to your existing configuration:

```yaml
plugins:
  features:
    - id: downstream-cache-affinity
      enabled: true
      config: {}
```

Enablement belongs on the outer row; the feature config must be empty. Omit the
row or set `enabled: false` to keep it off (the default).

- Explicit client `prompt_cache_key` values win. Without an authoritative session,
  nothing is generated; client session hints are not used.
- The generated value is exactly 50 ASCII characters: `aipca1_` plus unpadded
  base64url SHA-256 of the domain-separated backend-instance/session inputs.
  It is opaque and stable for the same backend and session across retries,
  restarts and proxy nodes; different backends use separate hints.
- Only the derived hint is forwarded, not the raw session ID. The existing final
  session scrub is unchanged. This feature logs neither the hint nor the session.
- Direct OpenAI Chat and Responses forward the effective key. In V1, the
  OpenAI-compatible profile gate enables forwarding only for `mistral` and
  `deepinfra`; plain custom-compatible and other unquirked profiles omit it.
  Existing OpenResponses forwarding is unchanged.
- The hint is advisory, not a guaranteed cache hit or session, continuation,
  routing or cache-residency authority. Actual rejection follows normal
  pre-output error handling; retry/failover depends on the configured policy.

**Large-payload cost:** enabling this feature installs a canonical-required
attempt transform, so requests take the canonical path instead of the
large-payload wire fast path, even when no hint can be generated or forwarded.
V1 adds no core surface or wire-path synthesis. When absent/disabled, this feature
does not change fast-path eligibility.
