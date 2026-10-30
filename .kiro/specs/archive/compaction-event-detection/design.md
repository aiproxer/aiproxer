# Design Document

## Overview

Add a proxy-derived compaction lifecycle that can be observed by feature plugins without changing model traffic. The implementation has four pieces:

1. a typed fail-open `pkg/lipsdk/compaction.Observer` contract;
2. one concrete process-owned detector in `internal/core/compactiondetect`;
3. request-side observation of the effective canonical baseline after a B-leg opens;
4. response-side observation of canonical events actually released by `retryRecvStream`.

## Scope

In scope: typed observer subscription, existing canonical compaction signals, versioned surveyed-agent signatures, bounded A-leg heuristic/transaction state, request-open and final-release integration. Out of scope: general agent identification, unsupported request controls, traffic mutation, durable event storage.

## Existing Architecture

Secure prepare resolves authoritative A-leg and freezes the post-transform/pre-request effective canonical `baseline`; this is the request fingerprint input.

Existing `OperationContextCompaction`, `ItemKindCompaction`, and normalized walkers are reused.

Response detection belongs at the final `retryRecvStream` canonical release boundary after hooks/gates/finalizers/recovery.

`ProcessServices` owns cross-request detector state across generation reloads.

## Public SDK Contract

Create `pkg/lipsdk/compaction`:

```go
type Phase string
const (
    PhaseStarted   Phase = "started"
    PhaseCompleted Phase = "completed"
)

type Evidence string
const (
    EvidenceProtocolStrict  Evidence = "protocol_strict"
    EvidenceSignatureStrict Evidence = "signature_strict"
    EvidenceHistoryHeuristic Evidence = "history_heuristic"
)

type Event struct {
    Phase         Phase
    Evidence      Evidence
    RuleID        string
    TransactionID string
    TraceID       string
    ALegID        string
    BLegID        string
    AttemptSeq    int
    SessionID     string
    OccurredAt    time.Time
}

type Observer interface {
    OnCompaction(context.Context, Event) error
}
```

No request/response body or numeric confidence score is exposed.

`FeatureBundle` gains `CompactionObservers []compaction.Observer`. The single merge surface concatenates it, and `RequestRuntimeSnapshot` exposes the frozen slice using existing observer conventions. No new StageID is required; this is a specialized observation surface.

## Core Detector

Create a concrete `internal/core/compactiondetect.Detector`; no exported interface is needed.

Conceptual API:

```go
func (d *Detector) RequestOpened(meta RequestMeta, call lipapi.Call) []compaction.Event
func (d *Detector) ResponseReleased(meta ResponseMeta, ev lipapi.Event) []compaction.Event
```

The detector is pure with respect to external I/O and returns derived events for dispatch.

### Rule representation

Use a static table: versioned ID, mode (`single`/`series`/`completionOnly`), request/post matcher, evidence.

Matchers inspect canonical normalized roles/items/text. Keep rule functions explicit rather than inventing a configurable regex DSL.

Initial versioned rule matrix is in `research.md`.

## Detection Flow

### Request-side flow

```text
client request
 -> secure-session/A-leg authority
 -> submit + request transforms + pre-request
 -> effective baseline frozen
 -> route/candidate admission
 -> backend Open succeeds
 -> Detector.RequestOpened(ALegID, TraceID, BLegID, baseline)
 -> fail-open compaction observer dispatch
```

Emit only after `Open` succeeds; retry/failover B-legs are deduplicated.

### Response-side flow

Observe each final `retryRecvStream` release exactly once; centralize return-site observation rather than branch-specific detection.

```text
selected canonical release
 -> Detector.ResponseReleased(...)
 -> fail-open compaction observer dispatch
 -> existing frontend/client delivery
```

Observation must not alter the event.

## Strict Detection

### Protocol strict

- Opened `OperationContextCompaction` -> `started`.
- Released `ItemKindCompaction` -> `completed` once.
- Successful terminal explicit compact operation -> completion if not already emitted.

### Signature strict

Require distinctive conjunctions. Families cover Codex checkpoint; Pi/OpenClaw context-summarizer + `<conversation>` + structured prompt/carrier; Cline agentic/basic post; OpenCode/Kilo anchored templates; Hermes current/legacy markers; Claude snapshot structured compaction request/post prefix; Gemini/Aider series; Roo/Crush. Exact rules are in `research.md`.

## History Heuristic

The heuristic is completion-only and runs only when strict post evidence did not already win.

For each successfully opened request store a bounded fingerprint:

```go
type requestFingerprint struct {
    EstimatedTokens int
    ItemCount       int
    TailHashes      [N][32]byte
    PrefixHash      [32]byte
    PrefixItems     int
    SeenAt          time.Time
}
```

SHA-256 semantic hashes use normalized role/kind/content; source text is discarded after hashing.

Require all of:

1. same authoritative A-leg;
2. prior context above a minimum size;
3. token reduction >= `max(absoluteFloor, previous*relativeFloor)` (initial RED tests should pin the concrete values, e.g. 8k and 25%);
4. at least two recent semantic tail hashes preserved in order;
5. meaningful older prefix removed/replaced.

Known reset/fork/new boundaries disable inference; ambiguity emits nothing.

## Transaction State

Process-owned state:

```go
type legState struct {
    LastRequest requestFingerprint
    Active      *transactionState
    LastSeen    time.Time
}
```

`transactionState` stores ID, rule ID/mode, first/last timestamp, and completion-emitted state only.

- `single`: first matching opened request emits start; strict later completion closes; otherwise stale/ordinary transition may close silently.
- `series`: later matching utility subcalls reuse the transaction and suppress starts; first strict/heuristic completion closes.
- `completionOnly`: no start is emitted; a post marker/heuristic creates one completed event transaction.

Transaction ID is a deterministic opaque hash of A-leg plus first triggering trace/request identity.

If one request proves old completion and independently starts another transaction, emit old completion first.

## Lifetime, Bounds, and Concurrency

`ProcessServices` owns one detector shared by runtime generations.

Use one small map mutex; never invoke observers while holding it.

Bound state with:

- max active A-leg entries;
- inactivity TTL;
- lazy opportunistic eviction on detector calls;
- no goroutine/ticker.

## Observer Dispatch

Dispatch frozen observers in order, isolate panic/error, continue to later observers, never under the detector lock, and never enqueue background work.

## Protocol Compatibility Boundary

Detection shall not broaden the current pinned protocol model. In particular:

- Codex V2 request `compaction_trigger` is not currently a canonical supported item;
- Hermes native `context_management`/`compact_threshold` has no current canonical request carrier.

Those controls require a separate compatibility change before detector rules are added.

## Error Handling

Observer errors/panics are fail-open; unknown/missing evidence emits nothing and cannot trigger failover/cancellation/replacement.

## Testing Strategy

TDD order: RED SDK/FeatureBundle contract; strict-rule matrix/near misses; heuristic/transaction/expiry/dedupe; no-start-before-Open and retry-B-leg dedupe; every final stream release exactly once; generation-reload continuity; concurrent A-leg race coverage; repository quality/architecture and simplification gates. No network credentials are required.

## Design Invariants

1. Detection is canonical, not provider-specific.
2. Authoritative A-leg is the state key.
3. Start means upstream execution actually opened.
4. Completion means evidence actually proves or strongly infers installation.
5. Events are metadata-only.
6. Listener failures never affect model traffic.
7. State is bounded, process-local, and content-free after hashing.
8. No new unsupported protocol semantics are smuggled into detection.
