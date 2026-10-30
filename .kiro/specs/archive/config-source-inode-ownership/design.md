# Technical Design

## Overview

This prerequisite keeps the accepted Linux configuration inode alive while its effective configuration is active. On Linux, lease-based runtime comparison is enabled only when the open source handle is positively tied to the ext4 kernel driver in the process mount namespace. Unsupported Linux startup keeps its existing bounded read behavior, while runtime comparison without a verified lease fails closed with the existing source-integrity result. Windows and other existing platform adapters keep their current behavior.

The source owner is transferred with the effective configuration through bootstrap, reload, publication, and shutdown. A successful generation swap is the commit point: later publication callbacks or bookkeeping cannot turn that committed update into a rejected reload or release its matching source owner.

### Goals

- Prevent accepted ext4 inode reuse from making an eligible atomic recovery look like an in-place update.
- Give each source handle one close authority and safe non-owning borrows.
- Keep candidate, generation, effective configuration, and active source ownership aligned across no-op, rejection, publication, and panic.
- Drain admitted attempts before requesting source close and include source finalization in idle waiting.
- Certify the supported Linux lifetime in mandatory CI without making ordinary checks depend on ext4.

### Non-Goals

- Claim inode-lifetime uniqueness for Linux filesystems other than a positively identified ext4 kernel driver.
- Change configuration meaning, the archived owning spec, SDK result enums, load categories, metric labels, or public runtime options.
- Add filesystem mutation, inode-generation ioctl evidence, raw-block inspection, mount-path prefix matching, a source watcher, or background close workers.
- Provision a host or Incus mount; only the Ubuntu CI test job creates its disposable loop fixture.

## Boundary Commitments

- **This spec owns** the fixed-source identity and lifetime behavior, source owner transfers, their reload/publication and host-shutdown integration, one-shot bootstrap consumers, and the required supported-filesystem certification lane.
- **This spec does not own** provider/plugin behavior, canonical config decoding rules, request execution, schema or wire changes, filesystem configuration, or the archived reload specification.
- **Allowed dependencies** are the existing configsource package and platform identity adapters, runtimehost/runtimebundle composition and lifecycle APIs, existing safe lifecycle observer/result vocabulary, the standard library, and the repository's existing Linux syscall dependency.
- **Platform commitment**: positively verified ext4-driver handles use linux-ext4-dev-ino-lease-v1. Linux with absent or uncertain evidence keeps valid startup loading but has no runtime lease. Windows retains win-fileid and reload; other supported non-Linux behavior is preserved without a new uniqueness claim.
- **Revalidation triggers**: changing runtime result mapping, source-integrity categories, generation publication truth, host shutdown ordering, platform identity schemes, or the mandatory Linux certification route.

## Architecture

### Existing Architecture Analysis

- internal/infra/configsource owns fixed-path bounded reads and classifies raw source identity and digest. ReadStable currently closes each opened file after a read; the Linux identity includes device, inode, and statx birth time. The active source metadata has no lifetime owner.
- internal/infra/runtimebundle/bootstrap_effective.go is the common effective-load path used by host construction and one-shot inspection/validation. The returned source metadata currently has no matching close-capable owner.
- internal/infra/runtimehost/reload_state.go holds active effective/source metadata; attempt_runner.go evaluates and compiles candidates; attempt_gate.go owns attempt admission and idle notification.
- internal/infra/runtimehost/manager.go changes the active generation pointer during Publish. internal/infra/runtimebundle/resource_ledger.go starts PhasePublish work after activation and records phase completion.
- internal/infra/runtimebundle/reload_host.go carries a second Host.activeSource metadata copy and coordinates Host.Close. The comparison baseline should live only in ReloadState.

### Architecture Pattern & Boundary Map

**Selected pattern**: explicit single-owner resource transfer with non-owning borrow tokens. The source adapter owns file identity and descriptor semantics; runtimehost owns source-baseline state and commit sequencing; runtimebundle owns composition, startup rollback, one-shot release, and Linux certification wiring.

pkg/lipapi, public SDK contracts, core configuration decoding, provider plugins, and backend connectors receive no source-handle ownership.

~~~mermaid
graph LR
    ConfigPath --> FixedSource
    FixedSource --> SourceSnapshot
    SourceSnapshot --> Bootstrap
    Bootstrap --> StartupOwner
    StartupOwner --> Coordinator
    Coordinator --> ReloadState
    ActiveOwner --> SourceBorrow
    SourceBorrow --> CandidateRead
    CandidateRead --> AtomicClassifier
    AtomicClassifier --> ReloadRunner
    ReloadRunner --> ManagerPublish
    ManagerPublish --> PublishedGeneration
    ReloadRunner --> SourceAdoption
    SourceAdoption --> ReloadState
    ShutdownGate --> ShutdownFinalizer
    ShutdownFinalizer --> ActiveOwner
~~~

### File Structure Plan

| Action | File | Responsibility |
|---|---|---|
| Add | scripts/configsource-fault-check.sh | Require exact coordinator and Host.Close tagged-fault discovery, execution and pass without skips in the local Linux gate and existing Ubuntu ext4 fixture lane. |
| Modify | internal/core/config/reload_strict_effective_contract_test.go | Mechanically migrate the existing source consumer fixture to TakeBaseline and explicit owner cleanup; preserve native reload assertions and core production behavior. |
| Modify | internal/stdhttp/admin/configreload/self_defense_management_isolation_test.go | Mechanically migrate bootstrap source-owner returns, coordinator transfer and temporary loader cleanup while preserving management recovery assertions. |
| Modify | cmd/lipstd/reload_signal_adapter_unix_test.go, internal/stdhttp/config_reload_soak_test.go | Mechanically provide valid synthetic native source identities while preserving signal reload and soak assertions. |
| Modify | internal/archtest shrinkage accounting and budget documentation | Account accurately for additive source ownership code with realistic bounded headroom under the user-requested secondary LOC budget policy; retain measured reports and mandatory guardrails. |
| Modify | internal/infra/configsource/types.go | Add private owner/provenance references and takeable snapshot metadata without granting close authority to metadata copies. |
| Add | internal/infra/configsource/lease.go | Implement the synchronized source owner, idempotent borrow release, RequestClose, context-bounded Close, cached close completion, and destructive owner slots. |
| Modify | internal/infra/configsource/identity_linux.go | Add the Linux lease identity and positive ext4-driver detector bound to the open handle and process mount namespace. |
| Preserve/adapt | internal/infra/configsource/identity_unix.go, identity_unix_common.go, identity_other.go, identity_windows.go | Keep existing non-Linux identity behavior and expose no Linux uniqueness claim. |
| Modify | internal/infra/configsource/fixed_source.go | Keep the candidate handle open through bounded read, same-handle stability checks, target reopen/validation, classification, and candidate-owner transfer. |
| Modify | internal/infra/configsource/classify.go | Require provenance-checked active/candidate borrows for Linux lease comparison and preserve raw no-op, eligible replacement, and non-atomic rejection semantics. |
| Modify | internal/infra/configsource/effective.go, errors.go | Guard and release the one-shot effective loader's retained snapshot without changing its return API; represent cleanup failures with the existing source-integrity/error vocabulary. |
| Modify | internal/infra/configsource/atomic_recycle_test.go | Replace the prior recycle test with the linux && configsource_cert tests TestFixedSource_PinnedAtomicRecovery and TestFixedSource_PinPreventsAcceptedInodeReuse. |
| Add/modify | internal/infra/configsource/lease_test.go, strict_source_contract_test.go, identity_linux_test.go | Cover borrow/close races, unsupported Linux startup, stable-read/target behavior, and deterministic capability seams. |
| Modify | internal/infra/runtimehost/reload_state.go | Own the active source, implement prevalidated callback-free adoption, and retain an idempotent receipt with the displaced owner. |
| Modify | internal/infra/runtimehost/attempt_runner.go | Guard candidate ownership, validate the adoption target before publication, isolate precommit cleanup, and immediately adopt committed updates. |
| Modify | internal/infra/runtimehost/coordinator.go, coordinator_fixed_source.go | Transfer startup and attempt outcome slots exactly once and clean up only uncommitted unused owners. |
| Modify | internal/infra/runtimehost/attempt_gate.go | Register one shutdown finalizer and include finalizing/finalizationDone in busy, notification, and WaitForIdle behavior. |
| Modify | internal/infra/runtimehost/manager.go | Isolate the post-swap StartPublished callback while preserving the committed active generation and public Publish signature. |
| Modify | internal/infra/runtimehost/attempt_runner_test.go, coordinator_test.go, attempt_gate_concurrency_test.go, shutdown_test.go | Prove candidate ownership, adoption receipt, finalization, and waiter behavior across ordinary and adversarial outcomes. |
| Modify | internal/infra/runtimebundle/bootstrap_effective.go | Return effective config, comparison metadata, optional source owner, and fixed overrides from the canonical loader seam. |
| Modify | internal/infra/runtimebundle/host_build.go, reload_host.go | Install startup rollback ownership immediately after load, transfer it into NewCoordinator only on successful construction, remove Host.activeSource, and join Host.Close source cleanup with existing safe shutdown. |
| Modify | internal/infra/runtimebundle/inspect.go, validate_structural.go, validate_distribution.go | Consume the startup source owner before registry/tracing/plugin work and return joined cleanup errors without retaining it. |
| Modify | internal/infra/runtimebundle/resource_ledger.go | Make PhasePublish start invocation panic-isolated and guarantee publishDone/publishErr caching, publishing clear, and waiter broadcast. |
| Modify | internal/infra/runtimebundle/effective_load_contract_test.go and relevant host/inspect/validation tests | Close the new loader owner explicitly and assert startup, one-shot, rollback, and cleanup ownership. |
| Add | scripts/configsource-certify.sh | Require Linux and explicit writable TMPDIR; verify both tagged tests are discovered and executed with no skip or missing test. |
| Modify | scripts/quality-gate.sh | Invoke certification after the root tests for Linux Go/module gates; do not provision host storage. |
| Modify | .github/workflows/ci.yml | In the Ubuntu test variant when test scope is true, provision and remove its RUNNER_TEMP ext4 fixture and invoke certification. In the existing Windows test variant under the same test-scope condition, run native configsource and runtimehost tests for identity and reload evidence. |
| Verify only | scripts/hooks/pre-commit | Confirm its existing quality-gate invocation reaches the new Linux certification; do not add a bypass or weaken the source-change cap. |

### Technology Stack

| Layer | Choice | Role |
|---|---|---|
| Runtime | Existing Go toolchain from go.mod | Source owner, bounded reader, reload transaction, and shutdown behavior. |
| Linux adapter | Existing golang.org/x/sys/unix dependency | Handle-bound statx mount ID and fstatfs evidence; no new dependency. |
| Filesystem metadata | Linux statx plus bounded /proc/self/mountinfo read | Positively identify the ext4 driver for an already-open source handle. |
| Tests and CI | Go tests, existing shell quality gate, Ubuntu CI test job | Separate portable default tests from mandatory ext4-backed certification. |

## System Flows

### Candidate and publication sequence

~~~mermaid
sequenceDiagram
    participant Gate
    participant Runner
    participant Source
    participant Manager
    participant State
    Gate->>Runner: Admit attempt before source borrow
    Runner->>Source: ReadStable with explicit active baseline
    Source-->>Runner: Snapshot and candidate owner
    Runner->>Runner: Build and prevalidate source update
    Runner->>Manager: Publish prepared generation
    Manager->>Manager: Swap active generation pointer
    Manager->>Manager: Isolate post-swap start callback
    Manager-->>Runner: Return committed publication
    Runner->>State: Adopt matching effective and source state
    State-->>Runner: Adoption receipt with displaced owner
~~~

Before publication commits, errors, cancellation, rejection, or raw no-op release only the candidate owner and retain the active baseline. An effective no-op transfers its eligible source owner through ordinary Apply because no generation swap occurred. After a successful swap, source adoption precedes endStage/endAttempt and any fallible bookkeeping.

### Source owner lifecycle

~~~mermaid
stateDiagram-v2
    [*] --> Open
    Open --> Borrowed: Acquire validated borrow
    Borrowed --> Open: Release last borrow
    Open --> CloseRequested: RequestClose
    Borrowed --> CloseRequested: RequestClose
    CloseRequested --> Closing: Borrow count reaches zero
    Closing --> Closed: Close result cached
    Closed --> [*]
~~~

The owner claims and clears its file slot under its lock, closes outside the lock exactly once, then caches completion and error. A context bounds a caller waiting for borrows; it cannot interrupt the kernel close syscall.

## Requirements Traceability

| Requirement IDs | Summary | Components | Interfaces | Flows |
|---|---|---|---|---|
| 1.1, 1.2 | Linux identity requires a live matching pin; invalid proof fails closed. | SourceLeaseOwner, ActiveSourceVersion, AtomicClassifier | Borrow, provenance validation, existing source-integrity result | Candidate and publication sequence |
| 2.1, 2.2, 2.3 | Verify only the ext4 kernel driver; uncertainty disables runtime lease but not valid startup loading. | Linux detector, FixedSource | Handle-bound detector result and unavailable baseline capability | Bootstrap and candidate read |
| 3.1, 3.2, 3.3 | Preserve Windows, existing non-Linux adapters, and unsupported-platform behavior. | Platform identity adapters | Existing win-fileid/dev-ino schemes and unsupported result | Platform-specific startup and runtime paths |
| 4.1, 4.2, 4.3 | Preserve bounded stable reads and classify changed same-source, identical no-op, and different eligible source correctly. | FixedSource, AtomicClassifier | SourceSnapshot, stability metadata, raw digest | Candidate read and classification |
| 5.1, 5.2, 5.3 | Metadata has no close authority; borrows validate provenance; close runs once and caches result. | SourceLeaseOwner, SourceBorrow | Owner slot, Borrow, idempotent Release, RequestClose, Close(ctx) | Source owner lifecycle |
| 6.1, 6.2, 6.3 | Bootstrap, successful host bind, failed rollback, and one-shot loads release or transfer the owner exactly once. | Bootstrap loader, Host builder, Coordinator, one-shot operations | Takeable startup owner slot and cleanup marker | Bootstrap and one-shot flow |
| 7.1, 7.2, 7.3, 7.4 | Precommit cleanup preserves active source; effective no-op and committed publish transfer once; receipt cannot dispose adopted owner. | AttemptRunner, Manager, ReloadState, Coordinator, Apply | Prevalidated update, adoption primitive, idempotent receipt | Candidate and publication sequence |
| 8.1, 8.2, 8.3, 8.4 | Shutdown waits for attempt and source finalization, respects context, and completes one callback. | AttemptGate, ReloadState, Host.Close | Shutdown-only callback, finalization state, WaitForIdle(ctx) | Shutdown finalization |
| 9.1, 9.2, 9.3 | Cleanup errors preserve primary outcome; post-swap failure preserves Published truth; diagnostics stay bounded and secret-safe. | Source cleanup marker, ReloadObserver, Manager, ResourceLedger, Host.Close | Existing result/category/label vocabulary | Rejection, commit, and shutdown error paths |
| 10.1, 10.2 | Bound host-owned descriptors and return to baseline after source lifecycle completion. | FixedSource, SourceLeaseOwner, detector, lifecycle tests | Active/candidate/target slots and transient detector handle | Read, release, and shutdown |
| 11.1, 11.2, 11.3 | Certify 1,000 ext4 cycles in mandatory Linux gates while keeping default checks portable. | Tagged cert tests, certify script, quality gate, Ubuntu CI test job | configsource_cert tag, explicit TMPDIR, job-owned loop fixture | Local Linux and Ubuntu CI certification |

## Components and Interfaces

| Component | Domain | Intent | Requirement coverage | Key dependencies | Contracts |
|---|---|---|---|---|---|
| SourceLeaseOwner and SourceBorrow | Infrastructure source adapter | Own the accepted file and expose checked temporary borrows. | 1.1, 1.2, 4.1, 4.2, 5.1, 5.2, 5.3, 10.1, 10.2 | os.File, identity metadata, caller context | State |
| Linux ext4 detector and FixedSource | Infrastructure source adapter | Prove supported mount from the open handle and perform a bounded stable read. | 2.1, 2.2, 2.3, 3.1, 3.2, 3.3, 4.1, 4.2, 4.3, 10.1 | statx, fstatfs, process mountinfo | Service, State |
| Bootstrap and one-shot source consumption | Composition root | Move or close a startup owner on each host, inspection, and validation path. | 6.1, 6.2, 6.3 | configsource, runtimebundle | State |
| AttemptRunner and ReloadState adoption | Runtime lifecycle | Prepare source update before publish and align source state with committed generation. | 5.2, 5.3, 7.1, 7.2, 7.3, 7.4, 9.1 | FixedSource, Manager, effective loader | State |
| Publication lifecycle | Generation lifecycle | Preserve committed publication when post-swap start work fails. | 7.3, 7.4, 9.1, 9.2, 9.3 | Manager, ResourceLedger, lifecycle observer | State |
| AttemptGate and Host.Close | Host shutdown | Keep the baseline through admitted work and wait for finalization. | 5.3, 8.1, 8.2, 8.3, 8.4, 9.1, 10.2 | Coordinator, ReloadState, context | State |
| Certification lane | Test/QA infrastructure | Require a real supported ext4 fixture for the inode-lifetime proof. | 11.1, 11.2, 11.3 | Go test tags, shell quality gate, Ubuntu runner | Batch |

### SourceLeaseOwner and SourceBorrow

**Intent**: Make one token the only authority that can request closure while allowing active and candidate identity checks to borrow the file safely.

**State model**: The private synchronized core stores the file, immutable identity/provenance, borrow count, close-requested/closing/closed state, completion notification, and cached close error. SourceLeaseOwner is moved through take-and-clear slots. ActiveSourceVersion carries metadata and a private core reference but exposes no descriptor or close method. A borrow validates exported identity, platform, scheme, and captured opaque identity before incrementing the borrow count; its release token is idempotent.

**Close contract**:

- RequestClose is idempotent, prevents later borrows, and claims closure immediately only when no borrows remain.
- The claimant clears the file slot under the core lock, closes outside the lock, then records completion/error. Otherwise the final release performs the close.
- Close(ctx) requests close and waits only until completion or the supplied context. Repeated calls return the cached result; close is never retried after any syscall result.
- No background goroutine, finalizer, raw-FD retention, or call to Fd racing final close is allowed.

### Linux ext4 detector and FixedSource

**Intent**: Certify one handle's mount support, then read and validate the fixed source while preserving its identity lifetime.

**Detector contract**:

1. Keep the same open regular-file handle throughout capability detection.
2. Request statx mount ID with AT_EMPTY_PATH and require the returned mask to contain it.
3. Read at most 1 MiB from /proc/self/mountinfo. Require exactly one entry matching mount ID and device major/minor, filesystem type exactly ext4, and corroborating handle fstatfs magic.
4. Treat missing, malformed, ambiguous, truncated, absent, unsupported, or mismatched evidence as unavailable. fstatfs magic alone is insufficient. Do not match mount paths by prefix, mutate with ioctl, inspect raw blocks, or fall back to unbounded input.
5. Scope support to the ext4 kernel driver, including ext-family formats mounted by that driver. A detached mount absent from the process mountinfo view is unavailable.

For a supported Linux source, use linux-ext4-dev-ino-lease-v1 and require active/candidate support on the same device. Capture identity, size, and available modification/change timestamps from the candidate handle before reading and revalidate the same handle after the bounded maxBytes+1 read. Reopen the fixed path and validate regular-file mode, physical identity, size, and matching stability metadata on that target handle; close every target on every exit. Avoid separate os.Stat(path) and identity lookups that can observe different targets.

For unsupported Linux startup, preserve the existing bounded stable read and return an explicit unavailable baseline. Runtime ReadStable receives an explicit baseline; nil is reserved for bootstrap. Linux runtime comparison without a matching live pin fails with CategoryNonAtomicUpdate, which maps to ResultSourceIntegrity. Native Windows and other existing platform identity behavior remains intact.

### Bootstrap and one-shot source consumption

LoadBootstrapEffectiveWithSource and its loader seam return the effective config, comparison metadata, optional owner, and fixed CLI/environment overrides. The loader guards the snapshot immediately after read; decode, validation, or panic closes it.

buildHost installs rollback ownership immediately after the load and before nil-effective, access-mode, or tracing checks. bindHost passes a takeable startup owner slot to NewCoordinator; validation failure leaves the slot with the caller. Coordinator construction adopts it only after successful construction, with rollback protection until return. Successful bind clears the caller slot. An afterBind error closes the completed Host and joins cleanup errors. Earlier build/bind failure uses the existing initial-failure cleanup. Remove Host.activeSource; ReloadState is the single comparison-baseline owner.

prepareInspect covers PrepareInspect, InspectRoutes, and InspectInventory. ValidateStructural and ValidateDistribution consume the bootstrap owner as soon as effective loading succeeds and before registry, tracing, or plugin work. They retain no source owner in one-shot results. Cleanup failure is joined with any primary error. Update every loader seam and test fixture mechanically; pkg/lipruntime continues to delegate to BuildHost.

FixedSource.LoadEffective is also a one-shot consumer: it returns only effective configuration, atomic result, and error, so preserve that return API and retain no source owner in its result. Install a local snapshot-owner guard immediately after ReadStable succeeds. Release the snapshot on success, decode/validation failure, cancellation, and panic; on ordinary returns join any cleanup failure with the primary error, and on panic release ownership before propagating the panic. LoadEffectiveFromPath delegates to this same guarded path and adds no second close authority.

### AttemptRunner, Manager, ResourceLedger, and ReloadState

**AttemptRunner precommit**:

- Guard the candidate owner immediately after ReadStable, including raw no-op, loader/classifier/compiler error, cancellation, and publication rejection.
- Build and validate the effective/source update before Manager.Publish; preallocate its metadata copy and Published result bookkeeping.
- Production construction receives its concrete ReloadState adoption target. A missing target or invalid pairing fails before publication.
- Raw no-op closes the candidate and retains the active baseline. Effective no-op uses the takeable outcome slot and ordinary Apply adoption because no generation was swapped.

**Post-swap callbacks**:

- Manager.Publish swaps active generation before calling startPublishedWork. Isolate returned error and panic locally without changing Publish's public signature. Once swapped, return success and keep the generation active; report the failure through existing bounded lifecycle diagnostics. Never discard the committed generation or return a precommit failure.
- ResourceLedger.runStarts records startAttempted before each PhasePublish invocation and converts a panic to a bounded private error. execStartPhase caches publishDone/publishErr, clears publishing, and broadcasts on normal error. A PhasePublish completion guard performs the same finalization on unexpected unwind.
- Stop remaining starts after failure, do not retry partially started hooks, and let normal generation quiesce/close own attempted resources. Prepare/Activate remain precommit rollback paths.

**Committed adoption**:

Install a deferred guard over the prevalidated update and non-nil prepared generation before Publish. Immediately after successful Publish, set the committed flag by plain assignment, read generation ID through the existing atomic accessor, and invoke private ReloadState.adoptPublishedSource before endStage/endAttempt or other fallible bookkeeping. There is no callback, allocation, capability validation, or fallible library call between Publish success and the committed flag.

The adoption primitive uses the valid constructed state mutex and installs defer-unlock immediately after Lock. It consumes the validated owner slot while installing matching effective config, preallocated metadata, owner, and prebuilt Published last-result/last-success fields together. It performs no callback, injected seam, decode, normalization, allocation, or error return in the critical section. Before unlock it marks the update adopted and stores the displaced owner in the guarded receipt. Displaced-owner cleanup and diagnostics run afterward through panic-isolated guards.

Outer recovery closes a still-pending owner and reports the existing panic failure before commit. After commit, recovery idempotently completes the same adoption and returns the prebuilt Published result; it never disposes the committed owner. Published outcomes carry an idempotent adoption receipt and no close-capable owner. Startup Publish uses the same callback isolation, while its pin remains with the build rollback guard until coordinator bind succeeds.

For initial and coalesced attempts, Coordinator guards returned uncommitted source slots until Apply consumes them. Panic, unapplied/empty/busy/rejected outcome, or nil state closes only an unused uncommitted slot. A committed receipt confirms already-adopted state and cannot be cleaned as a candidate. Apply performs Published status/history bookkeeping without a second ownership transfer; effective Noop still destructively adopts its validated outcome slot. Isolate observer callbacks. Adoption target validity is established before publish, so production committed updates are never disposed by nil-state cleanup. Metadata clones are borrowers only.

### AttemptGate and Host.Close

Preserve BeginShutdown cancellation of admitted attempts, rejection of new admission/follow-ups, and the manager publication guard. Do not request baseline close before admitted work completes: attempt admission occurs before ActiveInput and source borrow.

Register one shutdown-only callback atomically under gate.mu. If an attempt exists, leave idleNotify open and defer the callback until Complete/Abandon has cleared the last attempt after outcome cleanup. Claim finalizing under the same lock and do not close idleNotify. If shutdown starts while idle, replace the already-closed idleNotify with a new open channel and claim finalization before unlocking. Repeated BeginShutdown cannot replace the channel or claim another callback.

Run the callback outside gate.mu. It detaches the source owner under state.mu, then requests close using a guarded local owner. The owner-release guard precedes optional diagnostics. A deferred callback completion block recovers/sanitizes panic, caches any finalization error, clears finalizing, sets finalizationDone, and closes the notification exactly once. Do not nest gate/state locks, start a helper goroutine, or wait for borrows in the callback.

WaitForIdle(ctx) returns only when active is nil, finalizing is false, and either shutdown has not begun or finalizationDone is true. Otherwise it waits on the current notification or context and rechecks. Gate Snapshot reports busy during finalization; shutdown still rejects admission. A waiter linearized before initially-idle BeginShutdown may return, while every waiter observing shutdown/finalizing waits for completion or its deadline.

Host.Close(ctx) remains BeginShutdown then WaitForIdle(ctx). Normal drain requests/waits for source completion with the same context, aggregates source errors, and continues existing manager/process/tracing sequencing. On deadline, return promptly while the registered callback remains armed; admitted attempt completion or final borrow release later completes source closure. Retries observe cached finalization/source errors and resume safe cleanup. No cancellation-stripped wait or helper goroutine is added.

### Error and Diagnostic Contract

- No new core LoadCategory, SDK result enum, or metric label is introduced. Unavailable/invalid Linux lease uses existing CategoryNonAtomicUpdate and maps to ResultSourceIntegrity, with bounded private unavailable/closed/provenance reasons. Do not introduce source_reload_unavailable.
- Post-swap start failure uses existing cleanup/cleanup_failed lifecycle diagnostics and a bounded post-publish-start reason; it never relabels Published or includes a raw panic value.
- Runtime candidate, target, displaced-owner, and deferred shutdown close failures use existing cleanup/cleanup_failed observation. Bootstrap and one-shot paths join cleanup errors without starting a runtime observer.
- A target-close failure before success returns the existing partial-unreadable/source-integrity failure. Source-level cleanup errors use a private typed marker so the runner can emit existing cleanup telemetry without a configsource observer dependency.
- Candidate cleanup after rejection/no-op retains the original result and reports the cleanup failure. Displaced-owner cleanup preserves committed publication. Deferred final-release cleanup caches its error and emits the bounded failure once through a panic-isolated sink installed on adoption.
- Host.Close reports source cleanup failure alongside subsequent safe cleanup. Bootstrap rollback joins it with the primary error. Raw close error text, panic values, source paths, and config bytes do not enter SDK status, metrics, history, or safe logs.

## Data Models

### Source state

- **SourceSnapshot**: bounded bytes, source ID, observed identity, size, modification metadata, digest/read time, and an optional takeable owner slot.
- **SourceLeaseOwner**: the sole close-capable token around a private synchronized file core.
- **ActiveSourceVersion**: comparison metadata plus a private, non-owning core reference. An unsupported Linux baseline explicitly says lease unavailable; native schemes may have no owner.
- **SourceBorrow**: validated idempotent release capability with no descriptor access or close authority.
- **SourceAdoptionReceipt**: confirms an owner was installed, records the displaced owner for post-lock cleanup, and contains no authority to close the adopted baseline.
- **ReloadState**: the sole active effective/source baseline and result/history state. Host metadata copies do not create another owner.

### Consistency and transfer rules

1. Bootstrap reads once and either transfers or closes the snapshot owner.
2. Candidate read borrows the active source and returns a separately owned candidate snapshot.
3. Precommit result owns a takeable candidate slot guarded until Apply or cleanup consumes it.
4. Successful generation publish immediately installs effective config and matching source state as one adoption operation.
5. Effective no-op installs a new accepted source through the ordinary outcome path without changing generation.
6. Shutdown first drains admitted attempts, then detaches and closes the active owner, and includes that finalization in idle completion.

## Error Handling

| Failure | Result and cleanup behavior |
|---|---|
| Linux support evidence unavailable | Valid bootstrap read may proceed with explicit unavailable baseline; runtime comparison fails closed as source integrity. |
| Same accepted physical identity has changed bytes | Existing non-atomic source-integrity result; retain active owner. |
| Candidate/target read or precommit work fails | Preserve existing source/config error; close uncommitted candidate and join/report cleanup error without replacing primary result. |
| Post-swap StartPublished or PhasePublish error/panic | Preserve Published generation/source truth; cache phase failure, clear publishing, stop further starts, and report bounded cleanup diagnostics. |
| Bootstrap, inspection, or validation cleanup fails | Return cleanup error joined with the primary failure where one exists; do not construct a runtime observer for one-shot paths. |
| Shutdown close/finalizer fails | Cache one finalization result, notify waiters once, return/aggregate through the caller context, and allow safe remaining host cleanup. |

## Testing Strategy

- **Configsource unit and contract tests**: supported and unavailable capability seams; regular-file, byte-limit, same-handle identity/size/time checks; reopened target metadata; raw no-op versus changed same-source rejection; invalid/closed provenance; concurrent borrow/close, one close, cached close error, and descriptor-baseline recovery.
- **Mandatory Linux filesystem certification**: rewrite atomic_recycle_test.go with exact linux && configsource_cert build tags and tests TestFixedSource_PinnedAtomicRecovery and TestFixedSource_PinPreventsAcceptedInodeReuse. On positively supported ext4, perform 1,000 rejected-candidate/recovery rename cycles, require every recovery eligible, prove the accepted inode is not reused while pinned and its identity remains unchanged. No skip, sleeps, or retry-until-pass.
- **Runtimehost lifecycle tests**: raw no-op, rejection, cancellation, precommit panic, effective no-op, Published receipt, post-adoption panic, and cleanup of only uncommitted slots.
- **Publication tests**: manager callback panic after asserting pointer swap; production PhasePublish hook panic/error; assert Published result, active generation, matching effective/source baseline, cached ledger error, publishing cleared, no retry, and later quiesce/close.
- **Shutdown tests**: channel barriers for initially-idle and admitted attempts; live borrow; concurrent WaitForIdle callers; prompt context deadline; completion/panic notification exactly once; repeated BeginShutdown/Complete/Abandon; final release and retry against cached cleanup errors.
- **Bootstrap/one-shot tests**: decode/validation/panic, nil effective, build/bind rollback, successful adoption, afterBind failure, PrepareInspect/InspectRoutes/InspectInventory, ValidateStructural, ValidateDistribution, FixedSource.LoadEffective/LoadEffectiveFromPath success/error/cancellation/panic/close-error release, and mechanical loader/test caller ownership.
- **Resource and diagnostics checks**: descriptor bound of active pin + candidate pin + target-validation handle, one transient mountinfo handle closed before bytes; source descriptors return to baseline; preserve existing result/label vocabulary and verify path/secret/panic text is absent.
- **Portable delivery checks**: ordinary unsupported-Linux lifecycle tests remain untagged; Windows/macOS package builds. Task 1.1 adds `go test -count=1 ./internal/infra/configsource/... ./internal/infra/runtimehost/...` to the existing native Windows CI test variant when test scope is true. Task 1.3 verifies its passing result on the published source revision matching the working Go tree; cross-compilation does not supply native identity/reload evidence. Linux quality gate uses explicit writable TMPDIR. Ubuntu CI alone provisions and cleans its isolated RUNNER_TEMP ext4 loop fixture.

## Security, Performance, and Integration Notes

- Configuration handles are read-only. Capability discovery does not mutate filesystem metadata or inspect raw devices.
- Diagnostics use bounded reason categories and contain no source path, raw close error, panic value, or source contents.
- Host-owned source reads hold at most one active pin, one candidate pin, and one target-validation file. Capability detection may hold one transient mountinfo descriptor, closed before reading candidate bytes. An explicit external borrower may extend a displaced pin lifetime.
- The local Linux quality gate requires an explicit writable TMPDIR on positively supported storage and never mounts host storage. The Ubuntu test job creates a disposable ext4 loop filesystem only beneath RUNNER_TEMP and always removes only the fixture it created.
- Preserve the existing 100 modified-Go-file source-change cap and normal precommit hook. Do not bypass mandatory hooks or split T1 into a normal commit that leaves the ABA failure or ownership lifecycle incomplete.

## Delivery Sequence and Task Sizing

- **1.1 T1 — Complete ownership integration and essential regressions, 6–9 hours.** One indivisible implementation task spanning configsource, runtimehost, runtimebundle, essential tests, certification script, quality gate, Ubuntu certification and native Windows test invocation. Work in 1–3 hour checkpoints for source primitives, all consumers, then shutdown/gates and regressions; checkpoints are not standalone normal commits. T1 ends only after mandatory hooks, supported-filesystem certification, and relevant consumer checks pass.
- **1.2 T2 — Adversarial race/error coverage, 2–3 hours, after 1.1.** Add deterministic barriers, ownership-transfer panic/close injection, and race coverage across source/state/coordinator/host. This essential acceptance coverage is not optional.
- **1.3 T3 — Platform and delivery certification, 1–3 hours, after 1.1 and 1.2.** Verify the focused native Windows CI invocation supplied by 1.1, Windows/macOS builds and portable tests, final tagged Linux certification/race evidence, support-boundary and TMPDIR documentation, diagnostics and gates. A draft prerequisite PR carrying the reviewed 1.1/1.2 commits provides the native executor before 1.3 completion. T3 changes documentation and records verification; it does not introduce uncommitted Go or workflow changes requiring a new native run. Mark 1.3 complete only after the native test step passes for the published source tree matching the working Go tree, then retain normal latest-head CI requirements after the documentation/task-state commit. Concrete code defects return to the integration owner for a reviewed normal repair commit and fresh native evidence.
