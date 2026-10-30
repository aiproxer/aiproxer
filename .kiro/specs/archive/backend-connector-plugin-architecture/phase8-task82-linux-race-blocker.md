# Task 8.2 external Linux race blocker

## Status

> [!warning] DEPRECATED
> Resolved on 2026-07-28. GitHub Actions `Codex connector race` run `30382811954` passed the Ubuntu `GOWORK=off go test -race ./...` gate on SHA `e796998cc8f7095bce65e450211cb1aef3b76def`. Task `8.2` is complete. The remainder of this file preserves the historical blocker contract.

Local Task 8.2 repair criteria were implemented on Windows. The task validation line required:

```text
(cd connectors/codex && GOWORK=off go test -race ./...)
```

On this Windows host that command fails with a cgo toolchain error (`runtime/cgo: ... cgo.exe: exit status 2`). Repo policy already skips `make test-race` on Windows.

## What was added

- Workflow: `.github/workflows/codex-connector-race.yml`
  - Runner: `ubuntu-latest`
  - Exact step: `working-directory: connectors/codex` + `GOWORK=off go test -race ./...`
- Static guard: `internal/archtest/codex_race_ci_test.go` asserts that workflow text.

## Why 8.2 stays unchecked

Until that Ubuntu workflow (or an equivalent Ubuntu agent run) has been observed green for the same SHA that carries this repair, Task 8.2 must remain unchecked. Do not claim Linux race evidence from Windows-only local runs.
