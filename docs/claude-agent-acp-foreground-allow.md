# Foreground-allow certification: blocked

Issue [#1223](https://github.com/aiproxer/aiproxer-enterprise/issues/1223)
is **not complete**. `foregroundAllowCertifiedProfile` remains empty and
`permission_policy: allow` still rejects before runtime execution. Do not set
the constant from the positive tool rows alone.

## Candidate and topology

The candidate adds bounded, finite input schemas for Read/Grep/Glob, native
`toolCall.name` recognition, conflicting/subagent identity refusal, unique
one-time option selection, and connector-owned managed ask rules for all three
tools. Deny session metadata is unchanged. Startup retains the policy in the
resolved-profile disclosure.

The probe uses the real adapter, SDK, bundled CLI, sidecar, AuthEnv, deployment
resolver, startup identity/policy gates, and permission handler. The service's
unset enabling constant is **not** relaxed: a scratch product-level relay
evaluates the unenabled candidate, not an admitted production instance.

Linux/amd64 user/mount/PID/network namespaces isolate a synthetic host and a
loopback TLS provider. Synthetic administrator paths and credential stores
replace the host topology only for this test. There is no external provider
traffic, real login, billed inference, or assertion that real administrator
authority is absent. This is not a sandbox promised to production operators.

| Component | Observed identity |
| --- | --- |
| Adapter / Agent SDK / bundled CLI | 0.89.1 / 0.3.293 / 2.1.293 |
| Complete upstream closure SHA-256 | `2c70d058987bf4de9043c05685b6f311b8cdce214735a0e43eeec66f5e81232f` |
| Node | tested with 22.23.4; admission requires >=22, not an exact version or digest |
| Three-file sidecar SHA-256 | `96edb09c1a0b0346b328463ac5a8d63e657e3131813528956f492ce023ec81d0` |

The closure digest is the one this probe observed. The current certified
closure carries patched transitive libraries and a different tree digest; the
adapter, SDK and bundled-CLI bytes are unchanged. Current identity and install
recipe: [Claude Agent ACP backend](claude-agent-acp-backend.md#exact-pinned-identity).

## Observations

| Case | Observed result |
| --- | --- |
| Earlier tools-only allow metadata | Read executed without any permission callback; cannot enforce allow-input validation |
| Managed ask rules plus valid Read/Grep/Glob | Each requested permission; handler selected offered `allow-once`; real tool result reached the loopback provider |
| `Read` with `run_in_background: true` | Runtime schema rejected the unexpected parameter; no callback or file content |
| Bash under the candidate tool set | Runtime rejected the disabled tool; no callback or command execution |
| Read `/proc/self/environ` | Runtime rejected device-file access; this does not establish general credential protection |
| Read synthetic host-login `.claude/.credentials.json` outside the workspace | **Failed confidentiality check:** handler approved; tool result contained synthetic access/refresh tokens and reached the provider |

The credential counterexample is an ordinary, well-formed foreground Read.
Read-only does not mean credential-safe. String/shape validation and forcing a
callback are insufficient to distinguish authorized workspace content from
credential stores. A display-title filter or a short credential-filename
denylist is not an adequate filesystem-access policy.

## Reproduction and remaining work

The local evidence collection is
`~/.cache/aip-tmp/opencode/foreground-allow-1223/`: `candidate_test.go`,
`overlay.json`, `isolate.sh`, `probe.py`, per-case `rpc.json`, `result.json`,
and `gate.json`. The relay is compiled with `go test -overlay=... -c` in the
Claude connector module; `unshare --user --map-root-user --mount --pid --fork
--net --mount-proc sh .../isolate.sh` starts the isolated probe. Paths in these
scratch scripts identify the locally retained pinned installation; no runtime
packages are installed or redistributed. Evidence is local, not a public CI
artifact. Initial nonmatching-installation failures and the timed-out broad run
are retained; setup failures are not behavioral evidence.

Enablement requires an approved filesystem-access boundary that prevents
credential disclosure, including resolution/alias escape considerations,
followed by certification of that implementation and the complete required
negative/control matrix. Production service admission, live-account smoke,
remote race evidence, administrator-policy interactions and cancellation proof
are not certified by these positive tool rows. No bypass mode or persistent
approval is enabled by this change.
