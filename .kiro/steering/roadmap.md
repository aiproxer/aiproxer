# Roadmap (Steering)

Owner-ranked product goals. `VISION.md` overrides this file on any conflict; vision items are direction, specs and PRs still ship small slices.

## Ranked goals

1. **Public open-source release.** Complete the billing split, rebranding, and necessary release validation. Existing release commitments stand (#398 with #348, #389, #394, #641); retain #394 capacity certification as measurement of the release candidate plus fixes for demonstrated blockers, not the full speculative hardening backlog.
2. **Quota-aware key/account rotation.** Deliver a working end-to-end implementation, including actual OAuth-account switching.
3. **Cross-vendor usage and transcript reporting.** Deliver a usable single-operator OSS capability without reintroducing Enterprise financial logic into OSS.

## WIP rule

At most **3 active product goals**. The limit counts goals, not sequential release PRs.

Exceptions (no slot consumed): demonstrated regressions, security/credential isolation, fabricated or lost history, hidden degradation, and direct release blockers. Speculative hardening sweeps, advisory lint, new verification infrastructure, and diagnostic enhancements without a blocking defect wait.

## Competing work

- **Proxy Memory:** freeze feature development. Preserve the completed implementation and existing branches. Finish only bounded corrections or closeout work that is already nearly complete.
- **Other open PRs:** merge worthwhile, substantially completed changes after normal review. Park the rest without follow-on campaigns.
- **Weekly metric:** manually count goal-advancing merged PRs divided by all merged PRs. No automation.
