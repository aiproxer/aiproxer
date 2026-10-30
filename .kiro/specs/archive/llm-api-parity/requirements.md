# LLM API parity — requirements

## Purpose

Define **spec-documented parity** for the non-realtime protocol surfaces the Go proxy already bundles, with **matrices and automated tests** as the only authority for “implemented” claims. Normative behavior is taken from official vendor documentation and SDK-observable wire shapes indexed in [go-core-reimplementation-v1/research.md](../go-core-reimplementation-v1/research.md).

## In scope

- **OpenAI Responses API** — create (stream + JSON), request/response item shapes supported by the bundled frontend subset, SSE ordering and terminals, tools, usage, multimodal **request** paths; **assistant** multimodal output only where the parity matrix marks `implemented`.
- **OpenAI Chat Completions (legacy-compatible)** — same boundaries for the bundled surface.
- **Anthropic Messages** — messages + SSE, tools, usage/stop semantics, multimodal request blocks; assistant multimodal output per matrix.
- **Gemini `generateContent` / `streamGenerateContent`** — contents, system instruction, generation config, tools, streaming framing, usage metadata per matrix.
- **AWS Bedrock `Converse` / `ConverseStream`** — connector + emulator evidence for the Converse surface used by the proxy.
- **ACP** — **prompt-turn subset only** (initialize, authenticate, session create/reuse, prompt, progress, cancel, resource/reference payloads in the declared subset). No terminal, filesystem, slash-command, or full-agent parity.

## Out of scope

- Realtime / voice / live session APIs.
- Pairwise protocol translators (canonical middle only).
- Python LIP behavior as normative authority (fixtures may regress behavior only).

## Shared requirements

1. **R-TRACE** Every parity matrix row SHALL name: normative doc reference, owning repository path(s), and test artifact(s) proving `implemented`, `proxy_proven`, `wire_only`, `planned`, or `out_of_scope`.
2. **R-CANON** No bundled adapter SHALL silently drop semantics for a row marked `implemented` in the canonical model; gaps SHALL be `planned`, `out_of_scope`, or `vendor_extension_only` with a documented extension key.
3. **R-TDD** Work on a row marked `implemented` SHALL begin with failing tests at the appropriate layer (`pkg/lipapi`, `internal/refclient`, `internal/refbackend`, plugin package tests, `internal/testkit/conformance`), then implementation.
4. **R-ACP-BOUND** ACP documentation and matrices SHALL list excluded ACP families explicitly; product docs SHALL not imply full ACP parity.

## Evidence layers

- Canonical: `pkg/lipapi`, core collectors and capability negotiation.
- Wire reference: `internal/refclient/*`, `internal/refbackend/*`.
- Adapters: `internal/plugins/frontends/*`, `internal/plugins/backends/*`.
- Cross-API: `internal/testkit/conformance` FE×BE matrix.
- Protocol-specific: additional conformance tests or subpackages for semantics not visible in the Cartesian matrix alone.

## Acceptance (roadmap)

The roadmap in `.dev/llm_api_parity_plan.md` is satisfied when: all protocol families have matrices; every `implemented` row has automated evidence; release gates block parity-ready labels unless matrices and CI agree. Detailed row IDs live in `design.md`.
