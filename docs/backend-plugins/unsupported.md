# Unsupported Backend Connectors

This document records backend services and subscription bridges that are explicitly **unsupported-by-policy** in AIProxer due to the absence of public, third-party documented API contracts.

---

## `github-copilot` (GitHub Copilot Direct Bridge)

- **Status:** Unsupported-by-policy
- **Service Identity:** `https://api.githubcopilot.com`
- **Evaluation Date:** September 2026 (Task 8.2)

### Policy Rationale

GitHub Copilot does not publish or permit a third-party direct HTTP API for model inference. Direct access requires:
1. An internal, unpublished service-token exchange endpoint (`https://api.github.com/copilot_internal/v2/token`) that is not part of GitHub's public REST API catalog.
2. Hardcoded or spoofed editor client identifiers and private headers (`Editor-Version`, `Copilot-Integration-Id`, `Openai-Organization: github-copilot`).
3. Direct calls to `https://api.githubcopilot.com` which are restricted to first-party editor extensions.

Per AIProxer architectural guardrails and provider ethics:
- **No private reverse-engineering:** AIProxer does not integrate with unpublished `copilot_internal` endpoints or reverse-engineer editor-internal token flows.
- **No client spoofing:** AIProxer transmits truthful User-Agent and client identifiers; spoofing VS Code or other first-party clients is prohibited.
- **No ACP substitution:** An ACP connector (`connectors/acp`, `cursorcliacp`) must not be substituted for direct HTTP model bridges.

### Verified Public Documentation Baseline

The following official GitHub documentation resources were reviewed to confirm the absence of any supported third-party model inference contract:

- **GitHub REST API for Copilot:**
  <https://docs.github.com/en/rest/copilot>
  *Scope:* Enterprise and organization usage metrics (`/copilot/usage`) and seat management (`/orgs/{org}/copilot/billing/seats`). No inference or model-entitlement endpoints are documented.

- **GitHub OAuth Device Flow:**
  <https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow>
  *Scope:* General authorization for GitHub user identities. Produces a standard GitHub user token (`gho_...`), but does not document or authorize Copilot model token exchange.

- **GitHub Copilot Extensions:**
  <https://docs.github.com/en/copilot/building-copilot-extensions>
  *Scope:* Webhook-based extensions that run within GitHub's UI or supported IDEs; does not provide standalone model inference endpoints.

- **GitHub Models:**
  <https://docs.github.com/en/github-models>
  *Scope:* Developer prototyping with GitHub personal access tokens against `https://models.inference.ai.azure.com` (Azure AI infrastructure), which is distinct from the GitHub Copilot subscription service.

If GitHub publishes an official, supported third-party direct inference API for Copilot in the future, this policy decision can be revisited.

---

## `claude-subscription` / `anthropic-oauth` (raw Claude OAuth bridge)

- **Status:** Unsupported-by-policy, for the raw bridge only. Using your own Claude subscription is supported through the official Claude Code runtime (see below).
- **Service Identity:** `https://api.anthropic.com`
- **Evaluation Date:** October 2026

### What is supported

You can use **your own** Claude subscription (Free, Pro, Max, Team, or Enterprise plan) through the optional [`claudeagentacp` connector](../claude-agent-acp-backend.md). It runs the official, unmodified Claude Code runtime (via the Agent SDK), and sign-in completes through Anthropic's own flow: `claude auth login` on the host, or a token from the official `claude setup-token` command. Anthropic's Claude Code terms explicitly allow an end user to sign in to the unmodified Claude Code with their own subscription, and state that Pro and Max usage limits assume ordinary, individual use of Claude Code and the Agent SDK.

### What is not allowed, and how the proxy enforces it

A subscription's usage allowance belongs to its owner. It must never be resold, shared, or made available to other people or unrelated entities. Concretely:

1. **No sharing a subscription through the proxy.** A subscription-backed Claude backend serves only its owner. The `claudeagentacp` connector is local and single-user: the host rejects it in multi-user deployments.
2. **No raw OAuth bridge.** The proxy never collects, stores, or forwards Claude.ai OAuth or session tokens to call the Anthropic API directly. Anthropic's terms forbid third-party applications from offering Claude.ai login, routing requests through Free, Pro, or Max plan credentials on behalf of their users, or intermediating Claude.ai credentials. A raw bridge would also require impersonating Claude Code's client identity, which the proxy never does.
3. **No modified Claude Code.** The connector runs the official runtime exactly as published; it admits only pinned, verified runtime bytes and refuses external CLI overrides.

### Sanctioned alternatives for everything else

For shared, multi-user, or commercial use, use the in-process `anthropic` backend with Anthropic Console API keys, or Claude through the `vertex` and `bedrock` connectors with cloud-provider credentials.

### Verified public documentation baseline

- **Claude Code legal and compliance:**
  <https://code.claude.com/docs/en/legal-and-compliance>
  *Scope:* Permits end users to sign in to the unmodified Claude Code with their own subscription. Forbids third-party developers from offering Claude.ai login, routing requests through Free/Pro/Max credentials on behalf of their users, collecting, storing, or intermediating Claude.ai credentials, and reselling or intermediating Claude usage.

- **Anthropic Consumer Terms of Service:**
  <https://www.anthropic.com/legal/consumer-terms>
  *Scope:* Governs Free, Pro, and Max plans. Forbids sharing account credentials, making an account available to anyone else, and reselling the Services. Automated access is permitted only through an API key or where Anthropic explicitly permits it.

- **Anthropic Commercial Terms of Service:**
  <https://www.anthropic.com/legal/commercial-terms>
  *Scope:* Governs API-key, Team, and Enterprise use, including products that offer Claude to other people.

Operators remain responsible for complying with their own plan's terms. If Anthropic changes these terms, this policy will be revisited.
