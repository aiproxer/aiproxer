# Research — Go core stage four: advanced feature extension platform

Spec name: `go-core-stage-four-feature-extension-platform`

## Research question

What advanced Python LIP behaviors make the proxy materially more useful for agentic coding workflows, and what architectural seams does the Go rewrite need so those behaviors can be added later as plugins **without further core changes**?

## Baseline observations

### Current Go baseline

The Go repo already has the right *core* direction:

- canonical `lipapi`
- routing and continuity
- a runnable server distribution
- bundled frontend/backend plugins
- a feature plugin mechanism
- existing hook types:
  - submit hook
  - request-part hook
  - response-part hook
  - tool reactor

That is a good start.
It is not yet the complete extension platform needed for the Python feature set.

### Current gap

The current Go hook surface is strongest for:

- early request rejection/annotation
- localized request mutation
- localized response mutation
- tool lifecycle reaction

The Python feature set needs more than that:

- whole-session context
- workspace resolution
- private auxiliary requests
- request-wide history shaping
- completion buffering / replacement
- traffic/capture legs
- state with TTL
- authn/authz principal propagation

## Advanced Python LIP feature inventory

Below is the feature inventory that matters for future migration planning.

### A. Request/context shaping features

| Feature | What it improves | Needed seam(s) |
|---|---|---|
| Auto append first prompt | injects startup context without changing the client | session opener, submit hook, request transform |
| Dynamic outbound rewrite | adjusts user/model-bound prompts before upstream send | request transform, request part hook |
| Stale tool-output compaction | reduces context bloat from superseded tool results | request transform |
| Dynamic tool-output compression | reduces cost from large remaining tool outputs | request transform; aux request later if model-assisted compression is added |
| ProxyMem recall/injection | cross-session memory and context carry-over | session opener, request transform, state store, aux request |
| Secret redaction before upstream send | prevents accidental secret leakage to providers | request transform, request part hook |
| Context-window enforcement | keeps requests within safe context budget | request transform |
| User-submit hooks | per-request project/user custom shaping | submit hook, request transform |

### B. Tool policy and workspace safety features

| Feature | What it improves | Needed seam(s) |
|---|---|---|
| Allowed/disallowed tool names | limits tool exposure and protects turns | tool catalog filter, tool reactor |
| Dangerous command protection | blocks destructive commands and returns steering | tool reactor, workspace view, state store |
| File sandboxing | blocks file-changing operations outside workspace boundary | workspace resolver, tool reactor |
| Project root discovery | gives safety policies a shared workspace boundary | workspace resolver |
| Steering generators | e.g. prevent full test suite / guide command choice | tool reactor, state store |
| Dynamic tool-call rewrite | rewrite or soften dangerous tool actions | tool reactor |
| Developer-tool exemptions | allows safe wrappers/linters while blocking dangerous ops | tool policy data + tool reactor |

### C. Response shaping and control features

| Feature | What it improves | Needed seam(s) |
|---|---|---|
| Dynamic inbound rewrite | post-processes backend response before client sees it | response part hook, completion gate |
| Think-tag cleanup | stream-safe response normalization | response part hook |
| Auto continue/proceed removal | removes low-value mechanical continuations | response part hook or completion gate |
| Quality verifier with inline recall | secondary model can steer or replace a bad completion in the same request | completion gate, aux request, state store |

### D. Reliability/orchestration features

| Feature | What it improves | Needed seam(s) |
|---|---|---|
| Pre-output failover / B2BUA behavior | hides recoverable upstream failures | already core-owned |
| Routing of auxiliary requests | lets verifier/memory/etc. use different route roles | aux request, route roles, route hints |
| Hybrid/planning helper flows | future multi-model workflows | aux request, route roles, completion gate as needed |

### E. Identity/security features

| Feature | What it improves | Needed seam(s) |
|---|---|---|
| SSO-based user authentication | shared/multi-user operation with identity | transport auth, principal propagation |
| Principal-aware policy | different tools/routes/features by user/role | principal view, tool catalog filter, route hints, observers |
| Access-mode-sensitive controls | later multi-user safety policy | transport auth + principal + policy plugins |

### F. Observability/evidence features

| Feature | What it improves | Needed seam(s) |
|---|---|---|
| Four-leg usage accounting | tracks original vs mutated traffic and backend billing | traffic observers |
| Usage statistics generation | operational visibility and cost analysis | traffic observers |
| Session text capture | debugging/audit trail | traffic observer or capture sink |
| CBOR wire capture | exact evidence for debugging and replay | privileged capture sink |
| Secret-aware exports | prevents observability from leaking secrets | redaction boundary |

## Why the current Go seams are insufficient on their own

### Existing seams that are good and should stay

- submit hook: good for early reject/annotate
- request part hook: good for local part edits
- response part hook: good for local stream/event edits
- tool reactor: good for tool-event enforcement and rewrite

### Missing seams

- no dedicated session-start/context stage
- no request-wide history transform stage
- no tool catalog filter stage before model exposure
- no completion-wide gate for buffered replacement
- no shared aux-request service
- no plugin state store
- no workspace resolver service
- no four-leg traffic observer/capture contracts
- no transport-auth contract in the HTTP layer
- no explicit redaction boundary for observers

## Core architectural conclusion

The next stage should **not** migrate one production Python feature after another.

It should first create the **stable seams** that those features need, because otherwise each migrated feature will force a special case into core and erase the reason for the rewrite.

## The minimum seam set required

The following seam set is the smallest one that credibly supports the advanced Python feature family:

1. session opener
2. request transform
3. tool catalog filter
4. route hint provider
5. completion gate
6. aux request service
7. plugin state store
8. workspace resolver
9. traffic observer
10. privileged capture sink
11. transport auth/provider
12. redaction boundary

## Proof plugins that best validate the seam set

The most efficient proof set is:

1. auto-append reference plugin
2. tool allow/block reference plugin
3. workspace guard reference plugin
4. traffic transcript/capture reference plugin
5. verifier stub reference plugin

That proves all the hard cases without prematurely building production-grade versions of every Python feature.

## Migration guidance

When a future feature request arrives, the team should first ask:

1. Is this request about identity or user access?
   - use transport auth / principal propagation
2. Is it about session/workspace initialization?
   - use session opener / workspace resolver
3. Is it about request-wide prompt/history shaping?
   - use request transform
4. Is it about tool exposure before model call?
   - use tool catalog filter
5. Is it about attempted tool execution?
   - use tool reactor
6. Is it about local stream/event cleanup?
   - use response part hook
7. Is it about whole-completion gating or replacement?
   - use completion gate
8. Does it need another model call?
   - use aux request service
9. Does it need memory across requests/sessions?
   - use plugin state store
10. Is it about metrics, transcripts, or captures?
    - use traffic observer / capture sink

If none of those fit, the seam is missing and should be added **before** the feature.
