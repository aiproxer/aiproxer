# `claudeagentmodel` release acceptance

This directory records the release-acceptance surface for the experimental
`claudeagentmodel` connector. It is not a broad subscription certification.

## Record versus verify

Candidate recording and approved-artifact verification are deliberately separate:

- **Record** (`AIP_CLAUDE_MODEL_CERT_RECORD=1`) observes one installed candidate
  and writes its identity JSON to `AIP_CLAUDE_MODEL_CERT_OUT`. It does not take an
  expected file and it never claims approval.
- **Verify** (`TestCertification_InstalledBundle`) requires the runtime, the CLI
  and `AIP_CLAUDE_MODEL_CERT_EXPECT`, an approved identity JSON. It compares every
  identity field exactly, executes the selected installed Go binary through its
  real gRPC descriptor/Configure/ListModels path, and fails closed on any drift.

An ordinary checkout skips only when all three verify inputs are unset. A
partially configured verify gate — including runtime and CLI without EXPECT —
fails with an actionable error.

## What verification binds

The verified identity covers:

- the packaged Go binary SHA-256 against `plugin.backendplugin.json`;
- manifest plugin ID, version, protocol major/minor, Linux/amd64 platform and
  `agent_runtime` / `local_only` / `per_instance` / `oauth_user` / experimental
  export posture;
- every sidecar companion (`bridge.mjs`, `session.mjs`, `tool-server.mjs`,
  `package.json`, `package-lock.json`);
- the installed `node_modules` closure: lexical depth-first path+NUL+file-hash
  records, excluding `.bin` and npm's root `.package-lock.json` bookkeeping, with
  a non-trivial file count;
- the installed `@anthropic-ai/claude-agent-sdk/package.json` version and
  `sdk.mjs` entrypoint, plus the pin in `package.json`/`package-lock.json`;
- Node and Claude Code versions and SHA-256 values;
- production bundle resolution and runtime probing (`resolveSidecar` /
  `probeRuntime`), plus the private sidecar hello identity;
- the selected installed Go executable's real `Negotiate`, `Describe`,
  `Configure` and `ListModels` results, including a non-empty unique observed
  model set.

The committed approved identity pins the Go binary, manifest, sidecar/lock
files, installed dependency closure, Node/CLI identity and SDK pin. It does not
pin the discovered model catalogue: those IDs are account/runtime data, written
only to the private observed/record JSON. Verification requires a non-empty
unique observed model set and accepts a changed catalogue.

It never sends a user prompt and requires no model completion. Discovery
initializes the installed SDK and reports the operator login's current model IDs
for private evidence; it is not a tool-loop or text acceptance.

The committed local candidate approval is
[`approved-local-candidate.json`](approved-local-candidate.json). It is a local
candidate approval, not a published release.

## Exact installed-bundle verify gate

Run these commands from the repository root. The `go -C` form keeps the working
directory independent of the install directory.

```sh
# 1. Candidate install (outside the repository).
go run ./tools/backendplugin/package_plugins -root . -profile full \
  -select claudeagentmodel -dest /absolute/scratch/claudeagentmodel

# 2. Operator installs the locked sidecar closure.
(cd /absolute/scratch/claudeagentmodel/claudeagentmodel/sidecar && npm ci)

# 3. Verify the exact installed bytes against the committed approved identity.
AIP_CLAUDE_MODEL_CERT_RUNTIME=/absolute/scratch/claudeagentmodel/claudeagentmodel \
AIP_CLAUDE_MODEL_CERT_CLI=/absolute/path/to/claude \
AIP_CLAUDE_MODEL_CERT_EXPECT=/absolute/repository/connectors/claudeagentmodel/certification/approved-local-candidate.json \
AIP_CLAUDE_MODEL_CERT_OUT=/absolute/private/observed-evidence.json \
  env GOWORK=off go -C connectors/claudeagentmodel test ./internal/service -run '^TestCertification_InstalledBundle$' -count=1 -v
```

`-count=1` is required: the installed bytes and child processes are external
inputs that Go test caching cannot observe, so a cached PASS is not evidence.
`AIP_CLAUDE_MODEL_CERT_OUT` is optional in verify mode; when set it must be an
absolute private path and the observed JSON is forced to mode `0600` for both
new and existing destination files. A set verify environment fails closed on any
missing input or identity mismatch.

## Record a new candidate

Record mode requires the runtime, CLI and output path, but no EXPECT. It writes
the observed identity and never mutates the approved file:

```sh
AIP_CLAUDE_MODEL_CERT_RECORD=1 \
AIP_CLAUDE_MODEL_CERT_RUNTIME=/absolute/scratch/claudeagentmodel/claudeagentmodel \
AIP_CLAUDE_MODEL_CERT_CLI=/absolute/path/to/claude \
AIP_CLAUDE_MODEL_CERT_OUT=/absolute/private/candidate.json \
  env GOWORK=off go -C connectors/claudeagentmodel test ./internal/service -run '^TestCertification_RecordInstalledBundle$' -count=1 -v
```

`-count=1` is required for candidate recording too: a cached observation is not
a fresh candidate record.

A recorded candidate becomes approved only by an explicit maintainer review that
commits it as `approved-local-candidate.json`; recording alone cannot satisfy the
verify gate.

## Staged packaging and rollback gate

```sh
go test -tags=integration ./tools/backendplugin/ \
  -run '^TestPackage_ClaudeAgentModelArchiveDeployment$' -count=1
```

The test packages `claudeagentmodel` together with `claudeagentacp`, installs the
archive into a fresh root, parses the strict manifest and package index, launches
the installed binary over gRPC, and checks descriptor/manifest parity, sidecar
presence and the host multi-user denial policy. It then removes the optional
connector and proves the separately packaged ACP binary and descriptor still
work. A synthetic version-only CLI drives production `Configure`; no account or
SDK query is touched.

## Recorded local candidate evidence

The values below are the candidate recorded on 2026-11-03 from current `main`
at `f1266f3b659476a83c349ce0d099d174a0f42510`, exact-bundle verified on the
same clean source, and approved as the committed oracle in #1715 (merged as
`f9a42bf9f`). The approval binds this artifact/runtime/closure identity; it does
not claim a published binary release or re-run the live acceptance matrix.

| Provenance | Value |
|---|---|
| Build-time source HEAD | `f1266f3b659476a83c349ce0d099d174a0f42510` |
| Build-time source fingerprint | `eaa5c1a39f9d24c1573f1433f82e3987656d34e70842759736b3c7d748358c39` |
| Build-time state | clean `fix/claude-agent-model-closeout` worktree at `origin/main`; fingerprint captured immediately before packaging |
| Final verified source | the same clean commit; record and exact-bundle verify ran without source changes between them |
| Clean-head reproducibility | `GOWORK=off CGO_ENABLED=0 go -C connectors/claudeagentmodel build -trimpath -buildvcs=false -ldflags=-buildid= -o /absolute/scratch/aip-backend-claudeagentmodel ./cmd/aip-backend-claudeagentmodel` -> `be7154cd42d8f6aa6c96b14c5864f252899f4002ae3ee8fa6ea01d5e1d313785` |
| Go toolchain | go1.26.10 |
| npm | 10.9.9 |
| Packaging command | `go run ./tools/backendplugin/package_plugins -root . -profile full -select claudeagentmodel -dest /absolute/scratch/claudeagentmodel-closeout` (the actual private scratch path is recorded only in the handoff evidence) |

| Artifact | Value |
|---|---|
| Platform | linux/amd64 |
| Go binary SHA-256 | `be7154cd42d8f6aa6c96b14c5864f252899f4002ae3ee8fa6ea01d5e1d313785` |
| Manifest SHA-256 | `28cf6fa974776ebe559535f9fca03299d6cef96117a21b8906604ac3ffa2c123` |
| `sidecar/bridge.mjs` SHA-256 | `abafc3994a09bc3a0db6fed64db92ac23d8ad5e4869ed261ca2b93c6c8d3b2ae` |
| `sidecar/session.mjs` SHA-256 | `54d7309da5fc27d597da6e1f07a7f313b0bc60f9df2080426805c4163191825f` |
| `sidecar/tool-server.mjs` SHA-256 | `dd022b32d73c35c50d085b00797f870562c87447e403c74ca3be40dd51f04cbc` |
| `sidecar/package.json` SHA-256 | `d689fb9e65dcea536c17e1f13ddd9d467ede17e0ff496fe525677147e355169f` |
| `sidecar/package-lock.json` SHA-256 | `6d60981b1833d2061e7e4c4e07938e364105035c3fe18267420cbcd4fb53b899` |
| Installed dependency tree SHA-256 | `6ee9941268434879dc9751c8bc06835271d7a53d113f91d1a9735287170357e5` |
| Installed dependency file count | 7698 |
| Node version / SHA-256 | 22.23.4 / `94efd062ed066c1a0b332e18249859c905e920eeada5df55d24c99b919629a90` |
| Claude Code CLI version / SHA-256 | 2.1.316 / `fd13c70000fec3f2116dfbcbb6121210b4a29328bf0e46210a59dd594a67d824` |
| Pinned Agent SDK version | 0.3.316 |
| Discovered models | 13 observed at the local candidate run; the catalogue is account/plan data, may change, and is not pinned by the approved oracle. Verification requires a non-empty unique observed set. |

## Private real-account smoke

The opt-in harness is `TestPrivateSmoke_OwnPlan` in
`internal/service/smoke_test.go`. It uses the operator's own native login and one
fixed, non-sensitive text prompt. Its record contains only OS/arch, Node/CLI/SDK
versions, discovered model IDs, model count, duration, event count, finish
reason, usage presence and a typed error class. It never records prompts, tool
arguments, tool results, response bodies, headers, account identifiers or
credentials.

```sh
AIP_CLAUDE_MODEL_SMOKE=1 \
AIP_CLAUDE_MODEL_SMOKE_RUNTIME=/absolute/scratch/claudeagentmodel/claudeagentmodel \
AIP_CLAUDE_MODEL_SMOKE_CLI=/absolute/path/to/claude \
AIP_CLAUDE_MODEL_SMOKE_WORKSPACE=/absolute/existing/workspace \
AIP_CLAUDE_MODEL_SMOKE_OUT=/absolute/private/smoke.json \
  env GOWORK=off go -C connectors/claudeagentmodel test ./internal/service -run '^TestPrivateSmoke_OwnPlan$' -count=1 -v
```

`-count=1` is required for the smoke: a cached PASS is not a live observation.

Run it only with remaining plan capacity and the account owner's authorization.
If the account is plan-exhausted, record `plan_exhausted` as the blocker and do
not run or claim a live acceptance. Keep the evidence private.

## Limitations, not claims

- The #1698 acceptance repair exercised real OpenCode 2.2.5 and real Claude Code
  2.1.316 tool loops, direct Anthropic continuation, and the P1 refusal/retry
  path on its reviewed tree. The full 10-case HTTP matrix and the Codex profile
  were not rerun on the final head; the earlier matrix remains historical
  evidence, not a Cartesian certification.
- Live acceptance ran on the #1698 merge head. Current `main` differs by the
  #1702 advisory lint modernization, the #1713 Go toolchain pin and the #1714
  root `x/crypto` removal; no new live tool-loop run was performed on this exact
  head. The fresh record/verify above binds the current artifact, not a new live
  matrix.
- The private real-account text smoke was run; its evidence stays private and is
  not committed here.
- Fresh artifact approval was recorded in #1715 (merged as `f9a42bf9f`); the
  committed oracle is the approved identity.
- The maintainer legal/policy sign-off is recorded in
  [`docs/claude-agent-model.md`](../../../docs/claude-agent-model.md#maintainer-legalpolicy-sign-off)
  for the narrow experimental, opt-in, local-only, single-user envelope. It does
  not approve a broad public subscription-backend claim, sharing, pooling,
  resale, rotation, or remote/multi-user hosting.
- macOS/Windows/WSL, remote hosting, multi-user sharing, account rotation,
  restart-durable parked turns and parallel native tools remain deferred.

The synthetic and staged suites prove the connector's contract; they do not
prove a subscription entitlement or replace the recorded policy sign-off.
