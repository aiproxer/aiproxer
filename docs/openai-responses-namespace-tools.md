# OpenAI Responses namespace tools

Current Codex CLI releases advertise some tool groups as OpenAI Responses
`namespace` declarations, for example:

```json
{
  "type": "namespace",
  "name": "multi_agent_v1",
  "description": "Tools for spawning and managing sub-agents.",
  "tools": [
    {"type": "function", "name": "close_agent", "parameters": {...}}
  ]
}
```

A model backend receives flat function tools, not this grouping. The
`openai-responses` frontend therefore projects each namespace member to the
unique function name `<namespace>__<member>` and keeps the reverse mapping for
the same request. It also prefixes every member description with the namespace
description, so the model still sees what the group means.

The mapping is per request and is never persisted or sent to a backend. If the
model calls a projected name, the response restores the client's original bare
name and adds the `namespace` field on the function-call item, in both streaming
and non-streaming output. Call ids, arguments, history order, and tool order are
unchanged.

## Configuration

```yaml
plugins:
  frontends:
    - id: openai-responses
      enabled: true
      config:
        namespace_tools: project   # default
```

- `project` (default) serves the request with the projection described above.
  The first projected request in a handler generation logs one bounded warning
  with namespace/member/history counts and no tool names.
- `reject` refuses any request that carries a namespace declaration or
  namespaced history. The error names `namespace_tools=reject`.

`namespace_tools` is accepted only by the `openai-responses` frontend.

## Refusals

The frontend refuses rather than guesses when:

- two declarations project to the same function name, including a top-level
  function that already has the projected spelling;
- two namespace members share a bare name and a `tool_choice` names only that
  bare name;
- a replayed `function_call` carries a namespace/member pair whose projected
  name collides with a declared or already mapped name, or a namespace/member
  name is empty;
- a namespace description plus a member description exceeds the canonical
  description bound, or the projected name exceeds the canonical tool-name
  bound;
- a namespace member is not a function tool (custom, hosted, or nested
  namespace members remain unsupported).

A namespaced history item whose declaration is absent from the current request
is still projected deterministically, preserving its call id, arguments, and
order. It is not added to the declared tool list and cannot be selected by
`tool_choice`; if the model later calls the projected name, the response
restores the original namespace and bare name. The warning's `history_members`
count makes that projection visible to the operator.

`additional_tools` input items, custom tool declarations/history, and hosted
tool history remain rejected; this slice does not carry them. The wire
large-payload fast path always declines namespace declarations and namespaced
history so the raw client shape is never forwarded unprojected.
