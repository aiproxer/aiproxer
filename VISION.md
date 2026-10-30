# AIProxer Product Vision

> **Owner-ratified. This document overrides any conflicting statement elsewhere in the repository**, including agent instructions, steering, specs, docs, and code comments. Agents read it before making design, review, or scope decisions. Agents never edit it; they propose changes in a PR description for the owner to decide.
>
> **Vision, not inventory.** It describes where the product is going. It does not claim that every capability below already exists. For current behaviour, see `README.md` and `docs/`.

## Open-core model

AIProxer follows an **open-core** model:

- **AIProxer**: the free, open-source, MIT-licensed core.
- **AIProxer Enterprise**: a closed-source commercial add-on that builds on, and integrates with, the open-source core.

The move to the MIT licence happens together with the rebranding and the split into two products. Until then, the repository `LICENSE` file stays authoritative for the current code.

Naming:

| | Open-source core | Commercial add-on |
| --- | --- | --- |
| Product name | AIProxer | AIProxer Enterprise |
| Package / slug | `aiproxer` | `aiproxer-enterprise` |

The open-source core is always just `aiproxer`, never `aiproxer-community`, `aiproxer-oss`, or similar. Code identifiers, paths, and the Go module still carry the pre-rebrand names until the `aiproxer-complete-rebranding` spec completes. After that, no trace of any former project name remains in the repository.

**Boundary rule.** AIProxer is complete and genuinely useful for an individual user or a single operator. AIProxer Enterprise adds **organisational** control: multiple users, central governance, organisational billing and policy, compliance, and the management GUI. The open-source core is never deliberately limited to push users toward Enterprise.

## AIProxer: product vision

### Mission

A **universal compatibility layer**: connect any client or agent that speaks a popular LLM API to any popular type of inference backend. AIProxer translates and normalises between APIs on the fly wherever needed.

Users can bring their existing subscriptions (OpenAI, Anthropic, Cursor, Meta AI, xAI/Grok, Google Gemini) to the agents of their choice. This frees them from vendor-specific tooling.

### Key capabilities

**Freedom of model and routing**
- Static or dynamic custom model routing.
- Mixing several models in one session, even when their backends use incompatible API types.
- Overriding model names and routes that client tools hard-code, so users keep their freedom of choice and can reuse their subscriptions.

**Accounts and subscriptions**
- Multiple accounts and API keys with automatic rotation. Users define OAuth accounts and API keys once and reuse them without switching manually. A key is used until it is exhausted; when it hits its quota, the next one takes over seamlessly, without disrupting the session, surfacing errors, or breaking the agent loop.
- Usage-window warm-up: control when the first request opens a vendor usage window (for example a 5-hour session window), so that window timing matches the user's working pattern.

**Session reliability**
- Automatic prevention of agent-loop breaks.
- Automatic, in-flight repair of invalid tool calls.
- Intelligent steering: refocus a model that has lost focus or been distracted, and correct obvious errors before they waste time and tokens on unproductive sessions.
- Model-specific steering: targeted fixes for issues tied to particular models or model families.

**Safety**
- Automatic detection and blocking of dangerous tool calls.
- Prevention of secret leaks (for example API keys) through in-flight redaction.

**Efficiency**
- Token usage compression.
- Keeping provider caches warm, to avoid usage wasted on cache misses.

**Memory**
- Agent memory at the proxy level that works across sessions, agents, and devices, so memory is no longer tied to one agent or one machine.

**Visibility**
- Cross-vendor transcript logging.
- Cross-vendor usage logging.
- Usage reporting, including subscription-based plans and multi-account setups.

**Control**
- Interactive, in-session control of proxy behaviour, for fine-grained customisation without leaving the session.

## AIProxer Enterprise: product vision

Vendor-independent control, billing, and deployment of LLM usage across an organisation.

### Governance and administration
- A web GUI for managers.
- Automated provisioning and management of API keys and LLM access, with SSO, SAML, RADIUS, and similar identity integrations, so keys never have to be distributed or managed by hand.
- Access control for specific models or model families, per group or individual user.
- Usage policies with enforced limits: budgets per organisational unit, department, team, and individual user.
- Live and historical usage reporting, with charts, tables, breakdowns, filters, and aggregations.
- Central storage of session transcripts, with search.

### Cost reduction
- **Pareto routing**: choose models along the best time / cost / quality trade-off, according to configurable user preferences.
- Automated in-session slicing: route different parts of a session to different models for the best cost/quality balance.
- **Least-cost routing (LCR)**: when several providers can serve a request, try the cheapest first, with no client-side configuration changes.

### Security ("Agent Shield")
- GDPR / PII redaction.
- Usage-policy enforcement: an independent "judge" checks whether a session complies with the organisation's approved AI-usage policy.
- Abuse prevention for public-facing agents: prompt-injection prevention, context-poisoning prevention, and prevention of users deliberately pushing agents into misconduct.
- Tool-call firewall: allow lists and deny lists for tool use.
- Modality enforcement: restrict allowed input modalities, to block injection attempts that go beyond text (for example adversarial payloads hidden in images or voice instructions).

## Decision principles

<!-- vision-digest:begin -->
**Lines never crossed**
1. **No fabricated history.** Never present a model with a conversation that did not happen: never reorder model-authored content, never invent turns.
2. **No leakage.** No secret or credential leaks across the client boundary, or between users and tenants.
3. **No hidden degradation.** Every degradation is explicit: logged, observable, and configurable by the operator.

**Priorities when goals conflict (highest first)**
1. **Interoperability and session continuity.** When a backend cannot represent a request exactly, the default policy is to serve it with explicit, honest degradation rather than refuse it. Operators can switch to a strict policy that refuses instead. Never break a user's session or agent loop when a safe alternative exists.
2. **Fidelity.** Preserve the client's intent and content as exactly as the target backend allows.
3. **Efficiency.** Cost, tokens, and latency.
4. **Simplicity of the core.** Prefer the smallest mechanism that serves the priorities above.

**Applying them**
- A change that narrows which clients, backends, or combinations work must name the priority above that justifies it, or be escalated to the owner.
- Vision items are direction, not committed scope. Specs and PRs still ship small slices.
<!-- vision-digest:end -->
