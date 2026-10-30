# LLM API parity — tasks

Each task lists matrix row IDs from `design.md`. Complete in order unless noted.

## Phase 1 — Specs (owner: spec)

- [x] **P1.1** Publish `requirements.md` + `design.md` + `tasks.md`; obtain approvals in `spec.json`.
- [x] **P1.2** Scrub repo docs: no “parity” claim without a matrix row in `implemented` or `wire_only` (README points here).
- [x] **P1.3** Cross-link from [refclient-spec-matrix.md](../go-core-reimplementation-v1/refclient-spec-matrix.md) and [refbackend-spec-matrix.md](../go-core-reimplementation-v1/refbackend-spec-matrix.md) to this spec’s row IDs.

_Rows: all_

## Phase 2 — Canonical (`pkg/lipapi` + core)

- [x] **P2.1** Implement `OAR-MM-OUT` / `OAC-MM-OUT` / `ANT-MM-OUT` / `GEM-MM-OUT` prerequisites: canonical assistant media ref events + `Collected` aggregation + `ValidateEventEnvelope` + hook validation. _Rows: OAR-MM-OUT, OAC-MM-OUT, ANT-MM-OUT, GEM-MM-OUT_
- [x] **P2.2** Optional `FinishReason` on terminal finished event + `Collected.FinishReason`. _Rows: OAR-SSE, OAC-STREAM, ANT-USAGE_
- [x] **P2.3** Capability bits if any new row requires negotiation. _Rows: TBD from gap review_ — **no new bits**; see `design.md` Canonical stream extensions (P2.3) + `required_capabilities_assistant_output_test.go`.

## Phase 3 — Evidence

- [x] **P3.1** Extend `internal/refclient/*` tests for each `wire_only` → `implemented` promotion per protocol. _Rows: per-protocol_
- [x] **P3.2** Extend `internal/refbackend/*` symmetrically.
- [x] **P3.3** Add `internal/testkit/conformance/parity_openai_test.go` (Responses + Chat wire semantics).
- [x] **P3.4** Add `parity_anthropic_test.go`, `parity_gemini_test.go`, `parity_bedrock_test.go`, `parity_acp_test.go` as rows demand.

## Phase 4 — Protocol tracks

### 4A OpenAI

- [x] **P4A.1** Wire `assistant_image_ref` / `assistant_file_ref` through openairesponses + openailegacy encoders when backends emit them. _Rows: OAR-MM-OUT, OAC-MM-OUT_
- [x] **P4A.2** Backend mapping from vendor assistant output items → canonical events. _Rows: OAR-MM-OUT_

### 4B Anthropic + Gemini

- [x] **P4B.1** Anthropic encoder paths for assistant multimodal refs. _Row: ANT-MM-OUT_
- [x] **P4B.2** Gemini encoder paths; document if non-stream usage stays omitted. _Row: GEM-MM-OUT_

### 4C Bedrock

- [x] **P4C.1** Close any remaining `planned` BRK-* rows with refbackend + connector tests.

### 4D ACP

- [x] **P4D.1** Keep ACP-* subset rows green; update matrix footnotes if scope changes.

## Phase 5 — Gates

- [x] **P5.1** Add `TestParityMatrixCompleteness` (or similar): every bundled protocol id has at least one parity suite file or documented exception.
- [x] **P5.2** Update `docs/release-gates.md` with parity-ready checklist.
- [x] **P5.3** CI: optional job or Makefile target `make parity-checks` running conformance parity tests only.

## Definition of done (per row)

1. Tests existed and failed before the change (for new `implemented` rows).
2. Matrix status updated in the same change as code.
3. `go test ./...` and `make release-gates` pass.
