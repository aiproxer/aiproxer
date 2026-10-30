# Token Accounting Fixtures

These fixtures are a Phase 0 baseline for future tokenizer parity and provider-count tests.

Rules:

- Fixtures are generated or verified against external reference tooling such as LiteLLM or Python tokenizer libraries before exact counts become normative.
- Phase 0 intentionally avoids shipping tokenizer implementations in Go.
- Exact counts appear only when they are deterministic and justified by an identified source.
- Cases without verified exact counts must stay marked `pending` with explanatory metadata.
- Future tests may skip `pending` cases or assert that they remain unresolved until verified evidence is added.

Expected future workflow:

1. Generate or verify counts with the reference Python/LiteLLM toolchain.
2. Record the toolchain, library family, version, and model mapping in fixture metadata.
3. Promote a case from `pending` to `verified` only after deterministic evidence is captured.

These fixtures must not be treated as production truth for billing until later implementation phases and external verification are complete.
