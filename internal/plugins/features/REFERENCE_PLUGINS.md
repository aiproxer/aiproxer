# Reference feature plugins (stage two)

Optional YAML-enabled examples (non-noop) for each hook family:

| YAML `id` | Family | Purpose |
|-----------|--------|---------|
| `tool-call-repair` | tool-call finalizer | Deterministic native tool-call argument repair (ADR 0007). Standard aipstd injects an enabled row when omitted; `enabled: false` opts out. |
| `ref-submit-annotate` | submit | Adds `x_aip_ref_submit` JSON extension on the canonical call. |
| `ref-request-suffix` | request/response parts | Appends a suffix to the first user text part; prefixes assistant text deltas. |
| `ref-tool-prefix` | tool reactor | Rewrites tool argument deltas with a configurable prefix. |

Example:

```yaml
plugins:
  features:
    - id: ref-submit-annotate
      enabled: true
      config:
        marker: "my-env"
    - id: ref-request-suffix
      enabled: true
      config:
        suffix: " [staging]"
        response_prefix: "STG:"
    - id: ref-tool-prefix
      enabled: true
      config:
        prefix: ">>"
```

`tool-call-repair` is standard-distribution only by default. Custom/minimal bundles must register it explicitly. It operates only on native structured tool-call events; it does not infer calls from text/XML/Markdown and never invokes another model or fetches schemas.

```yaml
plugins:
  features:
    - id: tool-call-repair
      enabled: true
      config:
        mode: conservative
        max_args_bytes: 65536
        on_unrepairable: pass_through # or error
        schema:
          max_schema_bytes: 262144
          max_nesting_depth: 32
          max_nodes: 4096
          max_properties: 1024
          max_local_ref_depth: 32
          max_cache_entries: 64
          max_cache_bytes: 4194304
```

To opt out in `aipstd`, add an explicit disabled row:

```yaml
plugins:
  features:
    - id: tool-call-repair
      enabled: false
```

Missing `$schema` defaults to Draft 2020-12; explicit drafts 4, 6, 7, 2019-09, and 2020-12 are supported. Local fragment references are allowed. Network, filesystem, and unresolved external references are rejected before compilation. Valid calls replay exact buffered originals; rewrites are post-validated; unrepairable calls either replay originals or return a safe typed error according to `on_unrepairable`. Errors never contain raw arguments, tool names, schema bodies, values, or external URLs, and JSON Pointer paths are bounded to 256 runes.

The feature buffers each active native tool-call lifecycle until `tool_call_finished`, bounded by `max_args_bytes`. It adds no model round trip and performs no external I/O. Inventory reports it in the existing `tool_event_reaction` stage.

Tool reactor error policy (global hook bus) in root config:

```yaml
hooks:
  tool_reactor_error_policy: fail_open   # or fail_closed, swallow_event
```

## Stage-four reference proof set (extension seams)

Narrow plugins that validate the published stage-four surface (see design §19). Each is registered in the standard bundle.

| YAML `id` | Seams | Purpose |
|-----------|-------|---------|
| `ref-autoappend-file` | session opener, request transform | First-new-session label + append configurable text to the first user text part. |
| `ref-tool-policy` | tool catalog filter, tool reactor | Drop blocked tool defs; swallow tool events for blocked names or prefixes. |
| `ref-workspace-guard` | workspace resolver, request transform, catalog filter, tool reactor | Static workspace view; session state unlocks a gated tool on later requests; heat-tool guard via workspace label. |
| `ref-traffic-transcript` | traffic observer, redactor, raw capture | In-memory transcript and raw log; redacts substrings on the observation path only. |
| `ref-verifier-stub` | completion gate | After successful aux `Collect` with role `verifier`, replaces the completion with a short stream; pass-through if aux is disabled. |
