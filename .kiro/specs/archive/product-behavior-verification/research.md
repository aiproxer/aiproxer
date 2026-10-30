# Research and Design Decisions

## Summary

- Feature: `product-behavior-verification`.
- Classification: brownfield, test tooling only.
- Audited source: `96858454a8c8274ffebb08d97f93e10cef4282fb` on `main`.
- Finding: the repository has strong unit, contract and in-process composition coverage, plus a single-scenario bash smoke of the spawned binary. Nothing routinely runs real client journeys through the built `lipstd` with honest pass accounting and a time limit.

## Source Evidence

| Ref | Source (pinned) | Finding |
| --- | --- | --- |
| S01 | [feature-stub-smoke.sh](https://github.com/matdev83/go-llm-interactive-proxy/blob/96858454a8c8274ffebb08d97f93e10cef4282fb/scripts/feature-stub-smoke.sh); [stubbackend](https://github.com/matdev83/go-llm-interactive-proxy/blob/96858454a8c8274ffebb08d97f93e10cef4282fb/tools/stubbackend/main.go); [scenario config](https://github.com/matdev83/go-llm-interactive-proxy/blob/96858454a8c8274ffebb08d97f93e10cef4282fb/testdata/feature-stub-smoke/tool-name-correction/config.yaml.tmpl) | The spawned-binary pattern already works. The config shape with `custom-*-compatible` backends pointed at a loopback stub is reusable. Its assertions are substring line counts, it covers one frontend, it is POSIX-only and it is not in any routine path. |
| S02 | [dogfood composition](https://github.com/matdev83/go-llm-interactive-proxy/blob/96858454a8c8274ffebb08d97f93e10cef4282fb/internal/stdhttp/dogfood_smoke_test.go); [CLI serve](https://github.com/matdev83/go-llm-interactive-proxy/blob/96858454a8c8274ffebb08d97f93e10cef4282fb/cmd/lipstd/command.go#L259-L301) | In-process `BuildHost` tests are valuable but skip the CLI's config loading, logging, management server, signals and shutdown. |
| S03 | [standard frontends](https://github.com/matdev83/go-llm-interactive-proxy/blob/96858454a8c8274ffebb08d97f93e10cef4282fb/internal/standardplugins/standard_contributions.go#L112-L120) | Six shipped frontend families: SystemOne, OpenAI Responses, OpenAI legacy, Anthropic, Gemini, OpenResponses. |
| S04 | [traceability](https://github.com/matdev83/go-llm-interactive-proxy/blob/96858454a8c8274ffebb08d97f93e10cef4282fb/internal/testkit/contract/traceability.go#L29-L93); [failover case](https://github.com/matdev83/go-llm-interactive-proxy/blob/96858454a8c8274ffebb08d97f93e10cef4282fb/internal/testkit/conformance/deployment_test.go#L247-L280) | The post-output no-retry claim is linked to a pre-output failover test. This is a real defect in test metadata. It is fixed separately from `main` (Deferred); V1's failure journeys give the process-level witness. |
| S05 | [taskrunner wait](https://github.com/matdev83/go-llm-interactive-proxy/blob/96858454a8c8274ffebb08d97f93e10cef4282fb/tools/taskrunner/runner.go#L277-L320); [POSIX adapter](https://github.com/matdev83/go-llm-interactive-proxy/blob/96858454a8c8274ffebb08d97f93e10cef4282fb/tools/taskrunner/process_posix.go) | After a kill, `waitForProcess` blocks on `cmd.Wait` without limit. A descendant in another process group that holds the pipes can stall past any deadline. `exec.Cmd.WaitDelay` bounds it. |
| S06 | [devcheck test events](https://github.com/matdev83/go-llm-interactive-proxy/blob/96858454a8c8274ffebb08d97f93e10cef4282fb/tools/devcheck/main.go#L355-L386) | Devcheck already parses `go test -json` pass, fail and skip events. Reconciling them against a required list is a small extension. |
| S07 | [runtime config reload](https://github.com/matdev83/go-llm-interactive-proxy/blob/96858454a8c8274ffebb08d97f93e10cef4282fb/docs/runtime-config-reload.md) | Reload is explicit: `POST /admin/config/reload` when `LIP_RELOAD_MANAGEMENT_ADDRESS` is set, or `SIGHUP` on Unix. The management API route is portable. |
| S08 | [delivery budgets](https://github.com/matdev83/go-llm-interactive-proxy/blob/96858454a8c8274ffebb08d97f93e10cef4282fb/.kiro/steering/delivery.md); [test policy](https://github.com/matdev83/go-llm-interactive-proxy/blob/96858454a8c8274ffebb08d97f93e10cef4282fb/.kiro/steering/testing.md) | A slice merges on its own, so do red-main failures. Test helpers stay obviously correct. Probes for relaxed assertions are not committed. Bounded real-stack sentinels are layer 5 of the test architecture. |

## Re-scoping Decision

The first draft of this spec (commit `533aa9446`) covered every delivered vision capability through 12 behaviour groups. It ran the full Claude ACP runtime on every change, used delegated cgroup v2 containment, added a crash-safe evidence chain with run IDs and supervisor finalization, enforced a capability catalog, routed the pre-commit hook through the gate, and rewired the CI and release graphs. The owner's review rejected that shape for practical reasons:

| Problem in the first draft | Consequence | V1 decision |
| --- | --- | --- |
| The gate stayed BLOCKED until all 12 tasks landed, and the known tool-result-elision conflict had to be recorded as failing. | The gate is red from day one, blocks every PR and release, and delivers no value until the end. | Each task merges green. Defects on `main` are fixed from `main` or quarantined with an issue, per `delivery.md`. |
| The pre-commit hook ran the up-to-ten-minute gate. | Every commit pays the whole budget. | Explicit command only; the hook is unchanged. |
| Delegated cgroup v2 was required; the maintainer's host reports `Delegate=no`. | Root setup on every host; Windows hosts can never run the gate. | No containment in V1, because the linked-only configs spawn no descendants. Plus the taskrunner `WaitDelay` fix. Containment is deferred with the executable-connector journeys. |
| The pinned Node ACP runtime was in the always-run baseline. | One connector's heavy third-party runtime taxes every change. | Deferred to an on-demand or nightly journey. |
| CI and release graphs were rewired and broad checks removed from the blocking path. | Cannot be verified while remote CI is billing-blocked, and loosens existing gates inside a testing spec. | Deferred until remote CI returns; V1 is `ci-stand-in` evidence. |
| An enforced capability catalog and mandatory negative controls applied to every feature. | A permanent per-feature tax and a new ratchet, against `testing.md`. | Deferred; an advisory report first. Non-vacuity probes are recorded in PRs and not committed. |
| Crash-safe evidence (run IDs, incomplete records, supervisor as final writer). | A large mechanism for a rare case. | The verdict comes only from the process that observed it; a killed run has no verdict. |
| The first draft marked its own approvals as true. | Only the owner approves. | Approvals reset. |

## Alternatives

| Option | Assessment |
| --- | --- |
| Extend the bash `feature-stub-smoke` | Rejected: substring assertions, POSIX-only, and hard to extend to multi-turn, fault and lifecycle journeys. |
| A separate stub-provider process (`tools/stubbackend`) | Rejected for V1: file-scripted and SSE-only, and fault and cancellation observation would need a control protocol. In-test `httptest` providers are simpler and still independent of the proxy's encoders. |
| Go tests that spawn the real binary, with in-test providers and devcheck reconciliation | **Selected:** portable, typed assertions, one helper set, and a cheap recipe for new journeys. |

## Risks

- **Wire fidelity of fake providers (medium):** a fake provider that accepts anything proves nothing. Each provider asserts the fields its journey depends on, and each critical journey records a non-vacuity probe.
- **Defects found on `main` (medium):** journeys may expose real bugs. Fix them from `main` first or quarantine with an issue. Never weaken the assertion.
- **Budget (low):** linked-only proxies start quickly, and the cold build of `lipstd` dominates. Task 7 measures cold and warm runs on Linux and Windows.
- **Flakiness (medium):** readiness and cancellation use real time. Use observed state and bounded polling, not sleeps, and give each journey its own proxy and ports.
