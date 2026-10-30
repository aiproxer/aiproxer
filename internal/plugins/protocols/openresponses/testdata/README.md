# OpenResponses 2026-04-24 Test Data Provenance

This directory contains vendored authoritative source artifacts for the OpenResponses `2026-04-24` specification profile.

- **Source Repository**: `openresponses/openresponses`
- **Source Commit**: `92c12d96d7b61d6d15e2214daa5e9c6000ab6e1c`
- **License**: Apache License, Version 2.0

## Vendored Artifacts

| Role | Upstream Path | Local Path | SHA-256 Digest |
| --- | --- | --- | --- |
| `license` | `LICENSE` | `LICENSE` | `sha256:43070e2d4e532684de521b885f385d0841030efa2b1a20bafb76133a5e1379c1` |
| `normative_spec` | `src/specifications/2026-04-24.mdx` | `specifications/2026-04-24.mdx` | `sha256:a7a9e3848722b11feaea51df62233ac7b676b6dda42007d9fde02d41dd22aa75` |
| `schema` | `schema/openapi.json` | `schema/openapi.json` | `sha256:997c4cf16c349751502813f46ea79b2c88880b23171b69f7f2c3d4bf5b330529` |
| `compliance_tests` | `src/lib/compliance-tests.ts` | `compliance/compliance-tests.ts` | `sha256:63b5e6595ac831ee74b8e887af76c28d69aee8e2ec7d9e99dc688eec4bccb7fb` |
| `official_example_param` | `src/examples/ResponseParam.json` | `official_examples/ResponseParam.json` | `sha256:34bdc5059a09c5ead8d1dbe4b8981d7f782c8e205f256e672ae91d67756d3331` |
| `official_example_resource` | `src/examples/ResponseResource.json` | `official_examples/ResponseResource.json` | `sha256:f3787e0361ffdfd1ffd78a3535e91bad6e45468a50e0ac697ad9d2b500153791` |

## Note on Upstream Examples

Upstream commit `92c12d96d7b61d6d15e2214daa5e9c6000ab6e1c` provides two official JSON example payloads in `src/examples/` (`ResponseParam.json` and `ResponseResource.json`). Standalone example files for compact requests/responses or SSE events do not exist as separate files in the upstream repository at this commit and are explicitly not fabricated.
