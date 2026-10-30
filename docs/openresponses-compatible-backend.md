# Generic OpenResponses compatible backend

The `custom-openresponses-compatible` backend mode is the built-in generic remote
OpenResponses backend. It is pinned to the OpenResponses `2026-04-24` profile
(`profile: "2026-04-24"` is the only supported value). Route selectors use the
configured `backend_prefix`, for example `prefix:my-model`.

It is an essential built-in compatible mode (origin `built_in_compatible`), not
an external connector: it never launches a plugin process and never accepts
literal YAML credentials (use `api_key_env_var_root` or omit it for no-auth
endpoints).

## Enable

```yaml
plugins:
  backends:
    - id: ors
      kind: custom-openresponses-compatible
      enabled: true
      config:
        backend_prefix: ors
        base_url: https://api.provider.example/v1
        api_key_env_var_root: ORS_API_KEY
```

Config surface: `backend_prefix`, `profile`, `base_url`,
`api_key_env_var_root`, `models`, `capabilities`, `dialects`,
`request_limits`, `response_limits`. Provider-connector-owned controls
(OpenRouter attribution, routing, billing, provider-specific request controls)
are rejected at load time.

## Capability surface

The `capabilities` list is the honest, portable feature claim for the pinned
profile. When omitted, the generic mode claims:

`streaming`, `tools`, `vision`, `documents`, `reasoning`,
`parallel_tool_calls`, `ordered_items`, `assistant_phase`, `item_references`,
`compaction`, `opaque_extensions`.

The following capabilities are **not** part of the default surface and require
an explicit declaration to claim:

- `video_input` — inline video references (`input_video`) are representable on
  the pinned profile, but only forward when the operator declares them.
- `reasoning_replay` — replaying historical reasoning parts is a hard capability
  declared when a call carries reasoning output.

Declaring the full default surface plus any of those is valid.

## Exact dialect and extension declaration

Admission is exact, not just capability-wide. A call is rejected before any
network round trip unless the instance declares every exact dialect and
extension type the call requires:

- The default `dialects.item` list declares the portable profile item dialect
  (`openresponses.2026-04-24`) **and** the exact `item_reference` item dialect,
  which is what makes the default `item_references` capability truthful:
  `item_reference` input items forward verbatim on the pinned wire.
- Opaque extension content parts and extension items require the exact
  namespace/type/implementor in `dialects.extensions`. The canonical namespace
  of a vendor-prefixed wire type (`acme:widget` → `acme`) is derived
  deterministically from the leading segment before the first `:` or `/`, so an
  operator declares `{namespace: acme, type: acme:widget}` to admit it.
  Structured tool-result extension parts are admitted under the same exact
  declaration.

```yaml
dialects:
  extensions:
    - namespace: acme
      type: acme:widget
```

Removing the default `item_reference` item dialect (or the exact extension
declarations) makes the corresponding calls fail closed before any HTTP request
instead of being silently degraded.

## Removed / rejected capabilities

Two capability names are **not representable on the pinned `2026-04-24`
profile** and cannot be claimed at all:

- `annotations` — the pinned content-part schema has no annotation carrier
  (`InputTextContentParam`/`InputFileContentParam` define no `annotations`
  field), so the generic mode cannot satisfy the capability end-to-end.
- `assistant_media_refs` — the pinned profile has no assistant-media response
  surface, so the generic mode cannot deliver assistant media references.

Consequences for operators:

1. `annotations` has been **removed from the default capability list**. A
   generic OpenResponses backend never advertises it.
2. Declaring either name in `capabilities:` is **rejected at config load** with
   an error, so operators cannot make dishonest capability claims that would
   later fail or silently degrade.

```yaml
# Rejected at load time:
capabilities: [streaming, tools, annotations]
# Rejected at load time:
capabilities: [streaming, tools, assistant_media_refs]
```

Calls that genuinely need these semantics must route to a backend that can
represent them (for example a native OpenAI Responses backend or an external
connector with a response media surface). Content parts the generic mode cannot
encode (annotations, assistant media references, summaries, reasoning parts,
and other unrepresentable canonical kinds) are rejected **before any upstream
request** — never silently text-mapped or dropped.

## Create reasoning control compatibility

For the pinned `2026-04-24` create profile, this backend supports only the
lossless canonical reasoning control `reasoning: {"effort": "..."}`. The
authoritative pinned schema enumerates `none`, `low`, `medium`, `high`, and
`xhigh`; the selected effort is forwarded unchanged. Omitted, `null`, `{}`, and `{"effort":null}`
map to the canonical empty effort because that contract has no presence bit.
Unknown nested reasoning fields, non-object reasoning values, unknown enum
values, and non-string or empty `effort` values are rejected before any upstream request. Reasoning output
fields such as `summary`, `content`, and `encrypted_content` are response-item
fields, not supported create controls; other create reasoning fields remain
unsupported.

The request control is consumed while constructing the canonical call and does
not alter response-item construction, so no separate response regression is
needed for this subset.

## Pinned content-part profile

The generic mode encodes canonical message content to the pinned `2026-04-24`
content parts:

- text → `input_text` / `output_text`
- images → `input_image` (`image_url`)
- files → `input_file` with `filename`, `file_data` (base64, ≤ 32 MiB), and
  `file_url`
- videos → `input_video` (`video_url`)
- vendor-prefixed custom parts (containing `:` or `/`) → preserved opaquely

Fields the pinned profile does **not** define are rejected rather than dropped:

- `input_file.file_id` — not in `InputFileContentParam`; a non-null value is
  rejected before canonical construction.
- `input_video.video_data` — not in `InputVideoContent`; a non-null value is
  rejected before canonical construction.

Explicit `null` for these fields is treated as absent.
## Create `text.format` support

The pinned generated create schema supports `text.format` with the lossless
subset `{"type":"text"}` and `{"type":"json_object"}`. These map to the
canonical response MIME types `text/plain` and `application/json`, and are
forwarded by the compatible backend with the same shape. `json_schema` is not
supported: the canonical model has no carriers for its `name`, `schema`,
`strict`, or `description`, so narrowing it would lose information. Unknown
format fields, invalid objects, and `text.verbosity` are rejected at frontend
admission before executor/network activity. Omitted and null `text` remain
accepted as absent according to the pinned schema.
