# Design

## Overview
One explicitly allowed HTTP carrier supplies a provider-neutral workspace hint. Existing local-agent connectors are immediate consumers. Invalid supplied hints fail explicitly; absent hints are inert.

### Goals
- Make stock HTTP session/workspace requests reach the existing canonical workspace hint.
- Reuse existing pool and filesystem owners; preserve default and authority behavior.

### Non-Goals
The Deferred list in requirements.md is outside this slice.

## Boundary Commitments
### This Spec Owns
The header/alias contract, shared syntax validation and stock frontend projection.
### Out of Boundary
Filesystem validation, process ownership, session authority, pool keys, credentials, administrator policy, routing, canonical shape and platform certification.
### Allowed Dependencies
`lipsdk.HTTPHeaders`, neutral `sessionwire`, existing `lipapi.Call.Extensions` and existing frontend invalid-request writers. Backend `WorkspaceHintsFromCall`/`ResolveWorkspace`/`RuntimeKey` consume the result without changes.
### Revalidation Triggers
SDK/header alias changes require config/frontend integration checks. Materialization changes require ordinary and bounded-body, streaming, session and routing characterization.

## Architecture
### Seams Reused
Reuse explicit SDK HTTP aliases/defaults and the neutral `sessionwire` carrier owner. Reuse `Call.Extensions["workspace_path"]`; no new canonical field, extension stage, registry, persisted binding or authority.

The new seam is necessary because no stock HTTP carrier currently projects a workspace hint (1.1). Its interface is:
```go
func ApplyWorkspaceHint(call *lipapi.Call, headers http.Header, aliases lipsdk.HTTPHeaders) error
```
It uses existing first-nonempty alias/header selection. Missing/whitespace-only input returns without allocation or mutation. Validate raw selected input size, UTF-8 and controls before trimming. Malformed nonempty input returns a static error with no path echo or logging.

Absolute-path syntax accepts POSIX rooted paths, rooted drive paths and valid UNC paths; drive-relative, single-backslash root-relative and malformed UNC roots reject. This is syntax only: no filesystem I/O, cleanup or resolution occurs at ingress. Drive/UNC fixtures prove parser behavior, not usability or new runtime certification on a Linux host; backend native-platform policy retains that decision.

Projection marshals one JSON string. Existing canonical workspace hint keys are not overwritten. Existing backend precedence is unchanged. Context workspace views are not rewritten: the hint and pinned view are separate channels, and this slice does not claim the view is transported into ACP. Headers remain request-local; stable client-session identity plus existing workspace-keyed pooling supplies the observed session/workspace association, not persisted session discovery.

Each stock frontend uses its existing `DecodeOptions.Headers` and `frontendpipe.ProofInput.Headers` at canonical materialization. Capture once and apply once per request, covering ordinary and bounded-body paths rather than adding a handler-only bypass. Invalid hints reuse existing frontend 400 writers. No provider name or SDK dependency enters ingress/core.

### Project Boundary Questions
- Plugin-owned wire syntax; core session/routing authority unchanged.
- Existing canonical hint only; no canonical shape or ABI change.
- Streaming-first materialization retained; no post-output retry changes.
- Local-only posture, policy and credentials remain at existing owners; this hint is not an authorization grant or filesystem sandbox.

## File Structure Plan
### New Files
- `internal/plugins/frontends/sessionwire/workspace.go` and `workspace_test.go`: one bounded parser/projection and proportional regressions.
### Modified Files
- `pkg/lipsdk/httpheaders.go` and `httpheaders_test.go`: `HeaderWorkspace`, `HTTPHeaders.Workspace`, defaults and value helper.
- `internal/core/config/http_headers.go` and existing tests: `HTTPHeadersConfig.Workspace`, effective aliases, workspace cross-field collision checks.
- Existing `decode.go`, `handler.go` and `profile.go` owners/tests in `openailegacy`, `openairesponses`, `openresponses`, `anthropic`, `gemini`: shared helper at ordinary and `CompileProof` materialization seams, including existing continuation materialization where applicable.
- Existing operator HTTP-header documentation: short example and hint/validation/default semantics.
No `pkg/lipapi`, routeoverride, backend workspace, pool, authentication or MCP changes are planned. A newly required core contract or authority blocks implementation pending design revision.

## Components and Interfaces
| Component | Intent | Requirements |
| --- | --- | --- |
| SDK/config aliases | Explicit header contract | 1.1, 1.4 |
| sessionwire helper | Bounded parsing and isolated projection | 1.2, 1.3, 2.1, 2.2 |
| Stock materialization | Shared helper on all inference paths | 1.1, 3.1 |
| Existing local-agent consumer | Validate/use workspace and isolate sessions | 2.3, 3.2 |

## Error Handling
Absent hints are inert. Invalid nonempty hints return existing protocol-specific invalid-request HTTP 400 with a static diagnostic before execution, without echoing paths. Backends retain existing unusable-workspace classification. There is no new wire error table.

## Testing Strategy
Use bounded table fixtures for missing/empty, POSIX/drive/UNC, aliases/duplicates, size, raw controls, UTF-8, relative and malformed paths. Assert only the workspace key is added and existing canonical hints/session/route/auth fields remain unchanged. Extend existing family decode and bounded-materialization fixtures without a Cartesian matrix. Reuse backend pool/workspace tests instead of duplicating them. Run SDK/config/frontend suites, vet/lint, generic architecture/contracts and `kirocheck`.

The downstream task 3.2 spawned-distribution run retains PID/start-time/cwd, stable session identity, repeat/two-project/return and cleanup observations. No synthetic-only downgrade or expanded Claude platform claim is accepted.
