# Downstream cache affinity: built-process acceptance

Run from the repository root, with Python 3 and the repository's Go toolchain — the
`toolchain` directive in `go.mod`, which must be a security-patched release:

```sh
mkdir -p /var/tmp/aip
GOTOOLCHAIN="$(awk '$1 == "toolchain" { print $2 }' go.mod)" TMPDIR=/var/tmp/aip \
  python3 scripts/cache-affinity-process-e2e.py --evidence /absolute/external/new-directory
```

The evidence directory must not exist. No provider credentials are needed or read:
the child receives an allowlisted environment and emulator-only authentication.
The script builds **actual `cmd/aipstd`**, starts it as a child, waits for HTTP
readiness, sends public HTTP client requests, and captures independent provider
HTTP traffic and returning JSON replies. It terminates and waits for the child
and shuts down the emulator even on assertion failure. No in-process executor,
fabricated canonical session, or generated-key injection is used.

The build overlay replaces embedded catalog data only: actual Mistral and
DeepInfra entries keep all semantics, quirks, and native namespace policy, with
only their endpoint authority redirected to loopback. Their original `/v1` and
`/v1/openai` paths remain. Synthetic Responses copies opt into the same quirk;
the actual catalog opts into this feature only for Chat. A synthetic profile
without the quirk and ordinary custom-compatible Chat/Responses rows prove the
wire gates. Synthetic profiles are test fixtures, not production support claims.

Sessions are created by secure admission and reused by round-tripping the public
`X-AIP-Session-Id`, `X-AIP-Resume-Token`, and `X-AIP-A-Leg-Id` response carriers.
Resume deliberately does not issue a new token. Each request must return the
provider's reply and cause exactly one provider capture. Captures are checked
against all issued session/token/A-leg values before persistence. Evidence includes
build/spawn argv, environment, PID, binary hash, overlays, outbound JSON/headers,
reply checks, and checksums. The only stored bearer header is an emulator fixture.

| Claim | Source | Rejection probe |
| --- | --- | --- |
| Same admitted session/backend has a stable opaque key; different sessions differ | Feature transform contract and acceptance request | `--mutant` omits the transform's assignment in a build overlay |
| Explicit client key survives public HTTP translation | Acceptance request; transform's fill-only contract | Real HTTP request with `client-owned-key` |
| Default-off omits generated keys | Standard feature opt-in contract | Run without a feature row |
| Unsupported profiles/custom backends omit all keys | Serializer/profile quirk gate | No-quirk profile and unmarked custom rows |
| Raw session credentials never reach provider | Secure-session boundary | Compare every outbound body/header with issued carriers |

For non-vacuity, rerun with `--mutant` and a separate new evidence directory.
It must fail on `missing generated feature contribution`, not build/startup failure.
Production source is never mutated: both binaries use the same checkout, and the
mutant changes a temporary overlay file only. A normal rerun must pass before
this test is considered ready for review.

## Owning frontend regression

The initial real-process run reaches Mistral and returns the reply, proves stable
generated keys on resume and different keys for independently admitted sessions,
but failed explicit-key preservation: the Chat frontend did not lift
`prompt_cache_key` into the canonical call. The provider sees a generated key
instead of `client-owned-key`. Both owning decoders now use typed string fields
and map the value into canonical `PromptCacheKey`, keeping it out of provider
extra-body extensions. Numeric keys receive the existing invalid-JSON decode error.
Responses keys decline wire proof and use canonical decode rather than disappearing
in its wire fast path. Compact owning regressions cover these cases.

The matrix runs seven backend cases in both enabled and default-off processes,
with four requests each: create, resume, independent session, and explicit key.
Resume and explicit requests carry actual issued session values in **both** body
metadata and headers; outbound captures must omit both carrier keys and all raw
values. No live-provider or race claims. Full passing acceptance evidence is
distinct from independent review approval.
