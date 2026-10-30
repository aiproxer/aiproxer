# Stub-gateway certification harness (test-only)

Reproduces the recorded real-runtime gateway certification for the
`claudeagentacp` connector's `gateway` credential source against the exact
pinned dependency closure (see
`.kiro/specs/archive/claude-agent-acp-gateway/research.md` for the byte digests). The harness
never talks to any third-party gateway: the stub below is this repo's
project-owned test asset.

## Run

```sh
# 1) Point at a pinned, operator-installed runtime holding the sidecar next
#    to bin/aip-backend-claudeagentacp (see docs/claude-agent-acp-backend.md).
#    The env dir must contain:
#      AIP_GATEWAY_CERT_RUNTIME/node_modules/@agentclientprotocol/claude-agent-acp
#    The shipped sidecar is staged and digest-verified before execution.
# Run from the repository root. Missing runtime or unsupported platform fails.
AIP_GATEWAY_CERT_RUNTIME=/path/to/install \
  make claude-gateway-certify
```

The Go test launches `stub-gateway.mjs` on a loopback port and
`driver-gw.mjs` against the shipped sidecar, then asserts the certification
rows: capability advertisement, successful `authenticate` with the exact
payload, gateway-routed streamed turn, fresh admission pass, one-use replay
refusal, and secrets/non-endpoint leakage scans.

Excellent manual reproduction (no Go needed): start the stub gateway with
`GW_PORT=... GW_LOG_FILE=... node stub-gateway.mjs`, then
`GW_ADAPTER_ROOT=... GW_SIDECAR=... GW_PORT=... GW_LOG_FILE=... node driver-gw.mjs`.

Nothing here runs in normal CI: without `AIP_GATEWAY_CERT_RUNTIME` the test
skips itself. The shipped sidecar bytes stay pinned and certified elsewhere.
