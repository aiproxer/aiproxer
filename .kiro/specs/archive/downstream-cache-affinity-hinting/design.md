# Design Document

## Overview

One opt-in standard feature fills an empty canonical `PromptCacheKey` from the admitted authoritative session. Three existing serializers learn to forward PCK, and the OpenAI-compatible family forwards it only for provider profiles that declare a new quirk.

```text
plugins.features: [{id: downstream-cache-affinity, enabled: true}]
        |
        v  StandardBundle().Features -> featureDownstreamCacheAffinity
existing PlaneAttemptTransforms (canonical path only)
        |  fill-only: PCK empty && AuthoritativeSessionID set
        v
Call.PromptCacheKey = "aipca1_" + b64url(SHA-256(domain || BackendID || 0 || sessionID))
        |
        v  existing request hooks / admission / AdaptCallForCandidate / session scrub
serializer
  openairesponses.ParamsForCall  -> ResponseNewParams.PromptCacheKey         (direct + compat)
  openailegacy.ParamsForCall     -> ChatCompletionNewParams.PromptCacheKey   (direct + compat)
  openaicompat.Open{Chat,Responses} clears it unless profile quirk openai.prompt_cache_key
  openresponses protocol encode  -> already forwards (unchanged)
```

Core, SDK contracts, `execbackend.Backend`, `AttemptMeta`, `runtimebundle`, `featurehost`, and the backend-plugin protocol are untouched.

## Goals

- Stable locality hint for clients that send no cache key.
- Explicit client PCK always wins.
- Zero cost when the feature is off.
- No new core, SDK, protocol or composition surface.

## Non-Goals

See requirements **Deferred**. In particular: no per-backend capability metadata, no keyed derivation, no connector work, no metrics, no wire-path synthesis.

---

# 1. Feature package

```text
internal/plugins/features/downstreamcacheaffinity/
├── transform.go        // ID, Transform, Derive
└── transform_test.go
```

The package imports only the standard library, `pkg/lipapi` and `pkg/lipsdk/{request,hooks}`.

## 1.1 Derivation

```go
const (
    ID              = "downstream-cache-affinity"
    GeneratedPrefix = "aipca1_"
    GeneratedLength = 50 // 7 + base64.RawURLEncoding.EncodedLen(sha256.Size)
    transformOrder  = 1000 // after existing standard transforms (all currently 0)
    domain          = "aiproxer/downstream-cache-affinity/v1\x00"
)

// Derive returns the hint for one backend and authoritative session.
func Derive(backendID, sessionID string) string {
    h := sha256.New()
    h.Write([]byte(domain))
    h.Write([]byte(backendID))
    h.Write([]byte{0})
    h.Write([]byte(sessionID))
    return GeneratedPrefix + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
```

**Why unkeyed SHA-256 is sufficient.** `AuthoritativeSessionID` is 32 bytes from `crypto/rand` (`internal/core/securesession/app/ids.go:16,40`). A 256-bit random preimage cannot be guessed or enumerated, so an HMAC key adds no secrecy. Dropping the key removes the secure-session root plumbing entirely and makes the value stable across restarts and across proxy nodes, which a process-local or root-derived key would not guarantee. The domain prefix and the NUL separators keep the value unlinkable to any other hash of the session ID.

Namespace is `AttemptMeta.BackendID` (configured backend instance ID, `internal/core/runtime/executor_attempt_transform.go:33`). It is always set, stable per config, and separates providers so two upstreams cannot correlate a user by hint.

## 1.2 Transform

```go
type Transform struct{}

func (Transform) ID() string                     { return ID }
func (Transform) Order() int                     { return transformOrder }
func (Transform) FailureMode() hooks.FailureMode { return hooks.FailOpen }

var cont = request.AttemptDecision{Kind: request.AttemptContinue}

func (Transform) HandleAttempt(_ context.Context, call *lipapi.Call, meta request.AttemptMeta, _ request.Services) (request.AttemptDecision, error) {
    if call == nil {
        return cont, nil
    }
    if pck, err := call.PromptCacheKeyValue(); err != nil || pck != "" {
        return cont, nil // explicit wins; conflicts are reported by Call.Validate / serializers
    }
    sid := strings.TrimSpace(meta.Session.AuthoritativeSessionID)
    backend := strings.TrimSpace(meta.BackendID)
    if sid == "" || backend == "" {
        return cont, nil
    }
    call.PromptCacheKey = Derive(backend, sid)
    return cont, nil
}
```

The transform never excludes a candidate and never reads client hints, A-leg, scope, workspace, trace or model. It has no state, so parallel and failover attempts are independent by construction; the executor already gives each candidate its own `attempt` copy.

## 1.3 Registration

Follow the `featureAgentLoopGuard` / `featurePartsNoop` pattern in `internal/standardplugins/features_install.go`:

```go
func featureDownstreamCacheAffinity(n yaml.Node) (lipfeature.FeatureBundle, error) {
    if err := requireEmptyFeatureYAML(downstreamcacheaffinity.ID, n); err != nil {
        return lipfeature.FeatureBundle{}, err
    }
    cs := lipfeature.NewContributionSet()
    if err := lipfeature.Contribute(cs, lipfeature.PlaneAttemptTransforms, downstreamcacheaffinity.ID,
        []request.AttemptTransform{downstreamcacheaffinity.Transform{}}); err != nil {
        return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", downstreamcacheaffinity.ID, err)
    }
    return lipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}
```

and add `{ID: downstreamcacheaffinity.ID, Factory: featureDownstreamCacheAffinity}` to `StandardBundle().Features` (`internal/standardplugins/standard_table.go:141`). Absent or `enabled: false` rows never call the factory, so the plane has no occupant and the large-payload gate (`internal/core/largebody/authority_gate.go`, `eligibility.go`) keeps today's eligibility. Generation reload recomposes features as for every other standard feature; no extra lifecycle is needed.

## 1.4 Large-payload trade-off

`PlaneAttemptTransforms` is `RequestBodyCanonicalRequired` (`pkg/lipsdk/feature/plane_manifest.go:538-540`). Enabling the feature therefore sends every request through the canonical path. That is the intended, documented opt-in cost. V1 adds no bypass and no wire-path synthesis (Deferred).

---

# 2. Serializers

## 2.1 Direct OpenAI Responses and Chat

In `internal/plugins/backends/openairesponses/invoke.go:ParamsForCall` and `internal/plugins/backends/openailegacy` `ParamsForCall`:

```go
pck, err := call.PromptCacheKeyValue()
if err != nil {
    return zero, fmt.Errorf("<pkg>: %w", err)
}
if pck != "" {
    p.PromptCacheKey = openai.String(pck)
}
```

Both SDK params expose typed `PromptCacheKey param.Opt[string]` (openai-go v3.66.0). No untyped JSON override and no length check: OpenAI rejects over-long keys before output, which is already an explicit pre-output failure.

## 2.2 OpenAI-compatible family gate

`internal/plugins/backends/openaicompat/invoke.go` `OpenChat`/`OpenResponses` call the shared `ParamsForCall` (lines 24, 43). After 2.1, they would forward PCK to every compatible upstream, including arbitrary custom ones whose support is unconfirmed and which may reject the field (§2.4.1). Add one field to `InvokeRequest`:

```go
ForwardPromptCacheKey bool
```

and clear the field when it is false (`p.PromptCacheKey = param.Opt[string]{}`). The compatible backend sets it from one constructor input threaded through `BuildCompatibleWithHeaders` (already receives profile headers, `internal/standardplugins/provider_profile_binding.go:240-247`). Plain `custom-*-compatible` construction passes `false`.

## 2.3 Profile quirk

`internal/providerprofiles/schema.go` already has a bounded, family-checked quirk list (`QuirkID`, `quirkAllowed`, lines 81-87, 490-493). Add:

```go
QuirkOpenAIPromptCacheKey QuirkID = "openai.prompt_cache_key"
```

allowed for `FamilyOpenAIChat` and `FamilyOpenAIResponses` only. `quirkAllowed` (`schema.go:496-500`) is currently a single boolean expression over one quirk; adding a second quirk for a different family set requires turning it into a switch, or the new quirk fails validation as `unknown quirk`. The two profile family builders pass `slices.Contains(profile.Profile.Quirks, QuirkOpenAIPromptCacheKey)` to the compatible constructor. The value flows from the compiled profile resolved by the existing marker-aware `wrapCompatibleLifecycle` (`provider_profile_binding.go:304`), never from the generic YAML node, so no lifecycle change is needed.

## 2.4 Catalog

Add `"quirks": ["openai.prompt_cache_key"]` to the `mistral` and `deepinfra` rows (both Chat family) in `internal/providerprofiles/catalog.json`. No Responses-family row qualifies, so `FamilyOpenAIResponses` remains an accepted but unused validation surface in V1. Other fields stay byte-identical.

## 2.4.1 Why the set is exactly these two rows

`research.md` carries the full evidence table. The rule is narrow on purpose: the vendor must document `prompt_cache_key` on the surface this family's serializer writes, **and** must recommend session-scoped granularity. Providers meeting only the first condition are excluded because the design derives a per-session value.

- `meta` documents the field but says the opposite about granularity: "Don't over-partition: unique keys per user or per session lower hit rates."
- `fireworks` documents session affinity and cache isolation for different purposes; the cited guide does not confirm `prompt_cache_key` on the catalog row's Responses surface. Isolation prevents cross-key sharing, not caching within each key, and is not inherently worse for all workloads.
- `groq` names `prompt_cache_key` under "Unsupported Features".
- `deepseek-*` and `alibaba*` lack confirmed documentation of this request field and session-scoped fit on their catalog surfaces. DeepSeek's `prompt_cache_hit_tokens` is response telemetry, not a request carrier or proof that PCK is unsupported.

Undocumented providers default to off, so the gate is closed by construction. The selective survey does not audit all 137 eligible rows; automatic caching or absence from a guide does not prove lack of key support.

Because `ParamsForCall` is shared with the compatible family (§2.2), an unquirked row receiving the field may reject the request. Only an actual rejection before output follows normal pre-output error classification; retry/failover depends on policy. The default-off gate avoids unverified request-field forwarding without claiming universal rejection. Native Groq PCK rejection was not directly observed; earlier DeepSeek-model rejection reports concern Azure/Volcano gateways, not native DeepSeek.

## 2.5 Unchanged paths

- `internal/plugins/protocols/openresponses/encode.go:59-63` already forwards PCK for OpenResponses-compatible upstreams; that is an existing contract (OpenResponses defines `prompt_cache_key`).
- Executable connectors receive PCK through the existing semantic carrier and ignore it unless they read it. The Codex connector derives its own key (`connectors/codex/internal/codex/attempt.go:50`) and is unaffected.
- Anthropic and Gemini backends do not read PCK.

---

# 3. Authority and privacy

- The transform sees the raw session ID only through the existing `AttemptMeta.Session` view and writes only the hash.
- The final candidate session scrub before `be.Open` is unchanged and still clears every raw session field.
- The feature emits no logs or metrics, so nothing sensitive can leak through them.
- Residency, keep-warm and continuation consume backend-observed evidence only; nothing in this design writes to them.

---

# 4. Tests (per testing.md proportionality)

| Seam | Test |
|---|---|
| Derivation | fixed vector; length 50; URL-safe alphabet; backend and session separation |
| Transform | table: explicit PCK kept; semantic-extension PCK kept; alias conflict untouched; no session → none; client hint only → none; generated otherwise |
| Registration | enabled → one occupant; absent / `enabled: false` → zero occupants; non-empty config rejected |
| Integration | one standard-runtime test: authoritative session + fake backend → exact value at `Open`, session fields empty |
| Direct serializers | Responses and Chat: explicit, empty, conflict |
| Compat gate | profile with quirk forwards; same family without quirk and plain custom row omit |
| Schema | quirk accepted for both OpenAI families, rejected elsewhere |

No new architecture checks, TCK package, benchmarks or census. Existing generic arch rules already cover feature→core import direction.

---

# 5. Validation

| Check | Result |
|---|---|
| Core vs plugin ownership | Policy in feature; serializers own wire; core unchanged |
| Canonical neutrality | Reuses existing `Call.PromptCacheKey` |
| Streaming-first | No stream change |
| Provider SDK leaks | None into core |
| Post-output retry | Unchanged |
| Proportionality | Reuses `PlaneAttemptTransforms`, standard feature table, profile quirks; adds one SDK-free package and one bool |

Estimated size: ~150 lines production Go, ~250 lines tests, under 15 files.
