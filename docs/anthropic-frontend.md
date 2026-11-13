# Anthropic Messages frontend

The `anthropic` frontend serves `POST /v1/messages` for clients that speak the
Anthropic Messages API, translating them into canonical calls. This page covers
the operator-visible behaviour of generation controls the canonical call can and
cannot carry: the request-level `output_config`, and the per-message controls on
an inline `role: system` message.

## `empty_system_messages`

```yaml
plugins:
  frontends:
    - id: anthropic
      enabled: true
      config:
        empty_system_messages: serve   # default
```

| Value | Behaviour |
| --- | --- |
| `serve` (default) | Serve the turn after the reductions below. Each reduction **class** is logged once per handler generation, on the first request that produced it; later requests that only repeat an already reported class add no line. |
| `reject` | Refuse any request that carries a contentless inline system carrier, before routing. |

An unknown value fails plugin configuration decoding, and a generation whose
resolved policy is unknown fails at composition instead of serving.

## `per_message_controls`

```yaml
plugins:
  frontends:
    - id: anthropic
      enabled: true
      config:
        per_message_controls: serve   # default
```

| Value | Behaviour |
| --- | --- |
| `serve` (default) | Serve the message after the reductions below. Each reduction **class** is logged once per handler generation, on the first request that produced it. |
| `reject` | Refuse any inline `role: system` message that declares a control this frontend cannot forward (`output_config` or `clear_at`), before routing. A contentless carrier without controls stays governed by `empty_system_messages`. |

## What the frontend can carry

The request-level `output_config.effort` is carried onto the canonical
generation options (`Options.ReasoningEffort`). A native Anthropic backend
forwards it upstream as `output_config.effort`. A compatible backend leaves the
field off because those upstreams are not guaranteed to accept it; capability
negotiation then strips the canonical effort and logs the downgrade, so the
loss is explicit rather than silent.

Every other request-level `output_config` field (`format`, unknown siblings, or
a value that is not an object) is reported, never silently dropped. A malformed
or empty `effort` is reported and not guessed.

## What the frontend cannot carry

Claude Code 2.1.322 in `--bare` mode sends a per-turn control message:

```json
{"role": "system", "content": [], "output_config": {"effort": "medium"}}
```

The vendor SDK documents two per-message fields on a `role: "system"` input
message: `clear_at` (a rendering lifetime) and `output_config` (per-turn output
configuration; `format` stays request-level only). The canonical call has no
per-message carrier for either, and the frontend does not forward per-turn
`output_config.effort`, so serving this message reduces real semantics. The
request-level value is forwarded independently; a carrier's per-turn effort is
reported whenever it declares one, so a notice never implies that a matching
request-level value made something safe.

A `role: "system"` message that **does** carry content and also declares
`output_config` (or `clear_at`) keeps its content and position, but the
per-message control is dropped and reported. Forwarding per-message generation
controls through the canonical call is a public-contract slice of its own and is
deliberately deferred; this page does not claim those controls are preserved.

The frontend therefore classifies every reduction instead of calling it
loss-free:

| Class | Meaning |
| --- | --- |
| `empty_content` | The contentless carrier was removed from the canonical history. |
| `per_turn_effort_not_forwarded` | The carrier declared an `output_config.effort` this frontend does not forward (matching or differing from the request-level value). |
| `message_fields_not_forwarded` | The carrier declared a field this frontend does not model, such as `clear_at`, an unknown `output_config` sibling, an unknown message field, or a non-string `output_config.effort`. |
| `request_effort_not_forwarded` | The request-level `output_config.effort` was present but not a non-empty string, so no canonical value could be derived. |
| `request_output_config_fields_not_forwarded` | The request-level `output_config` carried a field this frontend does not model (`format`, an unknown sibling, or a non-object value). |
| `content_system_per_turn_effort_not_forwarded` | A served content-bearing system message declared a per-turn `output_config.effort` this frontend does not forward. |
| `content_system_fields_not_forwarded` | A served content-bearing system message declared another field this frontend does not model, such as `clear_at`. |

## Operator notice

`serve` emits one warning line per reduction class, so a class that first appears
on a later request is still reported:

```text
anthropic: empty inline system carrier reduced (not loss-free); set empty_system_messages=reject to refuse
  class=empty_content count=1 scope=first_request_with_this_class_in_this_handler_generation
anthropic: empty inline system carrier reduced (not loss-free); set empty_system_messages=reject to refuse
  class=per_turn_effort_not_forwarded count=1 scope=first_request_with_this_class_in_this_handler_generation
anthropic: request output_config reduced (not loss-free); unsupported fields are not forwarded
  class=request_output_config_fields_not_forwarded count=1 scope=first_request_with_this_class_in_this_handler_generation
anthropic: inline system message controls not forwarded (not loss-free); set per_message_controls=reject to refuse
  class=content_system_per_turn_effort_not_forwarded count=1 scope=first_request_with_this_class_in_this_handler_generation
```

`class` comes from a closed label set and `count` is the number of carriers,
fields, or messages in that request that declared the class. The notice never
contains client text, a path, an effort value, or another message-field value;
its `scope` says it describes the first request in this handler generation that
produced that class, so it is not a request or total counter.

## Always refused

These keep their existing refusals regardless of the policy:

- an empty `user` or `assistant` turn, in the middle of history or as the only turn;
- an empty shorthand string system content (`"content": ""`), which is a
  different wire shape from an empty array;
- a history made only of contentless carriers.

## `check-config` coverage

`aipstd check-config` runs the structural validator: effective-config load,
structural registry, default-route and inventory projection. It does **not**
decode every frontend plugin subtree, and the `anthropic` frontend is one of the
subtrees it does not schema-decode. Observed behaviour on this slice:

| Config | `check-config` | `serve` (generation compile) |
| --- | --- | --- |
| `empty_system_messages: serve` / `reject` / absent | `configuration is valid` | serves |
| `empty_system_messages: bogus` | `configuration is valid` | refuses: `compose request plane: anthropic: empty_system_messages must be "serve" or "reject", got "bogus"` |

So `check-config` is structural evidence, not a complete plugin-config audit: a
typo in this key (or in another frontend key such as `namespace_tools`) passes
`check-config` and fails at startup/reload compilation instead. Unit tests in
`internal/plugins/frontends/anthropic` are the authoritative behaviour proof for
this policy.
