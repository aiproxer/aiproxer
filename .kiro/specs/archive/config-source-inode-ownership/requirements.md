# Requirements Document

## Project Description

Go LLM Interactive Proxy operators need a fixed configuration source to remain recoverable after an atomic replacement candidate is rejected. On Linux, the current device/inode/birth-time identity can be repeated after the accepted inode is released and reused. The system shall retain the accepted source's physical identity for its active lifetime on positively supported Linux mounts, preserve startup loading and existing Windows/non-Linux behavior, and keep reload publication, cleanup, and shutdown outcomes coherent.

## Boundary Context

- **In scope**: Fixed-source startup and runtime reads; accepted-source identity and lifetime; candidate classification; host reload publication and shutdown; operator inspection and validation loads; supported-filesystem certification and its required CI invocation.
- **Out of scope**: New configuration semantics, new SDK result categories or metric labels, filesystem-wide uniqueness claims beyond the stated Linux support boundary, and host or Incus mount provisioning.
- **Boundary ownership**: Observable behavior of fixed configuration loading, runtime reload, operator validation, and shutdown. Internal component ownership is committed in `design.md`.
- **Revalidation triggers**: Changes to reload result vocabulary, publication semantics, config-source integrity behavior, supported platform behavior, or the Linux certification gate.

## Requirements

### Requirement 1: Linux accepted-source identity lifetime (AC-01)

**Objective:** As a proxy operator, I want the accepted Linux configuration source to remain distinguishable while it is active, so that a rejected candidate does not make a later atomic recovery look like an in-place edit.

#### Acceptance Criteria

- **1.1** While a Linux configuration source is the active baseline, the system shall treat a later candidate as the same physical source only when matching live source proof remains available; it shall not rely on birth time or inode-generation metadata as a substitute for that proof.
- **1.2** If matching live source proof is unavailable, forged, closed, or inconsistent with the active baseline, then the system shall fail runtime comparison with the existing source-integrity outcome.

### Requirement 2: Conservative Linux filesystem boundary (AC-02)

**Objective:** As a proxy operator, I want Linux lease support enabled only when the filesystem driver is positively identified, so that unsupported or ambiguous mounts fail safely without blocking valid startup configuration.

#### Acceptance Criteria

- **2.1** Where Linux runtime lease support is enabled, the system shall require positive evidence that the open configuration handle belongs to the ext4 kernel driver in the process mount namespace; shared filesystem magic alone shall not establish support.
- **2.2** If the required handle-bound mount evidence is unavailable, malformed, ambiguous, truncated, unsupported, or inconsistent, then the system shall report the runtime lease as unavailable rather than guessing support.
- **2.3** When a valid configuration is read at startup on Linux without positively available lease support, the system shall preserve the existing bounded startup-load behavior; when a runtime comparison later requires that unavailable lease, the system shall fail closed with the existing source-integrity outcome.

### Requirement 3: Existing platform behavior (AC-01)

**Objective:** As an operator on a supported non-Linux platform, I want configuration loading and reload behavior to remain compatible with the existing platform identity scheme.

#### Acceptance Criteria

- **3.1** When the system runs on Windows, it shall preserve the existing `win-fileid` identity and runtime reload behavior without requiring the Linux lease.
- **3.2** When the system runs on another platform with an existing adapter, it shall preserve that adapter's current behavior; this feature shall not claim new identity uniqueness guarantees for it.
- **3.3** When the system runs on a currently unsupported platform, it shall preserve the current unsupported behavior.

### Requirement 4: Stable reads and atomic replacement classification (AC-03)

**Objective:** As a proxy operator, I want bounded configuration reads to reject torn or in-place changes and recognize an identical source as a no-op.

#### Acceptance Criteria

- **4.1** When the system reads a source candidate, it shall require a regular file, enforce the configured byte limit, and reject a read whose source identity, size, or available stability metadata changes during the read or target validation.
- **4.2** If the candidate changes bytes on the accepted physical source, then the system shall reject it as a non-atomic update.
- **4.3** When candidate identity and digest match the active baseline, the system shall classify the read as a raw no-op; when the candidate is a different eligible physical source, the system shall permit normal candidate evaluation.

### Requirement 5: Single close authority and safe borrows (AC-04)

**Objective:** As a runtime owner, I want source handles to have one close authority and bounded non-owning access, so that metadata copies cannot release the active source or race its close.

#### Acceptance Criteria

- **5.1** When callers copy active source metadata, the system shall not grant those copies authority to close the accepted source.
- **5.2** While a source comparison is in progress, the system shall keep the validated active and candidate source proofs usable for the full comparison; if either proof is missing, forged, mismatched, closing, or closed, the system shall fail closed.
- **5.3** When source closure is requested concurrently or repeatedly, the system shall close the owned handle at most once, reject later borrows, and return the cached completion result without retrying close.

### Requirement 6: Bootstrap and one-shot ownership (AC-05)

**Objective:** As a host integrator, I want every accepted startup source to be transferred or released on every load path, so that startup and inspection do not leak source handles.

#### Acceptance Criteria

- **6.1** When bootstrap loading fails after opening a source, or host construction fails before ownership transfer completes, the system shall release that source and preserve the primary failure together with any cleanup failure.
- **6.2** When host construction succeeds, the system shall retain exactly one active source owner with its matching effective configuration and comparison metadata; when post-bind work fails, it shall close the resulting host and report joined cleanup errors.
- **6.3** When one-shot effective loading, inspection, structural validation, or distribution validation finishes effective loading, the system shall release its source before subsequent registry, tracing, or plugin work and shall retain no source owner in the returned result.

### Requirement 7: Reload publication and source adoption (AC-06)

**Objective:** As a runtime owner, I want the active generation and accepted configuration source to change together, so that publication status and source recovery remain consistent across errors and panics.

#### Acceptance Criteria

- **7.1** When a candidate is a raw no-op, rejected, cancelled, or fails before publication commits, the system shall retain the existing active source owner and release only the uncommitted candidate owner.
- **7.2** When an effective no-op accepts a different source without publishing a generation, the system shall transfer that source owner once while preserving the active generation.
- **7.3** When generation publication commits, the system shall keep the published generation, effective configuration, source metadata, and live source owner aligned; post-publication start or bookkeeping panic shall not change the outcome from Published or dispose the adopted source.
- **7.4** When publication or coordinator bookkeeping returns or releases an uncommitted outcome, the system shall close only an uncommitted unused source slot; a committed adoption receipt shall not grant cleanup authority over the active baseline.

### Requirement 8: Shutdown finalization and idle waiting (AC-07)

**Objective:** As an operator stopping the host, I want admitted reloads to finish using their accepted source before shutdown closes it, and I want idle waiting to reflect source finalization.

#### Acceptance Criteria

- **8.1** When shutdown begins with an admitted attempt, the system shall keep the active source usable until that attempt completes or abandons its outcome, then request source closure exactly once.
- **8.2** When shutdown begins while idle or after the last admitted attempt completes, the system shall keep waiters blocked until source finalization completes or their supplied context expires.
- **8.3** When finalization succeeds or panics, the system shall notify all remaining waiters exactly once; repeated shutdown or attempt completion shall not rerun finalization or close a notification more than once.
- **8.4** When host shutdown reaches its context deadline while an attempt or live borrow remains, the system shall return promptly and leave final source cleanup armed to complete on attempt completion or final borrow release.

### Requirement 9: Cleanup errors and secret-safe outcomes (AC-08)

**Objective:** As an operator, I want cleanup failures reported without losing the original reload or publication outcome or exposing raw internal error data.

#### Acceptance Criteria

- **9.1** When closing a candidate, target, bootstrap, one-shot, displaced active source, or deferred shutdown source fails, the system shall preserve the applicable primary outcome, aggregate errors where required, and report runtime cleanup through the existing bounded lifecycle vocabulary.
- **9.2** When publication-start work returns an error or panics after the generation is active, the system shall preserve Published status, clear publishing state, avoid retrying partially started work, and leave the committed generation eligible for normal quiesce and close.
- **9.3** When source-integrity or cleanup diagnostics are reported, the system shall use existing result and label vocabulary and shall not expose raw close errors, panic values, source paths, or configuration contents in status, metrics, history, or safe logs.

### Requirement 10: Descriptor bounds and leak recovery (AC-10)

**Objective:** As an operator, I want source ownership to have a bounded descriptor cost and to return to baseline after repeated reload and shutdown activity.

#### Acceptance Criteria

- **10.1** While the host performs a serialized source read, it shall retain at most one active pin, one candidate pin, and one target-validation file; capability detection may use one transient mount-information handle that is closed before candidate bytes are read.
- **10.2** When bootstrap, one-shot, reload, no-op, rejection, and shutdown cycles finish, the system's source-owned descriptor count shall return to its starting baseline, excluding a displaced pin held by an explicit external borrower.

### Requirement 11: Supported-filesystem certification and portable CI (AC-09, AC-11)

**Objective:** As a maintainer, I want the Linux inode-lifetime guarantee certified in a mandatory supported-filesystem lane while ordinary checks remain portable.

#### Acceptance Criteria

- **11.1** When the mandatory supported-filesystem certification runs, it shall perform 1,000 rejected-candidate/recovery rename cycles, require every recovery to be eligible, verify the accepted inode is not reused while pinned and its identity does not change, and fail on any skip, missing test, sleep-based synchronization, or retry-until-pass behavior.
- **11.2** When the local Linux quality gate runs, it shall invoke the tagged certification with an explicit writable `TMPDIR` and fail if supported storage or test discovery is unavailable; it shall not provision a host mount. When the Ubuntu CI test job runs, it shall provision an isolated ext4 loop fixture under `RUNNER_TEMP`, invoke the same certification, fail if provisioning, support detection, or test discovery is unavailable, and clean up only that job-owned fixture.
- **11.3** When ordinary default/precommit checks run, they shall remain portable and include deterministic unsupported-startup and lifecycle coverage; Windows/macOS builds and ordinary platform tests shall remain in the delivery checks.
