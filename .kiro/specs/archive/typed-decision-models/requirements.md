# Requirements Document

## Project Description (Input)
Applications built on decision models (TypeSafe's "System One" family: Jev and compatible servers) call each vendor's typed decision API directly. AIProxer offers them nothing today: it has no decision endpoint, its canonical contract only models chat-style turns, and its only Jev integration is the fixed, metadata-only session classifier. Each application therefore integrates every decision provider separately and loses the proxy's authentication, routing, failover, usage evidence and billing.

AIProxer should accept the TypeSafe System One wire protocol (`POST /v1/systemone`), carry decision requests and typed results through its canonical execution path, and route them to TypeSafe and System One-compatible upstreams (including the OpenRouter and Command Code aggregator endpoints) with the same authentication, failover, usage and billing seams as ordinary inference, without fabricating or silently degrading decision semantics. Source: GitHub issue #804 and its research appendix.

## Introduction
A decision request carries one piece of shared evidence and a set of named, typed questions (`noul` yes/no, `choice`, `score`) and expects probability distributions back, not generated text. This slice makes AIProxer a drop-in `POST` endpoint for TypeSafe System One clients and routes those requests to any configured System One-compatible upstream.

## V1 Slice
- **Delivers**: a TypeSafe System One client pointed at AIProxer (base URL swap) can submit multi-question decisions and receive typed, validated answers from TypeSafe, OpenRouter's or Command Code's System One endpoint, or an operator-configured compatible server, with the proxy's client authentication, failover, usage evidence and billing admission applied.
- **First consumer**: TypeSafe SDK and plain-HTTP clients calling `POST /v1/systemone` through the standard distribution.
- **Safe degradation**: with no decision-capable backend configured, decision requests fail with an explicit error before any upstream call and all other traffic is unaffected; anything the proxy cannot represent faithfully is rejected before upstream I/O rather than approximated.

## Deferred
- OpenAI Decisions frontend (`POST /v1/decisions`) and backend, including per-question refusals and boolean choice values — follow-up: TBD
- Image and video evidence (Cloudflare Clef / Clef Flash, OpenAI inline images) and Clef state-truncation guarding — follow-up: TBD
- Databricks AI Decide translation adapter — follow-up: TBD
- TypeSafe-shaped `GET /v1/models` discovery (dedicated listener or virtual host) and decision models in the existing model list — follow-up: TBD
- Per-profile decision limits beyond the System One baseline (for example OpenRouter's smaller Jev context) and certified profiles for self-hosted Kev, Laya and Nimble — follow-up: TBD
- Secret-guard inspection and redaction of decision evidence (V1 rejects decisions while a guard is active) — follow-up: TBD
- Decision support in executable connectors (new backend-plugin ABI service) and in the existing `openrouter`, `commandcode-*`, `cloudflare` and `databricks` connectors — follow-up: TBD
- Large-body wire fast path, batch endpoints, streaming, and backend-specific extension fields (Laya `answer_confidence`, OpenRouter `provider`) — follow-up: TBD
- Per-question, per-image or per-evaluation tariff units — follow-up: TBD
- Decision-aware handling of authority output-token clamps (V1 excludes decision candidates under a clamp, fail closed) — follow-up: TBD

## Boundary Context
- **In scope**: System One request decoding and validation, typed decision answers, routing and failover among decision-capable backends, a System One-compatible backend family with catalog profiles, usage and cost evidence, billing admission through the existing seams, and explicit rejection of anything unsupported.
- **Out of scope**: translating decisions to or from chat/LLM protocols; synthesizing probabilities, confidence or refusals; changing the session-classification feature or its Jev client; changing chat frontends, chat backends or their discovery.
- **Adjacent expectations**: client authentication, decode admission and request-size limits behave as for other frontends. Upstream credentials are operator-configured environment references. Billing is active only when the host injects billing ports; stock `lipstd` stays non-billing.
- **Boundary ownership**: canonical decision semantics in the public canonical contract; System One wire handling in a new frontend plugin; upstream wire handling in a new compatible-family backend with catalog profiles; existing core routing, failover, usage and billing reused unchanged in behaviour.
- **Revalidation triggers**: canonical call and event contracts, capability negotiation, the secret-guard stage, request-size estimation for billing quotes, provider-profile families.

## Requirements

### Requirement 1: System One endpoint
**Objective:** As an application developer using a TypeSafe System One client, I want AIProxer to accept my existing decision requests unchanged, so that I can switch providers by changing only the base URL.

#### Acceptance Criteria
1. When an authenticated client sends a valid System One decision request, the AIProxer decision endpoint shall return a System One response whose `answers` are keyed by the request's question IDs, whose `model` names the upstream model that evaluated it, and whose `usage` reports upstream token counts.
2. The AIProxer decision endpoint shall deliver the request's evidence to the upstream with its JSON type and content unchanged, and shall preserve question IDs, choice option names and order, and score level order end to end.
3. If a decision request is malformed or outside the System One contract (missing required field, unknown question type, duplicate keys, a choice with no options or more than 255, a score with fewer than 2 or more than 10 levels, or a field the contract does not define), then the AIProxer decision endpoint shall reject it with HTTP 422 and a body naming the offending field, before any upstream call.
4. If a decision request exceeds the configured body, nesting-depth or question-count limits, then the AIProxer decision endpoint shall reject it before any upstream call with a protocol-legal error.
5. If a decision request is unauthenticated or unauthorized, then the AIProxer decision endpoint shall reject it with the same authentication outcome as the proxy's other frontends.

### Requirement 2: Faithful typed answers
**Objective:** As an application developer, I want every answer to be the upstream's real typed judgment, so that my thresholds and routing logic act on truthful probabilities.

#### Acceptance Criteria
1. When an upstream answers a decision, the AIProxer decision endpoint shall return for each `noul` question a probability in [0, 1]; for each `choice` question the selected option and a probability for every requested option; and for each `score` question the probability-weighted zero-based score, its legend and per-level probabilities.
2. If an upstream answer is missing, extra, names an unknown option or level, contains a non-finite or out-of-range value, or has a distribution that does not sum to 1 within round-off tolerance, then AIProxer shall treat that attempt as failed and shall not return a repaired or fabricated answer.
3. The AIProxer decision endpoint shall return a `confidence` value only where the upstream reported one, and shall never derive one.
4. The AIProxer decision endpoint shall omit upstream fields outside the System One response contract from the client response.

### Requirement 3: Routing to decision-capable upstreams
**Objective:** As an operator, I want decision requests routed and failed over only among upstreams that can serve them, so that clients get reliable answers without misrouting to chat models.

#### Acceptance Criteria
1. The AIProxer router shall route a decision request only to backends that declare decision support for the selected model, and shall return an explicit error before any upstream call when no such candidate exists.
2. When an attempt fails before any answer is returned to the client because of an upstream rate limit, overload, server error, timeout, invalid answer or upstream account error, AIProxer shall try the next eligible candidate within the route's attempt budget.
3. If an upstream rejects a decision request as invalid, then AIProxer shall return HTTP 422 to the client without trying another candidate.
4. Where an operator configures the TypeSafe, OpenRouter or Command Code System One profile, or a custom System One-compatible base URL with a bearer credential reference, AIProxer shall serve decision requests through that upstream.
5. The AIProxer router shall serve non-decision traffic unchanged whether or not decision-capable backends are configured, and shall reject any chat request routed to a decision-only backend before any upstream call.

### Requirement 4: Usage and billing
**Objective:** As an operator running billed or metered deployments, I want decisions accounted like other inference, so that usage and charges stay attributable and bounded.

#### Acceptance Criteria
1. When a decision attempt completes or fails after reaching an upstream, AIProxer shall record that attempt's upstream-reported input and output token counts, distinguishing reported zero from unreported usage.
2. Where an upstream reports a monetary cost for an attempt, AIProxer shall record it as provider-reported cost evidence for that attempt.
3. Where billing is enabled, AIProxer shall apply the existing credit screen and operational exposure admission to a decision request before any upstream call, quoting from the request's evidence and question size, and shall attribute all attempts of one inbound request to a single billing call.
4. Where billing is not enabled, AIProxer shall serve decision requests without customer charges, as it does for other inference.

### Requirement 5: Safety and privacy
**Objective:** As a security-conscious operator, I want decision traffic to respect the proxy's protection posture, so that enabling decisions does not open an unguarded egress path.

#### Acceptance Criteria
1. While a request secret guard is active for the request, AIProxer shall reject decision requests with an explicit policy error before any upstream call.
2. The AIProxer proxy shall not write decision evidence, instructions, criteria or answers to logs or metric labels by default.
3. The AIProxer proxy shall not forward client-supplied request headers or upstream routing, session or tracing controls to a decision upstream.
