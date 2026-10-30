# Task 9.3 external native security evidence blocker

## Status

> [!warning] DEPRECATED
> Resolved on 2026-07-28. Local `make backend-plugin-security-checks` passed, Ubuntu release gates run `30382809655` passed, Codex Linux race run `30382811954` passed, and cross-platform run `30382807401` passed on SHA `e796998cc8f7095bce65e450211cb1aef3b76def`. Darwin connector IPC remains deliberately unsupported/fail-closed and unclaimed. Task `9.3` is complete. The remainder of this file preserves the historical blocker contract.

Local Task 9.3 threat-model hardening, adversarial tests, fuzz targets, and
`make backend-plugin-security-checks` were implemented on this Windows host.

Task validation still requires native evidence that cannot be honestly claimed
from Windows-only runs:

```text
make backend-plugin-security-checks test-fuzz test-race
```

- `make test-race` on Windows skips the Go race detector (repo policy /
  `scripts/race-check.ps1`). It must not be reported as race-clean.
- Linux `SO_PEERCRED` same-UID wrong-process / generation binding under `-race`
  needs an observed green Ubuntu (or equivalent) run for the SHA carrying this
  work.
- Darwin approved peer-cred local IPC is intentionally **fail-closed** today
  (`processhost/channel_darwin.go` → `ReasonUnsupportedChannel`). That is the
  accepted control until a Darwin native profile is implemented and evidenced;
  it is not mTLS-weakening fallback.

## What was added for CI evidence

- `make backend-plugin-security-checks` (adversarial + threat-model docs +
  bounded `FuzzManifest` / `FuzzServerFrame`).
- QA workflow step wiring `make backend-plugin-security-checks`.
- Race/fuzz nightly continues to own Linux `test-race` + deeper fuzz; security
  fuzz targets are included via the new Makefile target and/or nightly
  `test-fuzz` extensions.

## Why 9.3 stays unchecked

Until Linux race/security CI (or an equivalent Ubuntu agent run) has been
observed green for the same SHA that carries this hardening, and Darwin remains
documented fail-closed without false “approved profile” claims, Task 9.3 must
remain unchecked. Do not claim cross-platform race or Darwin peer-cred evidence
from Windows-only local runs.

## Human decision (2026-07-27)

Local macOS execution is skipped because no macOS host is available. This is an
approved local validation waiver only; implementation semantics (including
Darwin fail-closed peer-cred) are unchanged. After PR push, CI remains the
source of native Linux race/security evidence and any future Darwin-native
observations. This blocker stays open; do not check task `9.3` until required
external evidence is observed for the reviewed SHA.
