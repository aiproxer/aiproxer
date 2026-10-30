# ACP Runtime Deduplication Phase 3.1 Race-Evidence Note

- **Status:** external validation limitation recorded at closeout; no ACP implementation failure was observed.
- **Local limitation:** Windows `GOWORK=off go test -race ./...` cannot start because the Windows cgo toolchain exits before tests run.
- **Linux evidence:** strict Linux workflow [31265106123](https://github.com/matdev83/go-llm-interactive-proxy/actions/runs/31265106123) failed on unrelated root-module findings: data races in `internal/refclient/openresponses` and a timeout-sensitive `tools/backendplugin` test. The workflow did not report a failure in `connector-support/acp`.
- **ACP evidence:** canonical ACP unit tests, seed fuzz tests, three 30-second ACP fuzz campaigns, RPC-error tests, executable ACP parity, and CLI ACP parity all passed at the merged closeout head.
- **Follow-up:** run `GOWORK=off go test -race ./...` from `connector-support/acp` on a Linux host when dedicated external-module race evidence is required. This follow-up is independent of the completed ACP deduplication implementation.
