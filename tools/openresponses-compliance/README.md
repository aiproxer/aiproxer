# OpenResponses Official Compliance Runner (test-only)

This directory is an **isolated, test-only** JavaScript/TypeScript tool that
executes the **ACTUAL pinned official OpenResponses compliance suite** against a
full Go deployment. It is deliberately kept out of the production/root Go build:

- it contains **no Go files** and is never imported by any Go package,
- `go build`, `go vet`, and the default `go test ./...` require **no JavaScript
  runtime** (Requirement 11.10),
- only the full compliance scripts (`scripts/test-openresponses-compliance.*`)
  invoke it, and only when the environment gate
  `AIP_RUN_OFFICIAL_COMPLIANCE=1` is set,
- the `-static` gate wired into `make qa` never touches Node.

## What is vendored (pinned, immutable)

All suite sources are vendored byte-for-byte from the pinned upstream commit
`openresponses/openresponses@92c12d96d7b61d6d15e2214daa5e9c6000ab6e1c`
(Apache-2.0) and pinned by SHA-256 in [`MANIFEST.json`](MANIFEST.json):

| Path | Upstream origin |
| --- | --- |
| `src/lib/compliance-tests.ts` | `src/lib/compliance-tests.ts` |
| `src/lib/sse-parser.ts` | `src/lib/sse-parser.ts` |
| `src/generated/kubb/zod/*.ts` | `src/generated/kubb/zod/*.ts` (111 generated schema files) |
| `bin/compliance-test.ts` | `bin/compliance-test.ts` (official runner) |
| `LICENSE` | upstream Apache-2.0 LICENSE |

The digest of `src/lib/compliance-tests.ts` equals the pinned manifest digest
`sha256:63b5e6595ac831ee74b8e887af76c28d69aee8e2ec7d9e99dc688eec4bccb7fb`
recorded in `internal/plugins/protocols/openresponses/testdata/official_2026-04-24_manifest.json`.

## Dependencies (pinned exactly)

`package-lock.json` pins exact versions and npm integrity hashes. Installing them
is a **setup** step (`npm ci`), never part of a test run:

- `zod@4.6.7` — the schemas use the core API shared by zod 3 and 4. The
  upstream `bun.lock` resolves `3.25.76`, whose whole release line carries a
  memory-exhaustion advisory (GHSA-mrhx-c7xc-m3w5); 4.6.7 is the patched
  release and the pinned 17-case suite passes unchanged on it.
- `esbuild@0.28.2` — bundles the TypeScript runner (extensionless imports) at
  run time in memory.
- `ws@8.22.0` — WebSocket shim. Node's built-in `WebSocket` cannot send the
  authorization header the OpenResponses frontend requires on WebSocket upgrade;
  the official suite relies on Bun's WebSocket-with-headers, so `scripts/run.mjs`
  shims the global `WebSocket` with `ws`.

## How to run

Setup (one time, pinned by the lockfile):

```sh
npm ci
```

Run against a deployment base URL:

```sh
node scripts/run.mjs --base-url http://127.0.0.1:<port>/openresponses/v1 \
  --api-key sk-test --model gpt-4o-mini --json
```

The Go harness test `TestOfficialComplianceSuite_FullDeployment` in
`internal/integration/openresponses` deploys the full independent path
(official client → OpenResponses frontend → core → generic OpenResponses
backend → independent `internal/refbackend/openresponses` origin) and invokes
this runner. Enable it with `AIP_RUN_OFFICIAL_COMPLIANCE=1`.

## Architecture gate

`internal/archtest/openresponses_js_tool_boundary_test.go` keeps this tool
isolated: it asserts the tool exists and is pinned, that no Go package imports
it, that the vendored `compliance-tests.ts` digest matches the pinned manifest,
that `package-lock.json` pins exact dependency versions, and that the `-static`
compliance gate never requires Node.
