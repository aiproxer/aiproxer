# Completion Validation

## Merged Baseline
- Original connector: #1006, merge `275d990d145537479f0aa6f9719aac0d03c678e2`.
- Runtime-discovered model amendment: #1039, published head `2d9f04f4220a4ea7d71dfe559488261455c66394`, merge `06acc57c5398cdd2a676677e6ccb674e762ab520`.
- Safe workspace diagnostic follow-up: #1078, merge `5cfc8ef9db0bbb35edbe8b9fdf0b3bcbcb6e8700`, the closeout main baseline. GitHub confirms all three PRs merged.
- Shared prerequisites #1030, #1041, #1047, #1048, #1052 and #1053 merged before consumer delivery. No successor spec dependency metadata exists for this feature.

## Accepted Evidence
All 12 leaves and three groups are checked. Feature integration validation mapped all 25 acceptance criteria with preserved ownership and no implementation gaps.

Task 3.2 independently accepted one built stock proxy and packaged connector source, `82402a7ca8ec6a0574e8580fb0b5f7e7963a6d02`: host login, explicit administrator-policy honor, deny-only profile, instance `claude-acp`, no alternate backend and one attempt. SDK observations bound exact Haiku/Sonnet/Opus 5.5 models and high effort; omitted effort reset; invalid capabilities produced HTTP 400 with zero SDK input. Distinct-default workspace A/B/A observations proved cwd selection, compatible reuse/isolation/return, rejected reload preservation, completed-drain selective retirement and shutdown without owned survivors. The accepted run recorded 14 HTTP turns, 28 inventory polls and 11 SDK inputs; billing attribution was not observed.

Dedicated native/race run [38497650858](https://github.com/matdev83/go-llm-interactive-proxy/actions/runs/38497650858) and all six full race/fuzz lanes in [38497672004](https://github.com/matdev83/go-llm-interactive-proxy/actions/runs/38497672004) passed for tested source `82402a7c` (artifact-tested revision, not the dispatch workflow revision).

The later canonical-workspace P1 fix rejects malformed direct canonical/plugin hints before discovery or reuse. Warm/cold regression fixtures prove zero SDK input/startup, preserved history and A/B/A behavior, bounded raw/decoded values and no echo. Dedicated native/race run [38593617902](https://github.com/matdev83/go-llm-interactive-proxy/actions/runs/38593617902) passed at `c5fe85a2b7ae7d1440a22c21574075d19d53c414`; connector files are identical to final #1039 head. P3 preserves the already-safe workspace reason without broadening provider error disclosure; RED/GREEN warm/cold diagnostics, full connector tests, related SDK/adapter/frontend contracts, vet, lint and PR checks passed. Earlier paid live proof is not attributed to either later fix.

## Retained Limits and Deferred Scope
Certification remains Node 22.23.3, adapter 0.89.1, SDK 0.3.293 and bundled CLI 2.1.293, Linux/amd64 deny-only. Portable checks do not expand certification. Allow policy remains disabled; background/subagents, usage mapping and remote-policy refresh guarantees remain outside this completed slice. Administrator authority is honored, not bypassed. Completed-drain retirement is proven; active post-publication drain overlap is not claimed. Requirements' Deferred list remains unchanged.

This closeout changes only specification metadata, evidence and archive location. It does not certify a new runtime, rerun paid live calls or expand implementation scope.
