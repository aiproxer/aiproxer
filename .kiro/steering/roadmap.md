# Roadmap (Steering)

Owner-ranked product goals. `VISION.md` overrides this file on any conflict; vision items are direction, specs and PRs still ship small slices.

## Ranked goals

1. **Public open-source release.** The billing split and the AIProxer rebrand are complete; the remaining work is the necessary release validation. Existing release commitments stand (#398 with #348, #389, #394, #641); retain #394 capacity certification as measurement of the release candidate plus fixes for demonstrated blockers, not the full speculative hardening backlog.
2. **Quota-aware key/account rotation.** Deliver a working end-to-end implementation, including actual OAuth-account switching.
3. **Claude subscription model connector (#822).** Delivered: the owner-approved opt-in, local-only Claude Agent SDK model connector, through the completed and archived `claude-agent-model-bridge` spec. The release record in `docs/claude-agent-model.md` carries the narrow maintainer legal/policy sign-off and the documented final-head live-acceptance limitation. This frees the third active goal slot.

## Parked goals

- **Cross-vendor usage and transcript reporting.** Parked on owner decision; the third active goal slot is now free. No new work starts beyond completing already-committed closeout.

## WIP rule

At most **3 active product goals**. The limit counts goals, not sequential release PRs.

Exceptions (no slot consumed): demonstrated regressions, security/credential isolation, fabricated or lost history, hidden degradation, and direct release blockers. Speculative hardening sweeps, advisory lint, new verification infrastructure, and diagnostic enhancements without a blocking defect wait.

## Competing work

- **Proxy Memory:** completed and archived (durable Enterprise terminal handoff and the B1-B9 local certification matrix). No further work; task 2.4 stays deferred by owner decision D2.
- **Other open PRs:** merge worthwhile, substantially completed changes after normal review. Park the rest without follow-on campaigns.
- **Weekly metric:** manually count goal-advancing merged PRs divided by all merged PRs. No automation.
