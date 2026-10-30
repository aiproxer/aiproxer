# Implementation Plan

- [x] 1.1 Implement the neutral HTTP workspace carrier and integration proof
  - Red-first shared syntax/alias/projection tests; minimal SDK/config and five stock-family materialization calls; unchanged absent-header and authority behavior; proportional tests, scoped suites, vet/lint and contracts.
  - Done when 1.1–3.1 pass and the carrier is ready for the immediately following Claude task 3.2 spawned-distribution proof (3.2). The shared slice does not claim that live proof before it runs.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 2.1, 2.2, 2.3, 3.1, 3.2_
  - _Boundary: shared HTTP ingress (SDK aliases, config, sessionwire, stock frontend materialization); backend workspace/pool ownership unchanged._
  - _Validation: SDK/config/frontend tests, vet/lint, dev-contract-check, kirocheck; downstream task 3.2 live process/workspace proof._

## Completion Status
- [x] Carrier merged in #1048 (`a8aa3d64a7592851c3ddb8502b044816a29b7da5`).
- [x] Downstream Claude task 3.2 independently accepted at `82402a7ca8ec6a0574e8580fb0b5f7e7963a6d02`: stock HTTP carrier selected the process cwd rather than a distinct default, with project A/B/A isolation and compatible return.
- [x] Consumer merged in #1039 (`06acc57c5398cdd2a676677e6ccb674e762ab520`); completion metadata and whole bundle archived. Deferred scope is unchanged.
