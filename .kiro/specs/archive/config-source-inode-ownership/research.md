# Research and Design Decisions

## Summary

- **Feature**: `config-source-inode-ownership`
- **Discovery Scope**: Brownfield extension with cross-package lifecycle integration; conversion is grounded in the reviewed brief and existing code paths, with no new domain research.
- **Key Findings**:
  - Linux source identity currently includes device, inode, and statx birth time, while accepted active state retains only identity and digest. Releasing the accepted descriptor allows inode reuse before a recovery attempt.
  - A retained read-only descriptor keeps the accepted ext4 inode allocated for the lifetime of the active baseline. This supports a lifetime guarantee without treating birth time or an inode-generation ioctl as uniqueness proof.
  - Ownership crosses startup, reload publication, coordinator bookkeeping, one-shot validation, and shutdown. The manager changes its active pointer before post-publish work runs, so source adoption and callback failure handling must preserve the already-committed publication.

## Research Log

### Linux source lifetime and support boundary

- **Context**: The accepted source can be unlinked and its inode reused after a rejected candidate, causing the next recovery replacement to repeat the previous identity.
- **Sources Consulted**: Reviewed proposal in `brief.md`; `internal/infra/configsource/fixed_source.go`, `classify.go`, and `identity_linux.go`; Linux VFS and ext4 lifetime references listed below.
- **Findings**: The current Linux scheme is `statx-btime`; `ReadStable` closes its opened handle after each read and separately stats the path. A live descriptor prevents VFS inode teardown and ext4 inode-number reuse until release. The observed regression and focused experiment are recorded in the brief.
- **Implications**: The accepted Linux baseline must own a live pin. Linux lease support is restricted to positive identification of the ext4 kernel driver; the scheme does not claim that other filesystems violate the lifetime invariant.

### Startup, reload, and shutdown integration

- **Context**: A correct source pin must survive the whole host lifecycle and be transferred with the effective configuration that it describes.
- **Sources Consulted**: `internal/infra/runtimebundle/bootstrap_effective.go`, `effective_load_contract_test.go`, `inspect.go`, `validate_structural.go`, `validate_distribution.go`, `reload_host.go`; `internal/infra/runtimehost/reload_state.go`, `attempt_gate.go`, and `shutdown.go`; reviewed lifecycle contracts in `brief.md`.
- **Findings**: Startup effective loading currently returns metadata only. `Host` contains both coordinator state and a second `activeSource` copy. One-shot loaders share the same startup seam. `WaitForIdle` currently observes only the active attempt, while source finalization must also be awaited. A direct external bootstrap-loader caller exists in `effective_load_contract_test.go` and must explicitly release the new owner.
- **Implications**: Bootstrap rollback, one-shot consumption, reload outcome cleanup, committed adoption, and shutdown finalization must be described as one ownership contract. Metadata clones remain non-owning.

### Publication boundary

- **Context**: A panic in publication-only work occurs after the new generation has become active.
- **Sources Consulted**: `internal/infra/runtimehost/manager.go`, `internal/infra/runtimebundle/resource_ledger.go`, and `internal/infra/runtimebundle/reload_host.go`, plus the exact callback and adoption contract in `brief.md`.
- **Findings**: `Manager.Publish` owns the generation pointer swap; `ResourceLedger` tracks a separate `PhasePublish` lifecycle and caches completion state; host reload state currently applies the completed outcome after runner completion. A post-swap panic cannot safely be reported as a precommit failure or used to discard the active generation.
- **Implications**: Publication callback failure is isolated after the swap, ledger publishing state is finalized and broadcast, and a prevalidated callback-free source adoption runs immediately after successful publication before fallible attempt bookkeeping.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Decision |
|---|---|---|---|---|
| Retain accepted read-only descriptor | Keep the accepted ext4 inode alive while it is the active baseline | Uses inode lifetime directly; prevents reuse without mutating the file | Holds one descriptor per active baseline; only positively verified ext4 receives the Linux lease scheme | Selected |
| Use inode generation as identity | Add filesystem generation metadata to the source identity | Low descriptor cost | No trustworthy read-only capability check; ext4 may permit mutation or omit dependable generation behavior | Rejected |
| Treat filesystem magic as proof | Enable support from `fstatfs` magic alone | Simple detection | Does not distinguish ext2/ext3/ext4 drivers; can overstate the tested boundary | Rejected |
| Generalize Linux lease to every filesystem | Assume all Linux filesystems preserve inode allocation while referenced | Broader availability | The lifetime evidence in this proposal is scoped to the inspected ext4 implementation | Rejected; unsupported Linux runtime fails closed |
| Background close worker or finalizer | Complete lease closure asynchronously | Could move blocking work away from callers | Adds goroutine/finalizer ownership and unbounded cleanup timing | Rejected; close is synchronous, borrow-counted, and context-bounded only while waiting |
| Treat post-publish panic as publish failure | Propagate callback panic through the precommit error path | Simple error flow | Contradicts the active generation pointer and can dispose a committed source baseline | Rejected; preserve Published truth and report bounded cleanup diagnostics |

## Design Decisions

### Decision: Keep the accepted ext4 inode alive

- **Context**: The active identity can repeat after the prior accepted inode is released and reused.
- **Alternatives Considered**: Trust birth time; use inode generation; retain the accepted read-only file descriptor.
- **Selected Approach**: Retain one read-only owner for the accepted source, use the `linux-ext4-dev-ino-lease-v1` provenance scheme for positively verified ext4 handles, and require a live matching baseline pin for Linux runtime comparison.
- **Rationale**: The handle lifetime prevents ext4 inode-number reuse while the baseline is active and avoids mutating the source or depending on coarse birth time.
- **Trade-offs**: The host retains one descriptor. A direct external borrower may delay displaced-pin closure.
- **Follow-up**: The mandatory ext4 lane verifies the lifetime property over 1,000 rejected-candidate/recovery cycles.

### Decision: Identify the ext4 driver conservatively

- **Context**: Filesystem magic alone does not identify the kernel driver.
- **Alternatives Considered**: Magic-only detection; pathname-prefix mount lookup; broad Linux fallback; handle-bound mount identification and bounded mount-table parsing.
- **Selected Approach**: On the same open regular-file handle, require `statx` mount-ID support and a matching entry in `/proc/self/mountinfo`, bounded to 1 MiB, with one matching mount ID, device major/minor, filesystem type exactly `ext4`, and corroborating `fstatfs` magic. Keep the handle open throughout detection.
- **Rationale**: This verifies the driver and the file's mount in the process mount namespace without mutating configuration or inspecting raw storage.
- **Trade-offs**: Missing, malformed, ambiguous, truncated, unsupported, or detached mount evidence makes Linux runtime lease support unavailable. Valid startup configuration still loads through the existing bounded read.
- **Follow-up**: No host mount provisioning is part of this spec; the Ubuntu CI job owns and removes only its isolated ext4 loop fixture.

### Decision: Give the source one close authority

- **Context**: Copies of `ActiveSourceVersion` currently carry metadata but cannot preserve inode lifetime or safely coordinate close with comparison.
- **Alternatives Considered**: Copy an owning wrapper; expose the file descriptor; retain raw file descriptors; use a private synchronized core with borrowed access.
- **Selected Approach**: A `SourceLeaseOwner` owns the `*os.File`; `ActiveSourceVersion` has a private core reference and exposes only validated, non-owning borrows. Owner handoff uses take-and-clear slots. `RequestClose` rejects new borrows and closes once the borrow count reaches zero; `Close(ctx)` waits only through the supplied context and repeated closure returns the cached result.
- **Rationale**: A single close authority prevents metadata copies from releasing a live baseline and makes concurrent final release deterministic.
- **Trade-offs**: The owner API and transfer slots must be mechanically threaded through bootstrap, one-shot, reload, and shutdown paths. No background goroutine, finalizer, unbounded wait, or retry of `close(2)` is introduced.
- **Follow-up**: Tests cover forged provenance, concurrent final close, cached errors, and file-descriptor reuse hazards.

### Decision: Adopt source state at the publication truth boundary

- **Context**: Publication swaps the manager's active pointer before `StartPublished` callback work and later attempt bookkeeping.
- **Alternatives Considered**: Adopt source state after all bookkeeping; treat callback panic as precommit failure; prevalidate adoption and install it before fallible bookkeeping.
- **Selected Approach**: Validate the adoption target and prepare the source/effective update, metadata copy, Published result, and receipt before publish. After successful `Manager.Publish`, perform only a plain committed-flag assignment and generation-ID accessor before the callback-free `ReloadState.adoptPublishedSource` handoff. Store the displaced owner in the receipt before unlocking. Committed recovery completes the same adoption idempotently and returns the prebuilt Published result.
- **Rationale**: The source baseline follows the manager's irreversible generation swap and cannot be disposed by later cleanup or bookkeeping panic.
- **Trade-offs**: The adoption primitive is deliberately narrow and must not allocate, invoke callbacks, validate capabilities, decode, normalize, or return an error while holding state lock.
- **Follow-up**: Adversarial tests inject post-swap manager and ledger-hook panic as well as post-adoption coordinator panic.

### Decision: Finalize source closure as part of shutdown idleness

- **Context**: Closing the baseline before an already-admitted reload borrows it can invalidate that attempt; returning idle before finalization completes can strand waiters.
- **Alternatives Considered**: Close at `BeginShutdown`; let `WaitForIdle` observe only attempt count; register one shutdown-only finalization callback and include it in the idle predicate.
- **Selected Approach**: Keep the baseline alive through the admitted attempt, claim finalization once after the last outcome cleanup or immediately when initially idle, execute the close callback outside the gate lock, and let `WaitForIdle` wait for both no active attempt and completed finalization.
- **Rationale**: Attempt admission precedes source borrow, and shutdown must preserve both order and context-bounded waiting.
- **Trade-offs**: The gate gains explicit finalizing/completion state and an open notification channel during initially-idle finalization.
- **Follow-up**: Barrier tests cover initially-idle and active-at-shutdown paths, multiple waiters, deadline, callback panic, and final borrower release.

### Decision: Keep T1 as one integrated implementation task

- **Context**: The source owner is not safe to commit independently of its startup/reload/shutdown consumers, while a consumer-only change leaks or disposes the pin.
- **Alternatives Considered**: Split source primitives and host integration into separate normal commits; keep one integration task with short internal checkpoints.
- **Selected Approach**: Keep T1 as a single 6–9 hour indivisible task, with 1–3 hour checkpoints for source primitives, all consumers, then shutdown/gates and essential regressions. T2 and T3 remain dependent follow-up tasks.
- **Rationale**: A normal implementation commit must cure the ABA failure and all essential ownership paths together.
- **Trade-offs**: T1 is an explicit exception to the usual 1–3 hour task size. Its checkpoints are not separate normal commits.
- **Follow-up**: Keep the repository's 100 modified-Go-file source-change cap and mandatory hooks unchanged.

## Risks & Mitigations

- **The process mount namespace cannot positively identify the source mount** — fail closed for Linux runtime comparison while preserving valid startup loading.
- **A retained or externally borrowed pin delays descriptor release** — cap host-owned read descriptors, use borrow-counted close, and return to the descriptor baseline after all owners release.
- **Regular-file close cannot be interrupted by context** — context bounds waiting for outstanding borrows; the close syscall remains synchronous and is never retried.
- **A panic occurs after generation publication** — isolate the callback, finalize the resource ledger, adopt prevalidated source state before fallible bookkeeping, and preserve Published truth.
- **A shutdown callback panics or cleanup fails** — recover and cache the finalization result, close the notification once, and report only bounded path-free lifecycle diagnostics.

## References

- `brief.md` — reviewed problem evidence, support boundary, ownership transaction, shutdown/error contracts, AC-01–11, and mandatory certification plan.
- `internal/infra/configsource/fixed_source.go`, `classify.go`, `identity_linux.go` — current fixed-source identity, bounded-read, target-validation, and atomic classification paths.
- `internal/infra/runtimebundle/bootstrap_effective.go`, `inspect.go`, `validate_structural.go`, `validate_distribution.go`, `reload_host.go` — shared bootstrap and host/one-shot composition paths.
- `internal/infra/runtimehost/reload_state.go`, `attempt_gate.go`, `attempt_runner.go`, `manager.go`; `internal/infra/runtimebundle/resource_ledger.go` — reload state, admission, publication, and lifecycle paths.
- [Linux VFS inode lifetime](https://raw.githubusercontent.com/torvalds/linux/v6.12/fs/inode.c) — inode references and eviction.
- [ext4 inode freeing order](https://raw.githubusercontent.com/torvalds/linux/v6.12/fs/ext4/ialloc.c) — inode number release after inode teardown.
- [`unlink(2)` open-file lifetime](https://man7.org/linux/man-pages/man2/unlink.2.html) — unlink does not invalidate an open file description.
- [`statx(2)`](https://man7.org/linux/man-pages/man2/statx.2.html) and [`proc_pid_mountinfo(5)`](https://man7.org/linux/man-pages/man5/proc_pid_mountinfo.5.html) — mount ID and per-process mount information used by the bounded support check.
- [ext4 ioctl implementation](https://raw.githubusercontent.com/torvalds/linux/v6.12/fs/ext4/ioctl.c) — why inode-generation ioctl is not adopted as uniqueness evidence.
